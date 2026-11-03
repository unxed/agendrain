package drain

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// expandCompacted is a reference resolver for the compact markers. It proves
// that every marker can be replaced by the identical value it points to.
func expandCompacted(t *testing.T, b *Bundle) *Bundle {
	t.Helper()
	raw, _ := json.Marshal(b)
	var out Bundle
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	out.Assets = b.Assets
	find := func(session string, event, block int) string {
		for _, s := range b.Sessions {
			if s.ID == session {
				if event < 1 || event > len(s.Events) || block < 1 || block > len(s.Events[event-1].Blocks) {
					t.Fatalf("dangling reference %s %d %d", session, event, block)
				}
				return s.Events[event-1].Blocks[block-1].Text
			}
		}
		t.Fatalf("dangling session reference %s", session)
		return ""
	}
	var expand func(v any) any
	expand = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			if id, ok := x["$agendrain_asset"].(string); ok && len(x) == 1 {
				data, ok := b.Assets[id]
				if !ok {
					t.Fatalf("missing asset %s", id)
				}
				return base64.StdEncoding.EncodeToString(data)
			}
			if ref, ok := x["$agendrain_duplicate"].(map[string]any); ok && len(x) == 1 {
				ev, _ := ref["event"].(json.Number).Int64()
				bl, _ := ref["block"].(json.Number).Int64()
				text := find(ref["session"].(string), int(ev), int(bl))
				ptr, inJSON := ref["pointer"].(string)
				if !inJSON {
					return text
				}
				d := json.NewDecoder(strings.NewReader(text))
				d.UseNumber()
				var target any
				if err := d.Decode(&target); err != nil {
					t.Fatal(err)
				}
				for _, part := range strings.Split(ptr, "/")[1:] { // "" is the root
					part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
					switch y := target.(type) {
					case map[string]any:
						target = y[part]
					case []any:
						i, _ := strconv.Atoi(part)
						target = y[i]
					}
				}
				return expand(target)
			}
			for k, y := range x {
				x[k] = expand(y)
			}
		case []any:
			for i, y := range x {
				x[i] = expand(y)
			}
		}
		return v
	}
	for si := range out.Sessions {
		for ei := range out.Sessions[si].Events {
			for bi := range out.Sessions[si].Events[ei].Blocks {
				bl := &out.Sessions[si].Events[ei].Blocks[bi]
				if bl.Type != "context" || !strings.Contains(bl.Text, "$agendrain_") {
					continue
				}
				d := json.NewDecoder(strings.NewReader(bl.Text))
				d.UseNumber()
				var v any
				if err := d.Decode(&v); err != nil {
					t.Fatal(err)
				}
				p, _ := json.Marshal(expand(v))
				bl.Text = string(p)
			}
		}
	}
	return &out
}

func sameJSON(t *testing.T, a, b string) bool {
	t.Helper()
	var x, y any
	da := json.NewDecoder(strings.NewReader(a))
	da.UseNumber()
	db := json.NewDecoder(strings.NewReader(b))
	db.UseNumber()
	if da.Decode(&x) != nil || db.Decode(&y) != nil {
		return a == b
	}
	return reflect.DeepEqual(x, y)
}

