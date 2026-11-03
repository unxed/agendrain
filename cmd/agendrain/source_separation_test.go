package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/unxed/agendrain/internal/drain"
)

func claudeSeparationStores(t *testing.T) (projects, desktop string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", filepath.Join(home, "config"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	config, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(home, "claude", "projects"), filepath.Join(config, "Claude", "local-agent-mode-sessions")
}

func putClaudeSeparationSession(t *testing.T, path, id, cwd string) {
	t.Helper()
	record, err := json.Marshal(map[string]any{"type": "user", "sessionId": id, "cwd": cwd, "message": map[string]string{"role": "user", "content": "synthetic message"}})
	if err != nil {
		t.Fatal(err)
	}
	cliPut(t, path, string(record)+"\n")
}

func separationIDs(b *drain.Bundle) []string {
	ids := make([]string, 0, len(b.Sessions))
	for _, s := range b.Sessions {
		ids = append(ids, s.ID)
	}
	slices.Sort(ids)
	return ids
}

func TestImplicitClaudeSourceSeparation(t *testing.T) {
	projects, desktop := claudeSeparationStores(t)
	putClaudeSeparationSession(t, filepath.Join(projects, "mixed", "cli.jsonl"), "cli", "/work/project")
	putClaudeSeparationSession(t, filepath.Join(projects, "mixed", "desk.jsonl"), "desk", `C:\Users\example\CLAUDE\SCRATCH-WORKSPACES\project`)
	putClaudeSeparationSession(t, filepath.Join(projects, "mixed", "desk", "subagents", "agent-child.jsonl"), "desk", "")
	putClaudeSeparationSession(t, filepath.Join(projects, "mixed", "cli", "subagents", "agent-child.jsonl"), "cli", "")
	putClaudeSeparationSession(t, filepath.Join(desktop, "native.jsonl"), "native", "/work/no-scratch-marker")
	for _, source := range []string{"claude", "claude-code", "claude-desktop"} {
		t.Run(source, func(t *testing.T) {
			b, err := loadInput(source, "", "")
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"cli", "cli/agent-child"}
			if source == "claude-desktop" {
				want = []string{"desk", "desk/agent-child", "native"}
			}
			if got := separationIDs(b); !slices.Equal(got, want) {
				t.Fatalf("sessions = %v; want %v", got, want)
			}
		})
	}
	for _, source := range []string{"claude", "claude-code", "claude-desktop"} {
		t.Run("explicit-"+source, func(t *testing.T) {
			b, err := loadInput(source, projects, "")
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"cli", "cli/agent-child", "desk", "desk/agent-child"}
			if got := separationIDs(b); !slices.Equal(got, want) {
				t.Fatalf("explicit sessions = %v; want %v", got, want)
			}
		})
	}
}

func TestEmptyClaudeDesktopStoreCannotBorrowCLI(t *testing.T) {
	projects, desktop := claudeSeparationStores(t)
	cliPut(t, filepath.Join(desktop, "skills", "README.md"), "synthetic skill")
	putClaudeSeparationSession(t, filepath.Join(projects, "cli.jsonl"), "cli", "/work/project")
	if b, err := loadInput("claude-desktop", "", ""); err == nil || !strings.Contains(err.Error(), "no sessions found") {
		t.Fatalf("expected no Desktop sessions, got bundle %v, error %v", b, err)
	}
}
