package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

const migrateUsage = "usage: devo migrate status <service> | baseline <service> <version> | resolve <service> <version> --applied|--retry  [--dir <path>] [-v]"

// migrateCommand implements `devo migrate <status|baseline|resolve>`.
func migrateCommand(args []string) error {
	if len(args) == 0 {
		return errors.New(migrateUsage)
	}
	sub, rest := args[0], args[1:]

	fs := flag.NewFlagSet("migrate "+sub, flag.ContinueOnError)
	dir := fs.String("dir", ".", "application directory")
	verbose := fs.Bool("v", false, "show raw docker output")
	fs.BoolVar(verbose, "verbose", false, "show raw docker output")
	applied := fs.Bool("applied", false, "resolve: the migration did run")
	retry := fs.Bool("retry", false, "resolve: run the migration again")

	// Flags may appear before, between or after the positional arguments.
	var pos []string
	for len(rest) > 0 {
		if err := fs.Parse(rest); err != nil {
			return err
		}
		rest = fs.Args()
		if len(rest) > 0 {
			pos = append(pos, rest[0])
			rest = rest[1:]
		}
	}

	var want int
	switch sub {
	case "status":
		want = 1
	case "baseline", "resolve":
		want = 2
	default:
		return fmt.Errorf("unknown migrate command %q; %s", sub, migrateUsage)
	}
	if len(pos) != want {
		return errors.New(migrateUsage)
	}
	if sub != "resolve" && (*applied || *retry) {
		return errors.New("--applied and --retry only apply to `devo migrate resolve`")
	}

	if _, err := os.Stat(filepath.Join(*dir, "devo.yaml")); err != nil {
		return fmt.Errorf("%s is not a Devo project", *dir)
	}
	if err := loadRegistry(*dir); err != nil {
		return err
	}

	switch sub {
	case "status":
		return migrationStatus(*dir, pos[0], *verbose)
	case "baseline":
		return baselineMigrations(*dir, pos[0], pos[1], *verbose)
	default:
		if *applied == *retry {
			return errors.New("resolve needs exactly one of --applied or --retry")
		}
		mode := "retry"
		if *applied {
			mode = "applied"
		}
		return resolveMigration(*dir, pos[0], pos[1], mode, *verbose)
	}
}