func compactFixture() *Bundle {
	prompt := strings.Repeat("System prompt section that repeats in every request snapshot. ", 10)
	requirement := strings.Repeat("User requirement: keep the exact wording of this decision. ", 6)
	tool := map[string]any{"name": "Read", "description": strings.Repeat("Reads a file. ", 30)}
	ctx := func(v any) Block { return Block{Type: "context", Text: jsonText(v)} }
	return &Bundle{Version: 1, Assets: map[string][]byte{}, Sessions: []Session{
		{ID: "parent", Source: "claude", Events: []Event{
			// Technical context first: it references the later user text.
			{Role: "system", Kind: "attachment", Blocks: []Block{ctx(map[string]any{"type": "queued", "prompt": requirement, "uuid": "1"})}},
			{Role: "user", Kind: "user", Blocks: []Block{{Type: "text", Text: requirement}}},
			{Role: "system", Kind: "shape", Blocks: []Block{ctx(map[string]any{"system": []any{prompt}, "tools": []any{tool}, "uuid": "2", "n": 1.5e300})}},
			{Role: "system", Kind: "shape", Blocks: []Block{ctx(map[string]any{"system": []any{prompt, "small"}, "tools": []any{tool}, "uuid": "3"})}},
			{Role: "tool", Kind: "tool_result", Blocks: []Block{{Type: "tool_result", Text: "identical tool output " + prompt}}},
			// One API message recorded as two content events with the same metadata.
			{Role: "assistant", Blocks: []Block{{Type: "text", Text: "Decision A"}, ctx(map[string]any{"usage": prompt, "id": "msg_1"})}},
			{Role: "assistant", Blocks: []Block{{Type: "text", Text: "Decision B"}, ctx(map[string]any{"usage": prompt, "id": "msg_1"})}},
		}},
		{ID: "parent/agent-1", ParentID: "parent", Source: "claude", Events: []Event{
			{Role: "system", Kind: "shape", Blocks: []Block{ctx(map[string]any{"system": []any{prompt}, "tools": []any{tool}, "uuid": "4"})}},
			{Role: "tool", Kind: "tool_result", Blocks: []Block{{Type: "tool_result", Text: "identical tool output " + prompt}}},
		}},
	}}
}

func TestCompactReferencesRepeatedContextValuesAcrossSessions(t *testing.T) {
	b := compactFixture()
	original := compactFixture()
	if err := Prepare(b, Options{Compact: true}); err != nil {
		t.Fatal(err)
	}
	s := joined(b)
	prompt := "System prompt section that repeats in every request snapshot."
	// Once in the first request snapshot and once inside the parent's tool output.
	if n := strings.Count(s, prompt); n != 20 {
		t.Fatalf("repeated prompt stored %d times: %s", n/10, s)
	}
	// User wording stays verbatim in place; the context copy points to it.
	if b.Sessions[0].Events[1].Blocks[0].Text != original.Sessions[0].Events[1].Blocks[0].Text {
		t.Fatal("user text changed")
	}
	if !strings.Contains(b.Sessions[0].Events[0].Blocks[0].Text, `"$agendrain_duplicate":{"block":1,"event":2,"session":"parent"}`) {
		t.Fatal(b.Sessions[0].Events[0].Blocks[0].Text)
	}
	// Whole duplicate blocks are referenced across parent and subagent sessions.
	if got := b.Sessions[1].Events[1].Blocks[0]; got.Type != "reference" || got.Text != "Exact duplicate of parent event 5" {
		t.Fatalf("%+v", got)
	}
	if len(b.Changes) > 3 {
		t.Fatalf("per-block change noise: %v", b.Changes)
	}
	expanded := expandCompacted(t, b)
	for si, sess := range original.Sessions {
		for ei, e := range sess.Events {
			for bi, bl := range e.Blocks {
				got := expanded.Sessions[si].Events[ei].Blocks[bi]
				if got.Type == "reference" {
					continue
				}
				if !sameJSON(t, got.Text, bl.Text) {
					t.Fatalf("session %d event %d not restored:\n%s\n%s", si, ei+1, got.Text, bl.Text)
				}
			}
		}
	}
}

