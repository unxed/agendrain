package drain

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeDesktopAnonymousNestedAuditLogsStaySeparate(t *testing.T) {
	d := t.TempDir()
	put(t, d, "local_A/audit.jsonl", `{"type":"user","uuid":"same-local-id","content":"Task A"}`+"\n")
	put(t, d, "local_B/audit.jsonl", `{"type":"user","uuid":"same-local-id","content":"Task B"}`+"\n")
	b, err := Load("claude-desktop", d)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Sessions) != 2 {
		t.Fatalf("unrelated anonymous audit logs merged: got %d sessions, want 2", len(b.Sessions))
	}
	if b.Sessions[0].ID == b.Sessions[1].ID {
		t.Fatal("anonymous file identities collide")
	}
	for _, s := range b.Sessions {
		if len(s.Events) != 1 {
			t.Fatalf("session %q contains unrelated events: %d", s.ID, len(s.Events))
		}
		if strings.Contains(jsonText(s), "Task A") && strings.Contains(jsonText(s), "Task B") {
			t.Fatal("unrelated conversations were mixed")
		}
	}
}

func TestClaudeRejectsMixedExplicitSessionIDs(t *testing.T) {
	for _, source := range []string{"claude", "claude-desktop"} {
		t.Run(source, func(t *testing.T) {
			d := t.TempDir()
			p := put(t, d, "local_A/audit.jsonl", `{"type":"user","sessionId":"a","content":"Task A"}`+"\n"+
				`{"type":"system","content":"Owner not recorded"}`+"\n"+
				`{"type":"user","sessionId":"b","content":"Task B"}`+"\n")
			input := p
			if source == "claude-desktop" {
				input = d
			}
			_, err := Load(source, input)
			if err == nil {
				t.Fatal("mixed session identities silently attributed to the last session")
			}
			if !strings.Contains(err.Error(), "multiple sessionId values") {
				t.Fatalf("unclear identity error: %v", err)
			}
		})
	}
}

func TestClaudeSingleExplicitSessionOwnsAnonymousMetadata(t *testing.T) {
	p := put(t, t.TempDir(), "audit.jsonl", `{"type":"system","content":"Earlier metadata"}`+"\n"+
		`{"type":"user","sessionId":"p","cwd":"/work/project","content":"Task"}`+"\n"+
		`{"type":"custom-title","sessionId":"p","customTitle":"Recorded title"}`+"\n"+
		`{"type":"system","content":"Later metadata"}`+"\n")
	b, err := Load("claude", p)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Sessions) != 1 || b.Sessions[0].ID != "p" || len(b.Sessions[0].Events) != 4 || b.Sessions[0].Title != "Recorded title" {
		t.Fatalf("single-session semantics changed: %s", jsonText(b.Sessions))
	}
}

func TestClaudeExplicitSegmentsStillDeduplicateAndKeepChildren(t *testing.T) {
	d := t.TempDir()
	event := `{"type":"user","sessionId":"p","uuid":"u","message":{"role":"user","content":"Exact requirement"}}` + "\n"
	put(t, d, "segment-a.jsonl", event)
	put(t, d, "segment-b.jsonl", event)
	put(t, d, "p/subagents/agent-x.jsonl", `{"type":"assistant","sessionId":"p","uuid":"child-event","message":{"role":"assistant","content":"Child finding"}}`+"\n")
	b, err := Load("claude", d)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Sessions) != 2 {
		t.Fatalf("sessions=%d want 2", len(b.Sessions))
	}
	for _, s := range b.Sessions {
		if len(s.Events) != 1 {
			t.Fatalf("session %q repeated events: %d", s.ID, len(s.Events))
		}
		if s.ID != "p" && (s.ID != "p/agent-x" || s.ParentID != "p") {
			t.Fatalf("child identity changed: %#v", s)
		}
	}
}

func TestClaudeAnonymousFallbackIDsDoNotContainAbsoluteInputRoot(t *testing.T) {
	d := t.TempDir()
	put(t, d, "local_A/audit.jsonl", `{"type":"user","content":"Task"}`+"\n")
	b, err := Load("claude-desktop", d)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.Sessions[0].ID, filepath.ToSlash(d)) || filepath.IsAbs(b.Sessions[0].ID) {
		t.Fatalf("fallback identity leaks absolute import root: %q", b.Sessions[0].ID)
	}
}

func TestClaudeAnonymousFallbackDoesNotMergeWithExplicitIdentity(t *testing.T) {
	for _, tc := range []struct{ name, path, id string }{
		{"legacy flat basename", "audit.jsonl", "audit"},
		{"reserved nested fallback", "local_A/audit.jsonl", "file:local_A/audit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := t.TempDir()
			put(t, d, tc.path, `{"type":"user","content":"Anonymous task"}`+"\n")
			put(t, d, "z_explicit.jsonl", `{"type":"user","sessionId":"`+tc.id+`","content":"Explicit task"}`+"\n")
			b, err := Load("claude", d)
			if err != nil {
				if !strings.Contains(err.Error(), "identity collision") {
					t.Fatalf("unclear collision error: %v", err)
				}
				return
			}
			if len(b.Sessions) != 2 {
				t.Fatalf("anonymous and explicit histories merged: %s", jsonText(b.Sessions))
			}
		})
	}
}

func TestClaudeAnonymousSingleFileRetainsLegacyBasename(t *testing.T) {
	p := put(t, t.TempDir(), "audit.jsonl", `{"type":"user","content":"Anonymous task"}`+"\n")
	b, err := Load("claude", p)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Sessions) != 1 || b.Sessions[0].ID != "audit" {
		t.Fatalf("single anonymous file identity changed: %s", jsonText(b.Sessions))
	}
}
