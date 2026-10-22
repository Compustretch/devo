package services

import (
	_ "embed"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed default_services.yaml
var defaultRegistry []byte

// Service is one entry in the registry. See default_services.yaml for the schema.
type Service struct {
	Name        string            `yaml:"-"`
	Description string            `yaml:"description"`
	Params      map[string]any    `yaml:"params"`
	Compose     map[string]any    `yaml:"compose"`
	Volumes     []string          `yaml:"volumes"`
	Files       map[string]string `yaml:"files"`
	// Smoke maps language -> path -> content. A project in that language gets
	// these files while the service is enabled.
	Smoke           map[string]map[string]string `yaml:"smoke"`
	Datasource      map[string]any               `yaml:"datasource"`
	DatasourcesFile string                       `yaml:"datasources_file"`
	// Plugins are Grafana plugins this service's datasource needs installed.
	Plugins []string `yaml:"plugins"`
	// PluginsEnv, on a datasource consumer, names the environment variable that
	// receives the comma-separated plugins of every enabled provider
	// (GF_INSTALL_PLUGINS for Grafana).
	PluginsEnv string `yaml:"plugins_env"`
	Dependencies     []string                     `yaml:"dependencies"`
	// Samples lists paths in Files that are demo content (seed data, example
	// dashboards). They are written for new projects and skipped when devo
	// adopts an existing one.
	Samples []string `yaml:"samples"`
	// Adopted overrides this service for adopted projects, whose app already
	// has its own schema, credentials and init scripts.
	Adopted *AdoptedOverride `yaml:"adopted"`
}

// AdoptedOverride changes a service for projects devo adopted rather than
// created. Params replace registry defaults; each Compose key replaces the
// service's key of the same name (an empty value removes it).
type AdoptedOverride struct {
	Params  map[string]any `yaml:"params"`
	Compose map[string]any `yaml:"compose"`
}

// SmokeLanguages are the languages devo ships smoke tests for. Every service in
// the built-in registry must have one per language listed here (see the test).
var SmokeLanguages = []string{"rust"}

var (
	registry = map[string]Service{}
	defaults = map[string]bool{} // service -> enabled by default for new projects
)

func init() {
	// Defaults are embedded, so a failure here is a programming error.
	if err := Load(); err != nil {
		panic(err)
	}
}

// LocalPaths are the optional registry files, lowest precedence first.
func LocalPaths() []string {
	var paths []string
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".config", "devo", "services.yaml"))
	}
	return append(paths, "devo.services.yaml")
}

// Load resets the registry to the embedded defaults, then overlays each path in
// order. Missing files are skipped. A service defined in a later layer replaces
// a same-named service from an earlier one; a key in a later layer's `services:` list
// overrides the same key from an earlier one.
func Load(paths ...string) error {
	svcs := map[string]Service{}
	on := map[string]bool{}

	if err := merge(svcs, on, defaultRegistry, "embedded defaults"); err != nil {
		return err
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if err := merge(svcs, on, b, p); err != nil {
			return err
		}
	}

	// Every key in the top list must have a definition somewhere.
	keys := make([]string, 0, len(on))
	for k := range on {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, ok := svcs[k]; !ok {
			return fmt.Errorf("services: %q is listed but has no definition block", k)
		}
	}

	registry, defaults = svcs, on
	return nil
}

func merge(svcs map[string]Service, on map[string]bool, data []byte, source string) error {
	// Top level: `services` is the true/false list; every other key is a
	// service definition named after the service.
	var raw map[string]yaml.Node
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("%s: %w", source, err)
	}
	for key, node := range raw {
		if key == "services" {
			var list map[string]bool
			if err := node.Decode(&list); err != nil {
				return fmt.Errorf("%s: services must be a map of name: true|false: %w", source, err)
			}
			for name, v := range list {
				on[name] = v
			}
			continue
		}

		if err := checkFields(key, &node); err != nil {
			return fmt.Errorf("%s: %w", source, err)
		}
		var s Service
		if err := node.Decode(&s); err != nil {
			return fmt.Errorf("%s: service %q: %w", source, key, err)
		}
		if err := validate(key, s); err != nil {
			return fmt.Errorf("%s: service %q: %w", source, key, err)
		}
		s.Name = key
		svcs[key] = s
	}
	return nil
}

