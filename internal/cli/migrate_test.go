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
// prints noise for `up`, records what is piped to `exec`, and can fail chosen
// exec calls (DEVO_EXEC_FAIL_ON=2,3) with a chosen message (DEVO_EXEC_ERROR).
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
    n=$(cat "$DEVO_EXEC_COUNT" 2>/dev/null || echo 0)
    n=$((n + 1))
    echo "$n" > "$DEVO_EXEC_COUNT"
    cat >> "$DEVO_EXEC_STDIN"
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
	return logPath, stdinPath
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
			if len(lines) != 3 {
				t.Fatalf("want 1 up and 2 exec calls, got %d:\n%s", len(lines), strings.Join(lines, "\n"))
			}
			if !strings.Contains(lines[0], "up --detach --wait clickhouse") {
				t.Errorf("first call should start only clickhouse: %q", lines[0])
			}
			if got, want := readFile(t, stdinPath), "SELECT 1;\nSELECT 2;\n"; got != want {
				t.Errorf("piped %q, want %q (forward migrations only, in name order)", got, want)
			}
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

func TestAddMigrateRollsBackAfterFailure(t *testing.T) {
	logPath, stdinPath := installMigrateDocker(t)
	t.Setenv("DEVO_EXEC_FAIL_ON", "2")
	t.Setenv("DEVO_EXEC_ERROR", "Code: 62. DB::Exception: Syntax error: boom. (SYNTAX_ERROR)")
	adoptedAppWithMigrations(t)

	status, stdout, stderr := runCaptured(t, "add", "-m", "clickhouse")
	if status == 0 {
		t.Fatal("add -m succeeded although a migration failed")
	}
	if got, want := readFile(t, stdinPath), "SELECT 1;\nSELECT 2;\nDROP 2;\nDROP 1;\n"; got != want {
		t.Errorf("piped %q, want forward 001, 002, then rollbacks 002 and 001 (%q)", got, want)
	}
	if n := countExecs(readLines(t, logPath)); n != 4 {
		t.Errorf("%d exec calls, want 4", n)
	}
	for _, want := range []string{
		"Applying 001_a.sql ... ok",
		"Applying 002_b.sql ... FAILED",
		"Syntax error: boom",
		"Rolling back 002_b.sql ... ok",
		"Rolling back 001_a.sql ... ok",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q:\n%s", want, stdout)
		}
	}
	if !strings.Contains(stderr, "migrations FAILED") || !strings.Contains(stderr, "previous state restored") {
		t.Errorf("no clear failure verdict:\n%s", stderr)
	}
	if strings.Contains(stdout+stderr, "SYNTAX_ERROR") || strings.Contains(stdout+stderr, "DB::Exception") {
		t.Errorf("server plumbing leaked into the output:\n%s%s", stdout, stderr)
	}
}

func TestAddMigrateRefusalOnFirstMigrationChangesNothing(t *testing.T) {
	logPath, stdinPath := installMigrateDocker(t)
	t.Setenv("DEVO_EXEC_FAIL_ON", "1")
	t.Setenv("DEVO_EXEC_ERROR", refusalOutput)
	adoptedAppWithMigrations(t)

	status, stdout, stderr := runCaptured(t, "add", "-m", "clickhouse")
	if status == 0 {
		t.Fatal("add -m succeeded although a migration refused")
	}
	// A refusal changed nothing, so its rollback must not run: the down for the
	// first migration would drop the existing database.
	if n := countExecs(readLines(t, logPath)); n != 1 {
		t.Errorf("%d exec calls, want 1 (no rollback after a refusal)", n)
	}
	if got := readFile(t, stdinPath); strings.Contains(got, "DROP") {
		t.Errorf("a rollback was sent after a refusal: %q", got)
	}
	if !strings.Contains(stdout, "migration 001 refused: database x already exists") {
		t.Errorf("reason missing:\n%s", stdout)
	}
	if !strings.Contains(stderr, "refused to run, nothing was changed") {
		t.Errorf("no clear failure verdict:\n%s", stderr)
	}
	if strings.Contains(stdout+stderr, "while executing") || strings.Contains(stdout+stderr, "DB::Exception") {
		t.Errorf("server plumbing leaked into the output:\n%s%s", stdout, stderr)
	}
}

func TestAddMigrateRefusalLaterRollsBackEarlierMigrationsOnly(t *testing.T) {
	logPath, stdinPath := installMigrateDocker(t)
	t.Setenv("DEVO_EXEC_FAIL_ON", "2")
	t.Setenv("DEVO_EXEC_ERROR", refusalOutput)
	adoptedAppWithMigrations(t)

	status, _, stderr := runCaptured(t, "add", "-m", "clickhouse")
	if status == 0 {
		t.Fatal("add -m succeeded although a migration refused")
	}
	if got, want := readFile(t, stdinPath), "SELECT 1;\nSELECT 2;\nDROP 1;\n"; got != want {
		t.Errorf("piped %q, want only 001 undone (%q)", got, want)
	}
	if n := countExecs(readLines(t, logPath)); n != 3 {
		t.Errorf("%d exec calls, want 3", n)
	}
	if !strings.Contains(stderr, "rolled back 1 earlier migration(s)") {
		t.Errorf("no clear failure verdict:\n%s", stderr)
	}
}

func TestAddMigrateReportsFailedRollback(t *testing.T) {
	logPath, _ := installMigrateDocker(t)
	t.Setenv("DEVO_EXEC_FAIL_ON", "2,3")
	t.Setenv("DEVO_EXEC_ERROR", "Code: 62. DB::Exception: Syntax error: boom. (SYNTAX_ERROR)")
	adoptedAppWithMigrations(t)

	status, stdout, stderr := runCaptured(t, "add", "-m", "clickhouse")
	if status == 0 {
		t.Fatal("add -m succeeded")
	}
	if n := countExecs(readLines(t, logPath)); n != 3 {
		t.Errorf("%d exec calls, want 3 (stop at the first failed rollback)", n)
	}
	if !strings.Contains(stdout, "Rolling back 002_b.sql ... FAILED") {
		t.Errorf("failed rollback not shown:\n%s", stdout)
	}
	for _, want := range []string{"ROLLBACK FAILED at 002_b.down.sql", "002_b.down.sql, 001_a.down.sql"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("verdict missing %q:\n%s", want, stderr)
		}
	}
}

func TestAddMigrateRequiresRollbackFiles(t *testing.T) {
	logPath, _ := installMigrateDocker(t)
	root := adoptedAppWithMigrations(t)
	if err := os.Remove(filepath.Join(root, "platform/clickhouse/init/002_b.down.sql")); err != nil {
		t.Fatal(err)
	}

	status, _, stderr := runCaptured(t, "add", "-m", "clickhouse")
	if status == 0 {
		t.Fatal("add -m ran migrations that have no rollback")
	}
	if !strings.Contains(stderr, "missing 002_b.down.sql") {
		t.Errorf("missing file not named:\n%s", stderr)
	}
	if lines := readLines(t, logPath); len(lines) != 0 {
		t.Errorf("docker was called before the rollback check: %v", lines)
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
