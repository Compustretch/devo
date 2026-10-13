package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateWithClickHouseOnly(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sample")
	
	// Pass the Options struct instead of separate arguments
	if err := Generate(Options{
		Root:     root,                            
		Services: map[string]bool{"clickhouse": true}, // Or whatever field name holds the service map
	}); err != nil {
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