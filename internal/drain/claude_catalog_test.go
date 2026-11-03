package drain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

func TestClaudeCatalogPreviewMetadataAndSelectedFamily(t *testing.T) {
	d := t.TempDir()
	put(t, d, "p.jsonl", `{"type":"assistant","sessionId":"p","content":"fallback"}`+"\n"+`{"type":"user","sessionId":"p","cwd":"/work/project","timestamp":"2026-11-01T01:00:00Z","message":{"content":[{"type":"image","source":{"type":"base64","data":"not-base64"}},{"type":"tool_result","content":"secret tool result"},{"type":"text","text":"Readable preview"}]}}`+"\n"+`{"type":"custom-title","sessionId":"p","customTitle":"Recorded title","timestamp":"2026-11-02T01:00:00Z"}`+"\n")
	put(t, d, "p/subagents/agent-x.jsonl", `{"type":"assistant","sessionId":"p","content":"Child"}`+"\n")
	put(t, d, "other.jsonl", `{"type":"user","sessionId":"other","content":"Other"}`+"\ncorrupt unrelated body\n")
	b, err := CatalogClaude(d)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Assets) != 0 || len(b.Sessions) != 3 {
		t.Fatalf("unexpected catalog size: %d sessions, %d assets", len(b.Sessions), len(b.Assets))
	}
	if err = b.Select("p"); err != nil {
		t.Fatal(err)
	}
	if len(b.Sessions) != 2 {
		t.Fatal("missing child")
	}
	s := b.Sessions[0]
	if s.Title != "Recorded title" || s.CWD != "/work/project" || s.UpdatedAt != "2026-11-02T01:00:00Z" || len(s.Events) != 1 || !strings.Contains(jsonText(s.Events), "Readable preview") || strings.Contains(jsonText(s.Events), "secret tool result") || strings.Contains(jsonText(s.Events), "not-base64") {
		t.Fatalf("bad preview metadata: %s", jsonText(s))
	}
	selected, err := loadSelectedClaude(d, "p/agent-x")
	if err != nil {
		t.Fatal(err)
	}
	if len(selected.Sessions) != 1 || selected.Sessions[0].ParentID != "p" {
		t.Fatal("selected child lost")
	}
	// Replace the deliberately invalid image before canonical root export.
	put(t, d, "p.jsonl", `{"type":"user","sessionId":"p","content":"Canonical root"}`+"\n")
	selected, err = loadSelectedClaude(d, "p")
	if err != nil {
		t.Fatal(err)
	}
	if len(selected.Sessions) != 2 {
		t.Fatal("selected descendants missing")
	}
}

func TestClaudeCatalogNativeForkIdentity(t *testing.T) {
	d := t.TempDir()
	oldID := "10000000-0000-4000-8000-000000000001"
	newID := "10000000-0000-4000-8000-000000000002"
	put(t, d, newID+".jsonl", `{"type":"user","sessionId":"`+oldID+`","content":"Copied context"}`+"\n"+`{"type":"custom-title","sessionId":"`+newID+`","customTitle":"Fork title"}`+"\n")
	b, err := CatalogClaude(d)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Sessions) != 1 || b.Sessions[0].ID != newID || b.Sessions[0].Title != "Fork title" {
		t.Fatal("fork identity changed")
	}
	selected, err := loadSelectedClaude(filepath.Join(d, newID+".jsonl"), newID)
	if err != nil || len(selected.Sessions) != 1 {
		t.Fatalf("selected fork: %v", err)
	}
}

func TestClaudeCatalogChildIndexStopsAfterIdentity(t *testing.T) {
	d := t.TempDir()
	put(t, d, "parent.jsonl", `{"type":"user","sessionId":"parent","content":"Parent"}`+"\n")
	put(t, d, "parent/subagents/agent-child.jsonl", `{"type":"assistant","sessionId":"parent","content":"Child preview"}`+"\n"+"corrupt child body\n")

	b, err := CatalogClaude(d)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Warnings) != 0 {
		t.Fatalf("child catalog scanned body after identity: %v", b.Warnings)
	}
	if err := b.Select("parent"); err != nil {
		t.Fatal(err)
	}
	var child *Session
	for i := range b.Sessions {
		if b.Sessions[i].ID == "parent/agent-child" {
			child = &b.Sessions[i]
		}
	}
	if child == nil || child.ParentID != "parent" || len(child.Events) != 0 {
		t.Fatalf("child metadata was not indexed without reading its body: %+v", child)
	}
	if _, err := loadSelectedClaude(d, "parent/agent-child"); err == nil {
		t.Fatal("canonical selected-family import accepted malformed child body")
	}
}

