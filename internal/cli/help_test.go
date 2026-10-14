package cli

import (
	"strings"
	"testing"
)

func TestHelpManualSeparatesSupportedCommandsFromRoadmap(t *testing.T) {
	for _, want := range []string{
		"CURRENTLY SUPPORTED",
		"devo init <template> <name>",
		"devo dev [--dir <path>]",
		"devo down [--dir <path>]",
		"ROADMAP — NOT IMPLEMENTED IN THIS BUILD",
		"env",
		"secrets",
		"ship",
	} {
		if !strings.Contains(helpManual, want) {
			t.Errorf("embedded help manual missing %q", want)
		}
	}
}

func TestCommandHelpReturnsSupportedSectionOnly(t *testing.T) {
	for command, wants := range map[string][]string{
		"init": {"devo init <template> <name>", "Current template:"},
		"dev":  {"devo dev [--dir <path>]", "scripts/start/", "scripts/stop/"},
		"down": {"devo down [--dir <path>]", "reverse"},
		"help": {"devo help [command]", "ROADMAP section"},
	} {
		got, err := commandHelpText(command)
		if err != nil {
			t.Fatalf("commandHelpText(%q): %v", command, err)
		}
		for _, want := range wants {
			if !strings.Contains(strings.ToLower(got), strings.ToLower(want)) {
				t.Errorf("help for %q missing %q", command, want)
			}
		}
	}
}

func TestCommandHelpRejectsRoadmapCommand(t *testing.T) {
	if _, err := commandHelpText("ship"); err == nil {
		t.Fatal("commandHelpText(\"ship\") succeeded, but ship is roadmap-only")
	}
}
