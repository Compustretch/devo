package project

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Config struct {
	Name     string
	Services map[string]bool
}

func Generate(root string, enabled map[string]bool) error {
	if err := os.MkdirAll(root, 0755); err != nil { return err }
	name := filepath.Base(filepath.Clean(root))
	cfg := Config{Name: name, Services: enabled}
	if err := Write(filepath.Join(root, "devo.yaml"), cfg); err != nil { return err }
	return GenerateFiles(root, enabled)
}

func Read(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil { return Config{}, err }
	cfg := Config{Services: map[string]bool{}}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "name:") {
			cfg.Name = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "name:")), "\"'")
		}
		for _, s := range []string{"clickhouse", "grafana"} {
			if line == s+": true" { cfg.Services[s] = true }
		}
	}
	if cfg.Name == "" { return Config{}, fmt.Errorf("invalid devo.yaml: missing name") }
	return cfg, nil
}

func Write(path string, cfg Config) error {
	var b strings.Builder
	fmt.Fprintf(&b, "name: %q\nservices:\n", cfg.Name)
	for _, s := range []string{"clickhouse", "grafana"} {
		fmt.Fprintf(&b, "  %s: %t\n", s, cfg.Services[s])
	}
	return os.WriteFile(path, []byte(b.String()), 0644)
}

func GenerateFiles(root string, enabled map[string]bool) error {
	for _, dir := range []string{"platform/clickhouse/init", "platform/grafana/provisioning/datasources", "platform/grafana/provisioning/dashboards", "platform/grafana/dashboards"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0755); err != nil { return err }
	}

	var compose strings.Builder
	compose.WriteString("services:\n")
	if enabled["clickhouse"] {
		compose.WriteString(`  clickhouse:
    image: clickhouse/clickhouse-server:25.3
    ports:
      - "8123:8123"
      - "9000:9000"
    environment:
      CLICKHOUSE_DB: app
      CLICKHOUSE_USER: app
      CLICKHOUSE_PASSWORD: app
      CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT: "1"
    volumes:
      - clickhouse_data:/var/lib/clickhouse
      - ./platform/clickhouse/init:/docker-entrypoint-initdb.d:ro
    healthcheck:
      test: ["CMD", "clickhouse-client", "--user", "app", "--password", "app", "--query", "SELECT 1"]
      interval: 5s
      timeout: 3s
      retries: 20
`)
	}
	if enabled["grafana"] {
		compose.WriteString(`  grafana:
    image: grafana/grafana:11.6.0
    ports:
      - "3000:3000"
    environment:
      GF_SECURITY_ADMIN_USER: admin
      GF_SECURITY_ADMIN_PASSWORD: admin
      GF_USERS_ALLOW_SIGN_UP: "false"
    volumes:
      - ./platform/grafana/provisioning:/etc/grafana/provisioning:ro
      - ./platform/grafana/dashboards:/var/lib/grafana/dashboards:ro
`)
		if enabled["clickhouse"] { compose.WriteString("    depends_on:\n      clickhouse:\n        condition: service_healthy\n") }
	}
	volumes := []string{}
	if enabled["clickhouse"] { volumes = append(volumes, "clickhouse_data:") }
	if len(volumes) > 0 {
		compose.WriteString("\nvolumes:\n")
		for _, v := range volumes { compose.WriteString("  " + v + "\n") }
	}
	if err := os.WriteFile(filepath.Join(root, "compose.yaml"), []byte(compose.String()), 0644); err != nil { return err }

	if enabled["clickhouse"] {
		if err := write(root, "platform/clickhouse/init/001-events.sql", `CREATE TABLE IF NOT EXISTS app.events
(
    event_time DateTime DEFAULT now(),
    event_type String,
    message String
)
ENGINE = MergeTree
ORDER BY (event_time, event_type);

INSERT INTO app.events (event_type, message)
VALUES ('devo.init', 'Devo development environment is ready');
`); err != nil { return err }
	}

	if enabled["grafana"] {
		ds := `apiVersion: 1
datasources:
  - name: ClickHouse
    uid: clickhouse
    type: grafana-clickhouse-datasource
    access: proxy
    url: http://clickhouse:8123
    isDefault: true
    editable: false
    jsonData:
      defaultDatabase: app
      defaultTable: events
      username: app
      tlsSkipVerify: true
    secureJsonData:
      password: app
`
		if !enabled["clickhouse"] {
			ds = `apiVersion: 1
datasources: []
`
		}
		if err := write(root, "platform/grafana/provisioning/datasources/datasource.yaml", ds); err != nil { return err }
		if err := write(root, "platform/grafana/provisioning/dashboards/dashboard.yaml", `apiVersion: 1
providers:
  - name: Devo
    orgId: 1
    folder: Devo
    type: file
    disableDeletion: true
    editable: true
    options:
      path: /var/lib/grafana/dashboards
`); err != nil { return err }
		if enabled["clickhouse"] {
			if err := write(root, "platform/grafana/dashboards/events.json", dashboard); err != nil { return err }
		} else {
			_ = os.Remove(filepath.Join(root, "platform/grafana/dashboards/events.json"))
		}
	}

	// Sort enabled services for stable generation and debugging.
	_ = sortedKeys(enabled)
	return nil
}

func write(root, name, content string) error {
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil { return err }
	return os.WriteFile(path, []byte(content), 0644)
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k, v := range m { if v { keys = append(keys, k) } }
	sort.Strings(keys)
	return keys
}

const dashboard = `{
  "id": null,
  "uid": "devo-events",
  "title": "Devo Events",
  "tags": ["devo"],
  "timezone": "browser",
  "schemaVersion": 39,
  "version": 1,
  "refresh": "5s",
  "panels": [
    {
      "id": 1,
      "type": "timeseries",
      "title": "Events over time",
      "datasource": {"type": "grafana-clickhouse-datasource", "uid": "clickhouse"},
      "gridPos": {"h": 9, "w": 24, "x": 0, "y": 0},
      "targets": [
        {"refId":"A","queryType":"sql","rawSql":"SELECT toStartOfMinute(event_time) AS time, count() AS events FROM app.events GROUP BY time ORDER BY time"}
      ]
    }
  ]
}
`
