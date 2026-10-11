package project

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/Compustretch/devo/internal/services"
	"gopkg.in/yaml.v3"
)

// Config is the parsed devo.yaml:
//
//	name: my-app
//
//	services:
//	  postgres: true
//	  grafana: false
//
//	# Per-service config. Only needed when overriding defaults.
//	postgres:
//	  version: "16"
type Config struct {
	Name     string
	Services map[string]bool                   // only enabled services are present
	Settings map[string]map[string]any // service -> overrides of registry params
}

func Read(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var raw map[string]any
	if err := yaml.Unmarshal(b, &raw); err != nil {
		return Config{}, fmt.Errorf("invalid devo.yaml: %w", err)
	}

	cfg := Config{
		Services: map[string]bool{},
		Settings: map[string]map[string]any{},
	}
	for key, val := range raw {
		switch key {
		case "name":
			s, ok := val.(string)
			if !ok {
				return Config{}, fmt.Errorf("invalid devo.yaml: name must be a string")
			}
			cfg.Name = s

		case "services":
			m, ok := val.(map[string]any)
			if !ok && val != nil {
				return Config{}, fmt.Errorf("invalid devo.yaml: services must be a map of name: true|false")
			}
			for s, v := range m {
				on, ok := v.(bool)
				if !ok {
					return Config{}, fmt.Errorf("invalid devo.yaml: services.%s must be true or false", s)
				}
				if !services.Valid(s) {
					return Config{}, fmt.Errorf("invalid devo.yaml: unknown service %q (known: %s)",
						s, strings.Join(services.Names(), ", "))
				}
				if on {
					cfg.Services[s] = true
				}
			}

		default:
			if !services.Valid(key) {
				return Config{}, fmt.Errorf("invalid devo.yaml: unknown key %q (expected name, services, or a service name)", key)
			}
			m, ok := val.(map[string]any)
			if !ok && val != nil {
				return Config{}, fmt.Errorf("invalid devo.yaml: %s must be a block of settings", key)
			}
			if err := services.CheckSettings(key, m); err != nil {
				return Config{}, fmt.Errorf("invalid devo.yaml: %w", err)
			}
			cfg.Settings[key] = m
		}
	}

	if cfg.Name == "" {
		return Config{}, fmt.Errorf("invalid devo.yaml: missing name")
	}
	return cfg, nil
}

// Write creates devo.yaml from scratch. Use SetEnabled to change an existing
// file so comments and layout survive.
func Write(path string, cfg Config) error {
	var b strings.Builder
	fmt.Fprintf(&b, "name: %q\n\nservices:\n", cfg.Name)
	for _, s := range services.Names() {
		fmt.Fprintf(&b, "  %s: %t\n", s, cfg.Services[s])
	}

	if len(cfg.Settings) > 0 {
		b.WriteString("\n# Per-service config. Only needed when you want to override defaults.\n")
		names := make([]string, 0, len(cfg.Settings))
		for n := range cfg.Settings {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			var buf bytes.Buffer
			enc := yaml.NewEncoder(&buf)
			enc.SetIndent(2)
			if err := enc.Encode(map[string]any{n: cfg.Settings[n]}); err != nil {
				return err
			}
			enc.Close()
			b.WriteString("\n")
			b.Write(buf.Bytes())
		}
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// SetEnabled flips one service in the `services:` block of an existing
// devo.yaml. It edits that line in place (or adds it), leaving comments, blank
// lines and override blocks untouched.
func SetEnabled(path, service string, on bool) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(b), "\n")

	servicesAt := -1
	inServices := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		topLevel := trimmed != "" && !strings.HasPrefix(trimmed, "#") &&
			!strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t")
		if topLevel {
			inServices = strings.HasPrefix(line, "services:")
			if inServices {
				servicesAt = i
			}
			continue
		}
		if inServices && strings.HasPrefix(trimmed, service+":") {
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			lines[i] = fmt.Sprintf("%s%s: %t", indent, service, on)
			return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
		}
	}

	if servicesAt < 0 {
		return fmt.Errorf("devo.yaml has no services: block")
	}
	entry := fmt.Sprintf("  %s: %t", service, on)
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:servicesAt+1]...)
	out = append(out, entry)
	out = append(out, lines[servicesAt+1:]...)
	return os.WriteFile(path, []byte(strings.Join(out, "\n")), 0o644)
}