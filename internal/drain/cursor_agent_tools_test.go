package drain

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Field numbers below come from the agent.v1 descriptors extracted from the
// official 2026.10.01-e373342 bundle (see cursor_agent_schema.go). No user data.
func caVarint(field, value uint64) []byte {
	return binary.AppendUvarint(binary.AppendUvarint(nil, field<<3), value)
}
func caDouble(field uint64, v float64) []byte {
	p := binary.AppendUvarint(nil, field<<3|1)
	return binary.LittleEndian.AppendUint64(p, math.Float64bits(v))
}

// caStep wraps a ToolCall as ConversationStep.tool_call stored as a blob.
func (f *caFixture) toolTurn(user []byte, calls ...[]byte) []byte {
	f.t.Helper()
	parts := [][]byte{caBytes(1, user)}
	for _, call := range calls {
		parts = append(parts, caBytes(2, f.blob(caBytes(2, call))))
	}
	return f.blob(caBytes(1, caJoin(parts...)))
}

func (f *caFixture) rootWithTurns(turns ...[]byte) {
	f.t.Helper()
	var parts [][]byte
	for _, turn := range turns {
		parts = append(parts, caBytes(8, turn))
	}
	f.meta(f.blob(caJoin(parts...)))
}

func eventsOfKind(b *Bundle, kind string) []Event {
	var out []Event
	for _, s := range b.Sessions {
		for _, e := range s.Events {
			if e.Kind == kind {
				out = append(out, e)
			}
		}
	}
	return out
}

func eventText(e Event) string {
	var out []string
	for _, bl := range e.Blocks {
		out = append(out, bl.Description, bl.Text)
	}
	return strings.Join(out, "\n")
}

func TestCursorAgentShellToolCallIsDecoded(t *testing.T) {
	f := newCAFixture(t)
	user := f.blob(caText(1, "run it"))
	args := caJoin(caText(1, "go test ./..."), caText(2, "/repo"), caVarint(3, 30000), caText(4, "inner-id"),
		caText(5, "go"), caBytes(8, caVarint(1, 1)))
	success := caJoin(caText(1, "go test ./..."), caText(2, "/repo"), caVarint(3, 0), caText(5, strings.Repeat("padding ", 30)+"ok  \tpkg\t0.1s\nline two\nline three\nline four\nline five\nline six\nline seven\nline eight\nline nine\nline ten\nline eleven\nline twelve\nline thirteen\nline fourteen"), caText(6, "warn"))
	shell := caJoin(caBytes(1, args), caBytes(2, caBytes(1, success)), caText(3, "Run tests"))
	call := caJoin(caBytes(1, shell), caText(57, "call-9\nfc_9"), caVarint(59, 1793707200000), caVarint(60, 1793707201000))
	f.rootWithTurns(f.toolTurn(user, call))
	b := loadCA(t, f.path)
	calls, results := eventsOfKind(b, "tool_call"), eventsOfKind(b, "tool_result")
	if len(calls) != 1 || len(results) != 1 {
		t.Fatalf("want one call and one result, got %d/%d", len(calls), len(results))
	}
	c := calls[0]
	if c.Name != "shell" || c.CallID != "call-9" || c.Time == "" || c.Blocks[0].Type != "tool_call" {
		t.Fatalf("wrong call identity: %+v", c)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(c.Blocks[0].Text), &decoded); err != nil {
		t.Fatal(err)
	}
	a := decoded["args"].(map[string]any)
	if a["command"] != "go test ./..." || a["working_directory"] != "/repo" || a["timeout"] != float64(30000) || decoded["description"] != "Run tests" {
		t.Fatalf("args lost: %v", decoded)
	}
	if _, derived := a["parsing_result"]; derived {
		t.Fatal("derived shell parser output should be omitted")
	}
	r := results[0]
	if r.Name != "shell" || r.CallID != "call-9" || r.Role != "tool" {
		t.Fatalf("wrong result identity: %+v", r)
	}
	text := eventText(r)
	for _, want := range []string{"result.success.stdout", "line fourteen", "warn", "exit_code"} {
		if !strings.Contains(text, want) {
			t.Errorf("result missing %q: %s", want, text)
		}
	}
	for _, w := range b.Warnings {
		if strings.Contains(w, "retained as an opaque") {
			t.Fatalf("tool call stayed opaque: %s", w)
		}
	}
	if len(b.Assets) != 0 {
		t.Fatal("decoded tool call should not retain protobuf attachment")
	}
}

