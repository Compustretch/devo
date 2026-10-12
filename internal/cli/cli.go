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
		// Detached, and wait until services are running (and healthy, where they
		// define a healthcheck), so the command returns when the stack is usable.
		if err := compose(args[1:], "up", "--build", "--detach", "--wait"); err != nil {
			return fail(err)
		}
		fmt.Printf("\nLocal stack is up.\n  Follow logs:  %s logs\n  Stop it:      %s down\n", appName, appName)
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

// loadRegistry layers the built-in service registry with the user-level and
// project-level registry files. Called by the commands that need services, so
// a broken local file doesn't break `devo --help`.
func loadRegistry() error {
	return services.Load(services.LocalPaths()...)
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

	if err := loadRegistry(); err != nil {
		return err
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

	// devo owns devo.yaml and compose.yaml: always generated, never copied
	// from the template.
	if err := project.GenerateServices(name, template, services.Defaults()); err != nil {
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
	if err := loadRegistry(); err != nil {
		return err
	}
	dir, err := directory(args)
	if err != nil {
		return err
	}
	cfg, err := project.Read(filepath.Join(dir, "devo.yaml"))
	if err != nil {
		return err
	}
	for _, s := range services.Names() {
		status := "disabled"
		if cfg.Services[s] {
			status = "enabled"
		}
		fmt.Printf("%-12s %s\n", s, status)
	}
	return nil
}

func mutate(args []string, add bool) error {
	if err := loadRegistry(); err != nil {
		return err
	}
	if len(args) == 0 {
		return fmt.Errorf("specify a service: %s", strings.Join(services.Names(), ", "))
	}
	service := args[0]
	if !services.Valid(service) {
		return fmt.Errorf("unknown service %q (known: %s)", service, strings.Join(services.Names(), ", "))
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

	// Edit just that line in devo.yaml so comments and override blocks survive.
	if err := project.SetEnabled(cfgPath, service, add); err != nil {
		return err
	}
	if add {
		cfg.Services[service] = true
	} else {
		delete(cfg.Services, service)
	}
	if err := project.GenerateFiles(dir, cfg); err != nil {
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
	// Only `up` is worth starting a VM for; `down` and `logs` need a running stack.
	base, err := ensureDocker(command[0] == "up")
	if err != nil {
		return err
	}
	cmdArgs := append(append([]string{}, base[1:]...), "-f", filepath.Join(dir, "compose.yaml"))
	cmdArgs = append(cmdArgs, command...)
	cmd := exec.Command(base[0], cmdArgs...)
	cmd.Dir = dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// ensureDocker makes sure the docker CLI and its daemon are usable and returns
// the command to run Compose with: `docker compose` (the v2 plugin) if it works,
// otherwise the standalone `docker-compose` binary (what Homebrew installs).
//
// If the daemon is down and the active Docker context is Colima, it starts
// Colima (when autoStart is set). Docker Desktop can't be started reliably from
// here, so that case gets a clear message instead.
func ensureDocker(autoStart bool) ([]string, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return nil, errors.New("Docker is required; install Docker Desktop, or Colima with `brew install colima docker docker-compose`")
	}

	if !dockerDaemonUp() {
		if !usingColima() {
			return nil, errors.New("can't reach the Docker daemon; start Docker Desktop (or your Docker engine) and try again")
		}
		if !autoStart {
			return nil, errors.New("the Docker daemon (Colima) isn't running, so there is no local stack to act on")
		}
		if _, err := exec.LookPath("colima"); err != nil {
			return nil, errors.New("Docker is configured to use Colima but `colima` isn't on your PATH; install it with `brew install colima`")
		}

		fmt.Println("Docker isn't running; starting Colima...")
		cmd := exec.Command("colima", "start")
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("couldn't start Colima: %w", err)
		}
		if !dockerDaemonUp() {
			return nil, errors.New("Colima started but the Docker daemon is still unreachable; check `docker context ls`")
		}
	}

	if exec.Command("docker", "compose", "version").Run() == nil {
		return []string{"docker", "compose"}, nil
	}
	if _, err := exec.LookPath("docker-compose"); err == nil &&
		exec.Command("docker-compose", "version").Run() == nil {
		return []string{"docker-compose"}, nil
	}
	return nil, errors.New("Docker Compose isn't installed (neither the `docker compose` plugin nor `docker-compose`); install it with `brew install docker-compose`")
}

func dockerDaemonUp() bool {
	return exec.Command("docker", "info").Run() == nil
}

func usingColima() bool {
	out, err := exec.Command("docker", "context", "show").Output()
	return err == nil && strings.HasPrefix(strings.TrimSpace(string(out)), "colima")
}