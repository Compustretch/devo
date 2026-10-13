package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateIncludesSharedGitignoreRules(t *testing.T) {
	root := filepath.Join(t.TempDir(), "app")
	if err := Generate(Options{Template: "rust", Name: "app", Dir: root}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatalf("read generated .gitignore: %v", err)
	}
	ignore := string(data)
	for _, pattern := range []string{".DS_Store", ".env", "node_modules/", "/target/", "/target\n"} {
		if !strings.Contains(ignore, pattern) {
			t.Errorf("generated .gitignore should contain %q; got:\n%s", pattern, ignore)
		}
	}
}

func TestSharedDirectoryIsNotAnApplicationTemplate(t *testing.T) {
	for _, name := range []string{"shared", "shared/.gitignore", ".", "../rust"} {
		if ValidTemplate(name) {
			t.Errorf("%q must not be exposed as a project template", name)
		}
		if err := Generate(Options{Template: name, Name: "app", Dir: t.TempDir()}); err == nil {
			t.Errorf("Generate must reject invalid template %q", name)
		}
	}
}
