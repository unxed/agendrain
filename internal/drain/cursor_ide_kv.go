package drain

// Cursor IDE agentKv. Storage layouts were read from the official Cursor
// 3.28.7 Linux build (commit 25b52e7d03ebf2d5608914158e119f7992432a30):
// workbench.desktop.main.js (composerBlobStore.js, composer migrations and
// serializers) and extensions/cursor-agent-host/dist/main.js (host session
// stores). The blobs hold the same agent.v1 records as the Cursor Agent CLI
// store, so cursorAgentReader decodes them with a different blob backend.
//
//   - Renderer CAS: cursorDiskKV "agentKv:blob:<hex sha256>" holds raw bytes
//     (SQLite BLOB) or, from older writers, hexadecimal TEXT; a single 0x00
//     byte denotes an empty blob.
//   - composerData.conversationState is the root agent.v1.ConversationStateStructure,
//     serialized as "~"+base64 or as hexadecimal. Pre-v11 composers instead
//     have "agentKv:checkpoint:<composerId>" naming the root blob (hex id).
//   - Agent host: <globalStorage>/anysphere.cursor-agent-host/sessions/<id>/
//     session.sqlite, with ItemTable "agentHost:sessionManifest" (JSON) naming
//     the latest root blob. Manifest v1 roots are ConversationStateStructure;
//     v2 roots are a "linear" layout (uint32le header length, JSON header,
//     scalar ConversationStateStructure fields) whose prompt/turn/archive lists
//     are rows "agentHost:<list>:<12-digit index>" holding 32-byte blob ids.

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
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	maxCursorStateText    = 96 << 20
	maxCursorLayoutHeader = 1 << 20
	maxCursorHostRows     = 100000
	maxCursorManifest     = 1 << 20
)

var cursorBlobIDHex = regexp.MustCompile(`^[0-9a-f]{64}$`)

func sha256Hex(p []byte) string {
	sum := sha256.Sum256(p)
	return hex.EncodeToString(sum[:])
}

// verified checks a fetched IDE blob against its content address and budget.
func (r *cursorAgentReader) verified(key string, data []byte) ([]byte, error) {
	if int64(len(data)) > maxSourceBytes-r.read {
		return nil, fmt.Errorf("Cursor Agent reachable blobs exceed %d-byte safety limit", maxSourceBytes)
	}
	sum := sha256Hex(data)
	if sum != key {
		return nil, fmt.Errorf("Cursor Agent blob checksum mismatch: %s", key)
	}
	r.read += int64(len(data))
	r.cache[key] = data
	return data, nil
}

// cursorKVFetch reads agentKv:blob rows. Renderer stores hex-decode TEXT
// values; agent host stores treat TEXT as UTF-8 bytes, as their readers do.
func cursorKVFetch(q queryer, hexText bool) func(string, int64) ([]byte, error) {
	return func(key string, budget int64) ([]byte, error) {
		rows, err := q.Query("SELECT typeof(value), length(CAST(value AS BLOB)) FROM cursorDiskKV WHERE key=?", "agentKv:blob:"+key)
		if err != nil {
			return nil, err
		}
		var typ string
		var size int64
		found := rows.Next()
		if found {
			err = rows.Scan(&typ, &size)
		}
		rows.Close()
		if err != nil {
			return nil, err
		}
		if !found || typ == "null" {
			return nil, fmt.Errorf("missing Cursor Agent blob %s", key)
		}
		text := typ == "text" && hexText
		if text {
			size /= 2
		}
		if size < 0 || size > budget {
			return nil, fmt.Errorf("Cursor Agent reachable blobs exceed %d-byte safety limit", maxSourceBytes)
		}
		rows, err = q.Query("SELECT CAST(value AS BLOB) FROM cursorDiskKV WHERE key=?", "agentKv:blob:"+key)
		if err != nil {
			return nil, err
		}
		var data []byte
		if rows.Next() {
			err = rows.Scan(&data)
		} else {
			err = fmt.Errorf("missing Cursor Agent blob %s", key)
		}
		rows.Close()
		if err != nil {
			return nil, err
		}
		if text {
			if data, err = hex.DecodeString(strings.ToLower(strings.TrimSpace(string(data)))); err != nil {
				return nil, fmt.Errorf("Cursor agentKv blob %s is not hexadecimal: %w", key, err)
			}
		}
		if len(data) == 1 && data[0] == 0 {
			data = []byte{}
		}
		return data, nil
	}
}

