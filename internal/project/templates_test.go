package project

import (
	"io/fs"
	"strings"
	"testing"
)

// Every file under embedded_templates/ is copied into users' new projects, so
// a bad file there is a bad project. This fails the build before that ships.
func TestEmbeddedTemplatesAreSound(t *testing.T) {
	err := fs.WalkDir(templates, "embedded_templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		name := d.Name()
		switch {
		case strings.HasSuffix(name, ".go"):
			t.Errorf("%s: Go files can't live in a template (they'd be compiled into the repo and copied into new projects); rename to %s.tmpl", path, name)
		case reservedFiles[strings.TrimSuffix(name, ".tmpl")]:
			t.Errorf("%s: devo generates this file itself; remove it from the template", path)
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() == 0 && name != ".gitkeep" {
			t.Errorf("%s: empty file; templates must have real content", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}