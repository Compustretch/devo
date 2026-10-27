package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Compustretch/devo/internal/services"
)

// extractMigrateFlag removes -m/--migrate from args wherever it appears, so
// both `devo add -m clickhouse` and `devo add clickhouse -m` work.
func extractMigrateFlag(args []string) ([]string, bool) {
	rest := make([]string, 0, len(args))
	found := false
	for _, a := range args {
		if a == "-m" || a == "--migrate" {
			found = true
			continue
		}
		rest = append(rest, a)
	}
	return rest, found
}

// isMigration reports whether a file in a migration directory is a forward
// migration. Rollback (.down.sql) and verification (.smoke.sql) files sit beside
// their migration but must never be applied as one.
func isMigration(name string) bool {
	return strings.HasSuffix(name, ".sql") &&
		!strings.HasSuffix(name, ".down.sql") &&
		!strings.HasSuffix(name, ".smoke.sql")
}

// migrationFiles returns the forward migrations in dir in name (byte) order.
func migrationFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && isMigration(e.Name()) {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	return files, nil
}

// runMigrations starts the service if needed, then pipes each forward migration
// to the service's migrate command in name order. It stops at the first failure.
func runMigrations(dir, service string) error {
	svc, ok := services.Get(service)
	if !ok {
		return fmt.Errorf("unknown service %q", service)
	}
	if svc.Migrate == nil {
		return fmt.Errorf("service %q has no migrations to run", service)
	}

	migrationDir := filepath.Join(dir, filepath.FromSlash(svc.Migrate.Dir))
	files, err := migrationFiles(migrationDir)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("no migrations directory at %s", migrationDir)
	}
	if err != nil {
		return err
	}
	if len(files) == 0 {
		fmt.Printf("No migrations in %s\n", svc.Migrate.Dir)
		return nil
	}

	base, err := ensureDocker(true)
	if err != nil {
		return err
	}
	warnIfUnshared(dir)

	run := func(stdin io.Reader, args ...string) error {
		cmdArgs := append(append([]string{}, base[1:]...), "-f", filepath.Join(dir, "compose.yaml"))
		cmdArgs = append(cmdArgs, args...)
		cmd := exec.Command(base[0], cmdArgs...)
		cmd.Dir = dir
		cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, os.Stdout, os.Stderr
		return cmd.Run()
	}

	fmt.Printf("Starting %s\n", service)
	if err := run(nil, "up", "--detach", "--wait", service); err != nil {
		return fmt.Errorf("starting %s: %w", service, err)
	}

	execArgs := append([]string{"exec", "-T", service}, svc.Migrate.Exec...)
	for i, name := range files {
		f, err := os.Open(filepath.Join(migrationDir, name))
		if err != nil {
			return err
		}
		fmt.Printf("Applying %s\n", name)
		err = run(f, execArgs...)
		f.Close()
		if err != nil {
			return fmt.Errorf("migration %s failed (%d of %d applied, the rest were not run): %w", name, i, len(files), err)
		}
	}
	fmt.Printf("Applied %d migration(s) from %s\n", len(files), svc.Migrate.Dir)
	return nil
}