// cursorStateBytes decodes composerData.conversationState.
func cursorStateBytes(s string) ([]byte, error) {
	if len(s) > maxCursorStateText {
		return nil, fmt.Errorf("inline conversation state exceeds %d-byte safety limit", maxCursorStateText)
	}
	if rest, ok := strings.CutPrefix(s, "~"); ok {
		rest = strings.TrimSpace(rest)
		if p, err := base64.StdEncoding.DecodeString(rest); err == nil {
			return p, nil
		}
		p, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(rest, "="))
		if err != nil {
			return nil, fmt.Errorf("inline conversation state is not base64: %w", err)
		}
		return p, nil
	}
	p, err := hex.DecodeString(strings.ToLower(strings.TrimSpace(s)))
	if err != nil {
		return nil, fmt.Errorf("inline conversation state is not hexadecimal: %w", err)
	}
	return p, nil
}

type cursorLayout struct {
	prompt   [][]byte // promptPrefix followed by summarySlot
	body     [2]int
	turns    [2]int
	archives [2]int
	lastTurn []byte
	scalars  []byte
	struct_  string
}

func cursorLayoutIDs(values []string, what string) ([][]byte, error) {
	var out [][]byte
	for _, v := range values {
		if !cursorBlobIDHex.MatchString(v) {
			return nil, fmt.Errorf("layout %s id %q is not a blob id", what, v)
		}
		id, _ := hex.DecodeString(v)
		out = append(out, id)
	}
	return out, nil
}

// cursorLayoutRoot parses an agent host manifest v2 ("linear") root blob.
func cursorLayoutRoot(p []byte) (*cursorLayout, error) {
	if len(p) < 4 {
		return nil, fmt.Errorf("conversation state root is too short to hold a header")
	}
	n := uint64(binary.LittleEndian.Uint32(p))
	if n > maxCursorLayoutHeader || n > uint64(len(p)-4) {
		return nil, fmt.Errorf("conversation state root header overruns the blob or the %d-byte limit", maxCursorLayoutHeader)
	}
	var h struct {
		Version      *int     `json:"version"`
		PromptPrefix []string `json:"promptPrefix"`
		SummarySlot  []string `json:"summarySlot"`
		Body         []int64  `json:"body"`
		LastTurn     *string  `json:"lastTurn"`
		Turns        []int64  `json:"turns"`
		Archives     []int64  `json:"archives"`
		StructureID  *string  `json:"structureId"`
	}
	if err := json.Unmarshal(p[4:4+n], &h); err != nil {
		return nil, fmt.Errorf("conversation state root header is not valid JSON: %w", err)
	}
	if h.Version == nil || *h.Version != 1 {
		return nil, fmt.Errorf("conversation state root header version is not 1")
	}
	out := &cursorLayout{scalars: p[4+n:]}
	prefix, err := cursorLayoutIDs(h.PromptPrefix, "prompt prefix")
	if err != nil {
		return nil, err
	}
	slot, err := cursorLayoutIDs(h.SummarySlot, "summary slot")
	if err != nil {
		return nil, err
	}
	out.prompt = append(prefix, slot...)
	for _, r := range []struct {
		name string
		in   []int64
		out  *[2]int
	}{{"body", h.Body, &out.body}, {"turns", h.Turns, &out.turns}, {"archives", h.Archives, &out.archives}} {
		if len(r.in) != 2 || r.in[0] < 0 || r.in[0] > r.in[1] || r.in[1] > 1<<32-1 {
			return nil, fmt.Errorf("conversation state root %s range is invalid", r.name)
		}
		if r.in[1]-r.in[0] > maxCursorHostRows {
			return nil, fmt.Errorf("conversation state root %s range exceeds %d rows", r.name, maxCursorHostRows)
		}
		*r.out = [2]int{int(r.in[0]), int(r.in[1])}
	}
	if h.LastTurn != nil {
		ids, err := cursorLayoutIDs([]string{*h.LastTurn}, "last turn")
		if err != nil {
			return nil, err
		}
		out.lastTurn = ids[0]
	}
	if h.StructureID != nil {
		if !cursorBlobIDHex.MatchString(*h.StructureID) {
			return nil, fmt.Errorf("layout structure id is not a blob id")
		}
		out.struct_ = *h.StructureID
	}
	return out, nil
}

