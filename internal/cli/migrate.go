package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/Compustretch/devo/internal/services"
)

// refusalRe matches the convention used by migrations that check a precondition
// before changing anything: they fail with a message containing "refused:".
var refusalRe = regexp.MustCompile(`\brefused:`)

// errorSuffixRe strips the trailing "(ERROR_NAME)" ClickHouse appends to messages.
var errorSuffixRe = regexp.MustCompile(`\s*\([A-Z0-9_]+\)\.?$`)

// extractMigrateFlags removes -m/--migrate and -v/--verbose from args wherever
// they appear, so `devo add -m clickhouse` and `devo add clickhouse -m` both work.
func extractMigrateFlags(args []string) (rest []string, migrate, verbose bool) {
	rest = make([]string, 0, len(args))
	for _, a := range args {
		switch a {
		case "-m", "--migrate":
			migrate = true
		case "-v", "--verbose":
			verbose = true
		default:
			rest = append(rest, a)
		}
	}
	return rest, migrate, verbose
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

// migrationStep pairs a forward migration with its rollback.
type migrationStep struct {
	up, down string
}

// pairWithRollbacks enforces that every forward migration has a rollback file
// (NNN_name.sql needs NNN_name.down.sql). It runs before anything is changed.
func pairWithRollbacks(dir string, files []string) ([]migrationStep, error) {
	var steps []migrationStep
	var missing []string
	for _, f := range files {
		down := strings.TrimSuffix(f, ".sql") + ".down.sql"
		if _, err := os.Stat(filepath.Join(dir, down)); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				missing = append(missing, down)
				continue
			}
			return nil, err
		}
		steps = append(steps, migrationStep{up: f, down: down})
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("migrations not run: every migration needs a rollback file, missing %s", strings.Join(missing, ", "))
	}
	return steps, nil
}

// failureReason reduces a failed command's output to one readable line. For a
// database error it is the message itself, without the server plumbing around it.
func failureReason(out string, err error) string {
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "DB::Exception") {
			continue
		}
		msg := line[strings.LastIndex(line, "DB::Exception: ")+len("DB::Exception: "):]
		if i := strings.Index(msg, ": while executing"); i >= 0 {
			msg = msg[:i]
		}
		msg = errorSuffixRe.ReplaceAllString(msg, "")
		return strings.TrimRight(strings.TrimSpace(msg), ".")
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	reason := strings.TrimSpace(lines[len(lines)-1])
	if reason == "" {
		reason = err.Error()
	}
	if len(reason) > 200 {
		reason = reason[:200] + "..."
	}
	return reason
}

type migrator struct {
	dir, migrationDir, service string
	base, execArgs             []string
	verbose                    bool
}

// compose runs a docker compose command and returns its combined output. The
// output is shown only with --verbose; otherwise it feeds the failure reason.
func (m *migrator) compose(stdin io.Reader, args ...string) (string, error) {
	cmdArgs := append(append([]string{}, m.base[1:]...), "-f", filepath.Join(m.dir, "compose.yaml"))
	cmdArgs = append(cmdArgs, args...)
	cmd := exec.Command(m.base[0], cmdArgs...)
	cmd.Dir = m.dir
	var buf bytes.Buffer
	var sink io.Writer = &buf
	if m.verbose {
		sink = io.MultiWriter(&buf, os.Stderr)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, sink, sink
	err := cmd.Run()
	return buf.String(), err
}

// apply pipes one SQL file to the service's migrate command.
func (m *migrator) apply(file string) (string, error) {
	f, err := os.Open(filepath.Join(m.migrationDir, file))
	if err != nil {
		return "", err
	}
	defer f.Close()
	return m.compose(f, m.execArgs...)
}

// rollback runs the rollback files for undo in reverse order and returns the
// final verdict as an error.
func (m *migrator) rollback(failed migrationStep, refused bool, undo []migrationStep) error {
	for i := len(undo) - 1; i >= 0; i-- {
		s := undo[i]
		fmt.Printf("Rolling back %s ... ", s.up)
		out, err := m.apply(s.down)
		if err != nil {
			fmt.Println("FAILED")
			fmt.Printf("  %s\n", failureReason(out, err))
			var left []string
			for j := i; j >= 0; j-- {
				left = append(left, undo[j].down)
			}
			return fmt.Errorf("migrations FAILED and ROLLBACK FAILED at %s; the database may be partly migrated. Run these by hand, in order: %s",
				s.down, strings.Join(left, ", "))
		}
		fmt.Println("ok")
	}
	switch {
	case refused && len(undo) == 0:
		return fmt.Errorf("migrations FAILED: %s refused to run, nothing was changed", failed.up)
	case refused:
		return fmt.Errorf("migrations FAILED: %s refused to run; rolled back %d earlier migration(s), previous state restored", failed.up, len(undo))
	default:
		return fmt.Errorf("migrations FAILED: %s failed; rolled back, previous state restored", failed.up)
	}
}

// runMigrations starts the service if needed, applies every forward migration in
// name order, and rolls the run back with the rollback files if one fails. It
// prints one line per step and a final SUCCESS or FAILED verdict.
func runMigrations(dir, service string, verbose bool) error {
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
	steps, err := pairWithRollbacks(migrationDir, files)
	if err != nil {
		return err
	}

	base, err := ensureDocker(true)
	if err != nil {
		return err
	}
	warnIfUnshared(dir)

	m := &migrator{
		dir:          dir,
		migrationDir: migrationDir,
		service:      service,
		base:         base,
		execArgs:     append([]string{"exec", "-T", service}, svc.Migrate.Exec...),
		verbose:      verbose,
	}

	fmt.Printf("Starting %s ... ", service)
	if out, err := m.compose(nil, "up", "--detach", "--wait", service); err != nil {
		fmt.Println("FAILED")
		fmt.Printf("  %s\n", failureReason(out, err))
		return fmt.Errorf("migrations FAILED: could not start %s, nothing was run", service)
	}
	fmt.Println("ok")

	var applied []migrationStep
	for _, step := range steps {
		fmt.Printf("Applying %s ... ", step.up)
		out, err := m.apply(step.up)
		if err == nil {
			fmt.Println("ok")
			applied = append(applied, step)
			continue
		}
		fmt.Println("FAILED")
		fmt.Printf("  %s\n", failureReason(out, err))

		// A migration that refuses to run changed nothing, so only the earlier
		// migrations of this run are undone. Any other failure may have left part
		// of the migration behind, so its own rollback runs too.
		refused := refusalRe.MatchString(out)
		undo := append([]migrationStep{}, applied...)
		if !refused {
			undo = append(undo, step)
		}
		return m.rollback(step, refused, undo)
	}

	fmt.Printf("SUCCESS: %d migration(s) applied from %s\n", len(steps), svc.Migrate.Dir)
	return nil
}
