package drain

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexFallbackOrder(t *testing.T) {
	p := put(t, t.TempDir(), "p.jsonl", `{"type":"session_meta","payload":{"id":"p"}}
{"type":"event_msg","payload":{"type":"user_message","message":"First prompt"}}
{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Later answer"}]}}
`)
	b := load(t, "codex", p)
	s := joined(b)
	if strings.Index(s, "First prompt") > strings.Index(s, "Later answer") {
		t.Fatal("fallback user event moved after answer:", s)
	}
}

func TestClaudeToolImagesBecomeAssets(t *testing.T) {
	p := put(t, t.TempDir(), "p.jsonl", `{"type":"user","sessionId":"p","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"screenshot","content":[{"type":"text","text":"Screenshot evidence"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw=="}}]}]}}`+"\n")
	b := load(t, "claude", p)
	if len(b.Assets) != 1 {
		t.Fatal("tool image was not extracted", joined(b))
	}
	if strings.Contains(joined(b), "iVBORw==") {
		t.Fatal("image still duplicated inline")
	}
}

func TestOpenCodeV2PartsAndUserFiles(t *testing.T) {
	p := database(t, `CREATE TABLE session_v2(id TEXT,parent_id TEXT,title TEXT,directory TEXT); CREATE TABLE session_message(id TEXT,session_id TEXT,type TEXT,seq INTEGER,time_created INTEGER,data TEXT); CREATE TABLE session_part(message_id TEXT,ordinal INTEGER,type TEXT,data TEXT);`,
		`INSERT INTO session_v2 VALUES('s',NULL,'Work','/repo');`,
		`INSERT INTO session_message VALUES('u','s','user',1,1,'{"text":"Requirement","files":[{"type":"image","url":"data:image/png;base64,iVBORw=="}]}'),('a','s','assistant',2,2,'{}');`,
		`INSERT INTO session_part VALUES('a',0,'text','{"text":"Decision"}'),('a',1,'tool','{"name":"bash","state":{"status":"completed","input":{"command":"test"},"content":[{"type":"image","url":"data:image/png;base64,iVBORw=="}]}}');`)
	b := load(t, "opencode", p)
	if !strings.Contains(joined(b), "Decision") || len(b.Assets) != 1 || strings.Contains(joined(b), "iVBORw==") {
		t.Fatal(joined(b))
	}
}

func TestSelectionFollowsSpawnResultReferences(t *testing.T) {
	b := &Bundle{Sessions: []Session{
		{ID: "p", Events: []Event{{Role: "assistant", Kind: "tool_call", Name: "spawn_agent", CallID: "c"}, {Role: "tool", Kind: "tool_result", CallID: "c", Blocks: []Block{textBlock(`{"agent_id":"child"}`)}}}},
		{ID: "child", Events: []Event{{Role: "assistant", Blocks: []Block{textBlock("Child decision")}}}},
		{ID: "other"},
	}}
	if err := b.Select("p"); err != nil {
		t.Fatal(err)
	}
	if len(b.Sessions) != 2 {
		t.Fatal(joined(b))
	}
}

func TestImageDescriptionsRemoveBytes(t *testing.T) {
	b := &Bundle{Assets: map[string][]byte{"image": {137, 80, 78, 71}}, Sessions: []Session{{ID: "p", Events: []Event{{Role: "user", Blocks: []Block{{Type: "image", Asset: "image", MIME: "image/png"}}}}}}}
	if err := Prepare(b, Options{Images: "describe", Descriptions: map[string]string{"image": "The test log shows PASS"}}); err != nil {
		t.Fatal(err)
	}
	if len(b.Assets) != 0 || !strings.Contains(joined(b), "The test log shows PASS") {
		t.Fatal(joined(b))
	}
}

func TestPortableRoundtripAndMarkdownOnlyZip(t *testing.T) {
	b := &Bundle{Version: 1, Assets: map[string][]byte{"image": {137, 80, 78, 71}}, Sessions: []Session{{ID: "p", Events: []Event{{Role: "user", Blocks: []Block{{Type: "image", Asset: "image", MIME: "image/png"}, {Type: "text", Text: "Exact decision"}}}}}}}
	d := t.TempDir()
	p := filepath.Join(d, "one.zip")
	if err := Write(b, p, Options{}); err != nil {
		t.Fatal(err)
	}
	copy := load(t, "agendrain", p)
	if !bytes.Equal(copy.Assets["image"], b.Assets["image"]) {
		t.Fatal("roundtrip lost image")
	}
	md := filepath.Join(d, "studio.zip")
	if err := Write(copy, md, Options{Format: "md-zip"}); err != nil {
		t.Fatal(err)
	}
	z, err := zip.OpenReader(md)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	for _, f := range z.File {
		if f.Name != "conversation.md" && f.Name != "images/00000001.png" {
			t.Error(f.Name)
		}
	}
}

