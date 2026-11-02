package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unxed/agendrain/internal/drain"
)

func TestCLIExportAndList(t *testing.T) {
	d := t.TempDir()
	input := filepath.Join(d, "p.jsonl")
	os.WriteFile(input, []byte(`{"type":"session_meta","payload":{"id":"p"}}`+"\n"+`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Continue"}]}}`+"\n"), 0600)
	var out, stderr bytes.Buffer
	if code := run([]string{"list", "--source", "codex", "--input", input}, &out, &stderr); code != 0 || !strings.Contains(out.String(), "p") {
		t.Fatalf("%d %s %s", code, out.String(), stderr.String())
	}
	p := filepath.Join(d, "out.zip")
	out.Reset()
	stderr.Reset()
	if code := run([]string{"export", "--source", "codex", "--input", input, "--output", p}, &out, &stderr); code != 0 {
		t.Fatalf("%d %s", code, stderr.String())
	}
	if _, e := os.Stat(p); e != nil {
		t.Fatal(e)
	}
	if code := run([]string{"export", "--source", "codex", "--input", input, "--output", p}, &out, &stderr); code == 0 {
		t.Fatal("overwrote existing export")
	}
}

func TestCLIRejectsUnknownAndConflictingFlags(t *testing.T) {
	for _, args := range [][]string{{"wat"}, {"export", "--format", "rar"}, {"export", "--files", "full"}, {"export", "extra"}} {
		var out, stderr bytes.Buffer
		if run(args, &out, &stderr) == 0 {
			t.Errorf("accepted %v", args)
		}
	}
}

func cliPut(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestCLIDiscoversActiveAndArchivedCodexSessions(t *testing.T) {
	d := t.TempDir()
	t.Setenv("CODEX_HOME", d)
	cliPut(t, filepath.Join(d, "sessions", "parent.jsonl"), `{"type":"session_meta","payload":{"id":"parent"}}`+"\n")
	cliPut(t, filepath.Join(d, "archived_sessions", "child.jsonl"), `{"type":"session_meta","payload":{"id":"child","source":{"subagent":{"thread_spawn":{"parent_thread_id":"parent"}}}}}`+"\n")
	cliPut(t, filepath.Join(d, "archived_sessions", "old.jsonl"), `{"type":"session_meta","payload":{"id":"old"}}`+"\n")
	var out, stderr bytes.Buffer
	if code := run([]string{"list", "--source", "codex", "--json"}, &out, &stderr); code != 0 {
		t.Fatalf("%d %s", code, stderr.String())
	}
	var rows []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("discovery omitted a store: %s", out.String())
	}
	for _, id := range []string{"parent", "old"} {
		out.Reset()
		stderr.Reset()
		output := filepath.Join(d, id+".zip")
		if code := run([]string{"export", "--source", "codex", "--session", id, "--output", output}, &out, &stderr); code != 0 {
			t.Fatalf("%d %s", code, stderr.String())
		}
		b, err := drain.Load("agendrain", output)
		if err != nil {
			t.Fatal(err)
		}
		want := 1
		if id == "parent" {
			want = 2
		}
		if len(b.Sessions) != want {
			t.Fatalf("selected %s: got %d sessions, want %d", id, len(b.Sessions), want)
		}
	}
}

func TestCLIClaudeDesktopSkipsEmptyDiscoveredStore(t *testing.T) {
	d := t.TempDir()
	t.Setenv("HOME", d)
	t.Setenv("USERPROFILE", d)
	t.Setenv("APPDATA", filepath.Join(d, "config"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(d, "config"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(d, "claude"))
	config, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(config, "Claude", "local-agent-mode-sessions"), 0700); err != nil {
		t.Fatal(err)
	}
	cliPut(t, filepath.Join(d, "claude", "projects", "project", "p.jsonl"), `{"type":"user","sessionId":"p","message":{"role":"user","content":"Continue the local work"}}`+"\n")
	var out, stderr bytes.Buffer
	if code := run([]string{"list", "--source", "claude-desktop"}, &out, &stderr); code != 0 || !strings.Contains(out.String(), "p\tclaude-desktop") {
		t.Fatalf("%d %s %s", code, out.String(), stderr.String())
	}
}

func TestCLIDiscoveredSessionDuplicatesMustBeExact(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "identical", true: "conflicting"}[conflict], func(t *testing.T) {
			d := t.TempDir()
			t.Setenv("CODEX_HOME", d)
			content := `{"type":"session_meta","payload":{"id":"p"}}` + "\n"
			cliPut(t, filepath.Join(d, "sessions", "p.jsonl"), content)
			if conflict {
				content += `{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Different history"}]}}` + "\n"
			}
			cliPut(t, filepath.Join(d, "archived_sessions", "p.jsonl"), content)
			var out, stderr bytes.Buffer
			args := []string{"list", "--source", "codex", "--json"}
			if conflict {
				// Catalogs deliberately do not read bodies; export must still
				// reject divergent histories with identical metadata.
				args = []string{"export", "--source", "codex", "--all", "--output", filepath.Join(d, "all.zip")}
			}
			code := run(args, &out, &stderr)
			if conflict {
				if code == 0 || !strings.Contains(stderr.String(), "conflicting session") {
					t.Fatalf("accepted conflicting histories: %d %s %s", code, out.String(), stderr.String())
				}
				out.Reset()
				stderr.Reset()
				if code := run([]string{"list", "--source", "codex", "--input", filepath.Join(d, "sessions")}, &out, &stderr); code != 0 {
					t.Fatalf("explicit input unexpectedly included other stores: %d %s", code, stderr.String())
				}
				return
			}
			var rows []any
			if code != 0 || json.Unmarshal(out.Bytes(), &rows) != nil || len(rows) != 1 {
				t.Fatalf("failed to deduplicate exact copies: %d %s %s", code, out.String(), stderr.String())
			}
		})
	}
}

func TestCLISelectedCodexDoesNotReadUnrelatedBodies(t *testing.T) {
	d := t.TempDir()
	t.Setenv("CODEX_HOME", d)
	cliPut(t, filepath.Join(d, "sessions", "p.jsonl"), `{"type":"session_meta","payload":{"id":"p"}}`+"\n")
	cliPut(t, filepath.Join(d, "sessions", "other.jsonl"), `{"type":"session_meta","payload":{"id":"other"}}`+"\ncorrupt unrelated body\n")
	var out, stderr bytes.Buffer
	if code := run([]string{"export", "--source", "codex", "--session", "p", "--output", filepath.Join(d, "p.zip")}, &out, &stderr); code != 0 {
		t.Fatalf("selected export read unrelated body: %d %s", code, stderr.String())
	}
}

func TestCLICodexCatalogDoesNotReadBodiesOrInventCounts(t *testing.T) {
	d := t.TempDir()
	t.Setenv("CODEX_HOME", d)
	cliPut(t, filepath.Join(d, "sessions", "p.jsonl"), `{"type":"session_meta","payload":{"id":"p"}}`+"\ncorrupt body\n")
	var out, stderr bytes.Buffer
	if code := run([]string{"list", "--source", "codex"}, &out, &stderr); code != 0 || !strings.Contains(out.String(), "metadata only") || strings.Contains(out.String(), "0 events") {
		t.Fatalf("catalog read body or invented count: %d %s %s", code, out.String(), stderr.String())
	}
	out.Reset()
	stderr.Reset()
	if code := run([]string{"list", "--source", "codex", "--json"}, &out, &stderr); code != 0 || strings.Contains(out.String(), `"events"`) {
		t.Fatalf("JSON catalog invented count: %d %s %s", code, out.String(), stderr.String())
	}
}
