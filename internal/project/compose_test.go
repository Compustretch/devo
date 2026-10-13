package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Compustretch/devo/internal/services"
	"gopkg.in/yaml.v3"
)

func exists(t *testing.T, root, rel string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(root, rel))
	return err == nil
}

func readYAML(t *testing.T, root, rel string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := yaml.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s is not valid YAML: %v", rel, err)
	}
	return m
}

func cfg(language string, on ...string) Config {
	c := Config{Name: "app", Language: language, Services: map[string]bool{}}
	for _, s := range on {
		c.Services[s] = true
	}
	return c
}

func TestGrafanaGetsPluginsAndDependencies(t *testing.T) {
	root := t.TempDir()
	if err := GenerateFiles(root, cfg("rust", "clickhouse", "postgres", "grafana")); err != nil {
		t.Fatal(err)
	}
	compose := readYAML(t, root, "compose.yaml")
	graf := compose["services"].(map[string]any)["grafana"].(map[string]any)

	env := graf["environment"].(map[string]any)
	if env["GF_INSTALL_PLUGINS"] != "grafana-clickhouse-datasource" {
		t.Errorf("GF_INSTALL_PLUGINS = %v, want the ClickHouse plugin", env["GF_INSTALL_PLUGINS"])
	}
	if env["GF_SECURITY_ADMIN_USER"] == nil {
		t.Error("existing Grafana environment must be preserved")
	}
	deps := graf["depends_on"].(map[string]any)
	if deps["clickhouse"] == nil || deps["postgres"] == nil {
		t.Errorf("grafana should wait for clickhouse and postgres, got %v", deps)
	}

	ds := readYAML(t, root, "platform/grafana/provisioning/datasources/datasource.yaml")
	if n := len(ds["datasources"].([]any)); n != 2 {
		t.Errorf("want 2 provisioned datasources, got %d", n)
	}
}

func TestNoPluginsWhenNoProviderNeedsOne(t *testing.T) {
	root := t.TempDir()
	if err := GenerateFiles(root, cfg("rust", "postgres", "grafana")); err != nil {
		t.Fatal(err)
	}
	compose := readYAML(t, root, "compose.yaml")
	env := compose["services"].(map[string]any)["grafana"].(map[string]any)["environment"].(map[string]any)
	if _, set := env["GF_INSTALL_PLUGINS"]; set {
		t.Errorf("GF_INSTALL_PLUGINS should be unset, got %v", env["GF_INSTALL_PLUGINS"])
	}
}

