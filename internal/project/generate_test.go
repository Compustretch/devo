package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateWithClickHouseOnly(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sample")
	if err := Generate(root, map[string]bool{"clickhouse": true}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"devo.yaml", "compose.yaml",
		"platform/clickhouse/init/001-events.sql",
	} {
		if _, err := os.Stat(filepath.Join(root, path)); err != nil {
			t.Errorf("expected %s: %v", path, err)
		}
	}
}
