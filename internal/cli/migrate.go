package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/Compustretch/devo/internal/services"
)

// refusalRe matches the convention used by migrations that check a precondition
// before changing anything: they fail with a message containing "refused:".
var refusalRe = regexp.MustCompile(`\brefused:`)

// errorSuffixRe strips the trailing "(ERROR_NAME)" ClickHouse appends to messages.
var errorSuffixRe = regexp.MustCompile(`\s*\([A-Z0-9_]+\)\.?$`)

// extractMigrateFlags removes -m/--migrate and -v/--verbose from args wherever
// they appear, so `devo add -m clickhouse` and `devo add clickhouse -m` both work.
func extractMigrateFlags(args []string) (rest []string, migrate, verbose bool) {
	rest = make([]string, 0, len(args))
	for _, a := range args {
		switch a {
		case "-m", "--migrate":
			migrate = true
		case "-v", "--verbose":
			verbose = true
		default:
			rest = append(rest, a)
		}
	}
	return rest, migrate, verbose
}

// isMigration reports whether a file in a migration directory is a forward
// migration. Rollback (.down.sql) and verification (.smoke.sql) files sit beside
// their migration but must never be applied as one.
func isMigration(name string) bool {
	return strings.HasSuffix(name, ".sql") &&
		!strings.HasSuffix(name, ".down.sql") &&
		!strings.HasSuffix(name, ".smoke.sql")
}

// migrationFiles returns the forward migrations in dir in name (byte) order.
func migrationFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && isMigration(e.Name()) {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	return files, nil
}

// failureReason reduces a failed command's output to one readable line. For a
// database error it is the message itself, without the server plumbing around it.
func failureReason(out string, err error) string {
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "DB::Exception") {
			continue
		}
		msg := line[strings.LastIndex(line, "DB::Exception: ")+len("DB::Exception: "):]
		if i := strings.Index(msg, ": while executing"); i >= 0 {
			msg = msg[:i]
		}
		msg = errorSuffixRe.ReplaceAllString(msg, "")
		return strings.TrimRight(strings.TrimSpace(msg), ".")
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	reason := strings.TrimSpace(lines[len(lines)-1])
	if reason == "" {
		reason = err.Error()
	}
	if len(reason) > 200 {
		reason = reason[:200] + "..."
	}
	return reason
}

type migrator struct {
	dir, migrationDir, relDir, service string
	base, execArgs                     []string
	verbose                            bool
}

func newMigrator(dir, service string, verbose bool) (*migrator, error) {
	svc, ok := services.Get(service)
	if !ok {
		return nil, fmt.Errorf("unknown service %q", service)
	}
	if svc.Migrate == nil {
		return nil, fmt.Errorf("service %q has no migrations to run", service)
	}
	return &migrator{
		dir:          dir,
		migrationDir: filepath.Join(dir, filepath.FromSlash(svc.Migrate.Dir)),
		relDir:       svc.Migrate.Dir,
		service:      service,
		execArgs:     append([]string{"exec", "-T", service}, svc.Migrate.Exec...),
		verbose:      verbose,
	}, nil
}

// loadFiles lists the forward migrations with their checksums, in name order.
func (m *migrator) loadFiles() ([]migrationFile, error) {
	names, err := migrationFiles(m.migrationDir)
	if err != nil {
		return nil, err
	}
	files := make([]migrationFile, 0, len(names))
	for _, n := range names {
		sum, err := checksumFile(filepath.Join(m.migrationDir, n))
		if err != nil {
			return nil, err
		}
		files = append(files, migrationFile{name: n, checksum: sum})
	}
	return files, nil
}

func (m *migrator) command(args ...string) *exec.Cmd {
	cmdArgs := append(append([]string{}, m.base[1:]...), "-f", filepath.Join(m.dir, "compose.yaml"))
	cmdArgs = append(cmdArgs, args...)
	cmd := exec.Command(m.base[0], cmdArgs...)
	cmd.Dir = m.dir
	return cmd
}