func TestCursorAgentEditReadMcpAndDiff(t *testing.T) {
	f := newCAFixture(t)
	user := f.blob(caText(1, "edit"))
	diff := "--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-old line of source code\n+new line of source code with extra text to exceed the inline threshold for readability\n+another added line\n+more\n+more\n+more\n"
	editSuccess := caJoin(caText(1, "/repo/x.go"), caVarint(3, 3), caVarint(4, 1), caText(5, diff))
	edit := caJoin(caBytes(1, caText(1, "/repo/x.go")), caBytes(2, caBytes(1, editSuccess)))
	editCall := caJoin(caBytes(12, edit), caText(57, "e1"))

	read := caJoin(caBytes(1, caText(1, "/repo/y.go")), caBytes(2, nil))
	readCall := caJoin(caBytes(8, read), caText(57, "r1"))

	// McpArgs.args is map<string, google.protobuf.Value>; Value.string_value=3, number_value=2.
	val := func(v []byte) []byte { return v }
	entry := func(k string, v []byte) []byte { return caBytes(2, caJoin(caText(1, k), caBytes(2, val(v)))) }
	mcpArgs := caJoin(caText(1, "srv"), entry("query", caText(3, "needle")), entry("limit", caDouble(2, 7)), caText(5, "search"), caText(9, "server-x"))
	mcpCall := caJoin(caBytes(15, caBytes(1, mcpArgs)), caText(57, "m1"))

	f.rootWithTurns(f.toolTurn(user, editCall, readCall, mcpCall))
	b := loadCA(t, f.path)
	var all string
	for _, e := range eventsOfKind(b, "tool_call") {
		all += e.Name + " " + eventText(e) + "\n"
	}
	for _, e := range eventsOfKind(b, "tool_result") {
		all += e.Name + " " + eventText(e) + "\n"
	}
	for _, want := range []string{"edit ", "read ", "mcp ", "/repo/x.go", "/repo/y.go", "lines_added", "+new line of source code", "needle", "server-x", `"limit":7`, "search"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in\n%s", want, all)
		}
	}
	for _, w := range b.Warnings {
		if strings.Contains(w, "retained as an opaque") || strings.Contains(w, "not in the verified") {
			t.Fatalf("unexpected warning %s", w)
		}
	}
}

func TestCursorAgentUnknownToolFieldsAndVariantsAreNoted(t *testing.T) {
	f := newCAFixture(t)
	user := f.blob(caText(1, "x"))
	// ShellToolCall with an unverified field 77 and a ToolCall variant 999.
	shell := caJoin(caBytes(1, caText(1, "ls")), caText(77, "mystery"))
	call := caJoin(caBytes(1, shell), caText(57, "u1"))
	future := caJoin(caBytes(999, caText(1, "future payload")), caText(57, "u2"))
	f.rootWithTurns(f.toolTurn(user, call, future))
	b := loadCA(t, f.path)
	all := ""
	for _, e := range eventsOfKind(b, "tool_call") {
		all += eventText(e)
	}
	if !strings.Contains(all, "_unknown_fields") || !strings.Contains(all, "mystery") || !strings.Contains(all, "future payload") {
		t.Fatalf("unknown fields silently dropped: %s", all)
	}
	warn := strings.Join(b.Warnings, "\n")
	if !strings.Contains(warn, "ShellToolCall has field 77") || !strings.Contains(warn, "ToolCall has field 999") {
		t.Fatalf("unknown fields not reported: %s", warn)
	}
}

func TestCursorAgentTruncatedToolCallFollowsOriginalStep(t *testing.T) {
	f := newCAFixture(t)
	user := f.blob(caText(1, "x"))
	original := caJoin(caBytes(1, caText(1, "echo full")), caBytes(2, caBytes(1, caText(5, "full output"))))
	step := f.blob(caBytes(2, caJoin(caBytes(1, original), caText(57, "t1"))))
	truncated := caBytes(34, caText(1, string(step)))
	f.rootWithTurns(f.toolTurn(user, truncated))
	b := loadCA(t, f.path)
	all := ""
	for _, e := range b.Sessions[0].Events {
		all += eventText(e)
	}
	if !strings.Contains(all, "echo full") || !strings.Contains(all, "full output") {
		t.Fatalf("original step not followed: %s", all)
	}

	// A missing original blob falls back to the truncated record, with a warning.
	g := newCAFixture(t)
	user = g.blob(caText(1, "x"))
	missing := caBytes(34, caJoin(caText(1, strings.Repeat("\x01", 32)), caVarint(4, 99)))
	g.rootWithTurns(g.toolTurn(user, caJoin(missing, caText(57, "t2"))))
	b = loadCA(t, g.path)
	if !strings.Contains(strings.Join(b.Warnings, "\n"), "TruncatedToolCall original step blob is missing") {
		t.Fatal("missing blob not reported", b.Warnings)
	}
}

