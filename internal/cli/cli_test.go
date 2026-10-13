package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
