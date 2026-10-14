package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateRustTemplateIncludesLifecycleScriptDirectories(t *testing.T) {
	root := filepath.Join(t.TempDir(), "ex")
	if err := Generate(Options{
		Template: "rust",
		Name:     "foo",
		Dir:      root,
	}); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"scripts/start", "scripts/stop"} {
		info, err := os.Stat(filepath.Join(root, path))
		if err != nil {
			t.Errorf("expected %s: %v", path, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("expected %s to be a directory", path)
		}
	}
}
