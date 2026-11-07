package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanRun(t *testing.T) {
	f := func(n, c string) migrationFile { return migrationFile{name: n, checksum: c} }
	files := []migrationFile{f("001_a.sql", "a"), f("002_b.sql", "b"), f("003_c.sql", "c")}
	e := func(c, s string) ledgerEntry { return ledgerEntry{checksum: c, status: s} }

	tests := []struct {
		name   string
		ledger map[string]ledgerEntry
		want   []string
		errHas string
	}{
		{"fresh runs all", nil, []string{"001_a.sql", "002_b.sql", "003_c.sql"}, ""},
		{"skips applied", map[string]ledgerEntry{"001_a.sql": e("a", statusApplied)}, []string{"002_b.sql", "003_c.sql"}, ""},
		{"skips baselined", map[string]ledgerEntry{"001_a.sql": e("a", statusBaselined), "002_b.sql": e("b", statusBaselined)}, []string{"003_c.sql"}, ""},
		{"all applied", map[string]ledgerEntry{"001_a.sql": e("a", statusApplied), "002_b.sql": e("b", statusApplied), "003_c.sql": e("c", statusApplied)}, nil, ""},
		{"checksum change", map[string]ledgerEntry{"001_a.sql": e("x", statusApplied)}, nil, "was changed after it was applied"},
		{"baselined checksum change", map[string]ledgerEntry{"001_a.sql": e("x", statusBaselined)}, nil, "was changed after it was applied"},
		{"failed blocks", map[string]ledgerEntry{"002_b.sql": e("b", statusFailed)}, nil, "002_b.sql is marked failed"},
		{"in progress blocks", map[string]ledgerEntry{"002_b.sql": e("b", statusInProgress)}, nil, "is marked in_progress"},
		{"pending retries", map[string]ledgerEntry{"001_a.sql": e("a", statusApplied), "002_b.sql": e("b", statusPending)}, []string{"002_b.sql", "003_c.sql"}, ""},
		{"pending may be edited", map[string]ledgerEntry{"001_a.sql": e("old", statusPending)}, []string{"001_a.sql", "002_b.sql", "003_c.sql"}, ""},
		{"out of order", map[string]ledgerEntry{"003_c.sql": e("c", statusApplied)}, nil, "out of order"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := planRun("clickhouse", files, tc.ledger)
			if tc.errHas != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errHas) {
					t.Fatalf("want error containing %q, got %v", tc.errHas, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, g := range got {
				names = append(names, g.name)
			}
			if strings.Join(names, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("want %v, got %v", tc.want, names)
			}
		})
	}
}

func TestChecksumIgnoresLineEndings(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.sql"), filepath.Join(dir, "b.sql")
	os.WriteFile(a, []byte("SELECT 1;\nSELECT 2;\n"), 0o644)
	os.WriteFile(b, []byte("SELECT 1;\r\nSELECT 2;\r\n"), 0o644)
	ca, _ := checksumFile(a)
	cb, _ := checksumFile(b)
	if ca != cb {
		t.Fatal("CRLF and LF checksums differ")
	}
	os.WriteFile(b, []byte("SELECT 1;\nSELECT 3;\n"), 0o644)
	cb, _ = checksumFile(b)
	if ca == cb {
		t.Fatal("different content gave the same checksum")
	}
}

func TestSQLQuote(t *testing.T) {
	if got, want := sqlQuote(`it's a\b`), `'it\'s a\\b'`; got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestMatchVersion(t *testing.T) {
	files := []migrationFile{{name: "001_init.sql"}, {name: "002_users.sql"}, {name: "0020_x.sql"}}
	for _, v := range []string{"002", "002_users", "002_users.sql"} {
		if i, err := matchVersion(files, v); err != nil || i != 1 {
			t.Fatalf("%s: got %d, %v", v, i, err)
		}
	}
	if _, err := matchVersion(files, "009"); err == nil {
		t.Fatal("expected no-match error")
	}
}

func TestParseLedger(t *testing.T) {
	m, err := parseLedger("001_a.sql\tabc\tapplied\t\n002_b.sql\tdef\tfailed\tboom\n")
	if err != nil || len(m) != 2 || m["002_b.sql"].status != statusFailed || m["002_b.sql"].detail != "boom" {
		t.Fatalf("got %v, %v", m, err)
	}
	if m, err := parseLedger(""); err != nil || len(m) != 0 {
		t.Fatalf("empty ledger: got %v, %v", m, err)
	}
}

func TestRecordSQLEscapesValues(t *testing.T) {
	got := recordSQL("clickhouse", "001_a.sql", "abc", statusFailed, `it's "bad" \ news`)
	if !strings.Contains(got, `'it\'s "bad" \\ news'`) {
		t.Errorf("detail not escaped: %s", got)
	}
}