func TestCursorAgentTodosArchivesAndConversationState(t *testing.T) {
	f := newCAFixture(t)
	user := f.blob(caText(1, "x"))
	todo := f.blob(caJoin(caText(1, "t1"), caText(2, "Write tests"), caVarint(3, 2), caVarint(4, 1793707200000)))
	oldMessage := f.blob([]byte(`{"role":"user","content":"earlier"}`))
	archive := f.blob(caJoin(caText(1, string(oldMessage)), caText(2, "Summary of earlier work"), caVarint(3, 8)))
	turn := f.blob(caBytes(1, caJoin(caBytes(1, user), caText(3, "req-1"), caText(7, "Routed Display"))))
	state := caJoin(caBytes(8, turn), caBytes(3, todo), caBytes(13, archive), caVarint(10, 1), caText(19, "feature/x"), caText(22, "cli"), caVarint(99, 5))
	f.meta(f.blob(state))
	b := loadCA(t, f.path)
	all := ""
	for _, e := range b.Sessions[0].Events {
		all += e.Kind + ":" + eventText(e) + "\n"
	}
	for _, want := range []string{"todo:", "Write tests", "compaction:", "Summary of earlier work", "summarized_message_count", "feature/x", "AGENT", "req-1", "Routed Display", "_unknown_fields"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in\n%s", want, all)
		}
	}
	if strings.Contains(strings.Join(b.Warnings, "\n"), "TodoItem is retained as an opaque") {
		t.Fatal("todo item still opaque")
	}
	if !strings.Contains(strings.Join(b.Warnings, "\n"), "ConversationStateStructure has field 99") {
		t.Fatal("unknown state field not reported", b.Warnings)
	}
}

func TestCursorAgentUnknownStepVariantIsRetainedNotFatal(t *testing.T) {
	f := newCAFixture(t)
	user := f.blob(caText(1, "x"))
	step := f.blob(caText(77, "novel step"))
	turn := f.blob(caBytes(1, caJoin(caBytes(1, user), caBytes(2, step))))
	f.rootWithTurns(turn)
	b := loadCA(t, f.path)
	if !strings.Contains(strings.Join(b.Warnings, "\n"), "ConversationStep (unknown variant)") {
		t.Fatal("unknown step variant not reported", b.Warnings)
	}
	if len(b.Assets) != 1 {
		t.Fatalf("unknown step bytes not retained: %d assets", len(b.Assets))
	}
}

func TestCursorAgentSessionCWDFromMetaJSON(t *testing.T) {
	f := newCAFixture(t)
	f.meta(f.blob(caText(9, "file:///from/state")))
	dir := filepath.Dir(f.path)
	if b := loadCA(t, f.path); b.Sessions[0].CWD != "/from/state" {
		t.Fatalf("workspace URI fallback lost: %q", b.Sessions[0].CWD)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"schemaVersion":1,"cwd":"/real/cwd","title":"ignored"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if b := loadCA(t, f.path); b.Sessions[0].CWD != "/real/cwd" {
		t.Fatalf("meta.json cwd not used: %q", b.Sessions[0].CWD)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"cwd":"relative/path"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if b := loadCA(t, f.path); b.Sessions[0].CWD != "/from/state" {
		t.Fatalf("non-absolute meta.json cwd accepted: %q", b.Sessions[0].CWD)
	}
}

func TestCursorAgentSchemaTablesAreSelfConsistent(t *testing.T) {
	caLoadSchema()
	for name, m := range caMessages {
		for _, f := range m.fields {
			for _, typ := range []string{f.typ, f.key, f.val} {
				if strings.HasPrefix(typ, "M:") && typ[2:] != caValueMessage && caMessages[typ[2:]] == nil {
					t.Errorf("%s.%s references missing message %s", name, f.name, typ)
				}
				if strings.HasPrefix(typ, "E:") && caEnumNames[typ[2:]] == nil {
					t.Errorf("%s.%s references missing enum %s", name, f.name, typ)
				}
			}
		}
	}
	tc := caMessages["ToolCall"]
	if tc == nil || tc.fields[1].name != "shell_tool_call" || tc.fields[12].name != "edit_tool_call" || tc.fields[57].name != "tool_call_id" {
		t.Fatal("ToolCall field map does not match the verified CLI descriptors")
	}
}