func TestGrafanaConfiguredPluginsAreDeduplicated(t *testing.T) {
	registryPath := filepath.Join(t.TempDir(), "devo.services.yaml")
	registry := `grafana:
  description: Grafana dashboards
  plugins_env: GF_INSTALL_PLUGINS
  datasources_file: platform/grafana/provisioning/datasources/datasource.yaml
  compose:
    image: grafana/grafana:11.6.0
    environment:
      GF_INSTALL_PLUGINS: "custom-plugin, grafana-clickhouse-datasource,custom-plugin"
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
	if err := GenerateFiles(root, cfg("rust", "clickhouse", "grafana")); err != nil {
		t.Fatal(err)
	}
	compose := readYAML(t, root, "compose.yaml")
	env := compose["services"].(map[string]any)["grafana"].(map[string]any)["environment"].(map[string]any)
	const want = "custom-plugin,grafana-clickhouse-datasource"
	if got := env["GF_INSTALL_PLUGINS"]; got != want {
		t.Errorf("GF_INSTALL_PLUGINS = %v, want %q", got, want)
	}
}

func TestRemovingServicesCleansUpTheirFiles(t *testing.T) {
	root := t.TempDir()
	must := func(c Config) {
		t.Helper()
		if err := GenerateFiles(root, c); err != nil {
			t.Fatal(err)
		}
	}

	must(cfg("rust", "clickhouse", "grafana"))
	for _, p := range []string{
		"platform/clickhouse/init/001-events.sql",
		"platform/grafana/dashboards/events.json",
		"platform/grafana/provisioning/datasources/datasource.yaml",
		"tests/clickhouse_smoke.rs",
		"tests/grafana_smoke.rs",
	} {
		if !exists(t, root, p) {
			t.Fatalf("expected %s after enabling", p)
		}
	}

	// Drop ClickHouse: its files and test go, its now-empty dirs go, Grafana stays.
	must(cfg("rust", "grafana"))
	for _, p := range []string{
		"platform/clickhouse",
		"platform/grafana/dashboards/events.json",
		"tests/clickhouse_smoke.rs",
	} {
		if exists(t, root, p) {
			t.Errorf("%s should be gone after removing clickhouse", p)
		}
	}
	for _, p := range []string{"platform/grafana/provisioning/dashboards/dashboard.yaml", "tests/grafana_smoke.rs"} {
		if !exists(t, root, p) {
			t.Errorf("%s should remain while grafana is enabled", p)
		}
	}

	// Drop everything: no generated leftovers, no empty directories.
	must(cfg("rust"))
	for _, p := range []string{"platform", "tests"} {
		if exists(t, root, p) {
			t.Errorf("%s should be removed once nothing is enabled", p)
		}
	}
	if !exists(t, root, "compose.yaml") {
		t.Error("compose.yaml itself must remain")
	}
}

func TestCleanupKeepsUserFilesAndSharedPaths(t *testing.T) {
	root := t.TempDir()
	if err := GenerateFiles(root, cfg("rust", "clickhouse")); err != nil {
		t.Fatal(err)
	}
	mine := filepath.Join(root, "tests", "my_own_test.rs")
	if err := os.WriteFile(mine, []byte("// mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := GenerateFiles(root, cfg("rust")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mine); err != nil {
		t.Error("a test the user wrote must never be removed, nor its directory")
	}
	if exists(t, root, "tests/clickhouse_smoke.rs") {
		t.Error("the generated test should still be removed")
	}
}

func TestSmokeTestsAreNotOverwritten(t *testing.T) {
	root := t.TempDir()
	if err := GenerateFiles(root, cfg("rust", "redis")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "tests", "redis_smoke.rs")
	if err := os.WriteFile(path, []byte("// edited by the user\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := GenerateFiles(root, cfg("rust", "redis", "postgres")); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "// edited by the user\n" {
		t.Errorf("an edited smoke test was overwritten: %q", b)
	}
	if !exists(t, root, "tests/postgres_smoke.rs") {
		t.Error("the new service's test should be added")
	}
}

func TestNoLanguageMeansNoSmokeTests(t *testing.T) {
	root := t.TempDir()
	if err := GenerateFiles(root, cfg("", "redis")); err != nil {
		t.Fatal(err)
	}
	if exists(t, root, "tests") {
		t.Error("no language is recorded, so no tests should be written")
	}
}

func TestSettingsOverrideDefaultsAndTyposFail(t *testing.T) {
	root := t.TempDir()
	c := cfg("rust", "postgres")
	c.Settings = map[string]map[string]any{"postgres": {"version": "16"}}
	if err := GenerateFiles(root, c); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(root, "compose.yaml"))
	if !strings.Contains(string(b), "image: postgres:16") {
		t.Errorf("version override not applied:\n%s", b)
	}
	// ...and it reaches the smoke test too.
	rs, _ := os.ReadFile(filepath.Join(root, "tests", "postgres_smoke.rs"))
	if !strings.Contains(string(rs), `"app"`) {
		t.Error("smoke test should carry the resolved user/database")
	}

	c.Settings = map[string]map[string]any{"postgres": {"versoin": "16"}}
	err := GenerateFiles(t.TempDir(), c)
	if err == nil || !strings.Contains(err.Error(), "available:") {
		t.Errorf("a misspelled setting should fail and list the valid ones, got %v", err)
	}
}