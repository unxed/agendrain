package drain

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Synthetic protobuf fixtures use the field numbers from the installed
// official Cursor Agent 2026.10.01-e373342 generated descriptors. No user data.
func caBytes(field uint64, value []byte) []byte {
	p := binary.AppendUvarint(nil, field<<3|2)
	p = binary.AppendUvarint(p, uint64(len(value)))
	return append(p, value...)
}
func caText(field uint64, value string) []byte { return caBytes(field, []byte(value)) }
func caJoin(parts ...[]byte) []byte            { return bytes.Join(parts, nil) }
func caMap(key string, value []byte) []byte    { return caJoin(caText(1, key), caBytes(2, value)) }

type caFixture struct {
	t    *testing.T
	path string
	db   *sql.DB
}

func newCAFixture(t *testing.T) *caFixture {
	t.Helper()
	p := filepath.Join(t.TempDir(), "store.db")
	db, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE meta(key TEXT PRIMARY KEY,value TEXT); CREATE TABLE blobs(id TEXT PRIMARY KEY,data BLOB);`); err != nil {
		t.Fatal(err)
	}
	return &caFixture{t, p, db}
}
func (f *caFixture) blob(p []byte) []byte {
	f.t.Helper()
	h := sha256.Sum256(p)
	if _, err := f.db.Exec(`INSERT OR REPLACE INTO blobs VALUES(?,?)`, hex.EncodeToString(h[:]), p); err != nil {
		f.t.Fatal(err)
	}
	return h[:]
}
func (f *caFixture) meta(root []byte) {
	f.t.Helper()
	p, _ := json.Marshal(map[string]any{"agentId": "p", "name": "Synthetic work", "createdAt": 1793707200000, "latestRootBlobId": hex.EncodeToString(root), "lastUsedModel": "bespoke-v12", "blobEncryptionKey": "DO_NOT_EXPORT_ENCRYPTION_KEY"})
	if _, err := f.db.Exec(`INSERT OR REPLACE INTO meta VALUES('0',?)`, hex.EncodeToString(p)); err != nil {
		f.t.Fatal(err)
	}
}
func loadCA(t *testing.T, input string) *Bundle {
	t.Helper()
	b := &Bundle{Version: Version, Assets: map[string][]byte{}}
	if err := b.cursorAgent(input); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCursorAgentReachableRecordedContextAndOpaqueTool(t *testing.T) {
	f := newCAFixture(t)
	prompt := f.blob([]byte(`{"role":"system","content":"Exact recorded system instruction"}`))
	image := f.blob([]byte{137, 80, 78, 71})
	selected := caJoin(caBytes(1, caJoin(caBytes(1, image), caText(7, "image/png"))), caBytes(4, caJoin(caText(1, "DO_NOT_EXPORT_LOCAL_FILE"), caText(2, "/repo/private.go"))))
	user := f.blob(caJoin(caText(1, "Exact user requirement"), caText(2, "u"), caBytes(3, selected)))
	answer := f.blob(caBytes(1, caText(1, "Exact assistant decision")))
	thinking := f.blob(caBytes(3, caText(1, "Recorded thinking")))
	toolBytes := caJoin(caBytes(1, caText(3, "Recorded shell tool")), caText(57, "call-1"))
	tool := f.blob(caBytes(2, toolBytes))
	turn := f.blob(caBytes(1, caJoin(caBytes(1, user), caBytes(2, thinking), caBytes(2, tool), caBytes(2, answer))))
	cmd := f.blob(caText(1, "git status"))
	output := f.blob(caJoin(caText(1, "clean output"), caText(2, "recorded stderr")))
	shell := f.blob(caBytes(2, caJoin(caBytes(1, cmd), caBytes(2, output))))
	// Cached file blobs and entirely unreachable corrupt records are not chat.
	cache := f.blob([]byte("DO_NOT_EXPORT_FILE_STATE_CACHE"))
	if _, err := f.db.Exec(`INSERT INTO blobs VALUES('unreachable-corrupt-id',X'FF')`); err != nil {
		t.Fatal(err)
	}
	root := f.blob(caJoin(caBytes(1, prompt), caBytes(8, turn), caBytes(8, shell), caBytes(12, caMap("/repo/cache.go", cache)), caText(9, "file:///repo")))
	f.meta(root)
	b := loadCA(t, f.path)
	s := joined(b)
	for _, required := range []string{"Exact recorded system instruction", "Exact user requirement", "Exact assistant decision", "Recorded thinking", "git status", "clean output", "recorded stderr", "private.go", "call-1", "agent.v1.ToolCall"} {
		if !strings.Contains(s, required) {
			t.Errorf("missing recorded context %q", required)
		}
	}
	for _, excluded := range []string{"DO_NOT_EXPORT_ENCRYPTION_KEY", "DO_NOT_EXPORT_LOCAL_FILE", "DO_NOT_EXPORT_FILE_STATE_CACHE"} {
		if strings.Contains(s, excluded) {
			t.Errorf("leaked %q", excluded)
		}
		for _, p := range b.Assets {
			if bytes.Contains(p, []byte(excluded)) {
				t.Errorf("attachment leaked %q", excluded)
			}
		}
	}
	if len(b.Assets) != 2 || !strings.Contains(strings.Join(b.Warnings, " "), "protobuf") {
		t.Fatal("image/tool bytes or opaque-record warning missing", b.Warnings)
	}
	if b.Sessions[0].ID != "p" || b.Sessions[0].Title != "Synthetic work" || b.Sessions[0].Source != "cursor-agent" {
		t.Fatal("wrong whitelisted metadata", b.Sessions[0])
	}
	if err := Prepare(b, Options{}); err != nil {
		t.Fatal(err)
	}
	if err := Write(b, filepath.Join(t.TempDir(), "handoff.zip"), Options{}); err != nil {
		t.Fatal(err)
	}
}

func TestCursorAgentInlineAndReferencedSubagents(t *testing.T) {
	f := newCAFixture(t)
	childUser := f.blob(caText(1, "Subagent requirement"))
	childTurn := f.blob(caBytes(1, caBytes(1, childUser)))
	childRoot := caBytes(8, childTurn)
	childState := caJoin(caBytes(1, childRoot), caText(5, "child-model"))
	childRef := f.blob(childState)
	root := f.blob(caJoin(caBytes(16, caMap("inline-child", childState)), caBytes(31, caMap("referenced-child", childRef))))
	f.meta(root)
	b := loadCA(t, filepath.Dir(f.path))
	if len(b.Sessions) != 3 {
		t.Fatalf("missing child sessions: %s", joined(b))
	}
	for _, s := range b.Sessions[1:] {
		if s.ParentID != "p" || !strings.Contains(joined(&Bundle{Sessions: []Session{s}}), "Subagent requirement") {
			t.Fatal("child state or relationship lost", s.ID)
		}
	}
}

func TestCursorAgentBlobIntegrityAndMalformedReferences(t *testing.T) {
	for _, kind := range []string{"checksum", "missing", "bad-wire", "short-id"} {
		t.Run(kind, func(t *testing.T) {
			f := newCAFixture(t)
			body := caText(1, "Requirement")
			ref := f.blob(body)
			if kind == "checksum" {
				f.db.Exec(`UPDATE blobs SET data=X'00' WHERE id=?`, hex.EncodeToString(ref))
			}
			if kind == "missing" {
				f.db.Exec(`DELETE FROM blobs WHERE id=?`, hex.EncodeToString(ref))
			}
			if kind == "short-id" {
				ref = []byte{1, 2}
			}
			rootBytes := caBytes(1, ref)
			if kind == "bad-wire" {
				rootBytes = []byte{0xff}
			}
			f.meta(f.blob(rootBytes))
			b := &Bundle{Assets: map[string][]byte{}}
			if err := b.cursorAgent(f.path); err == nil {
				t.Fatal("accepted", kind)
			}
		})
	}
}

func TestCursorAgentDepthLimitAndDiscovery(t *testing.T) {
	f := newCAFixture(t)
	state := []byte{}
	for i := 0; i < 70; i++ {
		state = caBytes(16, caMap("child", caBytes(1, state)))
	}
	f.meta(f.blob(state))
	if err := (&Bundle{}).cursorAgent(f.path); err == nil {
		t.Fatal("unbounded child recursion")
	}
	d := t.TempDir()
	t.Setenv("CURSOR_CONFIG_DIR", d)
	want := filepath.Join(d, "chats")
	if err := os.Mkdir(want, 0700); err != nil {
		t.Fatal(err)
	}
	paths := cursorAgentPaths()
	if len(paths) != 2 || paths[0] != want || paths[1] != filepath.Join(d, "acp-sessions") {
		t.Fatal("wrong native paths", paths)
	}
}

func TestCursorAgentAggregateBudgetBeforeBlobAllocation(t *testing.T) {
	f := newCAFixture(t)
	ref := f.blob([]byte("small reachable blob"))
	r := &cursorAgentReader{q: f.db, cache: map[string][]byte{}, read: maxSourceBytes - 1}
	if _, err := r.blob(ref); err == nil || !strings.Contains(err.Error(), "safety limit") {
		t.Fatalf("aggregate byte budget ignored: %v", err)
	}
	r.records = 100000
	if _, err := r.proto(nil); err == nil {
		t.Fatal("reachable record budget ignored")
	}
}
