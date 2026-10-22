package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Compustretch/devo/internal/services"
)

func TestVersionFlagPrintsInjectedVersion(t *testing.T) {
	previousVersion := appVersion
	appVersion = "test-build-version"
	t.Cleanup(func() { appVersion = previousVersion })

	previousStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = previousStdout })

	status := Run([]string{"--version"})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = previousStdout
	output, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if status != 0 {
		t.Fatalf("version command returned %d", status)
	}
	if got, want := strings.TrimSpace(string(output)), "devo test-build-version"; got != want {
		t.Errorf("version output = %q, want %q", got, want)
	}
}

func installFakeDocker(t *testing.T, composeExit string) string {
	t.Helper()
	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "docker.log")
	script := `#!/bin/sh
if [ "$1" = "info" ]; then exit 0; fi
if [ "$1" = "context" ] && [ "$2" = "show" ]; then printf 'default\n'; exit 0; fi
if [ "$1" = "compose" ] && [ "$2" = "version" ]; then exit 0; fi
printf '%s\n' "$*" >> "$DEVO_DOCKER_LOG"
case " $* " in
  *" up "*) exit "${DEVO_COMPOSE_UP_EXIT:-0}" ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DEVO_DOCKER_LOG", logPath)
	t.Setenv("DEVO_COMPOSE_UP_EXIT", composeExit)
	return logPath
}

func createDevProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range map[string]string{
		"devo.yaml":    "name: test\nlanguage: rust\nservices: {}\n",
		"compose.yaml": "services: {}\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestDevWaitsForComposeReadiness(t *testing.T) {
	logPath := installFakeDocker(t, "0")
	root := createDevProject(t)
	if status := Run([]string{"dev", "--dir", root}); status != 0 {
		t.Fatalf("dev returned %d, want success", status)
	}
	args, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "up --build --detach --wait") {
		t.Errorf("devo dev must wait for services to become healthy; docker args were %q", args)
	}
}

func TestDevPropagatesComposeStartupFailure(t *testing.T) {
	logPath := installFakeDocker(t, "17")
	root := createDevProject(t)
	if status := Run([]string{"dev", "--dir", root}); status == 0 {
		t.Fatal("dev returned success after Compose reported an unhealthy/failed startup")
	}
	args, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "up --build --detach --wait") {
		t.Errorf("devo dev did not invoke Compose with --wait: %q", args)
	}
}

func TestAddLoadsRegistryFromTargetDirectory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(func() {
		if err := services.Load(); err != nil {
			t.Errorf("restore built-in service registry: %v", err)
		}
	})
	root := t.TempDir()

	registry := `services:
  custom-cache: false

custom-cache:
  description: target project cache
  params:
    version: "7"
  compose:
    image: "redis:{{version}}"