// compose runs a docker compose command and returns its combined output. The
// output is shown only with --verbose; otherwise it feeds the failure reason.
func (m *migrator) compose(stdin io.Reader, args ...string) (string, error) {
	cmd := m.command(args...)
	var buf bytes.Buffer
	var sink io.Writer = &buf
	if m.verbose {
		sink = io.MultiWriter(&buf, os.Stderr)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, sink, sink
	err := cmd.Run()
	return buf.String(), err
}

// sql sends one statement to the service's migrate command and returns stdout
// alone, so query results are not mixed with server warnings on stderr.
func (m *migrator) sql(q string) (string, error) {
	cmd := m.command(m.execArgs...)
	var stdout, stderr bytes.Buffer
	var errSink io.Writer = &stderr
	if m.verbose {
		errSink = io.MultiWriter(&stderr, os.Stderr)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = strings.NewReader(q), &stdout, errSink
	if err := cmd.Run(); err != nil {
		return "", errors.New(failureReason(stdout.String()+stderr.String(), err))
	}
	return stdout.String(), nil
}

// apply pipes one SQL file to the service's migrate command.
func (m *migrator) apply(file string) (string, error) {
	f, err := os.Open(filepath.Join(m.migrationDir, file))
	if err != nil {
		return "", err
	}
	defer f.Close()
	return m.compose(f, m.execArgs...)
}

// start makes sure docker is available and the service is up.
func (m *migrator) start() error {
	base, err := ensureDocker(true)
	if err != nil {
		return err
	}
	m.base = base
	warnIfUnshared(m.dir)

	fmt.Printf("Starting %s ... ", m.service)
	if out, err := m.compose(nil, "up", "--detach", "--wait", m.service); err != nil {
		fmt.Println("FAILED")
		fmt.Printf("  %s\n", failureReason(out, err))
		return fmt.Errorf("migrations FAILED: could not start %s, nothing was run", m.service)
	}
	fmt.Println("ok")
	return nil
}

func (m *migrator) loadLedger() (map[string]ledgerEntry, error) {
	if _, err := m.sql(ledgerDDL); err != nil {
		return nil, fmt.Errorf("could not create the migration ledger %s: %v", ledgerTable, err)
	}
	out, err := m.sql(ledgerQuery(m.service))
	if err != nil {
		return nil, fmt.Errorf("could not read the migration ledger: %v", err)
	}
	return parseLedger(out)
}

func (m *migrator) record(f migrationFile, status, detail string) error {
	_, err := m.sql(recordSQL(m.service, f.name, f.checksum, status, detail))
	return err
}

func (m *migrator) recordOrWarn(f migrationFile, status, detail string) {
	if err := m.record(f, status, detail); err != nil {
		fmt.Printf("  warning: could not record %s as %s: %v\n", f.name, status, err)
	}
}

// prepare is the shared start of run, status, baseline and resolve. When there
// are no migration files it returns before touching docker.
func prepare(dir, service string, verbose bool) (*migrator, []migrationFile, map[string]ledgerEntry, error) {
	m, err := newMigrator(dir, service, verbose)
	if err != nil {
		return nil, nil, nil, err
	}
	files, err := m.loadFiles()
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil, fmt.Errorf("no migrations directory at %s", m.migrationDir)
	}
	if err != nil {
		return nil, nil, nil, err
	}
	if len(files) == 0 {
		return m, files, nil, nil
	}
	if err := m.start(); err != nil {
		return nil, nil, nil, err
	}
	ledger, err := m.loadLedger()
	if err != nil {
		return nil, nil, nil, err
	}
	return m, files, ledger, nil
}

// runMigrations applies the migrations the ledger says have not run yet. It
// refuses to run if an applied migration's file has changed, or if an earlier
// run left a migration in_progress or failed. A failed migration is never rolled
// back automatically: ClickHouse DDL is not transactional, so a rollback could
// destroy state that was there before.
func runMigrations(dir, service string, verbose bool) error {
	m, files, ledger, err := prepare(dir, service, verbose)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		fmt.Printf("No migrations in %s\n", m.relDir)
		return nil
	}
	todo, err := planRun(service, files, ledger)
	if err != nil {
		return err
	}
	if len(todo) == 0 {
		fmt.Printf("Nothing to apply: all %d migration(s) in %s are already applied\n", len(files), m.relDir)
		return nil
	}

	for _, f := range todo {
		fmt.Printf("Applying %s ... ", f.name)
		if err := m.record(f, statusInProgress, ""); err != nil {
			fmt.Println("FAILED")
			return fmt.Errorf("migrations FAILED: could not write the ledger, %s was not run: %v", f.name, err)
		}
		out, err := m.apply(f.name)
		if err == nil {
			fmt.Println("ok")
			if rerr := m.record(f, statusApplied, ""); rerr != nil {
				return fmt.Errorf("migrations FAILED: %s ran but the ledger could not be updated (%v); it stays marked in_progress. "+
					"Once you have confirmed it ran: devo migrate resolve %s %s --applied", f.name, rerr, service, f.name)
			}
			continue
		}

		fmt.Println("FAILED")
		reason := failureReason(out, err)
		fmt.Printf("  %s\n", reason)

		// A migration that refuses to run changed nothing, so it stays eligible.
		if refusalRe.MatchString(out) {
			m.recordOrWarn(f, statusPending, reason)
			return fmt.Errorf("migrations FAILED: %s refused to run, nothing was changed. "+
				"If this schema already exists, record it with: devo migrate baseline %s %s",
				f.name, service, strings.TrimSuffix(f.name, ".sql"))
		}
		m.recordOrWarn(f, statusFailed, reason)
		return fmt.Errorf("migrations FAILED: %s failed and is marked failed; the database may be partly migrated and nothing was rolled back. "+
			"Inspect it, then run: devo migrate resolve %s %s --applied|--retry", f.name, service, f.name)
	}

	fmt.Printf("SUCCESS: %d migration(s) applied from %s\n", len(todo), m.relDir)
	return nil
}