func cursorHostListKey(list string, index int) string {
	return fmt.Sprintf("agentHost:%s:%012d", list, index)
}

// cursorHostList reads list rows [start,end) and checks that none is missing.
func cursorHostList(q queryer, list string, span [2]int) ([][]byte, error) {
	if span[0] == span[1] {
		return nil, nil
	}
	rows, err := q.Query("SELECT CAST(value AS BLOB) FROM cursorDiskKV WHERE key >= ? AND key < ? AND value IS NOT NULL ORDER BY key", cursorHostListKey(list, span[0]), cursorHostListKey(list, span[1]))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][]byte
	for rows.Next() {
		var p []byte
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		if len(out) >= span[1]-span[0] {
			break
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) != span[1]-span[0] {
		return nil, fmt.Errorf("host session %s list holds %d of the %d rows in [%d, %d)", list, len(out), span[1]-span[0], span[0], span[1])
	}
	for i, p := range out {
		switch {
		case list != "archives" && len(p) != 32:
			return nil, fmt.Errorf("host session %s row %d is not a 32-byte blob id", list, span[0]+i)
		case list == "archives":
			// Archive rows: 32-byte archive id, uint16le slot count, slot ids, uint32le range.
			if len(p) < 42 || len(p) != 42+int(binary.LittleEndian.Uint16(p[32:]))*32 {
				return nil, fmt.Errorf("host session archives row %d has an invalid length", span[0]+i)
			}
			out[i] = p[:32]
		}
	}
	return out, nil
}

// cursorStripFields drops top-level protobuf fields, keeping the others' bytes.
func cursorStripFields(p []byte, drop map[uint64]bool) ([]byte, error) {
	var out []byte
	for len(p) > 0 {
		start := p
		tag, n := binary.Uvarint(p)
		if n <= 0 || tag>>3 == 0 {
			return nil, fmt.Errorf("invalid protobuf tag in layout scalars")
		}
		p = p[n:]
		switch tag & 7 {
		case 0:
			_, n = binary.Uvarint(p)
			if n <= 0 {
				return nil, fmt.Errorf("invalid protobuf varint in layout scalars")
			}
		case 1:
			n = 8
		case 5:
			n = 4
		case 2:
			size, c := binary.Uvarint(p)
			if c <= 0 || size > uint64(len(p)-c) {
				return nil, fmt.Errorf("invalid protobuf length in layout scalars")
			}
			n = c + int(size)
		default:
			return nil, fmt.Errorf("unsupported protobuf wire type in layout scalars")
		}
		if n > len(p) {
			return nil, fmt.Errorf("truncated protobuf value in layout scalars")
		}
		p = p[n:]
		if !drop[tag>>3] {
			out = append(out, start[:len(start)-len(p)]...)
		}
	}
	return out, nil
}

type cursorHostManifest struct {
	ManifestVersion     int    `json:"manifestVersion"`
	SessionID           string `json:"sessionId"`
	LatestRootBlobIDHex string `json:"latestRootBlobIdHex"`
	CreatedAtMs         int64  `json:"createdAtMs"`
	ListHeads           *struct {
		Turns    int `json:"turns"`
		Prompt   int `json:"prompt"`
		Archives int `json:"archives"`
	} `json:"listHeads"`
	LatestStructureIDHex string `json:"latestStructureIdHex"`
}