// Opt in to inspect an existing store without logging any conversation content.
func TestClaudeCatalogNativeStoreOptIn(t *testing.T) {
	input := os.Getenv("AGENDRAIN_TEST_CLAUDE_STORE")
	if input == "" {
		t.Skip("set AGENDRAIN_TEST_CLAUDE_STORE to index native metadata")
	}
	start := time.Now()
	b, err := CatalogClaude(input)
	if err != nil {
		t.Fatal("native Claude catalog failed")
	}
	if len(b.Assets) != 0 {
		t.Fatal("catalog decoded attachments")
	}
	t.Logf("indexed %d sessions, %d warnings in %s", len(b.Sessions), len(b.Warnings), time.Since(start))
}

func TestClaudeCatalogCompressedTranscript(t *testing.T) {
	d := t.TempDir()
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer encoder.Close()
	raw := encoder.EncodeAll([]byte(`{"type":"user","sessionId":"p","content":"Compressed preview"}`+"\n"), nil)
	if err := os.WriteFile(filepath.Join(d, "p.jsonl.zst"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	b, err := CatalogClaude(d)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Sessions) != 1 || b.Sessions[0].ID != "p" || !strings.Contains(jsonText(b.Sessions[0].Events), "Compressed preview") {
		t.Fatal("compressed catalog metadata lost")
	}
	b, err = loadSelectedClaude(d, "p")
	if err != nil || len(b.Sessions) != 1 {
		t.Fatalf("compressed selected import: %v", err)
	}
}

func TestClaudeCatalogAttachmentOnlyUserRemainsAvailable(t *testing.T) {
	for _, tc := range []struct{ name, content, kind, path, mime string }{
		{"file", `[{"type":"file","path":"/not-read/report.txt","mime_type":"text/plain","description":"Recorded file"}]`, "file", "/not-read/report.txt", "text/plain"},
		{"image", `[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"invalid-base64"}}]`, "image", "", "image/png"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := t.TempDir()
			put(t, d, "p.jsonl", `{"type":"user","sessionId":"p","message":{"content":`+tc.content+`}}`+"\n")
			b, err := CatalogClaude(d)
			if err != nil {
				t.Fatal(err)
			}
			if len(b.Assets) != 0 || len(b.Sessions) != 1 || len(b.Sessions[0].Events) != 1 {
				t.Fatal("attachment-only conversation was omitted or decoded")
			}
			blocks := b.Sessions[0].Events[0].Blocks
			if len(blocks) != 1 || blocks[0].Type != tc.kind || blocks[0].Path != tc.path || blocks[0].MIME != tc.mime {
				t.Fatalf("attachment marker metadata lost: %+v", blocks)
			}
			if strings.Contains(jsonText(b), "invalid-base64") {
				t.Fatal("encoded payload retained")
			}
		})
	}
}

func TestClaudeCatalogCountsUserRequests(t *testing.T) {
	d := t.TempDir()
	put(t, d, "c.jsonl", `{"type":"user","sessionId":"c","message":{"content":"one"}}`+"\n"+
		`{"type":"assistant","sessionId":"c","message":{"content":[{"type":"tool_use","name":"x"}]}}`+"\n"+
		`{"type":"user","sessionId":"c","message":{"content":[{"type":"tool_result","content":"r"}]}}`+"\n"+
		`{"type":"user","sessionId":"c","isMeta":true,"message":{"content":"meta"}}`+"\n"+
		`{"type":"user","sessionId":"c","message":{"content":[{"type":"text","text":"two"}]}}`+"\n")
	b, err := CatalogClaude(d)
	if err != nil {
		t.Fatal(err)
	}
	if b.Sessions[0].Requests != 2 {
		t.Fatal(b.Sessions[0].Requests)
	}
}
