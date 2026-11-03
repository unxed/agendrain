package drain

import (
	"strings"
	"testing"
)

func TestClaudeNativeForkKeepsCopiedHistoryUnderCurrentFileIdentity(t *testing.T) {
	d := t.TempDir()
	oldID := "10000000-0000-4000-8000-000000000001"
	newID := "10000000-0000-4000-8000-000000000002"
	put(t, d, oldID+".jsonl", `{"type":"user","sessionId":"`+oldID+`","uuid":"old","content":"Original requirement"}`+"\n")
	put(t, d, newID+".jsonl", `{"type":"user","sessionId":"`+oldID+`","uuid":"old","content":"Original requirement"}`+"\n"+
		`{"type":"custom-title","sessionId":"`+newID+`","customTitle":"Resumed project"}`+"\n"+
		`{"type":"assistant","sessionId":"`+newID+`","uuid":"new","content":"Current decision"}`+"\n")
	b, err := Load("claude", d)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Sessions) != 2 {
		t.Fatalf("fork and original were merged: %s", joined(b))
	}
	if err := b.Select(newID); err != nil {
		t.Fatal(err)
	}
	if len(b.Sessions) != 1 || b.Sessions[0].Title != "Resumed project" {
		t.Fatal("cannot select the resumed native file", joined(b))
	}
	for _, text := range []string{"Original requirement", "Current decision"} {
		if !strings.Contains(joined(b), text) {
			t.Fatal("fork history lost", text)
		}
	}
}
