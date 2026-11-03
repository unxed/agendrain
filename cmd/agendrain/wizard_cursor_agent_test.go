package main

import (
	"path/filepath"
	"testing"
)

func TestWizardDiscoversCursorAgentSeparatelyFromIDE(t *testing.T) {
	d := wizardTestHome(t)
	root := filepath.Join(d, "cursor-agent-config")
	t.Setenv("CURSOR_CONFIG_DIR", root)
	if choices := wizardEnvironments(); len(choices) != 0 {
		t.Fatalf("offered missing environments: %#v", choices)
	}
	cliPut(t, filepath.Join(root, "chats", "workspace", "session", "store.db"), "discovery does not parse stores")
	choices := wizardEnvironments()
	if len(choices) != 1 || choices[0].ID != "cursor-agent" {
		t.Fatalf("CLI history should be offered separately from absent IDE: %#v", choices)
	}
}
