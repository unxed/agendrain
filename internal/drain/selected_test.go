package drain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func TestLoadSelectedCodexSkipsUnrelatedBodies(t *testing.T) {
	d := t.TempDir()
	parent := put(t, d, "sessions/p.jsonl", `{"type":"session_meta","payload":{"id":"p"}}`+"\n"+`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Exact requirement"}]}}`+"\n")
	put(t, d, "sessions/unrelated.jsonl", `{"type":"session_meta","payload":{"id":"unrelated"}}`+"\nNOT JSON: this unrelated body must never be decoded\n")
	child := []byte(`{"type":"session_meta","payload":{"id":"child","source":{"subagent":{"thread_spawn":{"parent_thread_id":"p"}}}}}` + "\n" + `{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Child evidence"}]}}` + "\n")
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	compressed := encoder.EncodeAll(child, nil)
	encoder.Close()
	if err := os.MkdirAll(filepath.Join(d, "archived_sessions"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "archived_sessions", "child.jsonl.zst"), compressed, 0600); err != nil {
		t.Fatal(err)
	}
	put(t, d, "archived_sessions/grandchild.jsonl", `{"type":"session_meta","payload":{"id":"grandchild","source":{"subagent":{"thread_spawn":{"parent_thread_id":"child"}}}}}`+"\n")
	t.Setenv("CODEX_HOME", d)
	for _, tc := range []struct {
		name, source, input string
		count               int
	}{{"discovery", "codex", "", 3}, {"explicit directory", "codex", filepath.Join(d, "sessions"), 1}, {"explicit transcript", "codex", parent, 1}, {"auto transcript", "auto", parent, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := LoadSelected(tc.source, tc.input, "p")
			if err != nil {
				t.Fatal(err)
			}
			if len(b.Sessions) != tc.count || !strings.Contains(joined(b), "Exact requirement") || strings.Contains(joined(b), "unrelated") {
				t.Fatalf("wrong selected context: %s", joined(b))
			}
			if tc.count == 3 && !strings.Contains(joined(b), "Child evidence") {
				t.Fatal("compressed child body was not loaded")
			}
		})
	}
	if _, err := LoadSelected("codex", parent, "unrelated"); err == nil {
		t.Fatal("explicit transcript allowed selection of a neighboring independent chat")
	}
}

func TestLoadSelectedCodexStillRejectsCorruptSelectedBody(t *testing.T) {
	d := t.TempDir()
	put(t, d, "p.jsonl", `{"type":"session_meta","payload":{"id":"p"}}`+"\nNOT JSON\n")
	if _, err := LoadSelected("codex", d, "p"); err == nil {
		t.Fatal("accepted malformed selected transcript")
	}
}

func TestLoadSelectedNonCodexUsesNormalSelection(t *testing.T) {
	p := put(t, t.TempDir(), "p.jsonl", `{"type":"user","sessionId":"p","message":{"role":"user","content":"Keep this requirement"}}`+"\n")
	b, err := LoadSelected("claude", p, "p")
	if err != nil || len(b.Sessions) != 1 || !strings.Contains(joined(b), "Keep this requirement") {
		t.Fatalf("normal importer selection failed: %v", err)
	}
}

func TestCatalogCodexReadsOnlyMetadata(t *testing.T) {
	d := t.TempDir()
	t.Setenv("CODEX_HOME", d)
	put(t, d, "sessions/p.jsonl", `{"type":"session_meta","payload":{"id":"p","cwd":"/repo","title":"Local work"}}`+"\ncorrupt body must not be decoded\n")
	put(t, d, "archived_sessions/child.jsonl", `{"type":"session_meta","payload":{"id":"child","parent_thread_id":"p"}}`+"\ncorrupt body must not be decoded\n")
	put(t, d, "sessions/empty.jsonl", "")
	b, err := CatalogCodex("")
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Sessions) != 2 || len(b.Warnings) != 1 {
		t.Fatalf("catalog omitted headers or limitations: %s", joined(b))
	}
	for _, s := range b.Sessions {
		if s.Events != nil {
			t.Fatal("catalog pretends to know message count")
		}
		if s.ID == "p" && (s.CWD != "/repo" || s.Title != "Local work") {
			t.Fatal("catalog dropped working directory/title")
		}
	}
}
