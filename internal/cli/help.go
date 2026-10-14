package cli

import (
	_ "embed"
	"fmt"
	"strings"
)

//go:embed help.txt
var helpManual string

// printCommandHelp prints one supported command's section from the embedded
// manual. It returns an error for unknown commands rather than presenting
// roadmap proposals as working features.
func printCommandHelp(name string) error {
	section, err := commandHelpText(name)
	if err != nil {
		return err
	}
	fmt.Print(section)
	return nil
}

func commandHelpText(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "devo" || name == "all" {
		return helpManual, nil
	}

	const detailsHeading = "COMMAND DETAILS & FLAGS"
	const roadmapHeading = "ROADMAP — NOT IMPLEMENTED IN THIS BUILD"

	detailsStart := strings.Index(helpManual, detailsHeading)
	if detailsStart < 0 {
		return "", fmt.Errorf("embedded help manual is missing %q", detailsHeading)
	}
	detailsStart += len(detailsHeading)

	detailsEnd := strings.Index(helpManual[detailsStart:], roadmapHeading)
	if detailsEnd < 0 {
		return "", fmt.Errorf("embedded help manual is missing %q", roadmapHeading)
	}

	details := strings.Split(helpManual[detailsStart:detailsStart+detailsEnd], "\n")
	for i, line := range details {
		if !strings.HasPrefix(line, "  devo ") {
			continue
		}
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 || fields[0] != "devo" || fields[1] != name {
			continue
		}

		end := len(details)
		for j := i + 1; j < len(details); j++ {
			if strings.HasPrefix(details[j], "  devo ") {
				end = j
				break
			}
		}
		return strings.TrimSpace(strings.Join(details[i:end], "\n")) + "\n", nil
	}

	return "", fmt.Errorf("no command-specific help for %q; run `devo help` for supported commands", name)
}
