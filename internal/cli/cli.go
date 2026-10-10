package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Compustretch/devo/internal/project"
	"github.com/Compustretch/devo/internal/services"
)

const (
	appName    = "devo"
	appVersion = "0.1.0"
	docsURL    = "https://[internal]/devo"
	helpURL    = "#platform-help"
)

// Run is the CLI entry point. main.go calls this with os.Args[1:].
func Run(args []string) int {
	if len(args) == 0 {
		usage()
		return 0
	}

	switch args[0] {
	case "init":
		if err := initProject(args[1:]); err != nil {
			return fail(err)
		}
		return 0
	case "add":
		if err := mutate(args[1:], true); err != nil {
			return fail(err)
		}
		return 0
	case "remove":
		if err := mutate(args[1:], false); err != nil {
			return fail(err)
		}
		return 0
	case "services":
		if err := listServices(args[1:]); err != nil {
			return fail(err)
		}
		return 0
	case "dev":
		if err := compose(args[1:], "up", "--build"); err != nil {
			return fail(err)
		}
		return 0
	case "down":
		if err := compose(args[1:], "down"); err != nil {
			return fail(err)
		}
		return 0
	case "logs":
		if err := compose(args[1:], "logs", "-f"); err != nil {
			return fail(err)
		}
		return 0
	case "-v", "--version", "version":
		fmt.Printf("%s %s\n", appName, appVersion)
		return 0
	case "help", "-h", "--help":
		usage()
		return 0
	default:
		return fail(fmt.Errorf("unknown command %q; run `%s help`", args[0], appName))
	}
}

func fail(err error) int {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	return 1
}

func usage() {
	fmt.Printf(`devo — the fast path from laptop to production.

USAGE
  devo init <template> <name>
  devo add <service> [--dir .]
  devo remove <service> [--dir .]
  devo services [--dir .]
  devo dev [--dir .]
  devo logs [--dir .]
  devo down [--dir .]

COMMANDS
  init <template> <name>   Scaffold a new app from a platform template
  add <service>            Enable a service in the current project
  remove <service>         Disable a service in the current project
  services                 List services and their enabled state
  dev                      Start the local development stack
  logs                     Follow logs from the local stack
  down                     Stop the local development stack

FLAGS
  -h, --help               Show help
  -v, --version            Print version

EXAMPLES
  devo init rust my-app
  devo add grafana
  devo dev

MORE
  Docs    %s
  Help    %s
`, docsURL, helpURL)
}

// ---------------------------------------------------------------------------
// init
// ---------------------------------------------------------------------------

func initProject(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	rest := fs.Args()
	if len(rest) == 0 {
		return errors.New("usage: devo init <template> <name>")
	}

	template := rest[0]
	if !project.ValidTemplate(template) {
		return fmt.Errorf("unsupported template %q; try `devo init rust <name>`", template)
	}

	if len(rest) < 2 {
		return fmt.Errorf("usage: devo init %s <name>", template)
	}
	if len(rest) > 2 {
		return fmt.Errorf("unexpected argument %q", rest[2])
	}

	name := rest[1]
	if err := project.ValidateName(name); err != nil {
		return fmt.Errorf("invalid app name %q: %w", name, err)
	}

	if _, err := os.Stat(name); err == nil {
		return fmt.Errorf("target %q already exists; refusing to overwrite", name)
	} else if !os.IsNotExist(err) {
		return err
	}

	if err := project.Generate(project.Options{
		Template: template,
		Name:     name,
		Dir:      name,
	}); err != nil {
		_ = os.RemoveAll(name)
		return err
	}

	fmt.Printf("Created %s\n\nNext:\n  cd %s\n  cargo run\n", name, name)
	return nil
}

// ---------------------------------------------------------------------------
// services
// ---------------------------------------------------------------------------

func listServices(args []string) error {
	dir, err := directory(args)
	if err != nil {
		return err
	}
	cfg, err := project.Read(filepath.Join(dir, "devo.yaml"))
	if err != nil {
		return err
	}
	for _, s := range services.Names {
		status := "disabled"
		if cfg.Services[s] {
			status = "enabled"
		}
		fmt.Printf("%-12s %s\n", s, status)
	}
	return nil
}

func mutate(args []string, add bool) error {
	if len(args) == 0 {
		return errors.New("specify a service: clickhouse or grafana")
	}
	service := args[0]
	if !services.Valid(service) {
		return fmt.Errorf("unknown service %q", service)
	}
	dir, err := directory(args[1:])
	if err != nil {
		return err
	}
	cfgPath := filepath.Join(dir, "devo.yaml")
	cfg, err := project.Read(cfgPath)
	if err != nil {
		return err
	}

	if add {
		cfg.Services[service] = true
	} else {
		delete(cfg.Services, service)
	}
	if err := project.Write(cfgPath, cfg); err != nil {
		return err
	}
	if err := project.GenerateFiles(dir, cfg.Services); err != nil {
		return err
	}
	action := "Removed"
	if add {
		action = "Added"
	}
	fmt.Printf("%s %s\n", action, service)
	return nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func directory(args []string) (string, error) {
	fs := flag.NewFlagSet("directory", flag.ContinueOnError)
	dir := fs.String("dir", ".", "application directory")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() != 0 {
		return "", fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	return *dir, nil
}

func compose(args []string, command ...string) error {
	dir, err := directory(args)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, "devo.yaml")); err != nil {
		return fmt.Errorf("%s is not a Devo project", dir)
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return errors.New("Docker is required; install Docker Desktop or Docker Engine")
	}
	cmdArgs := append([]string{"compose", "-f", filepath.Join(dir, "compose.yaml")}, command...)
	cmd := exec.Command("docker", cmdArgs...)
	cmd.Dir = dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
