package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Compustretch/devo/internal/services"
)

// installMigrateDocker puts a fake docker on PATH. It logs every compose call,
// records what is piped to `exec`, and can fail the Nth exec.
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
  *" exec "*)
    n=$(cat "$DEVO_EXEC_COUNT" 2>/dev/null || echo 0)
    n=$((n + 1))
    echo "$n" > "$DEVO_EXEC_COUNT"
    cat >> "$DEVO_EXEC_STDIN"
    if [ "$n" = "${DEVO_EXEC_FAIL_ON:-0}" ]; then exit 1; fi
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
// returns its root. The init directory holds forward migrations plus the files
// that sit beside them and must never be applied.
func adoptedAppWithMigrations(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "app")
	files := map[string]string{
		"Cargo.toml":                                   "[package]\nname = \"app\"\n",
		"platform/clickhouse/init/002_b.sql":           "SELECT 2;\n",
		"platform/clickhouse/init/001_a.sql":           "SELECT 1;\n",
		"platform/clickhouse/init/001_a.down.sql":      "DROP DATABASE x;\n",
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

func TestAddMigrateAppliesForwardMigrationsInOrder(t *testing.T) {
	for _, args := range [][]string{
		{"add", "-m", "clickhouse"},
		{"add", "--migrate", "clickhouse"},
		{"add", "clickhouse", "-m"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			logPath, stdinPath := installMigrateDocker(t)
			adoptedAppWithMigrations(t)

			if status := Run(args); status != 0 {
				t.Fatalf("%v returned %d, want success", args, status)
			}

			lines := readLines(t, logPath)
			if len(lines) != 3 {
				t.Fatalf("want 1 up and 2 exec calls, got %d:\n%s", len(lines), strings.Join(lines, "\n"))
			}
			if !strings.Contains(lines[0], "up --detach --wait clickhouse") {
				t.Errorf("first call should start only clickhouse: %q", lines[0])
			}
			for _, l := range lines[1:] {
				if !strings.Contains(l, "exec -T clickhouse sh -c") || !strings.Contains(l, "clickhouse-client") {
					t.Errorf("unexpected exec call: %q", l)
				}
			}
			piped, err := os.ReadFile(stdinPath)
			if err != nil {
				t.Fatal(err)
			}
			if got, want := string(piped), "SELECT 1;\nSELECT 2;\n"; got != want {
				t.Errorf("piped to the container %q, want %q (forward migrations only, in name order)", got, want)
			}
		})
	}
}

func TestAddMigrateStopsAtFirstFailure(t *testing.T) {
	logPath, stdinPath := installMigrateDocker(t)
	t.Setenv("DEVO_EXEC_FAIL_ON", "1")
	adoptedAppWithMigrations(t)

	if status := Run([]string{"add", "-m", "clickhouse"}); status == 0 {
		t.Fatal("add -m succeeded although the first migration failed")
	}
	execs := 0
	for _, l := range readLines(t, logPath) {
		if strings.Contains(l, " exec ") {
			execs++
		}
	}
	if execs != 1 {
		t.Errorf("ran %d migrations after the first failed, want 1 attempt", execs)
	}
	piped, _ := os.ReadFile(stdinPath)
	if strings.Contains(string(piped), "SELECT 2;") {
		t.Errorf("the second migration was sent after the first failed: %q", piped)
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

func TestMigrateFlagOnlyAppliesToAdd(t *testing.T) {
	logPath, _ := installMigrateDocker(t)
	adoptedAppWithMigrations(t)
	if status := Run([]string{"add", "clickhouse"}); status != 0 {
		t.Fatalf("setup add returned %d", status)
	}

	if status := Run([]string{"remove", "-m", "clickhouse"}); status == 0 {
		t.Error("remove -m succeeded; -m belongs to add")
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

// The migrate command takes credentials from the container's own environment,
// so one registry entry serves the app user of a new project and the default
// user of an adopted one.
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
		"app user":              {[]string{"CLICKHOUSE_USER=app", "CLICKHOUSE_PASSWORD=p w"}, "--user app --password p w --multiquery"},
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
			want := strings.ReplaceAll(c.want, "p w", "p_w")
			if got != want {
				t.Errorf("client args = %q, want %q", got, want)
			}
		})
	}
}
