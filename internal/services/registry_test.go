package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRegistry(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "devo.services.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Load() }) // restore the built-in registry
	return p
}

// A misspelled field used to be dropped silently, leaving a service without
// its params or compose settings.
func TestMisspelledFieldIsRejected(t *testing.T) {
	p := writeRegistry(t, `
services:
  cache: false

cache:
  descripton: typo
  compose:
    image: "redis:7"
`)
	err := Load(p)
	if err == nil {
		t.Fatal("expected an error for the misspelled field, got nil")
	}
	for _, want := range []string{`service "cache"`, `unknown field "descripton"`, "description"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
}

func TestValidCustomServiceLoads(t *testing.T) {
	p := writeRegistry(t, `
services:
  cache: false

cache:
  description: my cache
  params:
    version: "7"
  compose:
    image: "redis:{{version}}"
`)
	if err := Load(p); err != nil {
		t.Fatalf("valid custom service rejected: %v", err)
	}
	if !Valid("cache") || !Valid("postgres") {
		t.Error("custom service should be added alongside the built-ins")
	}
}

func TestEveryBuiltInFieldIsKnown(t *testing.T) {
	// Load() ran in init() and would have panicked; this pins the field set
	// the check is derived from.
	for _, f := range []string{"description", "params", "compose", "volumes", "files",
		"smoke", "datasource", "datasources_file", "plugins", "plugins_env"} {
		if !serviceFields()[f] {
			t.Errorf("%q missing from the known service fields", f)
		}
	}
}