func TestExplicitBinaryLocalFile(t *testing.T) {
	d := t.TempDir()
	p := put(t, d, "image.png", string([]byte{137, 80, 78, 71, 13, 10, 26, 10, 0, 0, 0}))
	b := &Bundle{Assets: map[string][]byte{}, Sessions: []Session{{ID: "p", Events: []Event{{Role: "user", Blocks: []Block{{Type: "file", Path: p}}}}}}}
	if err := Prepare(b, Options{Files: "full", Roots: []string{d}}); err != nil {
		t.Fatal(err)
	}
	if len(b.Assets) != 1 {
		t.Fatal(joined(b))
	}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "\\u0000") {
		t.Fatal("binary was serialized as text")
	}
}

func TestZIPRejectsTraversalAndTampering(t *testing.T) {
	for _, name := range []string{"../conversations.json", "/conversations.json", "a/../conversations.json"} {
		var buf bytes.Buffer
		z := zip.NewWriter(&buf)
		f, _ := z.Create(name)
		f.Write([]byte("[]"))
		z.Close()
		p := filepath.Join(t.TempDir(), "bad.zip")
		os.WriteFile(p, buf.Bytes(), 0600)
		if _, err := Load("claude-desktop", p); err == nil {
			t.Errorf("accepted %s", name)
		}
	}
}

func TestModelStrippingFindsNestedCustomIDs(t *testing.T) {
	b := &Bundle{Assets: map[string][]byte{}, Sessions: []Session{{ID: "p", Events: []Event{{Role: "system", Blocks: []Block{{Type: "context", Text: `{"model":{"modelID":"bespoke-v12","providerID":"local"}}`}}}, {Role: "assistant", Blocks: []Block{textBlock("bespoke-v12 chose this")}}}}}}
	if err := Prepare(b, Options{StripModels: true}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(joined(b), "bespoke-v12") {
		t.Fatal(joined(b))
	}
}

func TestPruningKeepsUserAndAssistantDecisions(t *testing.T) {
	b := &Bundle{Sessions: []Session{{ID: "p", Events: []Event{
		{Role: "user", Time: "2026-01-01T00:00:00Z", Blocks: []Block{textBlock("Exact requirement")}},
		{Role: "tool", Kind: "tool_result", Time: "2026-01-01T00:00:00Z", Blocks: []Block{textBlock("Old huge output")}},
		{Role: "assistant", Time: "2026-01-01T00:00:00Z", Blocks: []Block{textBlock("Exact decision")}},
	}}}}
	if err := Prepare(b, Options{PruneToolsBefore: "2026-10-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	s := joined(b)
	if strings.Contains(s, "Old huge output") || !strings.Contains(s, "Exact requirement") || !strings.Contains(s, "Exact decision") {
		t.Fatal(s)
	}
}

func TestCodexCLIAndDesktop(t *testing.T) {
	for _, origin := range []string{"codex_cli_rs", "Codex Desktop"} {
		t.Run(origin, func(t *testing.T) {
			p := put(t, t.TempDir(), "rollout.jsonl", `{"type":"session_meta","payload":{"id":"p","originator":"`+origin+`","source":"vscode","base_instructions":{"text":"Exact base prompt"}}}`+"\n"+`{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"App instructions"}]}}`+"\n"+`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,iVBORw=="}]}}`+"\n")
			b := load(t, "codex", p)
			if len(b.Assets) != 1 || !strings.Contains(joined(b), origin) || !strings.Contains(joined(b), "Exact base prompt") || !strings.Contains(joined(b), "App instructions") {
				t.Fatal(joined(b))
			}
		})
	}
}

func TestSQLiteURIWithSpecialPathAndReadOnly(t *testing.T) {
	p := database(t, `CREATE TABLE proof(value TEXT); INSERT INTO proof VALUES('original');`)
	d := filepath.Join(t.TempDir(), "space # unicode тест")
	if err := os.Mkdir(d, 0700); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(d, "question?.db")
	if os.PathSeparator == '\\' {
		dest = filepath.Join(d, "question.db")
	}
	if err := os.Rename(p, dest); err != nil {
		t.Fatal(err)
	}
	db, err := openDB(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`INSERT INTO proof VALUES('changed')`); err == nil {
		t.Fatal("store writable")
	}
}
