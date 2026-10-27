package cli

import (
	"bufio"
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

const appName = "devo"

// appVersion is a var so release builds can set it:
//
//	go build -ldflags "-X github.com/Compustretch/devo/internal/cli.appVersion=1.2.3" ./cmd/devo
var appVersion = "0.1.0"

// Run is the CLI entry point. main.go calls this with os.Args[1:].
func Run(args []string) int {
	// Accept both `devo help dev` and `devo dev --help`. Help is handled
	// before command-specific flag parsing so it never starts services.
	if len(args) > 1 && args[0] != "help" && args[0] != "-h" && args[0] != "--help" {
		for _, arg := range args[1:] {
			if arg == "-h" || arg == "--help" {
				if err := printCommandHelp(args[0]); err != nil {
					return fail(err)
				}
				return 0
			}
		}
	}

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
		opts, err := parseDevArgs(args[1:])
		if err != nil {
			return fail(err)
		}
		if err := dev(opts); err != nil {
			return fail(err)
		}
		if opts.servicesOnly {
			fmt.Printf("\nServices are up; application start scripts were skipped.\n  Follow logs:  %s logs\n  Stop it:      %s down\n", appName, appName)
		} else {
			fmt.Printf("\nLocal stack is up.\n  Follow logs:  %s logs\n  Stop it:      %s down\n", appName, appName)
		}
		return 0
	case "down":
		if err := down(args[1:]); err != nil {
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
	case "help":
		if len(args) > 1 {
			if err := printCommandHelp(args[1]); err != nil {
				return fail(err)
			}
		} else {
			usage()
		}
		return 0
	case "-h", "--help":
		if len(args) > 1 {
			if err := printCommandHelp(args[1]); err != nil {
				return fail(err)
			}
		} else {
			usage()
		}
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
	fmt.Print(helpManual)
}

// loadRegistry layers the built-in service registry with the user-level and
// project-level registry files. Called by the commands that need services, so
// a broken local file doesn't break `devo --help`.
func loadRegistry(projectDir string) error {
	paths := services.LocalPaths()
	paths[len(paths)-1] = filepath.Join(projectDir, "devo.services.yaml")
	return services.Load(paths...)
}

// ---------------------------------------------------------------------------
// init
// ---------------------------------------------------------------------------

func initProject(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	nameFlag := fs.String("name", "", "app name to use when initializing the current directory in place")
	if err := fs.Parse(args); err != nil {
		return err
	}

	rest := fs.Args()
	if len(rest) == 0 {
		return errors.New("usage: devo init [--name <app>] <template> <name|.>")
	}

	template := rest[0]
	if !project.ValidTemplate(template) {
		return fmt.Errorf("unsupported template %q; try `devo init rust <name>`", template)
	}

	if len(rest) < 2 {
		return fmt.Errorf("usage: devo init %s <name|.>", template)
	}
	if len(rest) > 2 {
		return fmt.Errorf("unexpected argument %q", rest[2])
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	target := rest[1]
	// In place: "." or a name equal to the current directory's name
	// (case-insensitive) adopts the existing project instead of creating a
	// child directory.
	if target == "." || strings.EqualFold(target, filepath.Base(cwd)) {
		return initInPlace(cwd, template, *nameFlag)
	}
	if *nameFlag != "" {
		return errors.New("--name only applies when initializing the current directory in place")
	}

	name := target
	if err := project.ValidateName(name); err != nil {
		return fmt.Errorf("invalid app name %q: %w", name, err)
	}

	if err := loadRegistry("."); err != nil {
		return err
	}

	if _, err := os.Stat(name); err == nil {
		return fmt.Errorf("target %q already exists; refusing to overwrite (to adopt it, run `devo init %s .` inside it)", name, template)
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

// initInPlace adds devo to an existing project in dir: it writes devo.yaml and
// compose.yaml only. No template files are generated, no services are enabled,
// and an existing devo.yaml or compose.yaml is never overwritten.
func initInPlace(dir, template, nameOverride string) error {
	name := nameOverride
	if name == "" {
		name = strings.ToLower(filepath.Base(dir))
	}
	if err := project.ValidateName(name); err != nil {
		return fmt.Errorf("invalid app name %q: %w (pass --name to choose one)", name, err)
	}

	for _, f := range []string{"devo.yaml", "compose.yaml"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			return fmt.Errorf("%s already exists; refusing to overwrite", f)
		} else if !os.IsNotExist(err) {
			return err
		}
	}

	if err := loadRegistry(dir); err != nil {
		return err
	}

	if err := project.GenerateServicesNamed(dir, name, template, map[string]bool{}, true); err != nil {
		return err
	}

	fmt.Printf("Initialized %s in place (%s) with no services enabled.\n\nNext:\n  devo add <service>\n", name, dir)
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
	if err := loadRegistry(dir); err != nil {
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
    args, migrate := extractMigrateFlag(args)
    if migrate && !add {
        return errors.New("-m/--migrate only applies to `devo add`")
    }
    if len(args) == 0 {
        if err := loadRegistry("."); err != nil {
            return err
        }
        return fmt.Errorf("specify a service: %s", strings.Join(services.Names(), ", "))
    }
    service := args[0]
    dir, err := directory(args[1:])
    if err != nil {
        return err
    }
    if err := loadRegistry(dir); err != nil {
        return err
    }
    if !services.Valid(service) {
        return fmt.Errorf("unknown service %q (known: %s)", service, strings.Join(services.Names(), ", "))
    }
    cfgPath := filepath.Join(dir, "devo.yaml")
    cfg, err := project.Read(cfgPath)
    if err != nil {
        return err
    }

    // If adding a service, resolve and prompt for missing dependencies
    if add {
        var missing []string
        visited := make(map[string]bool)
        collectMissingDependencies(service, cfg.Services, &missing, visited)

        if len(missing) > 0 {
            fmt.Printf("Service %q requires the following unconfigured dependencies:\n", service)
            for _, dep := range missing {
                depS, _ := services.Get(dep)
                desc := depS.Description
                if desc == "" {
                    desc = "no description"
                }
                fmt.Printf("  - %s: %s\n", dep, desc)
            }
            fmt.Print("\nProceed to add all required dependencies? [y/N]: ")

            reader := bufio.NewReader(os.Stdin)
            line, err := reader.ReadString('\n')
            if err != nil {
                return err
            }
            answer := strings.TrimSpace(strings.ToLower(line))
            if answer != "y" && answer != "yes" {
                fmt.Println("Aborted.")
                return nil
            }

            // Provision missing dependencies first
            for _, dep := range missing {
                if err := project.SetEnabled(cfgPath, dep, true); err != nil {
                    return err
                }
                cfg.Services[dep] = true
                if err := project.GenerateFiles(dir, cfg); err != nil {
                    return err
                }
                fmt.Printf("Added dependency: %s\n", dep)
            }
        }
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
    if migrate {
        return runMigrations(dir, service)
    }
    return nil
}

func collectMissingDependencies(serviceName string, cfgServices map[string]bool, missing *[]string, visited map[string]bool) {
    if visited[serviceName] {
        return
    }
    visited[serviceName] = true

    s, ok := services.Get(serviceName)
    if !ok {
        return
    }

    for _, dep := range s.Dependencies {
        if !cfgServices[dep] {
            found := false
            for _, m := range *missing {
                if m == dep {
                    found = true
                    break
                }
            }
            if !found {
                *missing = append(*missing, dep)
            }
        }
        collectMissingDependencies(dep, cfgServices, missing, visited)
    }
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
	if command[0] == "up" {
		warnIfUnshared(dir)
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

// warnIfUnshared warns when the project lives where Colima's VM can't see it.
// Colima shares only the home directory with containers by default, so a bind
// mount from anywhere else (ClickHouse init scripts, Grafana provisioning)
// arrives as an empty directory instead of failing loudly.
func warnIfUnshared(dir string) {
	if !usingColima() {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	if real, err := filepath.EvalSymlinks(home); err == nil {
		home = real
	}
	if abs == home || strings.HasPrefix(abs, home+string(filepath.Separator)) {
		return
	}
	fmt.Fprintf(os.Stderr, "warning: %s is outside your home directory.\n"+
		"  Colima only shares your home directory with containers by default, so files this\n"+
		"  project mounts (ClickHouse init scripts, Grafana provisioning) will look empty inside them.\n"+
		"  Move the project under %s, or restart Colima with --mount <dir> to share it.\n\n", abs, home)
}
