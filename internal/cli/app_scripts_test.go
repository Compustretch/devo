package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func installLifecycleDocker(t *testing.T) string {
	t.Helper()
	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "docker.log")
	script := `#!/bin/sh
if [ "$1" = "info" ]; then exit 0; fi
if [ "$1" = "context" ] && [ "$2" = "show" ]; then printf 'default\n'; exit 0; fi
if [ "$1" = "compose" ] && [ "$2" = "version" ]; then exit 0; fi
printf '%s\n' "$*" >> "$DEVO_DOCKER_LOG"
case " $* " in
  *" up "*)
    if [ -n "${DEVO_COMPOSE_READY_FILE:-}" ]; then : > "$DEVO_COMPOSE_READY_FILE"; fi
    exit "${DEVO_COMPOSE_UP_EXIT:-0}" ;;
  *" down "*)
    if [ -n "${DEVO_APP_SCRIPT_LOG:-}" ]; then printf 'compose:down\n' >> "$DEVO_APP_SCRIPT_LOG"; fi
    exit 0 ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DEVO_DOCKER_LOG", logPath)
	t.Setenv("DEVO_COMPOSE_UP_EXIT", "0")
	return logPath
}

func lifecycleProject(t *testing.T) string {
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

func writeLifecycleScript(t *testing.T, root, phase, name, body string) {
	t.Helper()
	path := filepath.Join(root, "scripts", phase, name+".sh")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+body+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDevRunsStartScriptsAfterComposeReadiness(t *testing.T) {
	installLifecycleDocker(t)
	root := lifecycleProject(t)
	readyFile := filepath.Join(root, "compose-ready")
	logFile := filepath.Join(root, "app-scripts.log")
	t.Setenv("DEVO_COMPOSE_READY_FILE", readyFile)
	t.Setenv("DEVO_APP_SCRIPT_LOG", logFile)
	writeLifecycleScript(t, root, "start", "api", `test -f "$DEVO_COMPOSE_READY_FILE"
printf 'start:api\n' >> "$DEVO_APP_SCRIPT_LOG"`)
	writeLifecycleScript(t, root, "stop", "api", `printf 'stop:api\n' >> "$DEVO_APP_SCRIPT_LOG"`)

	if status := Run([]string{"dev", "--dir", root}); status != 0 {
		t.Fatalf("dev returned %d, want success", status)
	}
	got, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(got)) != "start:api" {
		t.Fatalf("application lifecycle log = %q, want start:api", got)
	}
}

func TestDevRejectsUnpairedScriptsBeforeStartingCompose(t *testing.T) {
	logPath := installLifecycleDocker(t)
	root := lifecycleProject(t)
	writeLifecycleScript(t, root, "start", "api", "exit 0")

	if status := Run([]string{"dev", "--dir", root}); status == 0 {
		t.Fatal("dev succeeded despite a start script without a matching stop script")
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("Compose was invoked despite invalid script pairs; docker log stat error = %v", err)
	}
}

func TestDevRollsBackApplicationAndComposeWhenStartFails(t *testing.T) {
	logPath := installLifecycleDocker(t)
	root := lifecycleProject(t)
	appLog := filepath.Join(root, "app-scripts.log")
	t.Setenv("DEVO_APP_SCRIPT_LOG", appLog)
	writeLifecycleScript(t, root, "start", "api", `printf 'start:api\n' >> "$DEVO_APP_SCRIPT_LOG"
exit 7`)
	writeLifecycleScript(t, root, "stop", "api", `printf 'stop:api\n' >> "$DEVO_APP_SCRIPT_LOG"`)

	if status := Run([]string{"dev", "--dir", root}); status == 0 {
		t.Fatal("dev returned success after the application start script failed")
	}
	got, err := os.ReadFile(appLog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(got)) != "start:api\nstop:api\ncompose:down" {
		t.Fatalf("application rollback log = %q, want start then stop", got)
	}
	dockerLog, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dockerLog), "up --build --detach --wait") || !strings.Contains(string(dockerLog), "down") {
		t.Fatalf("expected Compose startup and rollback commands, got %q", dockerLog)
	}
}

func TestDownStopsApplicationsInReverseOrderBeforeCompose(t *testing.T) {
	installLifecycleDocker(t)
	root := lifecycleProject(t)
	appLog := filepath.Join(root, "app-scripts.log")
	t.Setenv("DEVO_APP_SCRIPT_LOG", appLog)
	for _, name := range []string{"alpha", "beta"} {
		writeLifecycleScript(t, root, "start", name, "exit 0")
		writeLifecycleScript(t, root, "stop", name, `printf 'stop:`+name+`\n' >> "$DEVO_APP_SCRIPT_LOG"`)
	}

	if status := Run([]string{"down", "--dir", root}); status != 0 {
		t.Fatalf("down returned %d, want success", status)
	}
	got, err := os.ReadFile(appLog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(got)) != "stop:beta\nstop:alpha\ncompose:down" {
		t.Fatalf("shutdown order = %q, want beta, alpha, then Compose", got)
	}
}

func TestDevServicesOnlySkipsApplicationStartScripts(t *testing.T) {
	for _, flagName := range []string{"-s", "--services-only"} {
		t.Run(flagName, func(t *testing.T) {
			logPath := installLifecycleDocker(t)
			root := lifecycleProject(t)
			appLog := filepath.Join(root, "app-scripts.log")
			t.Setenv("DEVO_APP_SCRIPT_LOG", appLog)
			writeLifecycleScript(t, root, "start", "api", `printf 'start:api\n' >> "$DEVO_APP_SCRIPT_LOG"`)
			writeLifecycleScript(t, root, "stop", "api", `printf 'stop:api\n' >> "$DEVO_APP_SCRIPT_LOG"`)

			if status := Run([]string{"dev", flagName, "--dir", root}); status != 0 {
				t.Fatalf("dev %s returned %d, want success", flagName, status)
			}
			if _, err := os.Stat(appLog); !os.IsNotExist(err) {
				t.Fatalf("application scripts ran under %s; log stat error = %v", flagName, err)
			}
			dockerLog, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(dockerLog), "up --build --detach --wait") {
				t.Fatalf("expected Compose startup under %s, got %q", flagName, dockerLog)
			}
		})
	}
}

func TestDevServicesOnlyIgnoresUnpairedScripts(t *testing.T) {
	logPath := installLifecycleDocker(t)
	root := lifecycleProject(t)
	writeLifecycleScript(t, root, "start", "api", "exit 0")

	if status := Run([]string{"dev", "--services-only", "--dir", root}); status != 0 {
		t.Fatalf("dev --services-only returned %d, want success despite an unpaired start script", status)
	}
	dockerLog, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dockerLog), "up --build --detach --wait") {
		t.Fatalf("expected Compose startup, got %q", dockerLog)
	}
}
