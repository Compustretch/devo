package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// The ledger lives in ClickHouse's built-in default database, not the
// application's. Migration 001 may be what creates the application database, so
// the ledger has to exist independently of it.
const ledgerTable = "default.devo_migrations"

const (
	statusInProgress = "in_progress"
	statusApplied    = "applied"
	statusBaselined  = "baselined"
	statusFailed     = "failed"
	// statusPending marks a migration that is known but eligible to run: it
	// refused to run (changed nothing), or the user chose `resolve --retry`.
	statusPending = "pending"
)

// ledgerDDL creates an append-only history. The current state of a migration is
// its most recent row, so a crash between two writes never loses information.
const ledgerDDL = `CREATE TABLE IF NOT EXISTS ` + ledgerTable + ` (
	service String,
	version String,
	checksum String,
	status Enum8('in_progress' = 1, 'applied' = 2, 'failed' = 3, 'baselined' = 4, 'pending' = 5),
	detail String,
	updated_at DateTime64(6) DEFAULT now64(6)
) ENGINE = MergeTree ORDER BY (service, version, updated_at)`

type migrationFile struct {
	name, checksum string
}

type ledgerEntry struct {
	version, checksum, status, detail string
}

func (e ledgerEntry) done() bool {
	return e.status == statusApplied || e.status == statusBaselined
}

// checksumFile hashes a migration with line endings normalised, so a checkout
// with CRLF line endings does not look like an edited file.
func checksumFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func sqlQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	return "'" + s + "'"
}

func recordSQL(service, version, checksum, status, detail string) string {
	return fmt.Sprintf(
		"INSERT INTO %s (service, version, checksum, status, detail) VALUES (%s, %s, %s, %s, %s)",
		ledgerTable, sqlQuote(service), sqlQuote(version), sqlQuote(checksum), sqlQuote(status), sqlQuote(detail))
}

func ledgerQuery(service string) string {
	return fmt.Sprintf(
		"SELECT version, argMax(checksum, updated_at), toString(argMax(status, updated_at)), argMax(detail, updated_at) "+
			"FROM %s WHERE service = %s GROUP BY version ORDER BY version FORMAT TabSeparated",
		ledgerTable, sqlQuote(service))
}

func parseLedger(out string) (map[string]ledgerEntry, error) {
	entries := map[string]ledgerEntry{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 3 {
			return nil, fmt.Errorf("unexpected ledger row %q", line)
		}
		e := ledgerEntry{version: f[0], checksum: f[1], status: f[2]}
		if len(f) > 3 {
			e.detail = f[3]
		}
		entries[e.version] = e
	}
	return entries, nil
}

// planRun decides what to run. It changes nothing: every refusal happens here,
// before any migration executes.
func planRun(service string, files []migrationFile, ledger map[string]ledgerEntry) ([]migrationFile, error) {
	var problems []string
	lastDone := ""
	for _, f := range files {
		e, ok := ledger[f.name]
		if !ok {
			continue
		}
		switch {
		case e.done() && e.checksum != f.checksum:
			problems = append(problems, fmt.Sprintf("%s was changed after it was applied; restore the file or add a new migration", f.name))
		case e.done():
			lastDone = f.name
		case e.status == statusInProgress || e.status == statusFailed:
			problems = append(problems, fmt.Sprintf(
				"%s is marked %s; inspect the database, then run: devo migrate resolve %s %s --applied|--retry",
				f.name, e.status, service, f.name))
		}
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("migrations not run:\n  %s", strings.Join(problems, "\n  "))
	}

	var todo []migrationFile
	for _, f := range files {
		e, ok := ledger[f.name]
		if ok && e.done() {
			continue
		}
		if !ok && lastDone != "" && f.name < lastDone {
			return nil, fmt.Errorf("migrations not run: %s is new but sorts before applied migration %s (out of order)", f.name, lastDone)
		}
		todo = append(todo, f)
	}
	return todo, nil
}

// matchVersion finds one migration by file name, name without .sql, or number
// prefix ("002").
func matchVersion(files []migrationFile, v string) (int, error) {
	v = strings.TrimSuffix(v, ".sql")
	idx := -1
	for i, f := range files {
		base := strings.TrimSuffix(f.name, ".sql")
		if base == v || strings.HasPrefix(base, v+"_") {
			if idx != -1 {
				return -1, fmt.Errorf("%q matches more than one migration", v)
			}
			idx = i
		}
	}
	if idx == -1 {
		return -1, fmt.Errorf("no migration matches %q", v)
	}
	return idx, nil
}
