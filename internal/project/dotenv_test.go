package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readFile(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

func TestEnablingAServiceWritesItsEndpointAndCredentials(t *testing.T) {
	root := t.TempDir()
	if err := GenerateFiles(root, cfg("", "clickhouse")); err != nil {
		t.Fatal(err)
	}

	env := readFile(t, root, ".env")
	if !strings.Contains(env, "CLICKHOUSE_URL=http://127.0.0.1:8123\n") {
		t.Fatalf(".env lacks the ClickHouse endpoint:\n%s", env)
	}
	if strings.Contains(env, "PASSWORD") {
		t.Fatalf(".env must not hold credentials:\n%s", env)
	}

	auth := readFile(t, root, ".auth")
	for _, want := range []string{"CLICKHOUSE_USER=app\n", "CLICKHOUSE_PASSWORD=app\n"} {
		if !strings.Contains(auth, want) {
			t.Fatalf(".auth lacks %q:\n%s", want, auth)
		}
	}
	info, err := os.Stat(filepath.Join(root, ".auth"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf(".auth mode = %o, want 600", perm)
	}

	ignore := readFile(t, root, ".gitignore")
	for _, want := range []string{".env\n", ".auth\n"} {
		if !strings.Contains(ignore, want) {
			t.Fatalf(".gitignore does not list %q:\n%s", want, ignore)
		}
	}
}

func TestProjectParamsReachTheCredentials(t *testing.T) {
	root := t.TempDir()
	c := cfg("", "postgres")
	c.Settings = map[string]map[string]any{"postgres": {"user": "dev", "password": "s3cret pass", "database": "orders"}}
	if err := GenerateFiles(root, c); err != nil {
		t.Fatal(err)
	}
	auth := readFile(t, root, ".auth")
	if !strings.Contains(auth, "POSTGRES_USER=dev\n") || !strings.Contains(auth, "POSTGRES_PASSWORD='s3cret pass'\n") {
		t.Fatalf("overridden credentials missing or unquoted:\n%s", auth)
	}
	if !strings.Contains(readFile(t, root, ".env"), "POSTGRES_DB=orders\n") {
		t.Fatalf(".env ignores the database override:\n%s", readFile(t, root, ".env"))
	}
}

func TestExistingEnvAndAuthValuesSurviveRegeneration(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("APP_MODE=dev\nCLICKHOUSE_URL=http://db.internal:8123"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".auth"), []byte("export CLICKHOUSE_PASSWORD=hardened\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := GenerateFiles(root, cfg("", "clickhouse", "redis")); err != nil {
			t.Fatal(err)
		}
	}

	env := readFile(t, root, ".env")
	if !strings.Contains(env, "APP_MODE=dev\n") || !strings.Contains(env, "CLICKHOUSE_URL=http://db.internal:8123\n") {
		t.Fatalf("existing .env entries were lost or changed:\n%s", env)
	}
	if strings.Count(env, "CLICKHOUSE_URL=") != 1 {
		t.Fatalf("CLICKHOUSE_URL duplicated:\n%s", env)
	}
	if !strings.Contains(env, "REDIS_URL=redis://127.0.0.1:6379\n") {
		t.Fatalf("new service endpoint not added:\n%s", env)
	}

	auth := readFile(t, root, ".auth")
	if strings.Contains(auth, "CLICKHOUSE_PASSWORD=app") {
		t.Fatalf("a hardened password was overwritten:\n%s", auth)
	}
	if !strings.Contains(auth, "CLICKHOUSE_USER=app\n") {
		t.Fatalf("missing credential name was not added:\n%s", auth)
	}
}

func TestGitignoreIsExtendedNotReplaced(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("target/\n/.env"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := GenerateFiles(root, cfg("", "clickhouse")); err != nil {
		t.Fatal(err)
	}
	if got, want := readFile(t, root, ".gitignore"), "target/\n/.env\n.auth\n"; got != want {
		t.Fatalf(".gitignore = %q, want %q", got, want)
	}
}

func TestServicesWithoutEnvOrAuthLeaveNoFiles(t *testing.T) {
	root := t.TempDir()
	if err := GenerateFiles(root, cfg("")); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{".env", ".auth"} {
		if exists(t, root, f) {
			t.Fatalf("%s written for a project with no services", f)
		}
	}
}

func TestDotenvQuote(t *testing.T) {
	for in, want := range map[string]string{
		"app":         "app",
		"http://h:80": "http://h:80",
		"":            "''",
		"has space":   "'has space'",
		"a#b":         "'a#b'",
		"it's":        `"it's"`,
		`q"$x`:        `'q"$x'`,
		`it's $x`:     `"it's \$x"`,
	} {
		if got := dotenvQuote(in); got != want {
			t.Errorf("dotenvQuote(%q) = %s, want %s", in, got, want)
		}
	}
}
