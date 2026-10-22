package project

import (
	"fmt"
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
	active, err := services.ResolveFor(cfg.Services, cfg.Settings, cfg.Name, cfg.Adopted)
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
				if cur, ok := env[s.PluginsEnv].(string); ok {
					for _, plugin := range strings.Split(cur, ",") {
						plugin = strings.TrimSpace(plugin)
						if plugin != "" {
							plugins = append(plugins, plugin)
						}
					}
				}
				for _, p := range providers {
					for _, plugin := range p.Plugins {
						plugin = strings.TrimSpace(plugin)
						if plugin != "" {
							plugins = append(plugins, plugin)
						}
					}
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
		sample := map[string]bool{}
		for _, path := range svc.Samples {
			sample[path] = true
		}
		for path := range svc.Files {
			// An adopted project never received the demo files, so a file at
			// that path belongs to the app.
			if !owned[path] && !(cfg.Adopted && sample[path]) {
				stale = append(stale, path)
			}
		}
		if svc.DatasourcesFile != "" && !owned[svc.DatasourcesFile] {
			stale = append(stale, svc.DatasourcesFile)
		}
		if cfg.Language != "" && !cfg.Adopted {
			for path := range svc.Smoke[cfg.Language] {
				if !owned[path] {
					stale = append(stale, path)
				}
			}
		}
		for _, path := range stale {
			if err := removeAndPrune(root, path); err != nil {
				return fmt.Errorf("remove stale generated path %q: %w", path, err)
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

// projectRoot returns a stable absolute path for the project directory.
func projectRoot(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	return abs, nil
}

// projectPath resolves a registry-declared path and rejects traversal, absolute
// paths, and existing symlinks that lead outside the project tree.
func projectPath(root, name string) (string, error) {
	normalized := strings.ReplaceAll(name, `\`, "/")
	cleaned := filepath.Clean(normalized)
	if name == "" || filepath.IsAbs(name) || filepath.IsAbs(normalized) ||
		cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q must stay inside the project directory", name)
	}

	base, err := projectRoot(root)
	if err != nil {
		return "", err
	}
	target := filepath.Join(base, cleaned)
	probe := target
	for {
		info, statErr := os.Lstat(probe)
		if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			resolved, err := filepath.EvalSymlinks(probe)
			if err != nil {
				return "", fmt.Errorf("path %q traverses a dangling or invalid symlink: %w", name, err)
			}
			rel, relErr := filepath.Rel(base, resolved)
			if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
				return "", fmt.Errorf("path %q resolves outside the project directory", name)
			}
			break
		}
		resolved, err := filepath.EvalSymlinks(probe)
		if err == nil {
			rel, relErr := filepath.Rel(base, resolved)
			if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
				return "", fmt.Errorf("path %q resolves outside the project directory", name)
			}
			break
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			break
		}
		probe = parent
	}
	return target, nil
}

func write(root, name, content string) error {
	path, err := projectPath(root, name)
	if err != nil {
		return err
	}
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
	path, err := projectPath(root, name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return write(root, name, content)
}

// removeAndPrune deletes a generated file, then removes each parent directory
// that the deletion left empty (stopping at the first one that isn't).
func removeAndPrune(root, name string) error {
	rootPath, err := projectRoot(root)
	if err != nil {
		return err
	}
	path, err := projectPath(rootPath, name)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("remove path: %w", err)
	}
	for dir := filepath.Dir(path); dir != rootPath; dir = filepath.Dir(dir) {
		rel, err := filepath.Rel(rootPath, dir)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return fmt.Errorf("parent directory %q resolves outside project root", dir)
		}
		if err := os.Remove(dir); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			entries, readErr := os.ReadDir(dir)
			if readErr == nil && len(entries) > 0 {
				return nil // A nonempty directory may contain user-owned files.
			}
			if readErr != nil && os.IsNotExist(readErr) {
				continue
			}
			return fmt.Errorf("remove empty directory %q: %w", dir, err)
		}
	}
	return nil
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