func cursorItem(q queryer, key string, out any) (bool, error) {
	rows, err := records(q, "SELECT length(CAST(value AS BLOB)) AS size FROM ItemTable WHERE key=?", key)
	if err != nil || len(rows) == 0 {
		return false, err
	}
	if size, _ := rows[0]["size"].(float64); size > maxCursorManifest {
		return false, fmt.Errorf("%s exceeds %d-byte safety limit", key, maxCursorManifest)
	}
	rows, err = records(q, "SELECT CAST(value AS BLOB) AS value FROM ItemTable WHERE key=?", key)
	if err != nil || len(rows) == 0 {
		return false, err
	}
	if err := json.Unmarshal([]byte(str(rows[0]["value"])), out); err != nil {
		return false, fmt.Errorf("%s: %w", key, err)
	}
	return true, nil
}

// cursorHostRoot returns the ConversationStateStructure bytes of a host session.
func cursorHostRoot(r *cursorAgentReader, q queryer, wantID string) ([]byte, error) {
	var m cursorHostManifest
	ok, err := cursorItem(q, "agentHost:sessionManifest", &m)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("host session has no manifest")
	}
	if m.SessionID != wantID {
		return nil, fmt.Errorf("host session manifest names %q", m.SessionID)
	}
	if !cursorBlobIDHex.MatchString(m.LatestRootBlobIDHex) {
		return nil, fmt.Errorf("host session manifest root is not a blob id")
	}
	id, _ := hex.DecodeString(m.LatestRootBlobIDHex)
	root, err := r.blob(id)
	if err != nil {
		return nil, err
	}
	switch m.ManifestVersion {
	case 1:
		return root, nil
	case 2:
	default:
		return nil, fmt.Errorf("host session manifest version %d is not supported", m.ManifestVersion)
	}
	if m.ListHeads == nil {
		return nil, fmt.Errorf("host session manifest v2 has no list heads")
	}
	layout, err := cursorLayoutRoot(root)
	if err != nil {
		return nil, err
	}
	if layout.turns[1] != m.ListHeads.Turns || layout.body[1] != m.ListHeads.Prompt || layout.archives[1] != m.ListHeads.Archives {
		return nil, fmt.Errorf("host session root list ranges do not agree with its manifest")
	}
	if layout.struct_ != m.LatestStructureIDHex {
		return nil, fmt.Errorf("host session root structure id does not agree with its manifest")
	}
	body, err := cursorHostList(q, "prompt", layout.body)
	if err != nil {
		return nil, err
	}
	turns, err := cursorHostList(q, "turns", layout.turns)
	if err != nil {
		return nil, err
	}
	archives, err := cursorHostList(q, "archives", layout.archives)
	if err != nil {
		return nil, err
	}
	if layout.lastTurn != nil {
		turns = append(turns, layout.lastTurn)
	}
	// As the host materializes it: lists replace any list fields in the scalars.
	out, err := cursorStripFields(layout.scalars, map[uint64]bool{1: true, 8: true, 13: true})
	if err != nil {
		return nil, err
	}
	for _, f := range []struct {
		field uint64
		ids   [][]byte
	}{{1, append(layout.prompt, body...)}, {8, turns}, {13, archives}} {
		for _, id := range f.ids {
			out = append(out, caBytesField(f.field, id)...)
		}
	}
	return out, nil
}

func caBytesField(field uint64, value []byte) []byte {
	p := binary.AppendUvarint(nil, field<<3|2)
	p = binary.AppendUvarint(p, uint64(len(value)))
	return append(p, value...)
}

// cursorHostSessions lists agent host session stores next to a globalStorage DB.
func cursorHostSessions(stateDB string) map[string]string {
	dir := filepath.Join(filepath.Dir(stateDB), "anysphere.cursor-agent-host", "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, e := range entries {
		if !e.IsDir() || e.Type()&os.ModeSymlink != 0 {
			continue
		}
		p := filepath.Join(dir, e.Name(), "session.sqlite")
		if info, err := os.Lstat(p); err == nil && info.Mode().IsRegular() {
			out[e.Name()] = p
		}
	}
	return out
}

