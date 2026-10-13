package project

import (
	"os"
	"path/filepath"
	"strings"

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

			// Install the Grafana plugins that the enabled datasources need.
			if s.PluginsEnv != "" {
				env, _ := c["environment"].(map[string]any)
				env = cloneMap(env)
				var plugins []string
				if cur, ok := env[s.PluginsEnv].(string); ok && cur != "" {
					plugins = append(plugins, cur)
				}
				for _, p := range providers {
					plugins = append(plugins, p.Plugins...)
				}
				if len(plugins) > 0 {
					env[s.PluginsEnv] = strings.Join(uniq(plugins), ",")
					c["environment"] = env
				}
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

	on := map[string]bool{}
	owned := map[string]bool{} // paths an enabled service writes
	for _, s := range active {
		on[s.Name] = true
		for path := range s.Files {
			owned[path] = true
		}
		if s.DatasourcesFile != "" {
			owned[s.DatasourcesFile] = true
		}
		// Smoke-test paths can be shared by service definitions. Treat a path
		// declared by any enabled service as owned so cleanup for a disabled
		// service cannot remove a test another enabled service still declares.
		for path := range s.Smoke[cfg.Language] {
			owned[path] = true
		}
	}

	// Smoke tests for the project's language. Written only if missing, so edits
	// survive.
	if cfg.Language != "" {
		for _, s := range active {
			for path, content := range s.Smoke[cfg.Language] {
				if err := writeIfMissing(root, path, content); err != nil {
					return err
				}
			}
		}
	}

	// Remove what switched-off services generated: their files, datasource file
	// and smoke tests (a test for a service that isn't running can only fail),
	// then any directories that leave empty. A path an enabled service also
	// writes is kept. These files are generated; edit devo.yaml, not them.
	for _, name := range services.Names() {
		if on[name] {
			continue
		}
		svc, _ := services.Get(name)
		var stale []string
		for path := range svc.Files {
			if !owned[path] {
				stale = append(stale, path)
			}
		}
		if svc.DatasourcesFile != "" && !owned[svc.DatasourcesFile] {
			stale = append(stale, svc.DatasourcesFile)
		}
		if cfg.Language != "" {
			for path := range svc.Smoke[cfg.Language] {
				if !owned[path] {
					stale = append(stale, path)
				}
			}
		}
		for _, path := range stale {
			removeAndPrune(root, path)
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

func writeIfMissing(root, name, content string) error {
	if _, err := os.Stat(filepath.Join(root, name)); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return write(root, name, content)
}

// removeAndPrune deletes a generated file, then removes each parent directory
// that the deletion left empty (stopping at the first one that isn't).
func removeAndPrune(root, name string) {
	if err := os.Remove(filepath.Join(root, name)); err != nil {
		return
	}
	for dir := filepath.Dir(name); dir != "." && dir != string(filepath.Separator); dir = filepath.Dir(dir) {
		if os.Remove(filepath.Join(root, dir)) != nil {
			return
		}
	}
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}