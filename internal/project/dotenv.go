package project

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// Files devo maintains for the services a project enables. .env holds the
// endpoints an app uses to reach each service; .auth holds their default
// credentials and is created owner-only.
const (
	envFile  = ".env"
	authFile = ".auth"
)

// writeServiceEnv adds the enabled services' endpoints to .env and credentials
// to .auth, and makes sure Git ignores both.
func writeServiceEnv(root string, env, auth map[string]string) error {
	if len(env) == 0 && len(auth) == 0 {
		return nil
	}
	if err := mergeDotenv(root, envFile, env, 0o644); err != nil {
		return err
	}
	if err := mergeDotenv(root, authFile, auth, 0o600); err != nil {
		return err
	}
	var ignore []string
	if len(env) > 0 {
		ignore = append(ignore, envFile)
	}
	if len(auth) > 0 {
		ignore = append(ignore, authFile)
	}
	return ensureGitignored(root, ignore...)
}

// mergeDotenv appends each NAME=value in entries that the file does not define
// yet. Names already present are left alone, so a developer's edits survive
// every later `devo add`. The file is created with mode when it is missing.
func mergeDotenv(root, name string, entries map[string]string, mode os.FileMode) error {
	if len(entries) == 0 {
		return nil
	}
	path, err := projectPath(root, name)
	if err != nil {
		return err
	}
	existing, err := os.ReadFile(path)
	created := os.IsNotExist(err)
	if err != nil && !created {
		return err
	}

	defined := map[string]bool{}
	for _, line := range strings.Split(string(existing), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		if key, _, ok := strings.Cut(line, "="); ok {
			defined[strings.TrimSpace(key)] = true
		}
	}

	var keys []string
	for k := range entries {
		if !defined[k] {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)

	var b strings.Builder
	b.Write(existing)
	if created {
		b.WriteString("# Added by devo. Names that are already set here are never overwritten.\n")
	} else if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		b.WriteByte('\n')
	}
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, dotenvQuote(entries[k]))
	}
	return os.WriteFile(path, []byte(b.String()), mode)
}

// dotenvQuote leaves plain values bare and single-quotes anything a shell or
// dotenv loader could misread. A value with a single quote gets double quotes.
func dotenvQuote(v string) string {
	plain := v != ""
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			strings.ContainsRune("_-./:@%+,=", r):
		default:
			plain = false
		}
	}
	switch {
	case plain:
		return v
	case v == "":
		return "''"
	case !strings.Contains(v, "'"):
		return "'" + v + "'"
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`, "`", "\\`").Replace(v) + `"`
}

// ensureGitignored appends each pattern the project's .gitignore does not list
// yet, creating the file when there is none.
func ensureGitignored(root string, patterns ...string) error {
	path, err := projectPath(root, ".gitignore")
	if err != nil {
		return err
	}
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	listed := map[string]bool{}
	for _, line := range strings.Split(string(existing), "\n") {
		listed[strings.TrimSpace(line)] = true
	}
	var missing []string
	for _, p := range patterns {
		if !listed[p] && !listed["/"+p] {
			missing = append(missing, p)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	var b strings.Builder
	b.Write(existing)
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		b.WriteByte('\n')
	}
	for _, p := range missing {
		b.WriteString(p + "\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}
