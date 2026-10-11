package project

import (
	"context"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-git/go-billy/v5/helper/iofs"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/storage/memory"
)

// Templates live at internal/project/embedded_templates/<template>/...
// The "all:" prefix ensures dotfiles (.gitignore, .github/, ...) are embedded.
//
//go:embed all:embedded_templates
var embeddedTemplates embed.FS

const cloneTimeout = 10 * time.Second

// templateSource describes where a template comes from.
type templateSource struct {
	RepoURL       string // remote git repository
	LockedVersion string // template version compiled into the binary
}

var templateSources = map[string]templateSource{
	"rust": {
		RepoURL:       "https://github.com/example/rust-template.git", // TODO: real URL
		LockedVersion: "v1.4.0",
	},
}

// FetchTemplate writes the named template into dest. It prefers the remote
// template when its VERSION differs from the one compiled into the binary, and
// falls back to the embedded copy when offline, unauthenticated, or in sync.
// Call it from Generate in place of (or before) your existing file writing.
func FetchTemplate(template, dest string) (source string, err error) {
	src, ok := templateSources[template]
	if !ok {
		return "", fmt.Errorf("unsupported template %q", template)
	}

	tfs, source, err := resolveTemplateFS(template, src)
	if err != nil {
		return "", fmt.Errorf("resolve template: %w", err)
	}
	if err := copyFS(tfs, dest); err != nil {
		return source, fmt.Errorf("write template: %w", err)
	}
	return source, nil
}

// resolveTemplateFS returns an fs.FS rooted at the template's top level.
func resolveTemplateFS(template string, src templateSource) (fs.FS, string, error) {
	fallback := func(reason string) (fs.FS, string, error) {
		sub, err := fs.Sub(embeddedTemplates, "embedded_templates/"+template)
		return sub, "embedded (" + reason + ")", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), cloneTimeout)
	defer cancel()

	// A non-nil worktree filesystem is required to get a checkout in memory.
	wt := memfs.New()
	if _, err := git.CloneContext(ctx, memory.NewStorage(), wt, &git.CloneOptions{
		URL:   src.RepoURL,
		Depth: 1,
	}); err != nil {
		return fallback("offline/auth fallback")
	}

	f, err := wt.Open("VERSION")
	if err != nil {
		return fallback("missing version file")
	}
	defer f.Close()

	b, err := io.ReadAll(f)
	if err != nil {
		return fallback("version read error")
	}

	if strings.TrimSpace(string(b)) == src.LockedVersion {
		return fallback("version match")
	}

	return iofs.New(wt), "remote", nil
}

// copyFS copies every file and directory in src into dest.
func copyFS(src fs.FS, dest string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}

	return fs.WalkDir(src, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == "." {
			return nil
		}

		target := filepath.Join(dest, filepath.FromSlash(path))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}

		content, err := fs.ReadFile(src, path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, content, 0o644)
	})
}