package cli

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Compustretch/devo/internal/services"
)

// installMigrateDocker puts a fake docker on PATH. It logs every compose call,
// prints noise for `up`, and handles `exec` in two ways. SQL that touches the
// ledger table (devo_migrations) is answered from a small file-backed ledger
// (DEVO_LEDGER_ROWS), so runs see what earlier runs recorded; DEVO_LEDGER_FAIL_STATUS
// (comma list) makes inserts of those statuses fail. Everything else is a
// migration: its SQL is recorded in the stdin file, and the Nth migration exec
// fails when N is in DEVO_EXEC_FAIL_ON (e.g. 2,3) with DEVO_EXEC_ERROR.
func installMigrateDocker(t *testing.T) (logPath, stdinPath string) {
	t.Helper()
	binDir := t.TempDir()
	logPath = filepath.Join(binDir, "docker.log")
	stdinPath = filepath.Join(binDir, "exec-stdin.txt")
	script := `#!/bin/sh
if [ "$1" = "info" ]; then exit 0; fi
if [ "$1" = "context" ] && [ "$2" = "show" ]; then printf 'default\n'; exit 0; fi
if [ "$1" = "compose" ] && [ "$2" = "version" ]; then exit 0; fi
printf '%s\n' "$*" >> "$DEVO_DOCKER_LOG"
case " $* " in
  *" up "*)
    echo "NOISE [+] up 1/1 container healthy" >&2
    exit 0 ;;
  *" exec "*)
    sql=$(cat)
    case "$sql" in
      *devo_migrations*)
        case "$sql" in
          *"INSERT INTO"*)
            status=$(printf '%s' "$sql" | awk -F"'" '{print $8}')
            case ",${DEVO_LEDGER_FAIL_STATUS:-}," in
              *",$status,"*) echo "ledger write boom" >&2; exit 1 ;;
            esac
            printf '%s' "$sql" | awk -F"'" '{print $4 "\t" $6 "\t" $8 "\t" $10}' >> "$DEVO_LEDGER_ROWS" ;;
          *"SELECT"*)
            if [ -f "$DEVO_LEDGER_ROWS" ]; then
              awk -F'\t' '{r[$1]=$0} END{for(k in r) print r[k]}' "$DEVO_LEDGER_ROWS" | sort
            fi ;;
        esac
        exit 0 ;;
    esac
    n=$(cat "$DEVO_EXEC_COUNT" 2>/dev/null || echo 0)
    n=$((n + 1))
    echo "$n" > "$DEVO_EXEC_COUNT"
    printf '%s\n' "$sql" >> "$DEVO_EXEC_STDIN"
    case ",${DEVO_EXEC_FAIL_ON:-}," in
      *",$n,"*) printf '%s\n' "${DEVO_EXEC_ERROR:-boom}" >&2; exit 1 ;;
    esac
    exit 0 ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DEVO_DOCKER_LOG", logPath)
	t.Setenv("DEVO_EXEC_STDIN", stdinPath)
	t.Setenv("DEVO_EXEC_COUNT", filepath.Join(binDir, "exec-count"))
	t.Setenv("DEVO_LEDGER_ROWS", filepath.Join(binDir, "ledger-rows.tsv"))
	return logPath, stdinPath
}

// ledgerState returns the latest recorded status of every migration.
func ledgerState(t *testing.T) map[string]string {
	t.Helper()
	state := map[string]string{}
	for _, line := range readLines(t, os.Getenv("DEVO_LEDGER_ROWS")) {
		if f := strings.Split(line, "\t"); len(f) >= 3 {
			state[f[0]] = f[2]
		}
	}
	return state
}

func wantState(t *testing.T, want map[string]string) {
	t.Helper()
	got := ledgerState(t)
	for v, st := range want {
		if got[v] != st {
			t.Errorf("ledger status of %s = %q, want %q (ledger: %v)", v, got[v], st, got)
		}
	}
}

// adoptedAppWithMigrations adopts an existing "app" project in place and
// returns its root. The init directory holds forward migrations with their
// rollbacks, plus files that sit beside them and must never be applied.
func adoptedAppWithMigrations(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "app")
	files := map[string]string{
		"Cargo.toml":                                   "[package]\nname = \"app\"\n",
		"platform/clickhouse/init/002_b.sql":           "SELECT 2;\n",
		"platform/clickhouse/init/002_b.down.sql":      "DROP 2;\n",
		"platform/clickhouse/init/001_a.sql":           "SELECT 1;\n",
		"platform/clickhouse/init/001_a.down.sql":      "DROP 1;\n",
		"platform/clickhouse/init/001_a.smoke.sql":     "SELECT smoke;\n",
		"platform/clickhouse/init/001_a.fixture.jsonl": "{}\n",
		"platform/clickhouse/init/notes.txt":           "notes\n",
		"platform/clickhouse/init/tools/ignored.sql":   "SELECT nested;\n",
	}
	for p, content := range files {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
	if status := Run([]string{"init", "rust", "."}); status != 0 {
		t.Fatalf("init rust . returned %d", status)
	}
	return root
}

func captureOutput(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = outW, errW
	outCh, errCh := make(chan string), make(chan string)
	go func() { b, _ := io.ReadAll(outR); outCh <- string(b) }()
	go func() { b, _ := io.ReadAll(errR); errCh <- string(b) }()
	fn()
	outW.Close()
	errW.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	return <-outCh, <-errCh
}

func runCaptured(t *testing.T, args ...string) (status int, stdout, stderr string) {
	t.Helper()
	stdout, stderr = captureOutput(t, func() { status = Run(args) })
	return status, stdout, stderr
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

func countExecs(lines []string) int {
	n := 0
	for _, l := range lines {
		if strings.Contains(l, " exec ") {
			n++
		}
	}
	return n
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

const refusalOutput = "Received exception from server (version 25.3.14):\n" +
	"Code: 395. DB::Exception: Received from localhost:9000. DB::Exception: migration 001 refused: database x already exists: while executing 'FUNCTION throwIf(...)'. (FUNCTION_THROW_IF_VALUE_IS_NON_ZERO)\n" +
	"(query: SELECT throwIf(...))"

func TestAddMigrateAppliesForwardMigrationsInOrder(t *testing.T) {
	for _, args := range [][]string{
		{"add", "-m", "clickhouse"},
		{"add", "--migrate", "clickhouse"},
		{"add", "clickhouse", "-m"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			logPath, stdinPath := installMigrateDocker(t)
			adoptedAppWithMigrations(t)

			status, stdout, stderr := runCaptured(t, args...)
			if status != 0 {
				t.Fatalf("%v returned %d, want success\n%s", args, status, stderr)
			}

			lines := readLines(t, logPath)
			if !strings.Contains(lines[0], "up --detach --wait clickhouse") {
				t.Errorf("first call should start only clickhouse: %q", lines[0])
			}
			// ledger create + read, then in_progress, the migration, applied for each of two.
			if n := countExecs(lines); n != 8 {
				t.Errorf("%d exec calls, want 8:\n%s", n, strings.Join(lines, "\n"))
			}
			if got, want := readFile(t, stdinPath), "SELECT 1;\nSELECT 2;\n"; got != want {
				t.Errorf("piped %q, want %q (forward migrations only, in name order)", got, want)
			}
			wantState(t, map[string]string{"001_a.sql": "applied", "002_b.sql": "applied"})
			if !strings.Contains(stdout, "SUCCESS: 2 migration(s) applied") {
				t.Errorf("no clear success line:\n%s", stdout)
			}
			if strings.Contains(stdout+stderr, "NOISE") {
				t.Errorf("docker's progress output leaked without --verbose:\n%s%s", stdout, stderr)
			}
		})
	}
}

func TestAddMigrateVerboseShowsRawOutput(t *testing.T) {
	installMigrateDocker(t)
	adoptedAppWithMigrations(t)

	status, _, stderr := runCaptured(t, "add", "-m", "-v", "clickhouse")
	if status != 0 {
		t.Fatalf("returned %d", status)
	}
	if !strings.Contains(stderr, "NOISE") {
		t.Errorf("--verbose should show docker's output:\n%s", stderr)
	}
}

func TestAddMigrateSkipsMigrationsAlreadyApplied(t *testing.T) {
	_, stdinPath := installMigrateDocker(t)
	adoptedAppWithMigrations(t)

	if status, _, stderr := runCaptured(t, "add", "-m", "clickhouse"); status != 0 {
		t.Fatalf("first run returned %d\n%s", status, stderr)
	}
	status, stdout, stderr := runCaptured(t, "add", "-m", "clickhouse")
	if status != 0 {
		t.Fatalf("second run returned %d\n%s", status, stderr)
	}
	if got, want := readFile(t, stdinPath), "SELECT 1;\nSELECT 2;\n"; got != want {
		t.Errorf("second run re-ran migrations: piped %q, want %q", got, want)
	}
	if !strings.Contains(stdout, "Nothing to apply") {
		t.Errorf("no clear nothing-to-do line:\n%s", stdout)
	}
}

func TestAddMigrateRunsOnlyNewMigrations(t *testing.T) {
	_, stdinPath := installMigrateDocker(t)
	root := adoptedAppWithMigrations(t)
	if status, _, stderr := runCaptured(t, "add", "-m", "clickhouse"); status != 0 {
		t.Fatalf("first run returned %d\n%s", status, stderr)
	}
	if err := os.WriteFile(filepath.Join(root, "platform/clickhouse/init/003_c.sql"), []byte("SELECT 3;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if status, _, stderr := runCaptured(t, "add", "-m", "clickhouse"); status != 0 {
		t.Fatalf("second run returned %d\n%s", status, stderr)
	}
	if got, want := readFile(t, stdinPath), "SELECT 1;\nSELECT 2;\nSELECT 3;\n"; got != want {
		t.Errorf("piped %q, want only 003 added on the second run (%q)", got, want)
	}
}

func TestAddMigrateFailureIsRecordedAndNotRolledBack(t *testing.T) {
	_, stdinPath := installMigrateDocker(t)
	t.Setenv("DEVO_EXEC_FAIL_ON", "2")
	t.Setenv("DEVO_EXEC_ERROR", "Code: 62. DB::Exception: Syntax error: boom. (SYNTAX_ERROR)")
	adoptedAppWithMigrations(t)

	status, stdout, stderr := runCaptured(t, "add", "-m", "clickhouse")
	if status == 0 {
		t.Fatal("add -m succeeded although a migration failed")
	}
	got := readFile(t, stdinPath)
	if strings.Contains(got, "DROP") {
		t.Errorf("a rollback ran: %q", got)
	}
	if want := "SELECT 1;\nSELECT 2;\n"; got != want {
		t.Errorf("piped %q, want %q", got, want)
	}
	wantState(t, map[string]string{"001_a.sql": "applied", "002_b.sql": "failed"})
	for _, want := range []string{"Applying 001_a.sql ... ok", "Applying 002_b.sql ... FAILED", "Syntax error: boom"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q:\n%s", want, stdout)
		}
	}
	for _, want := range []string{"marked failed", "nothing was rolled back", "devo migrate resolve clickhouse 002_b.sql"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("verdict missing %q:\n%s", want, stderr)
		}
	}
	if strings.Contains(stdout+stderr, "SYNTAX_ERROR") || strings.Contains(stdout+stderr, "DB::Exception") {
		t.Errorf("server plumbing leaked into the output:\n%s%s", stdout, stderr)
	}
}

func TestAddMigrateBlocksOnFailedUntilResolved(t *testing.T) {
	_, stdinPath := installMigrateDocker(t)
	t.Setenv("DEVO_EXEC_FAIL_ON", "2")
	adoptedAppWithMigrations(t)
	if status, _, _ := runCaptured(t, "add", "-m", "clickhouse"); status == 0 {
		t.Fatal("setup: expected the second migration to fail")
	}
	before := readFile(t, stdinPath)

	status, _, stderr := runCaptured(t, "add", "-m", "clickhouse")
	if status == 0 {
		t.Fatal("run proceeded past a migration marked failed")
	}
	if !strings.Contains(stderr, "002_b.sql is marked failed") {
		t.Errorf("failed migration not named:\n%s", stderr)
	}
	if after := readFile(t, stdinPath); after != before {
		t.Errorf("a migration ran while one was marked failed: %q", after)
	}

	// The user inspects the database, cleans up, and asks for a retry.
	t.Setenv("DEVO_EXEC_FAIL_ON", "")
	if status, _, stderr := runCaptured(t, "migrate", "resolve", "clickhouse", "002", "--retry"); status != 0 {
		t.Fatalf("resolve --retry returned %d\n%s", status, stderr)
	}
	wantState(t, map[string]string{"002_b.sql": "pending"})
	status, stdout, stderr := runCaptured(t, "add", "-m", "clickhouse")
	if status != 0 {
		t.Fatalf("retry returned %d\n%s", status, stderr)
	}
	if got, want := readFile(t, stdinPath), "SELECT 1;\nSELECT 2;\nSELECT 2;\n"; got != want {
		t.Errorf("piped %q, want only 002 re-run (%q)", got, want)
	}
	wantState(t, map[string]string{"001_a.sql": "applied", "002_b.sql": "applied"})
	if !strings.Contains(stdout, "SUCCESS: 1 migration(s) applied") {
		t.Errorf("no clear success line:\n%s", stdout)
	}
}

func TestMigrateResolveAppliedMarksItDone(t *testing.T) {
	_, stdinPath := installMigrateDocker(t)
	t.Setenv("DEVO_EXEC_FAIL_ON", "2")
	adoptedAppWithMigrations(t)
	runCaptured(t, "add", "-m", "clickhouse")

	t.Setenv("DEVO_EXEC_FAIL_ON", "")
	if status, _, stderr := runCaptured(t, "migrate", "resolve", "clickhouse", "002_b", "--applied"); status != 0 {
		t.Fatalf("resolve --applied returned %d\n%s", status, stderr)
	}
	wantState(t, map[string]string{"002_b.sql": "applied"})
	status, stdout, _ := runCaptured(t, "add", "-m", "clickhouse")
	if status != 0 || !strings.Contains(stdout, "Nothing to apply") {
		t.Errorf("status %d, output:\n%s", status, stdout)
	}
	if got, want := readFile(t, stdinPath), "SELECT 1;\nSELECT 2;\n"; got != want {
		t.Errorf("piped %q, want %q", got, want)
	}
}

func TestAddMigrateRefusalOnFirstMigrationChangesNothing(t *testing.T) {
	_, stdinPath := installMigrateDocker(t)
	t.Setenv("DEVO_EXEC_FAIL_ON", "1")
	t.Setenv("DEVO_EXEC_ERROR", refusalOutput)
	adoptedAppWithMigrations(t)

	status, stdout, stderr := runCaptured(t, "add", "-m", "clickhouse")
	if status == 0 {
		t.Fatal("add -m succeeded although a migration refused")
	}
	if got := readFile(t, stdinPath); got != "SELECT 1;\n" {
		t.Errorf("piped %q, want only the refused migration", got)
	}
	wantState(t, map[string]string{"001_a.sql": "pending"})
	if !strings.Contains(stdout, "migration 001 refused: database x already exists") {
		t.Errorf("reason missing:\n%s", stdout)
	}
	for _, want := range []string{"refused to run, nothing was changed", "devo migrate baseline clickhouse 001_a"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("verdict missing %q:\n%s", want, stderr)
		}
	}
	if strings.Contains(stdout+stderr, "while executing") || strings.Contains(stdout+stderr, "DB::Exception") {
		t.Errorf("server plumbing leaked into the output:\n%s%s", stdout, stderr)
	}
}

func TestAddMigrateRefusalLaterKeepsEarlierMigrations(t *testing.T) {
	_, stdinPath := installMigrateDocker(t)
	t.Setenv("DEVO_EXEC_FAIL_ON", "2")
	t.Setenv("DEVO_EXEC_ERROR", refusalOutput)
	adoptedAppWithMigrations(t)

	status, _, stderr := runCaptured(t, "add", "-m", "clickhouse")
	if status == 0 {
		t.Fatal("add -m succeeded although a migration refused")
	}
	if got, want := readFile(t, stdinPath), "SELECT 1;\nSELECT 2;\n"; got != want {
		t.Errorf("piped %q, want no rollback (%q)", got, want)
	}
	wantState(t, map[string]string{"001_a.sql": "applied", "002_b.sql": "pending"})
	if !strings.Contains(stderr, "002_b.sql refused to run") {
		t.Errorf("no clear failure verdict:\n%s", stderr)
	}
}

func TestAddMigrateRefusesWhenAppliedFileChanged(t *testing.T) {
	_, stdinPath := installMigrateDocker(t)
	root := adoptedAppWithMigrations(t)
	if status, _, stderr := runCaptured(t, "add", "-m", "clickhouse"); status != 0 {
		t.Fatalf("first run returned %d\n%s", status, stderr)
	}
	if err := os.WriteFile(filepath.Join(root, "platform/clickhouse/init/001_a.sql"), []byte("SELECT 'edited';\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := readFile(t, stdinPath)

	status, _, stderr := runCaptured(t, "add", "-m", "clickhouse")
	if status == 0 {
		t.Fatal("run proceeded although an applied migration was edited")
	}
	if !strings.Contains(stderr, "001_a.sql was changed after it was applied") {
		t.Errorf("changed file not named:\n%s", stderr)
	}
	if after := readFile(t, stdinPath); after != before {
		t.Errorf("a migration ran despite the changed checksum: %q", after)
	}

	_, stdout, _ := runCaptured(t, "migrate", "status", "clickhouse")
	if !strings.Contains(stdout, "applied (FILE CHANGED)") {
		t.Errorf("status does not flag the changed file:\n%s", stdout)
	}
}

func TestMigrateBaselineRecordsWithoutRunning(t *testing.T) {
	_, stdinPath := installMigrateDocker(t)
	adoptedAppWithMigrations(t)
	if status := Run([]string{"add", "clickhouse"}); status != 0 {
		t.Fatalf("setup add returned %d", status)
	}

	status, stdout, stderr := runCaptured(t, "migrate", "baseline", "clickhouse", "001")
	if status != 0 {
		t.Fatalf("baseline returned %d\n%s", status, stderr)
	}
	if got := readFile(t, stdinPath); got != "" {
		t.Errorf("baseline ran SQL: %q", got)
	}
	wantState(t, map[string]string{"001_a.sql": "baselined"})
	if got := ledgerState(t); len(got) != 1 {
		t.Errorf("baseline through 001 recorded more than 001: %v", got)
	}
	if !strings.Contains(stdout, "nothing was run") {
		t.Errorf("baseline does not say it ran nothing:\n%s", stdout)
	}

	if status, _, stderr := runCaptured(t, "add", "-m", "clickhouse"); status != 0 {
		t.Fatalf("run after baseline returned %d\n%s", status, stderr)
	}
	if got, want := readFile(t, stdinPath), "SELECT 2;\n"; got != want {
		t.Errorf("piped %q, want only 002 after baselining 001 (%q)", got, want)
	}
}

func TestAddMigrateReportsLedgerWriteFailureAfterSuccess(t *testing.T) {
	_, stdinPath := installMigrateDocker(t)
	t.Setenv("DEVO_LEDGER_FAIL_STATUS", "applied")
	adoptedAppWithMigrations(t)

	status, _, stderr := runCaptured(t, "add", "-m", "clickhouse")
	if status == 0 {
		t.Fatal("add -m succeeded although the ledger could not record the result")
	}
	if got := readFile(t, stdinPath); got != "SELECT 1;\n" {
		t.Errorf("piped %q, want the run to stop after 001", got)
	}
	wantState(t, map[string]string{"001_a.sql": "in_progress"})
	for _, want := range []string{"ran but the ledger could not be updated", "devo migrate resolve clickhouse 001_a.sql --applied"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("verdict missing %q:\n%s", want, stderr)
		}
	}
}

func TestMigrateStatusListsEveryMigration(t *testing.T) {
	installMigrateDocker(t)
	t.Setenv("DEVO_EXEC_FAIL_ON", "2")
	t.Setenv("DEVO_EXEC_ERROR", "Code: 62. DB::Exception: Syntax error: boom. (SYNTAX_ERROR)")
	root := adoptedAppWithMigrations(t)
	runCaptured(t, "add", "-m", "clickhouse")
	if err := os.WriteFile(filepath.Join(root, "platform/clickhouse/init/003_c.sql"), []byte("SELECT 3;\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	status, stdout, stderr := runCaptured(t, "migrate", "status", "clickhouse")
	if status != 0 {
		t.Fatalf("status returned %d\n%s", status, stderr)
	}
	for _, want := range []string{"applied", "001_a.sql", "failed", "002_b.sql", "Syntax error: boom", "not applied", "003_c.sql"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("status output missing %q:\n%s", want, stdout)
		}
	}
}

func TestAddMigrateDoesNotRequireRollbackFiles(t *testing.T) {
	_, stdinPath := installMigrateDocker(t)
	root := adoptedAppWithMigrations(t)
	if err := os.Remove(filepath.Join(root, "platform/clickhouse/init/002_b.down.sql")); err != nil {
		t.Fatal(err)
	}

	if status, _, stderr := runCaptured(t, "add", "-m", "clickhouse"); status != 0 {
		t.Fatalf("add -m returned %d\n%s", status, stderr)
	}
	if got, want := readFile(t, stdinPath), "SELECT 1;\nSELECT 2;\n"; got != want {
		t.Errorf("piped %q, want %q", got, want)
	}
}

func TestMigrateCommandRejectsBadArguments(t *testing.T) {
	logPath, _ := installMigrateDocker(t)
	adoptedAppWithMigrations(t)

	for _, args := range [][]string{
		{"migrate"},
		{"migrate", "frobnicate", "clickhouse"},
		{"migrate", "status"},
		{"migrate", "baseline", "clickhouse"},
		{"migrate", "resolve", "clickhouse", "001"},
		{"migrate", "resolve", "clickhouse", "001", "--applied", "--retry"},
		{"migrate", "status", "clickhouse", "--applied"},
		{"migrate", "status", "redis"},
	} {
		if status := Run(args); status == 0 {
			t.Errorf("%v succeeded", args)
		}
	}
	if lines := readLines(t, logPath); len(lines) != 0 {
		t.Errorf("docker was called for a rejected command: %v", lines)
	}
}

func TestAddWithoutMigrateRunsNoDocker(t *testing.T) {
	logPath, _ := installMigrateDocker(t)
	adoptedAppWithMigrations(t)

	if status := Run([]string{"add", "clickhouse"}); status != 0 {
		t.Fatalf("add clickhouse returned %d", status)
	}
	if lines := readLines(t, logPath); len(lines) != 0 {
		t.Errorf("plain add called docker: %v", lines)
	}
}

func TestMigrateFlagsOnlyApplyToAdd(t *testing.T) {
	logPath, _ := installMigrateDocker(t)
	adoptedAppWithMigrations(t)
	if status := Run([]string{"add", "clickhouse"}); status != 0 {
		t.Fatalf("setup add returned %d", status)
	}

	for _, args := range [][]string{
		{"remove", "-m", "clickhouse"},
		{"add", "-v", "clickhouse"},
	} {
		if status := Run(args); status == 0 {
			t.Errorf("%v succeeded", args)
		}
	}
	if lines := readLines(t, logPath); len(lines) != 0 {
		t.Errorf("docker was called: %v", lines)
	}
}

func TestMigrateFlagRejectedForServiceWithoutMigrations(t *testing.T) {
	logPath, _ := installMigrateDocker(t)
	adoptedAppWithMigrations(t)

	if status := Run([]string{"add", "-m", "redis"}); status == 0 {
		t.Error("add -m redis succeeded although redis defines no migrations")
	}
	if lines := readLines(t, logPath); len(lines) != 0 {
		t.Errorf("docker was called: %v", lines)
	}
}

func TestMigrationFilesSelection(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"002_b.sql", "001_a.sql", "001_a.down.sql", "001_a.smoke.sql",
		"001_a.fixture.jsonl", "README.md", "003_c.sql",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "sub.sql"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := migrationFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := "001_a.sql 002_b.sql 003_c.sql"; strings.Join(got, " ") != want {
		t.Errorf("migrationFiles = %v, want %s", got, want)
	}
}

func TestFailureReason(t *testing.T) {
	cases := map[string]struct {
		out  string
		want string
	}{
		"preflight refusal": {refusalOutput, "migration 001 refused: database x already exists"},
		"syntax error":      {"Code: 62. DB::Exception: Syntax error: failed at position 5. (SYNTAX_ERROR)\n(query: SELEC 1)", "Syntax error: failed at position 5"},
		"not a database":    {"Error response from daemon: no such service: clickhouse\n", "Error response from daemon: no such service: clickhouse"},
		"no output":         {"", "exit status 1"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := failureReason(c.out, errors.New("exit status 1")); got != c.want {
				t.Errorf("failureReason = %q, want %q", got, c.want)
			}
		})
	}
}

// The migrate command takes credentials from the container's own environment,
// so it follows whatever user and password the project is configured with.
func TestClickhouseMigrateCommandUsesContainerCredentials(t *testing.T) {
	if err := loadRegistry("."); err != nil {
		t.Fatal(err)
	}
	svc, ok := services.Get("clickhouse")
	if !ok || svc.Migrate == nil {
		t.Fatal("clickhouse has no migrate entry")
	}

	stub := t.TempDir()
	if err := os.WriteFile(filepath.Join(stub, "clickhouse-client"),
		[]byte("#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := "PATH=" + stub + string(os.PathListSeparator) + "/usr/bin:/bin"

	for name, c := range map[string]struct {
		env  []string
		want string
	}{
		"no credentials set":    {nil, "--multiquery"},
		"app user":              {[]string{"CLICKHOUSE_USER=app", "CLICKHOUSE_PASSWORD=p w"}, "--user app --password p_w --multiquery"},
		"user without password": {[]string{"CLICKHOUSE_USER=app"}, "--user app --multiquery"},
	} {
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command(svc.Migrate.Exec[0], svc.Migrate.Exec[1:]...)
			cmd.Env = append([]string{path}, c.env...)
			out, err := cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Join(strings.Fields(strings.ReplaceAll(string(out), "p w", "p_w")), " ")
			if got != c.want {
				t.Errorf("client args = %q, want %q", got, c.want)
			}
		})
	}
}