// cursorIDEKV decodes agentKv state for composers of one Cursor IDE store.
type cursorIDEKV struct {
	b       *Bundle
	q       queryer
	known   map[string]bool
	reader  *cursorAgentReader
	decoded int
}

func (k *cursorIDEKV) newReader(q queryer, hexText bool) *cursorAgentReader {
	r := &cursorAgentReader{q: q, cache: map[string][]byte{}, active: map[string]bool{}, fetch: cursorKVFetch(q, hexText), turnSpans: map[string][][2]int{}}
	if k.reader != nil {
		r.read, r.records, r.fields = k.reader.read, k.reader.records, k.reader.fields
	}
	k.reader = r
	return r
}

func (k *cursorIDEKV) warnErr(what string, err error) {
	k.b.warn(strings.ReplaceAll(fmt.Sprintf("Cursor IDE %s could not be decoded (%v); readable composer/bubble context is kept", what, err), "Cursor Agent", "agentKv"))
}

// merge adds decoded agentKv state to s and returns child sessions. Turns
// whose user message ID matches a readable bubble are not emitted twice.
func (k *cursorIDEKV) merge(s *Session, m obj, hostPath string, bubbleIDs map[string]bool, transcript bool) []Session {
	var root Session
	var children []Session
	var spans [][2]int
	var warnings []string
	run := func(r *cursorAgentReader, state []byte) error {
		scratch := &Bundle{Assets: k.b.Assets}
		r.b = scratch
		if err := r.state(state, Session{ID: s.ID, Source: "cursor"}, 0); err != nil {
			return err
		}
		root, children, spans, warnings = scratch.Sessions[0], scratch.Sessions[1:], r.turnSpans[s.ID], scratch.Warnings
		return nil
	}
	done := false
	if hostPath != "" {
		err := snapshot(hostPath, func(hq *sql.Tx) error {
			r := k.newReader(hq, false)
			state, err := cursorHostRoot(r, hq, s.ID)
			if err != nil {
				return err
			}
			return run(r, state)
		})
		if err != nil {
			k.warnErr("agent host session "+s.ID, err)
		} else {
			done = true
		}
	}
	if !done {
		var state []byte
		var err error
		r := k.newReader(k.q, true)
		switch v := m["conversationState"].(type) {
		case string:
			if v != "" {
				state, err = cursorStateBytes(v)
				if err != nil {
					err = fmt.Errorf("garbled conversationState: %w", err)
				}
			}
		case nil:
		default:
			err = fmt.Errorf("conversationState has unexpected JSON type")
		}
		if err == nil && len(state) == 0 {
			var rows []obj
			rows, err = records(k.q, "SELECT CAST(value AS BLOB) AS value FROM cursorDiskKV WHERE key=?", "agentKv:checkpoint:"+s.ID)
			if err == nil && len(rows) > 0 {
				id, e := hex.DecodeString(strings.ToLower(strings.TrimSpace(str(rows[0]["value"]))))
				if e != nil || len(id) != 32 {
					err = fmt.Errorf("checkpoint pointer is not a hexadecimal blob id")
				} else {
					state, err = r.blob(id)
				}
			}
		}
		if err == nil && len(state) > 0 {
			err = run(r, state)
			done = err == nil
		}
		if err != nil {
			k.warnErr("agentKv state of composer "+s.ID, err)
		}
	}
	if !done {
		return nil
	}
	k.decoded++
	for _, w := range warnings {
		k.b.warn(strings.ReplaceAll(w, "Cursor Agent", "Cursor IDE agentKv"))
	}
	inTurn := make([]bool, len(root.Events))
	keep := make([]bool, len(root.Events))
	matched, kept := 0, 0
	for _, span := range spans {
		user := span[0] < span[1] && root.Events[span[0]].Role == "user" && root.Events[span[0]].Kind == "message" && bubbleIDs[root.Events[span[0]].ID]
		if user {
			matched++
		}
		for i := span[0]; i < span[1]; i++ {
			inTurn[i] = true
			keep[i] = !user
		}
		if !user && span[0] < span[1] {
			kept++
		}
	}
	if transcript && matched == 0 && kept > 0 {
		for i := range keep {
			keep[i] = keep[i] && !inTurn[i]
		}
		kept = 0
		k.b.warn("Cursor IDE agentKv turns of " + s.ID + " could not be matched to bubble IDs; the readable bubble transcript is kept and agentKv turns are not repeated")
	} else if transcript && kept > 0 {
		k.b.warn("Cursor IDE agentKv-only turns without a matching bubble are appended after the bubble transcript")
	}
	var pre, turns, post []Event
	for i, e := range root.Events {
		switch {
		case inTurn[i]:
			if keep[i] {
				turns = append(turns, e)
			}
		case e.Kind == "session_metadata" || e.Kind == "recorded_prompt":
			pre = append(pre, e)
		default:
			post = append(post, e)
		}
	}
	events := append(pre, s.Events...)
	events = append(events, turns...)
	s.Events = append(events, post...)
	if s.CWD == "" {
		s.CWD = root.CWD
	}
	for _, model := range root.Models {
		s.model(model)
	}
	if root.UpdatedAt > s.UpdatedAt {
		s.UpdatedAt = root.UpdatedAt
	}
	var out []Session
	for _, c := range children {
		duplicate := false
		for _, part := range strings.Split(strings.TrimPrefix(c.ID, s.ID+"/"), "/") {
			duplicate = duplicate || k.known[part]
		}
		if !duplicate {
			c.Source = "cursor"
			out = append(out, c)
		}
	}
	return out
}

