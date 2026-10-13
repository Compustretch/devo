package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateRustTemplate(t *testing.T) {
	root := filepath.Join(t.TempDir(), "ex")
	if err := Generate(Options{
		Template: "rust",
		Name:     "foo",
		Dir:      root,
	}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"Cargo.toml",
		"Dockerfile",
		"src/main.rs",
	} {
		if _, err := os.Stat(filepath.Join(root, path)); err != nil {
			t.Errorf("expected %s: %v", path, err)
		}
	}
}