func validate(name string, s Service) error {
	if name == "" {
		return fmt.Errorf("name cannot be empty")
	}
	if name == "name" || name == "services" || name == "language" {
		return fmt.Errorf("%q is reserved and can't be a service name", name)
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return fmt.Errorf("name must be lowercase letters, digits, '-' or '_'")
		}
	}
	if s.Compose["image"] == nil && s.Compose["build"] == nil {
		return fmt.Errorf("compose block needs an image or build")
	}
	for p := range s.Files {
		if err := validateDeclaredPath(name, "files", p); err != nil {
			return err
		}
	}
	if s.DatasourcesFile != "" {
		if err := validateDeclaredPath(name, "datasources_file", s.DatasourcesFile); err != nil {
			return err
		}
	}
	for language, paths := range s.Smoke {
		for p := range paths {
			if err := validateDeclaredPath(name, "smoke."+language, p); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateDeclaredPath rejects registry paths that are absolute or escape the
// generated project. Treat backslashes as separators too, so a registry gets
// the same validation regardless of the host OS.
func validateDeclaredPath(service, field, raw string) error {
	normalized := strings.ReplaceAll(raw, `\`, "/")
	cleaned := path.Clean(normalized)
	hasWindowsVolume := len(normalized) >= 2 &&
		((normalized[0] >= 'a' && normalized[0] <= 'z') ||
			(normalized[0] >= 'A' && normalized[0] <= 'Z')) && normalized[1] == ':'
	if raw == "" || filepath.IsAbs(raw) || path.IsAbs(normalized) ||
		hasWindowsVolume || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("service %q: %s path %q must be a relative file path inside the project", service, field, raw)
	}
	if cleaned == "devo.yaml" || cleaned == "compose.yaml" {
		return fmt.Errorf("service %q: %s path %q conflicts with Devo-managed file %q", service, field, raw, cleaned)
	}
	return nil
}

// Defaults returns the services enabled by default for new projects.
func Defaults() map[string]bool {
	out := map[string]bool{}
	for n, v := range defaults {
		if v {
			out[n] = true
		}
	}
	return out
}

// Names returns all registered service names, sorted.
func Names() []string {
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func Valid(name string) bool {
	_, ok := registry[name]
	return ok
}

func Get(name string) (Service, bool) {
	s, ok := registry[name]
	return s, ok
}

// Enabled returns the registered services switched on in m, sorted by name.
func Enabled(m map[string]bool) []Service {
	var out []Service
	for _, n := range Names() {
		if m[n] {
			out = append(out, registry[n])
		}
	}
	return out
}

func Parse(raw string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if !Valid(name) {
			return nil, fmt.Errorf("unknown service %q (valid: %s)", name, strings.Join(Names(), ", "))
		}
		out[name] = true
	}
	return out, nil
}

// CheckSettings verifies that every key in a project's override block for a
// service is a parameter that service declares, so typos fail loudly.
func CheckSettings(name string, settings map[string]any) error {
	s, ok := registry[name]
	if !ok {
		return fmt.Errorf("unknown service %q", name)
	}
	for k := range settings {
		if _, ok := s.Params[k]; !ok {
			avail := make([]string, 0, len(s.Params))
			for p := range s.Params {
				avail = append(avail, p)
			}
			sort.Strings(avail)
			return fmt.Errorf("%s: unknown setting %q (available: %s)", name, k, strings.Join(avail, ", "))
		}
	}
	return nil
}

// Resolve returns the enabled services with their params filled in: registry
// defaults, overridden by the project's per-service settings.
func Resolve(enabled map[string]bool, settings map[string]map[string]any, projectName string) ([]Service, error) {
	return ResolveFor(enabled, settings, projectName, false)
}

// ResolveFor is Resolve for a project that devo either created (adopted=false)
// or adopted. For an adopted project the services' demo files, smoke tests and
// init-script mounts are dropped and each service's adopted override applies.
func ResolveFor(enabled map[string]bool, settings map[string]map[string]any, projectName string, adopted bool) ([]Service, error) {
	var out []Service
	for _, s := range Enabled(enabled) {
		if err := CheckSettings(s.Name, settings[s.Name]); err != nil {
			return nil, err
		}

		vars := map[string]string{"project_name": projectName}
		for k, v := range s.Params {
			vars[k] = fmt.Sprint(v)
		}
		compose := s.Compose
		if adopted {
			if o := s.Adopted; o != nil {
				for k, v := range o.Params {
					vars[k] = fmt.Sprint(v)
				}
				compose = cloneCompose(compose)
				for k, v := range o.Compose {
					if m, ok := v.(map[string]any); ok && len(m) == 0 {
						delete(compose, k)
						continue
					}
					compose[k] = v
				}
			}
			compose = dropInitMounts(compose)
		}
		for k, v := range settings[s.Name] {
			vars[k] = fmt.Sprint(v)
		}

		s.Compose = subMap(compose, vars)
		if adopted {
			s.Smoke = nil
		}
		if s.Datasource != nil {
			s.Datasource = subMap(s.Datasource, vars)
		}
		files := make(map[string]string, len(s.Files))
		sample := map[string]bool{}
		for _, p := range s.Samples {
			sample[p] = true
		}
		for p, content := range s.Files {
			if adopted && sample[p] {
				continue
			}
			files[p] = subString(content, vars)
		}
		s.Files = files
		smoke := make(map[string]map[string]string, len(s.Smoke))
		for lang, byPath := range s.Smoke {
			m := make(map[string]string, len(byPath))
			for p, content := range byPath {
				m[p] = subString(content, vars)
			}
			smoke[lang] = m
		}
		s.Smoke = smoke
		out = append(out, s)
	}
	return out, nil
}

func cloneCompose(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// dropInitMounts removes bind mounts onto /docker-entrypoint-initdb.d. In an
// adopted project the app's own scripts may live in the mounted directory and
// are not written for this image, and mounting a missing directory makes
// Docker create it on the host.
func dropInitMounts(compose map[string]any) map[string]any {
	vols, ok := compose["volumes"].([]any)
	if !ok {
		return compose
	}
	var kept []any
	for _, v := range vols {
		if str, ok := v.(string); ok {
			parts := strings.Split(str, ":")
			if len(parts) >= 2 && parts[1] == "/docker-entrypoint-initdb.d" {
				continue
			}
		}
		kept = append(kept, v)
	}
	out := cloneCompose(compose)
	out["volumes"] = kept
	return out
}

func subMap(m map[string]any, vars map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = sub(v, vars)
	}
	return out
}

// sub walks a decoded YAML value, replacing {{key}} in every string.
func sub(v any, vars map[string]string) any {
	switch t := v.(type) {
	case string:
		return subString(t, vars)
	case map[string]any:
		return subMap(t, vars)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = sub(e, vars)
		}
		return out
	default:
		return v
	}
}

// Only known keys are replaced, so other "{{...}}" text (e.g. Grafana legend
// formats inside dashboard JSON) passes through untouched.
func subString(s string, vars map[string]string) string {
	for k, v := range vars {
		s = strings.ReplaceAll(s, "{{"+k+"}}", v)
	}
	return s
}

// serviceFields is the set of YAML field names a service block may contain,
// derived from the Service struct so it can't drift from it.
func serviceFields() map[string]bool {
	out := map[string]bool{}
	t := reflect.TypeOf(Service{})
	for i := 0; i < t.NumField(); i++ {
		name := strings.Split(t.Field(i).Tag.Get("yaml"), ",")[0]
		if name != "" && name != "-" {
			out[name] = true
		}
	}
	return out
}

// checkFields rejects misspelled or unknown fields in a service block. The YAML
// decoder would otherwise ignore them silently, leaving a service that quietly
// lacks its params or compose settings.
func checkFields(service string, node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return nil // Decode reports the type error
	}
	known := serviceFields()
	for i := 0; i+1 < len(node.Content); i += 2 {
		field := node.Content[i].Value
		if !known[field] {
			names := make([]string, 0, len(known))
			for n := range known {
				names = append(names, n)
			}
			sort.Strings(names)
			return fmt.Errorf("service %q: unknown field %q (known: %s)", service, field, strings.Join(names, ", "))
		}
	}
	return nil
}
// Dependencies returns the declared dependencies for a service.
func Dependencies(name string) []string {
    if s, ok := registry[name]; ok {
        return s.Dependencies
    }
    return nil
}