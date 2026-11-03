package drain

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Synthetic Cursor IDE fixtures. Key layouts and encodings follow the official
// Cursor 3.28.7 bundle (commit 25b52e7d03ebf2d5608914158e119f7992432a30):
// renderer agentKv:blob:<hex sha256> rows, composerData.conversationState as
// "~"+base64 or hex, agentKv:checkpoint pointers, and agent host session stores.
// The agent.v1 records reuse the CLI field numbers. No user data.

type ideKVFixture struct {
	t    *testing.T
	path string
	db   *sql.DB
}

func newIDEKVFixture(t *testing.T, path string) *ideKVFixture {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE ItemTable(key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB); CREATE TABLE cursorDiskKV(key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB);`); err != nil {
		t.Fatal(err)
	}
	return &ideKVFixture{t, path, db}
}

func (f *ideKVFixture) kv(key string, value any) {
	f.t.Helper()
	if _, err := f.db.Exec(`INSERT INTO cursorDiskKV VALUES(?,?)`, key, value); err != nil {
		f.t.Fatal(err)
	}
}
func (f *ideKVFixture) item(key string, value any) {
	f.t.Helper()
	p, _ := json.Marshal(value)
	if _, err := f.db.Exec(`INSERT INTO ItemTable VALUES(?,?)`, key, string(p)); err != nil {
		f.t.Fatal(err)
	}
}

// blob stores a renderer/host CAS blob as a SQLite BLOB.
func (f *ideKVFixture) blob(p []byte) []byte {
	h := sha256.Sum256(p)
	f.kv("agentKv:blob:"+hex.EncodeToString(h[:]), p)
	return h[:]
}

// hexBlob stores a blob the way older renderer builds did: hexadecimal TEXT.
func (f *ideKVFixture) hexBlob(p []byte) []byte {
	h := sha256.Sum256(p)
	f.kv("agentKv:blob:"+hex.EncodeToString(h[:]), hex.EncodeToString(p))
	return h[:]
}

func (f *ideKVFixture) composer(id string, m map[string]any) {
	m["composerId"] = id
	p, _ := json.Marshal(m)
	f.kv("composerData:"+id, string(p))
}

func ideTurn(f *ideKVFixture, userID, user, answer string) []byte {
	u := f.blob(caJoin(caText(1, user), caText(2, userID)))
	steps := [][]byte{caBytes(1, u)}
	if answer != "" {
		steps = append(steps, caBytes(2, f.blob(caBytes(1, caText(1, answer)))))
	}
	return f.blob(caBytes(1, caJoin(steps...)))
}

func inlineState(root []byte) string { return "~" + base64.StdEncoding.EncodeToString(root) }

func loadCursor(t *testing.T, input string) *Bundle {
	t.Helper()
	b := &Bundle{Version: Version, Assets: map[string][]byte{}}
	if err := b.cursor(input); err != nil {
		t.Fatal(err)
	}
	return b
}

func sessionByID(b *Bundle, id string) *Session {
	for i := range b.Sessions {
		if b.Sessions[i].ID == id {
			return &b.Sessions[i]
		}
	}
	return nil
}

func TestCursorIDEAgentKvInlineStateWithoutBubbles(t *testing.T) {
	f := newIDEKVFixture(t, filepath.Join(t.TempDir(), "state.vscdb"))
	prompt := f.blob([]byte(`{"role":"system","content":"Exact IDE system instruction"}`))
	user := f.hexBlob(caJoin(caText(1, "Exact IDE requirement"), caText(2, "u1")))
	thinking := f.blob(caBytes(3, caText(1, "IDE recorded thinking")))
	answer := f.blob(caBytes(1, caText(1, "Exact IDE decision")))
	turn := f.blob(caBytes(1, caJoin(caBytes(1, user), caBytes(2, thinking), caBytes(2, answer))))
	root := caJoin(caBytes(1, prompt), caBytes(8, turn), caText(9, "file:///repo"))
	f.composer("p", map[string]any{"name": "IDE task", "conversationState": inlineState(root)})
	f.kv("agentKv:artifact:p:shot.png", []byte{1, 2, 3})
	b := loadCursor(t, f.path)
	s := sessionByID(b, "p")
	if s == nil || s.Source != "cursor" || s.Title != "IDE task" || s.CWD != "/repo" {
		t.Fatalf("composer session metadata lost: %s", joined(b))
	}
	all := joined(b)
	order := []string{"Exact IDE system instruction", "Exact IDE requirement", "IDE recorded thinking", "Exact IDE decision"}
	last := -1
	for _, want := range order {
		i := strings.Index(all, want)
		if i < 0 || i < last {
			t.Fatalf("missing or misordered %q: %s", want, all)
		}
		last = i
	}
	w := strings.Join(b.Warnings, "\n")
	if strings.Contains(w, "only readable composer/bubble") || !strings.Contains(w, "agentKv") || !strings.Contains(w, "artifact") {
		t.Fatal("stale or missing agentKv warnings", b.Warnings)
	}
}

func TestCursorIDEAgentKvMergesWithBubblesWithoutDuplication(t *testing.T) {
	f := newIDEKVFixture(t, filepath.Join(t.TempDir(), "state.vscdb"))
	prompt := f.blob([]byte(`{"role":"system","content":"KV-only system instruction"}`))
	matched := ideTurn(f, "u1", "Bubble requirement", "Duplicate KV answer")
	extra := ideTurn(f, "u9", "KV-only follow-up", "KV-only answer")
	childState := caBytes(8, ideTurn(f, "c1", "Subagent requirement", ""))
	knownChildState := caBytes(8, ideTurn(f, "c2", "Known child duplicate", ""))
	root := caJoin(caBytes(1, prompt), caBytes(8, matched), caBytes(8, extra),
		caBytes(16, caMap("child-tool-call", caBytes(1, childState))),
		caBytes(16, caMap("known-child", caBytes(1, knownChildState))))
	f.composer("p", map[string]any{"name": "Merged", "conversationState": inlineState(root),
		"fullConversationHeadersOnly": []any{map[string]any{"bubbleId": "u1", "type": 1}, map[string]any{"bubbleId": "a1", "type": 2}}})
	f.kv("bubbleId:p:u1", `{"type":1,"text":"Bubble requirement"}`)
	f.kv("bubbleId:p:a1", `{"type":2,"text":"Bubble answer"}`)
	f.composer("known-child", map[string]any{"parentComposerId": "p", "conversation": []any{map[string]any{"type": 2, "text": "Known child bubble"}}})
	b := loadCursor(t, f.path)
	all := joined(b)
	if strings.Count(all, "Bubble requirement") != 1 || strings.Contains(all, "Duplicate KV answer") || strings.Contains(all, "Known child duplicate") {
		t.Fatalf("duplicated turn content: %s", all)
	}
	for _, want := range []string{"KV-only system instruction", "Bubble answer", "KV-only follow-up", "KV-only answer", "Known child bubble"} {
		if !strings.Contains(all, want) {
			t.Fatalf("missing %q: %s", want, all)
		}
	}
	p := sessionByID(b, "p")
	if strings.Index(joined(&Bundle{Sessions: []Session{*p}}), "Bubble answer") > strings.Index(joined(&Bundle{Sessions: []Session{*p}}), "KV-only follow-up") {
		t.Fatal("agentKv-only turns must follow the bubble transcript")
	}
	child := sessionByID(b, "p/child-tool-call")
	if child == nil || child.ParentID != "p" || child.Source != "cursor" || !strings.Contains(joined(&Bundle{Sessions: []Session{*child}}), "Subagent requirement") {
		t.Fatalf("agentKv child lost: %s", all)
	}
	if !strings.Contains(strings.Join(b.Warnings, "\n"), "agentKv-only") {
		t.Fatal("appended agentKv-only turns are not disclosed", b.Warnings)
	}
}

func TestCursorIDEAgentKvCheckpointAndIntegrityFallback(t *testing.T) {
	f := newIDEKVFixture(t, filepath.Join(t.TempDir(), "state.vscdb"))
	root := f.blob(caBytes(8, ideTurn(f, "u1", "Checkpoint requirement", "Checkpoint answer")))
	f.composer("legacy", map[string]any{"_v": 10})
	f.kv("agentKv:checkpoint:legacy", hex.EncodeToString(root))
	bad := f.blob(caText(1, "Tampered"))
	f.kv("agentKv:blob:"+hex.EncodeToString(bad), []byte{0xde, 0xad})
	f.composer("broken", map[string]any{"conversationState": inlineState(caBytes(8, bad)), "conversation": []any{map[string]any{"type": 1, "text": "Readable bubble survives"}}})
	f.composer("garbled", map[string]any{"conversationState": "~***", "conversation": []any{map[string]any{"type": 1, "text": "Garbled state survives"}}})
	b := loadCursor(t, f.path)
	all := joined(b)
	for _, want := range []string{"Checkpoint requirement", "Checkpoint answer", "Readable bubble survives", "Garbled state survives"} {
		if !strings.Contains(all, want) {
			t.Fatalf("missing %q: %s", want, all)
		}
	}
	w := strings.Join(b.Warnings, "\n")
	if !strings.Contains(w, "checksum") || !strings.Contains(w, "garbled") {
		t.Fatal("integrity failures are not disclosed", b.Warnings)
	}
}

func layoutRoot(header map[string]any, scalars []byte) []byte {
	h, _ := json.Marshal(header)
	out := binary.LittleEndian.AppendUint32(nil, uint32(len(h)))
	return append(append(out, h...), scalars...)
}

func TestCursorIDEAgentHostSessions(t *testing.T) {
	global := filepath.Join(t.TempDir(), "globalStorage")
	f := newIDEKVFixture(t, filepath.Join(global, "state.vscdb"))
	f.composer("p", map[string]any{"name": "Host merged", "conversation": []any{map[string]any{"type": 1, "text": "Renderer bubble"}}})
	hosts := filepath.Join(global, "anysphere.cursor-agent-host", "sessions")

	// manifestVersion 1: the root blob is a ConversationStateStructure.
	h1 := newIDEKVFixture(t, filepath.Join(hosts, "p", "session.sqlite"))
	r1 := h1.blob(caJoin(caBytes(1, h1.blob([]byte(`{"role":"system","content":"Host structure instruction"}`))), caBytes(8, ideTurn(h1, "x1", "Host structure requirement", "Host structure answer"))))
	h1.item("agentHost:sessionManifest", map[string]any{"manifestVersion": 1, "sessionId": "p", "latestRootBlobIdHex": hex.EncodeToString(r1), "origin": "host-native", "createdAtMs": 1, "migratedBlobCount": 0})

	// manifestVersion 2: linear layout root plus list rows.
	h2 := newIDEKVFixture(t, filepath.Join(hosts, "h2", "session.sqlite"))
	prefix := h2.blob([]byte(`{"role":"system","content":"Linear prefix instruction"}`))
	body := h2.blob([]byte(`{"role":"user","content":"Linear body context"}`))
	first := ideTurn(h2, "l1", "Linear first requirement", "Linear first answer")
	lastTurn := ideTurn(h2, "l2", "Linear last requirement", "Linear last answer")
	h2.kv("agentHost:prompt:000000000000", body)
	h2.kv("agentHost:turns:000000000000", first)
	root := h2.blob(layoutRoot(map[string]any{"version": 1, "promptPrefix": []string{hex.EncodeToString(prefix)}, "summarySlot": []string{}, "body": []int{0, 1}, "lastTurn": hex.EncodeToString(lastTurn), "turns": []int{0, 1}, "archives": []int{0, 0}}, caText(9, "file:///linear")))
	h2.item("agentHost:sessionManifest", map[string]any{"manifestVersion": 2, "sessionId": "h2", "latestRootBlobIdHex": hex.EncodeToString(root), "origin": "host-native", "createdAtMs": 1, "migratedBlobCount": 0, "listHeads": map[string]any{"turns": 1, "prompt": 1, "archives": 0}})
	h2.item("agentHost:listingProjection", map[string]any{"title": "Linear host task", "updatedAtMs": 1793707200000})

	// A manifest whose list heads disagree with its root is rejected, not guessed.
	h3 := newIDEKVFixture(t, filepath.Join(hosts, "h3", "session.sqlite"))
	bad := h3.blob(layoutRoot(map[string]any{"version": 1, "promptPrefix": []string{}, "summarySlot": []string{}, "body": []int{0, 0}, "turns": []int{0, 2}, "archives": []int{0, 0}}, nil))
	h3.item("agentHost:sessionManifest", map[string]any{"manifestVersion": 2, "sessionId": "h3", "latestRootBlobIdHex": hex.EncodeToString(bad), "origin": "host-native", "createdAtMs": 1, "migratedBlobCount": 0, "listHeads": map[string]any{"turns": 2, "prompt": 0, "archives": 0}})

	for _, input := range []string{f.path, global} {
		b := loadCursor(t, input)
		p := sessionByID(b, "p")
		if p == nil {
			t.Fatalf("renderer composer lost: %s", joined(b))
		}
		pj := joined(&Bundle{Sessions: []Session{*p}})
		for _, want := range []string{"Renderer bubble", "Host structure instruction"} {
			if !strings.Contains(pj, want) {
				t.Fatalf("host session not merged into composer (%q): %s", want, pj)
			}
		}
		h := sessionByID(b, "h2")
		if h == nil || h.Title != "Linear host task" || h.Source != "cursor" || h.CWD != "/linear" {
			t.Fatalf("linear host session metadata lost: %s", joined(b))
		}
		hj := joined(&Bundle{Sessions: []Session{*h}})
		last := -1
		for _, want := range []string{"Linear prefix instruction", "Linear body context", "Linear first requirement", "Linear first answer", "Linear last requirement", "Linear last answer"} {
			i := strings.Index(hj, want)
			if i < 0 || i < last {
				t.Fatalf("missing or misordered %q: %s", want, hj)
			}
			last = i
		}
		if !strings.Contains(strings.Join(b.Warnings, "\n"), "h3") {
			t.Fatal("inconsistent host session was not disclosed", b.Warnings)
		}
	}
}

func TestCursorIDEAgentKvLimits(t *testing.T) {
	f := newIDEKVFixture(t, filepath.Join(t.TempDir(), "state.vscdb"))
	state := []byte{}
	for i := 0; i < 70; i++ {
		state = caBytes(16, caMap("child", caBytes(1, state)))
	}
	f.composer("deep", map[string]any{"conversationState": inlineState(state), "conversation": []any{map[string]any{"type": 1, "text": "Deep readable"}}})
	b := loadCursor(t, f.path)
	if !strings.Contains(joined(b), "Deep readable") || !strings.Contains(strings.Join(b.Warnings, "\n"), "nesting") {
		t.Fatal("nesting limit not applied or not disclosed", b.Warnings)
	}
	if _, err := cursorStateBytes(strings.Repeat("a", maxCursorStateText+2)); err == nil {
		t.Fatal("oversized inline conversation state accepted")
	}
	if _, err := cursorLayoutRoot([]byte{0xff, 0xff, 0xff, 0x7f, '{'}); err == nil {
		t.Fatal("overrunning layout header accepted")
	}
	if got := fmt.Sprint(cursorHostListKey("turns", 7)); got != "agentHost:turns:000000000007" {
		t.Fatal(got)
	}
}
