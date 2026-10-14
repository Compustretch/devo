package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// applicationScript pairs scripts/start/<name>.sh with scripts/stop/<name>.sh.
type applicationScript struct {
	name      string
	startPath string
	stopPath  string
}

// discoverApplicationScripts finds paired, POSIX-shell lifecycle scripts. The
// sorted result gives startup a stable order and lets shutdown reverse it.
func discoverApplicationScripts(root string) ([]applicationScript, error) {
	starts, err := scriptFiles(filepath.Join(root, "scripts", "start"))
	if err != nil {
		return nil, err
	}
	stops, err := scriptFiles(filepath.Join(root, "scripts", "stop"))
	if err != nil {
		return nil, err
	}

	stopByName := make(map[string]string, len(stops))
	for _, path := range stops {
		stopByName[filepath.Base(path)] = path
	}

	paired := make([]applicationScript, 0, len(starts))
	for _, startPath := range starts {
		name := filepath.Base(startPath)
		stopPath, ok := stopByName[name]
		if !ok {
			return nil, fmt.Errorf("application start script %q has no matching scripts/stop/%s", filepath.ToSlash(mustRelative(root, startPath)), name)
		}
		delete(stopByName, name)
		paired = append(paired, applicationScript{
			name:      strings.TrimSuffix(name, filepath.Ext(name)),
			startPath: startPath,
			stopPath:  stopPath,
		})
	}
	if len(stopByName) > 0 {
		leftovers := make([]string, 0, len(stopByName))
		for name := range stopByName {
			leftovers = append(leftovers, name)
		}
		sort.Strings(leftovers)
		return nil, fmt.Errorf("application stop script %q has no matching scripts/start/%s", filepath.ToSlash(filepath.Join("scripts", "stop", leftovers[0])), leftovers[0])
	}

	sort.Slice(paired, func(i, j int) bool { return paired[i].name < paired[j].name })
	return paired, nil
}

func scriptFiles(dir string) ([]string, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.sh"))
	if err != nil {
		return nil, fmt.Errorf("find lifecycle scripts in %s: %w", dir, err)
	}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("stat lifecycle script %s: %w", path, err)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("lifecycle script path %s is a directory", path)
		}
	}
	return paths, nil // filepath.Glob returns matches in lexical order.
}

func mustRelative(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return rel
}

// runApplicationScript executes scripts using /bin/sh, so executable bits are
// not required for generated template files. Scripts run with the project root
// as their working directory and inherit Devo's environment and standard I/O.
func runApplicationScript(root, path string) error {
	cmd := exec.Command("/bin/sh", path)
	cmd.Dir = root
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", filepath.ToSlash(mustRelative(root, path)), err)
	}
	return nil
}

func dev(args []string) error {
	dir, err := directory(args)
	if err != nil {
		return err
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	scripts, err := discoverApplicationScripts(root)
	if err != nil {
		return err
	}
	projectArgs := []string{"--dir", root}
	if err := compose(projectArgs, "up", "--build", "--detach", "--wait"); err != nil {
		// Compose can report a readiness failure after creating some containers.
		if cleanupErr := compose(projectArgs, "down"); cleanupErr != nil {
			return errors.Join(fmt.Errorf("start Compose services: %w", err), fmt.Errorf("roll back Compose services: %w", cleanupErr))
		}
		return fmt.Errorf("start Compose services: %w", err)
	}

	// Include the failing start in rollback: a script can fail after creating a
	// process, and its stop hook is responsible for cleaning up that partial start.
	attempted := make([]applicationScript, 0, len(scripts))
	for _, script := range scripts {
		attempted = append(attempted, script)
		if err := runApplicationScript(root, script.startPath); err != nil {
			errs := []error{fmt.Errorf("start application %q: %w", script.name, err)}
			for i := len(attempted) - 1; i >= 0; i-- {
				if stopErr := runApplicationScript(root, attempted[i].stopPath); stopErr != nil {
					errs = append(errs, fmt.Errorf("roll back application %q: %w", attempted[i].name, stopErr))
				}
			}
			if downErr := compose(projectArgs, "down"); downErr != nil {
				errs = append(errs, fmt.Errorf("roll back Compose services: %w", downErr))
			}
			return errors.Join(errs...)
		}
	}
	return nil
}

func down(args []string) error {
	dir, err := directory(args)
	if err != nil {
		return err
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	projectArgs := []string{"--dir", root}

	// Prefer paired scripts, but still attempt every available stop hook if the
	// project has become inconsistent since its last `devo dev` invocation.
	scripts, discoverErr := discoverApplicationScripts(root)
	var errs []error
	if discoverErr != nil {
		errs = append(errs, discoverErr)
		stops, stopErr := scriptFiles(filepath.Join(root, "scripts", "stop"))
		if stopErr != nil {
			errs = append(errs, stopErr)
		} else {
			for i := len(stops) - 1; i >= 0; i-- {
				if err := runApplicationScript(root, stops[i]); err != nil {
					errs = append(errs, fmt.Errorf("stop application script: %w", err))
				}
			}
		}
	} else {
		for i := len(scripts) - 1; i >= 0; i-- {
			if err := runApplicationScript(root, scripts[i].stopPath); err != nil {
				errs = append(errs, fmt.Errorf("stop application %q: %w", scripts[i].name, err))
			}
		}
	}

	// Infrastructure teardown runs even if an application stop hook fails.
	if err := compose(projectArgs, "down"); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
