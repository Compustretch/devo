package project

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Compustretch/devo/internal/services"
)

func TestGeneratedPathsCannotEscapeProject(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "outside.txt")
	if err := os.WriteFile(outside, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := write(root, "../outside.txt", "should not be written"); err == nil {
		t.Fatal("expected a traversal path to be rejected")
	}
	removeAndPrune(root, "../outside.txt")
	if b, err := os.ReadFile(outside); err != nil || string(b) != "keep" {
		t.Fatalf("traversal cleanup modified %s: content=%q err=%v", outside, b, err)
	}

	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "keep.txt")
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "external")
	if err := os.Symlink(outsideDir, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := write(root, "external/created.txt", "should not be written"); err == nil {
		t.Fatal("expected a symlink escaping the project to be rejected")
	}
	removeAndPrune(root, "external/keep.txt")
	if _, err := os.Stat(outsideFile); err != nil {
		t.Fatalf("cleanup touched a file outside the project: %v", err)
	}
}

func TestGrafanaInstallsPluginsFromMultipleProviders(t *testing.T) {
	registryPath := filepath.Join(t.TempDir(), "devo.services.yaml")
	registry := `services:
  z-provider: false

z-provider:
  description: second datasource provider
  plugins: [second-datasource-plugin]
  compose:
    image: example/second-provider:latest
  datasource:
    name: Secondary
    uid: secondary
    type: second-datasource-plugin
    access: proxy
    url: http://z-provider:8080
    editable: false
`
	if err := os.WriteFile(registryPath, []byte(registry), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := services.Load(); err != nil {
			t.Errorf("restore built-in service registry: %v", err)
		}
	})
	if err := services.Load(registryPath); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	if err := GenerateFiles(root, cfg("rust", "clickhouse", "grafana", "z-provider")); err != nil {
		t.Fatal(err)
	}
	compose := readYAML(t, root, "compose.yaml")
	env := compose["services"].(map[string]any)["grafana"].(map[string]any)["environment"].(map[string]any)
	const want = "grafana-clickhouse-datasource,second-datasource-plugin"
	if got := env["GF_INSTALL_PLUGINS"]; got != want {
		t.Errorf("GF_INSTALL_PLUGINS = %v, want %q", got, want)
	}
}

func TestCustomServiceEnableGenerateDisableLifecycle(t *testing.T) {
	registryPath := filepath.Join(t.TempDir(), "devo.services.yaml")
	registry := `services:
  custom-cache: false

custom-cache:
  description: custom cache
  params:
    version: "7"
  compose:
    image: "redis:{{version}}"
  files:
    platform/custom-cache/cache.conf: |
      maxmemory 32mb
  smoke:
    rust:
      tests/custom_cache_smoke.rs: |
        #[test]
        fn custom_cache_smoke_placeholder() {}
`
	if err := os.WriteFile(registryPath, []byte(registry), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := services.Load(); err != nil {
			t.Errorf("restore built-in service registry: %v", err)
		}
	})
	if err := services.Load(registryPath); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	if err := GenerateFiles(root, cfg("rust", "custom-cache")); err != nil {
		t.Fatal(err)
	}
	compose := readYAML(t, root, "compose.yaml")
	cache := compose["services"].(map[string]any)["custom-cache"].(map[string]any)
	if cache["image"] != "redis:7" {
		t.Errorf("custom service image = %v, want redis:7", cache["image"])
	}
	for _, rel := range []string{"platform/custom-cache/cache.conf", "tests/custom_cache_smoke.rs"} {
		if !exists(t, root, rel) {
			t.Errorf("custom service did not generate %s", rel)
		}
	}

	if err := GenerateFiles(root, cfg("rust")); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"platform/custom-cache/cache.conf", "tests/custom_cache_smoke.rs"} {
		if exists(t, root, rel) {
			t.Errorf("disabling custom service left %s behind", rel)
		}
	}
}

func TestCleanupKeepsSharedSmokePathUntilLastServiceIsDisabled(t *testing.T) {
	registryPath := filepath.Join(t.TempDir(), "devo.services.yaml")
	registry := `services:
  first: false
  second: false

first:
  description: first shared-path service
  compose:
    image: example/first:latest
  smoke:
    rust:
      tests/shared_smoke.rs: |
        // first service
        #[test] fn shared_smoke() {}

second:
  description: second shared-path service
  compose:
    image: example/second:latest
  smoke:
    rust:
      tests/shared_smoke.rs: |
        // second service
        #[test] fn shared_smoke() {}
`
	if err := os.WriteFile(registryPath, []byte(registry), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := services.Load(); err != nil {
			t.Errorf("restore built-in service registry: %v", err)
		}
	})
	if err := services.Load(registryPath); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	if err := GenerateFiles(root, cfg("rust", "first", "second")); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(root, "tests", "shared_smoke.rs")
	if _, err := os.Stat(shared); err != nil {
		t.Fatalf("shared smoke test was not generated: %v", err)
	}
	if err := GenerateFiles(root, cfg("rust", "second")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(shared); err != nil {
		t.Fatalf("disabling first service removed a smoke test still owned by second: %v", err)
	}
	if err := GenerateFiles(root, cfg("rust")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(shared); !os.IsNotExist(err) {
		t.Errorf("shared smoke test should be removed after both services are disabled, err=%v", err)
	}
}