// hostOnly decodes agent host sessions that have no composerData record.
func (k *cursorIDEKV) hostOnly(hosts map[string]string, used map[string]bool) []Session {
	var ids []string
	for id := range hosts {
		if !used[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	var out []Session
	for _, id := range ids {
		s := Session{ID: id, Source: "cursor"}
		var listing struct {
			Title       string `json:"title"`
			UpdatedAtMs int64  `json:"updatedAtMs"`
		}
		_ = snapshot(hosts[id], func(hq *sql.Tx) error {
			if ok, err := cursorItem(hq, "agentHost:listingProjection", &listing); err != nil || !ok {
				return err
			}
			s.Title = listing.Title
			if listing.UpdatedAtMs > 0 {
				s.UpdatedAt = time.UnixMilli(listing.UpdatedAtMs).UTC().Format(time.RFC3339Nano)
			}
			return nil
		})
		before := k.decoded
		children := k.merge(&s, obj{}, hosts[id], nil, false)
		if k.decoded > before {
			out = append(out, s)
			out = append(out, children...)
		}
	}
	return out
}

// extras discloses agentKv rows that are deliberately not exported.
func (k *cursorIDEKV) extras() error {
	for _, c := range []struct{ prefix, text string }{
		{"agentKv:bubbleCheckpoint:", "Cursor IDE agentKv per-message checkpoints (earlier snapshots of the same conversations) are not exported"},
		{"agentKv:artifact:", "Cursor IDE agentKv artifact files are not exported"},
	} {
		rows, err := records(k.q, "SELECT count(*) AS n FROM cursorDiskKV WHERE substr(key,1,?)=?", len(c.prefix), c.prefix)
		if err != nil {
			return err
		}
		if n, _ := rows[0]["n"].(float64); n > 0 {
			k.b.warn(fmt.Sprintf("%s (%d records)", c.text, int(n)))
		}
	}
	if k.decoded == 0 {
		rows, err := records(k.q, "SELECT key FROM cursorDiskKV WHERE substr(key,1,13)='agentKv:blob:' LIMIT 1")
		if err != nil {
			return err
		}
		if len(rows) > 0 {
			k.b.warn("Cursor IDE agentKv blobs exist without a readable conversation root; unreferenced blobs are not scanned")
		}
	} else {
		k.b.warn("Experimental Cursor IDE agentKv decoding: storage layout checked against the Cursor 3.28.7 bundle, agent.v1 records decoded with the Cursor Agent CLI schema; file snapshot caches and encryption keys are excluded")
	}
	return nil
}