func TestCompactLeavesSmallValuesAndDefaultExportAlone(t *testing.T) {
	b := compactFixture()
	if err := Prepare(b, Options{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(joined(b), "agendrain_") || len(b.Changes) != 0 {
		t.Fatal("non-compact export changed")
	}
	b = compactFixture()
	if err := Prepare(b, Options{Compact: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.Sessions[0].Events[3].Blocks[0].Text, `"small"`) || !strings.Contains(b.Sessions[0].Events[3].Blocks[0].Text, `"uuid":"3"`) {
		t.Fatal(b.Sessions[0].Events[3].Blocks[0].Text)
	}
}

func TestCompactMovesEmbeddedBase64ToAssets(t *testing.T) {
	png := append([]byte{137, 80, 78, 71, 13, 10, 26, 10}, bytes.Repeat([]byte{7}, 2000)...)
	other := bytes.Repeat([]byte{9, 1}, 1500)
	b := &Bundle{Version: 1, Assets: map[string][]byte{}, Sessions: []Session{{ID: "s", Events: []Event{
		{Role: "user", Blocks: []Block{{Type: "image", MIME: "image/png"}}},
		{Role: "system", Kind: "api-request-blob", Blocks: []Block{{Type: "context", Text: jsonText(map[string]any{"content": []any{
			map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": base64.StdEncoding.EncodeToString(png)}},
			map[string]any{"type": "document", "source": map[string]any{"type": "base64", "media_type": "application/pdf", "data": base64.StdEncoding.EncodeToString(other)}},
		}})}}},
	}}}}
	b.Sessions[0].Events[0].Blocks[0].Asset = b.asset(png)
	original := jsonText(b.Sessions[0].Events[1].Blocks[0].Text)
	if err := Prepare(b, Options{Compact: true}); err != nil {
		t.Fatal(err)
	}
	e := b.Sessions[0].Events[1]
	if strings.Contains(e.Blocks[0].Text, base64.StdEncoding.EncodeToString(png)[:40]) || strings.Contains(e.Blocks[0].Text, base64.StdEncoding.EncodeToString(other)[:40]) {
		t.Fatal("base64 still inline")
	}
	// Bytes without an existing attachment block become a linked attachment.
	if len(e.Blocks) != 2 || e.Blocks[1].Asset == "" || e.Blocks[1].MIME != "application/pdf" || len(b.Assets) != 2 {
		t.Fatalf("%+v %d", e.Blocks, len(b.Assets))
	}
	if !sameJSON(t, expandCompacted(t, b).Sessions[0].Events[1].Blocks[0].Text, original) {
		t.Fatal("base64 not restorable")
	}
	d := t.TempDir()
	out := filepath.Join(d, "c.zip")
	if err := Write(b, out, Options{Format: "zip"}); err != nil {
		t.Fatal(err)
	}
	back := load(t, "agendrain", out)
	if len(back.Assets) != 2 || !sameJSON(t, expandCompacted(t, back).Sessions[0].Events[1].Blocks[0].Text, original) {
		t.Fatal("compact ZIP round trip lost bytes")
	}
	// Explicit image removal is not undone by compaction.
	b = &Bundle{Version: 1, Assets: map[string][]byte{}, Sessions: []Session{{ID: "s", Events: []Event{
		{Role: "system", Blocks: []Block{{Type: "context", Text: jsonText(map[string]any{"source": map[string]any{"type": "base64", "media_type": "image/png", "data": base64.StdEncoding.EncodeToString(png)}})}}},
	}}}}
	if err := Prepare(b, Options{Compact: true, Images: "omit"}); err != nil {
		t.Fatal(err)
	}
	if len(b.Assets) != 0 || len(b.Sessions[0].Events[0].Blocks) != 1 {
		t.Fatal("compaction re-added an omitted image")
	}
}

func TestJSONKeepsMarkupReadable(t *testing.T) {
	if got := jsonText(map[string]any{"a": "<system-reminder>&"}); got != `{"a":"<system-reminder>&"}` {
		t.Fatal(got)
	}
	b := &Bundle{Version: 1, Assets: map[string][]byte{}, Sessions: []Session{{ID: "s", Events: []Event{{Role: "user", Blocks: []Block{{Type: "text", Text: "<b>keep & read</b>"}}}}}}}
	out := filepath.Join(t.TempDir(), "m.zip")
	if err := Write(b, out, Options{Format: "zip"}); err != nil {
		t.Fatal(err)
	}
	back := load(t, "agendrain", out)
	files, err := zipData(out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(files["manifest.json"], []byte("<b>keep & read</b>")) || back.Sessions[0].Events[0].Blocks[0].Text != "<b>keep & read</b>" {
		t.Fatal(string(files["manifest.json"]))
	}
}

func TestCompactIsIdempotentOnReimport(t *testing.T) {
	b := compactFixture()
	if err := Prepare(b, Options{Compact: true}); err != nil {
		t.Fatal(err)
	}
	once := joined(b)
	if err := Prepare(b, Options{Compact: true}); err != nil {
		t.Fatal(err)
	}
	b.Changes = nil
	var first Bundle
	json.Unmarshal([]byte(once), &first)
	first.Changes = nil
	if joined(b) != joined(&first) {
		t.Fatalf("second compaction changed references:\n%s\n%s", once, joined(b))
	}
}
