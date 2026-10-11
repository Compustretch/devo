package project

import (
	"os"
	"path/filepath"

	"github.com/Compustretch/devo/internal/services"
	"gopkg.in/yaml.v3"
)

// GenerateFiles writes compose.yaml and each enabled service's files, driven
// entirely by the service registry.
func GenerateFiles(root string, cfg Config) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	active, err := services.Resolve(cfg.Services, cfg.Settings, cfg.Name)
	if err != nil {
		return err
	}

	// Services that provide a Grafana-style datasource.
	var providers []services.Service
	for _, s := range active {
		if s.Datasource != nil {
			providers = append(providers, s)
		}
	}

	svcs := map[string]any{}
	vols := map[string]any{}
	for _, s := range active {
		c := cloneMap(s.Compose)

		// Consumers wait for healthy providers.
		if s.DatasourcesFile != "" {
			deps := map[string]any{}
			for _, p := range providers {
				if p.Compose["healthcheck"] != nil {
					deps[p.Name] = map[string]any{"condition": "service_healthy"}
				}
			}
			if len(deps) > 0 {
				c["depends_on"] = deps
			}
		}

		svcs[s.Name] = c
		for _, v := range s.Volumes {
			vols[v] = map[string]any{}
		}
	}

	doc := map[string]any{"services": svcs}
	if len(vols) > 0 {
		doc["volumes"] = vols
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	if err := write(root, "compose.yaml", string(out)); err != nil {
		return err
	}

	// Per-service files.
	for _, s := range active {
		for path, content := range s.Files {
			if err := write(root, path, content); err != nil {
				return err
			}
		}
	}

	// Datasource files for consumers (e.g. Grafana).
	for _, s := range active {
		if s.DatasourcesFile == "" {
			continue
		}
		list := make([]map[string]any, 0, len(providers))
		for i, p := range providers {
			ds := cloneMap(p.Datasource)
			ds["isDefault"] = i == 0 // first provider wins
			list = append(list, ds)
		}
		b, err := yaml.Marshal(map[string]any{"apiVersion": 1, "datasources": list})
		if err != nil {
			return err
		}
		if err := write(root, s.DatasourcesFile, string(b)); err != nil {
			return err
		}
	}
	return nil
}

func write(root, name, content string) error {
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
} 