`
	if err := os.WriteFile(filepath.Join(root, "devo.services.yaml"), []byte(registry), 0o644); err != nil {
		t.Fatal(err)
	}
	config := "name: target\nlanguage: rust\nservices:\n  custom-cache: false\n"
	if err := os.WriteFile(filepath.Join(root, "devo.yaml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	if status := Run([]string{"add", "custom-cache", "--dir", root}); status != 0 {
		t.Fatalf("add returned %d for service defined in target project's registry", status)
	}

	updated, err := os.ReadFile(filepath.Join(root, "devo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updated), "custom-cache: true") {
		t.Errorf("target project's service wasn't enabled in devo.yaml:\n%s", updated)
	}
	compose, err := os.ReadFile(filepath.Join(root, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(compose), "image: redis:7") {
		t.Errorf("target project's service definition wasn't used to generate compose.yaml:\n%s", compose)
	}
}

func chdirTemp(t *testing.T, name string) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	root := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	return root
}

func TestInitInPlaceByDot(t *testing.T) {
	root := chdirTemp(t, "OvenRouter")
	if err := os.WriteFile("Cargo.toml", []byte("[package]\nname = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if status := Run([]string{"init", "rust", "."}); status != 0 {
		t.Fatalf("init rust . returned %d", status)
	}

	cfg, err := os.ReadFile(filepath.Join(root, "devo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`name: "ovenrouter"`, `language: "rust"`, "clickhouse: false"} {
		if !strings.Contains(string(cfg), want) {
			t.Errorf("devo.yaml missing %q:\n%s", want, cfg)
		}
	}
	for _, line := range strings.Split(string(cfg), "\n") {
		if strings.HasPrefix(line, "  ") && strings.HasSuffix(line, ": true") {
			t.Errorf("in-place init enabled a service: %q", line)
		}
	}
	if !strings.Contains(string(cfg), "adopted: true") {
		t.Errorf("devo.yaml doesn't record that the project was adopted:\n%s", cfg)
	}
	if _, err := os.Stat(filepath.Join(root, "compose.yaml")); err != nil {
		t.Errorf("compose.yaml not written: %v", err)
	}
	entries, _ := os.ReadDir(root)
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if len(got) != 3 { // Cargo.toml, devo.yaml, compose.yaml
		t.Errorf("in-place init touched extra paths: %v", got)
	}
}

func TestInitInPlaceByMatchingName(t *testing.T) {
	root := chdirTemp(t, "OvenRouter")

	if status := Run([]string{"init", "rust", "ovenrouter"}); status != 0 {
		t.Fatalf("init rust ovenrouter returned %d", status)
	}
	if _, err := os.Stat(filepath.Join(root, "devo.yaml")); err != nil {
		t.Errorf("devo.yaml not written in cwd: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "ovenrouter")); !os.IsNotExist(err) {
		t.Errorf("created a nested directory instead of adopting cwd")
	}
}

func TestInitInPlaceRefusesExistingConfig(t *testing.T) {
	root := chdirTemp(t, "app")
	for _, f := range []string{"devo.yaml", "compose.yaml"} {
		if err := os.WriteFile(f, []byte("keep\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if status := Run([]string{"init", "rust", "."}); status == 0 {
			t.Fatalf("init succeeded over existing %s", f)
		}
		b, _ := os.ReadFile(filepath.Join(root, f))
		if string(b) != "keep\n" {
			t.Errorf("%s was modified", f)
		}
		_ = os.Remove(f)
	}
}

func TestInitInPlaceNameOverride(t *testing.T) {
	root := chdirTemp(t, "My.Repo")
	if status := Run([]string{"init", "rust", "."}); status == 0 {
		t.Fatal("invalid directory name accepted without --name")
	}
	if status := Run([]string{"init", "--name", "my-repo", "rust", "."}); status != 0 {
		t.Fatalf("init --name returned %d", status)
	}
	cfg, _ := os.ReadFile(filepath.Join(root, "devo.yaml"))
	if !strings.Contains(string(cfg), `name: "my-repo"`) {
		t.Errorf("--name not applied:\n%s", cfg)
	}
}

func TestInitStillCreatesChildDirectory(t *testing.T) {
	root := chdirTemp(t, "parent")
	if status := Run([]string{"init", "rust", "child"}); status != 0 {
		t.Fatalf("init rust child returned %d", status)
	}
	if _, err := os.Stat(filepath.Join(root, "child", "devo.yaml")); err != nil {
		t.Errorf("child project not created: %v", err)
	}
	if status := Run([]string{"init", "rust", "child"}); status == 0 {
		t.Error("init overwrote an existing child directory")
	}
}

func TestAddClickhouseInAdoptedProjectHasNoSideEffects(t *testing.T) {
	root := chdirTemp(t, "OvenRouter")
	if err := os.WriteFile("Cargo.toml", []byte("[package]\nname = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	initDir := filepath.Join("platform", "clickhouse", "init")
	if err := os.MkdirAll(initDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mine := filepath.Join(initDir, "001_route_decisions.sql")
	if err := os.WriteFile(mine, []byte("-- app migration\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if status := Run([]string{"init", "rust", "."}); status != 0 {
		t.Fatalf("init returned %d", status)
	}
	if status := Run([]string{"add", "clickhouse"}); status != 0 {
		t.Fatalf("add clickhouse returned %d", status)
	}

	for _, path := range []string{
		"platform/clickhouse/init/001-events.sql",
		"platform/grafana",
		"tests",
	} {
		if _, err := os.Stat(filepath.Join(root, path)); !os.IsNotExist(err) {
			t.Errorf("add clickhouse wrote %s into an adopted project", path)
		}
	}
	if b, _ := os.ReadFile(mine); string(b) != "-- app migration\n" {
		t.Errorf("app's own init script was modified: %q", b)
	}

	compose, err := os.ReadFile(filepath.Join(root, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"docker-entrypoint-initdb.d", "CLICKHOUSE_USER", "CLICKHOUSE_PASSWORD", "CLICKHOUSE_DB"} {
		if strings.Contains(string(compose), bad) {
			t.Errorf("adopted compose.yaml contains %q:\n%s", bad, compose)
		}
	}
	if !strings.Contains(string(compose), "8123:8123") {
		t.Errorf("compose.yaml lost the HTTP port:\n%s", compose)
	}

	// Removing it must not touch the app's files either.
	if status := Run([]string{"remove", "clickhouse"}); status != 0 {
		t.Fatalf("remove clickhouse returned %d", status)
	}
	if _, err := os.Stat(mine); err != nil {
		t.Errorf("remove deleted the app's own init script: %v", err)
	}
}

func TestAddClickhouseInCreatedProjectIsUnchanged(t *testing.T) {
	root := chdirTemp(t, "parent")
	if status := Run([]string{"init", "rust", "child"}); status != 0 {
		t.Fatalf("init returned %d", status)
	}
	child := filepath.Join(root, "child")
	if status := Run([]string{"add", "clickhouse", "--dir", child}); status != 0 {
		t.Fatalf("add returned %d", status)
	}
	for _, path := range []string{"platform/clickhouse/init/001-events.sql", "tests/clickhouse_smoke.rs"} {
		if _, err := os.Stat(filepath.Join(child, path)); err != nil {
			t.Errorf("created project is missing %s: %v", path, err)
		}
	}
	compose, _ := os.ReadFile(filepath.Join(child, "compose.yaml"))
	if !strings.Contains(string(compose), "CLICKHOUSE_USER") {
		t.Errorf("created project lost its credentials:\n%s", compose)
	}
}
