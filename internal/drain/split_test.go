package drain

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultZIPSeparatesSubagentsButMarkdownStaysCombined(t *testing.T) {
	b := &Bundle{Version: Version, Assets: map[string][]byte{"picture": {137, 80, 78, 71}}, Sessions: []Session{
		{ID: "parent", Events: []Event{{Role: "user", Blocks: []Block{textBlock("Main exact requirement")}}}},
		{ID: "child", ParentID: "parent", Title: "Build investigation", Events: []Event{{Role: "assistant", Blocks: []Block{textBlock("Child exact finding"), {Type: "image", MIME: "image/png", Asset: "picture"}}}}},
	}}
	d := t.TempDir()
	p := filepath.Join(d, "handoff.zip")
	if err := Write(b, p, Options{}); err != nil {
		t.Fatal(err)
	}
	files, err := zipData(p)
	if err != nil {
		t.Fatal(err)
	}
	main := string(files["conversation.md"])
	child := string(files["subagents/00000001.md"])
	if !strings.Contains(main, "Main exact requirement") || strings.Contains(main, "Child exact finding") || !strings.Contains(main, "subagents/00000001.md") {
		t.Fatal(main)
	}
	if !strings.Contains(child, "Child exact finding") || !strings.Contains(child, "../assets/") || !strings.Contains(child, "../conversation.md") {
		t.Fatal(child)
	}
	copy := load(t, "agendrain", p)
	if len(copy.Sessions) != 2 || len(copy.Assets) != 1 {
		t.Fatal(joined(copy))
	}
	md := filepath.Join(d, "combined.zip")
	if err := Write(b, md, Options{Format: "md-zip"}); err != nil {
		t.Fatal(err)
	}
	all, err := zipData(md)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(all["conversation.md"]), "Child exact finding") || all["subagents/00000001.md"] != nil {
		t.Fatal("Markdown mode must remain one combined document")
	}
}
