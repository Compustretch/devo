package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ForestMars/devo/internal/project"
	"github.com/ForestMars/devo/internal/services"
)

func Run(args []string) error {
	if len(args) == 0 {
		usage()
		return nil
	}
	switch args[0] {
	case "init":
		return initProject(args[1:])
	case "add":
		return mutate(args[1:], true)
	case "remove":
		return mutate(args[1:], false)
	case "services":
		dir, err := directory(args[1:])
		if err != nil { return err }
		cfg, err := project.Read(filepath.Join(dir, "devo.yaml"))
		if err != nil { return err }
		for _, s := range services.Names {
			status := "disabled"
			if cfg.Services[s] { status = "enabled" }
			fmt.Printf("%-12s %s\n", s, status)
		}
		return nil
	case "dev":
		return compose(args[1:], "up", "--build")
	case "down":
		return compose(args[1:], "down")
	case "logs":
		return compose(args[1:], "logs", "-f")
	case "help", "-h", "--help":
		usage()
		return nil
	default:
		return fmt.Errorf("unknown command %q; run devo help", args[0])
	}
}

func usage() {
	fmt.Print("Devo: application development environments\n\n" +
		"Usage:\n" +
		"  devo init <directory> [--with clickhouse,grafana] [--without grafana]\n" +
		"  devo add <service> [--dir .]\n" +
		"  devo remove <service> [--dir .]\n" +
		"  devo services [--dir .]\n" +
		"  devo dev [--dir .]\n" +
		"  devo logs [--dir .]\n" +
		"  devo down [--dir .]\n")
}

func initProject(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	with := fs.String("with", "clickhouse,grafana", "services to enable")
	without := fs.String("without", "", "services to exclude")
	if err := fs.Parse(args); err != nil { return err }
	if fs.NArg() != 1 { return errors.New("usage: devo init <directory>") }

	selected, err := services.Parse(*with)
	if err != nil { return err }
	for _, s := range split(*without) {
		if !services.Valid(s) { return fmt.Errorf("unknown service %q", s) }
		delete(selected, s)
	}
	target := fs.Arg(0)
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("target %q already exists; refusing to overwrite", target)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := project.Generate(target, selected); err != nil {
		_ = os.RemoveAll(target)
		return err
	}
	fmt.Printf("Created %s\n\nNext:\n  cd %s\n  devo dev\n", target, target)
	return nil
}

func mutate(args []string, add bool) error {
	if len(args) == 0 { return errors.New("specify a service: clickhouse or grafana") }
	service := args[0]
	if !services.Valid(service) { return fmt.Errorf("unknown service %q", service) }
	dir, err := directory(args[1:])
	if err != nil { return err }
	cfgPath := filepath.Join(dir, "devo.yaml")
	cfg, err := project.Read(cfgPath)
	if err != nil { return err }

	if add {
		cfg.Services[service] = true
	} else {
		delete(cfg.Services, service)
	}
	if err := project.Write(cfgPath, cfg); err != nil { return err }
	if err := project.GenerateFiles(dir, cfg.Services); err != nil { return err }
	action := "Removed"
	if add { action = "Added" }
	fmt.Printf("%s %s\n", action, service)
	return nil
}

func directory(args []string) (string, error) {
	fs := flag.NewFlagSet("directory", flag.ContinueOnError)
	dir := fs.String("dir", ".", "application directory")
	if err := fs.Parse(args); err != nil { return "", err }
	if fs.NArg() != 0 { return "", fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " ")) }
	return *dir, nil
}

func compose(args []string, command ...string) error {
	dir, err := directory(args)
	if err != nil { return err }
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

func split(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" { out = append(out, part) }
	}
	return out
}
