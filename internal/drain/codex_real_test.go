package drain

import (
	"os"
	"strings"
	"testing"
)

// Native paginated rollouts resume the same thread in a second file with a
// different session_meta timestamp and a history_base cursor.
func TestCodexPaginatedSegmentsCatalogAndSelectedUnion(t *testing.T) {
	d := t.TempDir()
	t.Setenv("CODEX_HOME", d)
	header := `{"type":"session_meta","timestamp":"2026-01-01T00:00:00Z","payload":{"id":"p","cwd":"/repo","timestamp":"2026-01-01T00:00:00Z","history_mode":"paginated"}}` + "\n"
	repeated := `{"type":"response_item","timestamp":"2026-01-01T00:01:00Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Repeated exact requirement"}]}}` + "\n"
	first := header + repeated + repeated
	put(t, d, "sessions/a.jsonl", first)
	put(t, d, "archived_sessions/copy.jsonl", first)
	put(t, d, "sessions/b.jsonl", `{"type":"session_meta","timestamp":"2026-01-02T00:00:00Z","payload":{"id":"p","cwd":"/repo","timestamp":"2026-01-02T00:00:00Z","history_mode":"paginated","history_base":{"thread_id":"p","end_ordinal_exclusive":3,"end_byte_offset":123}}}`+"\n"+`{"type":"response_item","timestamp":"2026-01-02T00:01:00Z","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Later segment evidence"}]}}`+"\n")
	catalog, err := CatalogCodex("")
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Sessions) != 1 || catalog.Sessions[0].UpdatedAt != "2026-01-02T00:00:00Z" {
		t.Fatalf("catalog lost continuation: %#v", catalog.Sessions)
	}
	b, err := LoadSelected("codex", "", "p")
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Sessions) != 1 {
		t.Fatalf("sessions=%d", len(b.Sessions))
	}
	s := b.Sessions[0]
	if strings.Count(jsonText(s.Events), "Repeated exact requirement") != 2 || !strings.Contains(jsonText(s.Events), "Later segment evidence") {
		t.Fatal("segment union dropped or duplicated recorded context")
	}
	if len(s.Events) != 5 {
		t.Fatalf("events=%d want 2 segment contexts and 3 messages", len(s.Events))
	}
}

func TestCodexSegmentMergePreservesUnidentifiableRepeats(t *testing.T) {
	a := Session{ID: "p", Events: []Event{{Kind: "message", Role: "user", Blocks: []Block{{Type: "text", Text: "Repeat"}}}}}
	b := a
	b.Events = append([]Event{{Kind: "session_context", Role: "system", Blocks: []Block{{Type: "context", Text: "New segment"}}}}, a.Events...)
	if err := mergeCodexSession(&a, b); err != nil {
		t.Fatal(err)
	}
	if len(a.Events) != 3 {
		t.Fatal("undated repeated instruction lost")
	}
}

// Explicit opt-in smoke check: native histories are never part of fixtures or
// CI. Report counts only; never print the private bundle on test failure.
func TestNativeCodexStoreOptIn(t *testing.T) {
	home := os.Getenv("AGENDRAIN_NATIVE_CODEX_HOME")
	if home == "" {
		t.Skip("set AGENDRAIN_NATIVE_CODEX_HOME for a private local smoke check")
	}
	t.Setenv("CODEX_HOME", home)
	b, err := CatalogCodex("")
	if err != nil {
		t.Fatal(err)
	}
	roots := 0
	for _, s := range b.Sessions {
		if s.ParentID == "" {
			roots++
			if s.Title == "" {
				t.Fatal("native root missing a display title")
			}
		}
	}
	t.Logf("native catalog: %d sessions, %d roots", len(b.Sessions), roots)
	id := os.Getenv("AGENDRAIN_NATIVE_CODEX_SESSION")
	if id == "" {
		return
	}
	selected, err := LoadSelected("codex", "", id)
	if err != nil {
		t.Fatal(err)
	}
	events := 0
	for _, s := range selected.Sessions {
		events += len(s.Events)
	}
	t.Logf("native selected export: %d sessions, %d events", len(selected.Sessions), events)
}

func TestCodexSegmentMergeKeepsUndatedInstructionsAtTheirAnchor(t *testing.T) {
	event := func(id, when string) Event {
		return Event{ID: id, Time: when, Role: "user", Kind: "message", Blocks: []Block{{Type: "text", Text: id}}}
	}
	early := Session{ID: "p", CWD: "/early", Events: []Event{event("early", "2026-01-01T00:00:00Z"), event("early-undated", "")}}
	late := Session{ID: "p", CWD: "/late", Events: []Event{event("late", "2026-01-02T00:00:00Z"), event("late-undated", ""), event("decision", "2026-01-02T00:02:00Z")}}
	if err := mergeCodexSession(&early, late); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range early.Events {
		if e.Kind == "message" {
			got = append(got, e.ID)
		}
	}
	if strings.Join(got, ",") != "early,early-undated,late,late-undated,decision" {
		t.Fatalf("decision order changed: %v", got)
	}
	if early.CWD != "/late" {
		t.Fatalf("latest segment CWD=%q", early.CWD)
	}
	if !strings.Contains(jsonText(early.Events), "/early") || !strings.Contains(jsonText(early.Events), "/late") {
		t.Fatal("segment directory context lost")
	}
}

func TestCodexSelectedOrdersSegmentsWithoutMovingUndatedRecords(t *testing.T) {
	d := t.TempDir()
	t.Setenv("CODEX_HOME", d)
	segment := func(day, first, undated string) string {
		return `{"type":"session_meta","timestamp":"2026-01-` + day + `T00:00:00Z","payload":{"id":"p"}}` + "\n" +
			`{"type":"response_item","timestamp":"2026-01-` + day + `T00:01:00Z","payload":{"id":"` + first + `","type":"message","role":"user","content":[]}}` + "\n" +
			`{"type":"response_item","payload":{"id":"` + undated + `","type":"message","role":"user","content":[]}}` + "\n"
	}
	put(t, d, "sessions/a-later.jsonl", segment("02", "late", "late-undated"))
	put(t, d, "sessions/z-earlier.jsonl", segment("01", "early", "early-undated"))
	b, err := LoadSelected("codex", "", "p")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range b.Sessions[0].Events {
		if e.Kind == "message" {
			got = append(got, e.ID)
		}
	}
	if strings.Join(got, ",") != "early,early-undated,late,late-undated" {
		t.Fatalf("segment order changed: %v", got)
	}
}