// migrationStatus prints every migration with its ledger state.
func migrationStatus(dir, service string, verbose bool) error {
	m, files, ledger, err := prepare(dir, service, verbose)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		fmt.Printf("No migrations in %s\n", m.relDir)
		return nil
	}
	onDisk := map[string]bool{}
	for _, f := range files {
		onDisk[f.name] = true
		e, ok := ledger[f.name]
		state := "not applied"
		switch {
		case !ok:
		case e.done() && e.checksum != f.checksum:
			state = e.status + " (FILE CHANGED)"
		default:
			state = e.status
		}
		line := fmt.Sprintf("%-24s %s", state, f.name)
		if ok && e.detail != "" {
			line += "  -- " + e.detail
		}
		fmt.Println(line)
	}
	var orphans []string
	for name := range ledger {
		if !onDisk[name] {
			orphans = append(orphans, name)
		}
	}
	sort.Strings(orphans)
	for _, name := range orphans {
		fmt.Printf("%-24s %s\n", "file missing", name)
	}
	return nil
}

// baselineMigrations records every migration up to and including upTo as
// already applied, without running anything. It is the user's explicit
// statement that the schema is already at that point; devo never infers it.
func baselineMigrations(dir, service, upTo string, verbose bool) error {
	m, files, ledger, err := prepare(dir, service, verbose)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no migrations in %s", m.relDir)
	}
	idx, err := matchVersion(files, upTo)
	if err != nil {
		return err
	}
	for _, f := range files[:idx+1] {
		if e, ok := ledger[f.name]; ok && (e.status == statusInProgress || e.status == statusFailed) {
			return fmt.Errorf("%s is marked %s; resolve it first with: devo migrate resolve %s %s --applied|--retry",
				f.name, e.status, service, f.name)
		}
	}
	n := 0
	for _, f := range files[:idx+1] {
		if e, ok := ledger[f.name]; ok && e.done() {
			continue
		}
		if err := m.record(f, statusBaselined, "baseline"); err != nil {
			return fmt.Errorf("could not record %s: %v", f.name, err)
		}
		fmt.Printf("Baselined %s\n", f.name)
		n++
	}
	fmt.Printf("SUCCESS: %d migration(s) baselined through %s (nothing was run)\n", n, files[idx].name)
	return nil
}

// resolveMigration settles a migration left in_progress or failed. mode is
// "applied" (you checked that it ran) or "retry" (you cleaned up; run it again).
func resolveMigration(dir, service, version, mode string, verbose bool) error {
	if mode != "applied" && mode != "retry" {
		return errors.New("resolve needs --applied or --retry")
	}
	m, files, ledger, err := prepare(dir, service, verbose)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no migrations in %s", m.relDir)
	}
	idx, err := matchVersion(files, version)
	if err != nil {
		return err
	}
	f := files[idx]
	e, ok := ledger[f.name]
	if !ok || (e.status != statusInProgress && e.status != statusFailed) {
		return fmt.Errorf("%s is not in_progress or failed, nothing to resolve", f.name)
	}
	status := statusPending
	if mode == "applied" {
		status = statusApplied
	}
	if err := m.record(f, status, "resolved by user: "+mode); err != nil {
		return fmt.Errorf("could not record %s: %v", f.name, err)
	}
	fmt.Printf("%s marked %s\n", f.name, status)
	return nil
}
