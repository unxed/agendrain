package drain

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

const maxSourceBytes = 512 << 20

type obj = map[string]any

func object(v any) obj {
	m, _ := v.(map[string]any)
	if m == nil {
		return obj{}
	}
	return m
}
func str(v any) string { s, _ := v.(string); return s }
func arr(v any) []any  { a, _ := v.([]any); return a }
func first(v ...any) string {
	for _, x := range v {
		if s := str(x); s != "" {
			return s
		}
	}
	return ""
}
func jsonText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	p, _ := marshalJSON(v, "")
	return string(p)
}

// marshalJSON keeps <, > and & literal: recorded prompts are full of markup,
// and < escapes only make them longer and harder to read or search.
func marshalJSON(v any, indent string) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", indent)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
func textBlock(s string) Block { return Block{Type: "text", Text: s} }
func metadata(m obj, excluded ...string) []Block {
	skip := map[string]bool{}
	for _, k := range excluded {
		skip[k] = true
	}
	extra := obj{}
	for k, v := range m {
		if !skip[k] {
			extra[k] = v
		}
	}
	if len(extra) == 0 {
		return nil
	}
	return []Block{{Type: "context", Text: jsonText(extra)}}
}
func sessionMetadata(s *Session, m obj) {
	blocks := metadata(m, "id", "parentID", "parent_id", "title", "directory", "path", "model", "time_created", "time_updated")
	if len(blocks) > 0 {
		s.Events = append(s.Events, Event{Role: "system", Kind: "session_context", Blocks: blocks})
	}
}
func stamp(v any) string {
	if s := str(v); s != "" {
		return s
	}
	if n, ok := v.(float64); ok {
		if n > 1e11 {
			return time.UnixMilli(int64(n)).UTC().Format(time.RFC3339Nano)
		}
		return time.Unix(int64(n), 0).UTC().Format(time.RFC3339Nano)
	}
	return ""
}
func decode(raw []byte) (any, error) { var v any; err := json.Unmarshal(raw, &v); return v, err }
func (b *Bundle) warn(s string) {
	for _, w := range b.Warnings {
		if w == s {
			return
		}
	}
	b.Warnings = append(b.Warnings, s)
}
func (s *Session) model(v any) {
	switch x := v.(type) {
	case string:
		if strings.HasPrefix(x, "{") {
			if v, e := decode([]byte(x)); e == nil {
				s.model(v)
				return
			}
		}
		if x != "" {
			for _, m := range s.Models {
				if x == m {
					return
				}
			}
			s.Models = append(s.Models, x)
		}
	case map[string]any:
		for _, k := range []string{"model", "modelID", "model_id", "id"} {
			if y := str(x[k]); y != "" {
				s.model(y)
			}
		}
	}
}
func (b *Bundle) asset(data []byte) string {
	sum := sha256.Sum256(data)
	id := hex.EncodeToString(sum[:])
	if b.Assets == nil {
		b.Assets = map[string][]byte{}
	}
	b.Assets[id] = data
	return id
}

// Load never opens referenced project files. It reads only the selected store.
func Load(source, input string) (*Bundle, error) {
	if input == "" {
		return nil, fmt.Errorf("input path is required; use agendrain sources to find stores")
	}
	if source == "" || source == "auto" {
		var err error
		source, err = detect(input)
		if err != nil {
			return nil, err
		}
	}
	b := &Bundle{Version: Version, Assets: map[string][]byte{}}
	var err error
	switch source {
	case "codex":
		err = b.codex(input)
	case "claude", "claude-code":
		err = b.claude(input)
	case "claude-desktop":
		err = b.desktop(input)
	case "opencode":
		err = b.opencode(input)
	case "cursor":
		err = b.cursor(input)
	case "cursor-agent":
		err = b.cursorAgent(input)
	case "vtvibe":
		err = b.vtvibe(input)
	case "agendrain":
		err = b.portable(input)
	default:
		return nil, fmt.Errorf("unknown source %q", source)
	}
	if err != nil {
		return nil, err
	}
	if len(b.Sessions) == 0 {
		return nil, fmt.Errorf("no sessions found in %s", input)
	}
	seen := map[string]bool{}
	for _, s := range b.Sessions {
		if s.ID == "" {
			return nil, fmt.Errorf("session without id")
		}
		if seen[s.ID] {
			return nil, fmt.Errorf("duplicate session id %s: select a narrower input", s.ID)
		}
		seen[s.ID] = true
	}
	return b, nil
}

func detect(input string) (string, error) {
	info, err := os.Stat(input)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		for _, p := range []struct{ path, source string }{{"chat", "vtvibe"}, {"opencode.db", "opencode"}, {"storage/session", "opencode"}, {"globalStorage/state.vscdb", "cursor"}, {"store.db", "cursor-agent"}} {
			if _, e := os.Stat(filepath.Join(input, p.path)); e == nil {
				return p.source, nil
			}
		}
		return "", fmt.Errorf("directory source is ambiguous; specify --source")
	}
	if strings.HasSuffix(input, ".zip") {
		return "", fmt.Errorf("specify --source claude-desktop or agendrain for a ZIP")
	}
	if strings.HasSuffix(input, ".jsonl") || strings.HasSuffix(input, ".jsonl.zst") {
		var typ string
		err := lines(input, func(m obj) error {
			if typ == "" {
				if m["payload"] != nil {
					typ = "codex"
				} else if m["sessionId"] != nil || m["message"] != nil {
					typ = "claude"
				}
			}
			return nil
		})
		if err != nil {
			return "", err
		}
		if typ != "" {
			return typ, nil
		}
	}
	if strings.HasSuffix(input, ".db") || strings.HasSuffix(input, ".vscdb") {
		db, err := openDB(input)
		if err != nil {
			return "", err
		}
		defer db.Close()
		if tableExists(db, "cursorDiskKV") || tableExists(db, "ItemTable") {
			return "cursor", nil
		}
		if tableExists(db, "session") || tableExists(db, "session_v2") {
			return "opencode", nil
		}
		if tableExists(db, "blobs") && tableExists(db, "meta") {
			return "cursor-agent", nil
		}
	}
	raw, err := readLimited(input)
	if err != nil {
		return "", err
	}
	v, err := decode(raw)
	if err != nil {
		return "", err
	}
	m := object(v)
	if m["version"] != nil && m["sessions"] != nil {
		return "agendrain", nil
	}
	if m["info"] != nil {
		return "opencode", nil
	}
	if m["chat_messages"] != nil || len(arr(v)) > 0 {
		return "claude-desktop", nil
	}
	return "", fmt.Errorf("unrecognized source format; use --source")
}

func readLimited(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return bounded(f, maxSourceBytes)
}
func bounded(r io.Reader, n int64) ([]byte, error) {
	p, e := io.ReadAll(io.LimitReader(r, n+1))
	if e != nil {
		return nil, e
	}
	if int64(len(p)) > n {
		return nil, fmt.Errorf("input exceeds %d byte safety limit", n)
	}
	return p, nil
}
func lines(path string, fn func(obj) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var r io.Reader = f
	if strings.HasSuffix(path, ".zst") {
		z, e := zstd.NewReader(f, zstd.WithDecoderMaxMemory(128<<20))
		if e != nil {
			return e
		}
		defer z.Close()
		r = z
	}
	s := bufio.NewScanner(io.LimitReader(r, maxSourceBytes+1))
	s.Buffer(make([]byte, 64<<10), 64<<20)
	n, total := 0, 0
	for s.Scan() {
		n++
		total += len(s.Bytes()) + 1
		if total > maxSourceBytes {
			return fmt.Errorf("%s exceeds source size limit", path)
		}
		if len(bytes.TrimSpace(s.Bytes())) == 0 {
			continue
		}
		v, e := decode(s.Bytes())
		if e != nil {
			return fmt.Errorf("%s:%d: %w", path, n, e)
		}
		m, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%s:%d: expected JSON object", path, n)
		}
		if e := fn(m); e != nil {
			return fmt.Errorf("%s:%d: %w", path, n, e)
		}
	}
	if err := s.Err(); err != nil {
		return fmt.Errorf("%s:%d: %w", path, n+1, err)
	}
	return nil
}
func paths(root string, accept func(string) bool) ([]string, error) {
	i, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !i.IsDir() {
		return []string{root}, nil
	}
	var out []string
	err = filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if !d.IsDir() && accept(p) {
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}
func logPaths(root string) ([]string, error) {
	return paths(root, func(p string) bool { return strings.HasSuffix(p, ".jsonl") || strings.HasSuffix(p, ".jsonl.zst") })
}

func (b *Bundle) blocks(v any) []Block {
	if s, ok := v.(string); ok {
		if s == "" {
			return nil
		}
		return []Block{textBlock(s)}
	}
	if a, ok := v.([]any); ok {
		var out []Block
		for _, x := range a {
			out = append(out, b.blocks(x)...)
		}
		return out
	}
	m, ok := v.(map[string]any)
	if !ok {
		if v == nil {
			return nil
		}
		return []Block{{Type: "context", Text: jsonText(v)}}
	}
	t := str(m["type"])
	switch t {
	case "text", "input_text", "output_text":
		return append([]Block{textBlock(first(m["text"], m["content"]))}, metadata(m, "type", "text", "content")...)
	case "thinking", "reasoning":
		s := first(m["thinking"], m["text"])
		if s == "" {
			s = jsonText(m)
		}
		return []Block{{Type: "reasoning", Text: s}}
	case "image", "input_image", "image_url", "file", "document", "input_file":
		mime := first(m["mime"], m["mime_type"], m["media_type"])
		src := object(m["source"])
		if mime == "" {
			mime = first(src["media_type"], src["mime_type"])
		}
		path := first(m["path"], m["file_path"], m["filename"], m["file_name"])
		u := first(m["url"], m["image_url"], object(m["image_url"])["url"], src["url"])
		block := Block{Type: "file", Path: path, MIME: mime, Description: first(m["alt"], m["description"])}
		if strings.Contains(t, "image") || strings.HasPrefix(mime, "image/") {
			block.Type = "image"
		}
		encoded := first(src["data"], m["data"], m["file_data"])
		if strings.HasPrefix(u, "data:") {
			encoded = u
		}
		if strings.HasPrefix(encoded, "data:") {
			header, body, ok := strings.Cut(encoded, ",")
			if !ok {
				b.warn("Malformed inline attachment")
				return []Block{block}
			}
			block.MIME = strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")
			if strings.Contains(header, ";base64") {
				encoded = body
			} else {
				raw, e := url.QueryUnescape(body)
				if e == nil {
					block.Asset = b.asset([]byte(raw))
				}
				encoded = ""
			}
		}
		if encoded != "" {
			data, e := base64.StdEncoding.DecodeString(encoded)
			if e != nil {
				b.warn("Invalid base64 attachment: " + path)
			} else if len(data) > 32<<20 {
				b.warn("Attachment exceeds 32 MiB: " + path)
			} else {
				block.Asset = b.asset(data)
			}
		}
		if strings.HasPrefix(block.MIME, "image/") {
			block.Type = "image"
		}
		if u != "" && !strings.HasPrefix(u, "data:") && path == "" {
			block.Path = u
		}
		return []Block{block}
	case "tool_use", "tool_call", "function_call":
		return []Block{{Type: "tool_call", Text: jsonText(m), CallID: first(m["id"], m["call_id"]), Name: str(m["name"])}}
	case "tool_result", "function_call_output":
		meta := obj{}
		for k, v := range m {
			if k != "content" && k != "output" {
				meta[k] = v
			}
		}
		out := []Block{{Type: "tool_result", Text: jsonText(meta)}}
		content := m["content"]
		if content == nil {
			content = m["output"]
		}
		for _, bl := range b.blocks(content) {
			if bl.Type == "text" {
				bl.Type = "tool_result"
			}
			out = append(out, bl)
		}
		for i := range out {
			out[i].Origin = "tool_result"
			out[i].CallID = first(m["tool_use_id"], m["call_id"])
		}
		return out
	default:
		// Unknown recorded context is preserved, rather than silently dropped.
		return []Block{{Type: "context", Text: jsonText(m)}}
	}
}

func (b *Bundle) Select(id string) error {
	b.linkChildren()
	if id == "" {
		return nil
	}
	selected := map[string]bool{id: true}
	found := false
	for _, s := range b.Sessions {
		if s.ID == id {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("session %q not found", id)
	}
	for changed := true; changed; {
		changed = false
		for _, s := range b.Sessions {
			if selected[s.ParentID] && !selected[s.ID] {
				selected[s.ID] = true
				changed = true
			}
		}
	}
	out := make([]Session, 0, len(selected))
	for _, s := range b.Sessions {
		if selected[s.ID] {
			out = append(out, s)
		}
	}
	b.Sessions = out
	known := map[string]bool{}
	for _, s := range out {
		known[s.ID] = true
	}
	for _, s := range out {
		for _, ref := range spawnReferences(s) {
			if !known[ref] && !b.hasAgentAlias(ref) {
				b.warn("Referenced subagent " + ref + " is absent; provide the enclosing session store")
			}
		}
	}

	return nil
}
func references(v any) []string {
	var out []string
	switch x := v.(type) {
	case map[string]any:
		for k, v := range x {
			if k == "agent_id" || k == "thread_id" || k == "sessionID" || k == "new_agent_id" || k == "subagentComposerId" || k == "subagentSessionId" {
				if s := str(v); s != "" {
					out = append(out, s)
				}
			}
			out = append(out, references(v)...)
		}
	case []any:
		for _, v := range x {
			out = append(out, references(v)...)
		}
	}
	return out
}

func (b *Bundle) hasAgentAlias(id string) bool {
	for _, s := range b.Sessions {
		if strings.HasSuffix(s.ID, "/agent-"+id) || strings.HasSuffix(s.ID, "/"+id) {
			return true
		}
	}
	return false
}
func (b *Bundle) linkChildren() {
	index := map[string]int{}
	for i, s := range b.Sessions {
		index[s.ID] = i
	}
	for _, s := range b.Sessions {
		for _, id := range spawnReferences(s) {
			if i, ok := index[id]; ok && id != s.ID && b.Sessions[i].ParentID == "" && s.ParentID != id {
				b.Sessions[i].ParentID = s.ID
			}
		}
	}
}
func spawnTool(name string) bool {
	name = strings.ToLower(name)
	for _, n := range []string{"spawn_agent", "create_agent", "run_subagent", "task", "agent", "delegate_task"} {
		if name == n || strings.HasSuffix(name, "."+n) || strings.HasSuffix(name, "__"+n) {
			return true
		}
	}
	return false
}
func spawnReferences(s Session) []string {
	calls := map[string]bool{}
	for _, e := range s.Events {
		if spawnTool(e.Name) && e.CallID != "" {
			calls[e.CallID] = true
		}
		for _, bl := range e.Blocks {
			if bl.Type == "tool_call" {
				v, err := decode([]byte(bl.Text))
				if err != nil {
					continue
				}
				m := object(v)
				if spawnTool(first(bl.Name, m["name"], m["tool"])) {
					if c := first(bl.CallID, m["id"], m["call_id"], m["callID"]); c != "" {
						calls[c] = true
					}
				}
			}
		}
	}
	var out []string
	for _, e := range s.Events {
		isSpawn := spawnTool(e.Name) || calls[e.CallID] || strings.Contains(e.Kind, "agent_spawn")
		for _, bl := range e.Blocks {
			v, err := decode([]byte(bl.Text))
			if err != nil {
				continue
			}
			m := object(v)
			blockSpawn := spawnTool(first(bl.Name, m["name"], m["tool"])) || calls[bl.CallID]
			if isSpawn || blockSpawn {
				out = append(out, references(v)...)
			}
		}
	}
	return out
}

// Discover returns platform-native candidates; it never creates or modifies stores.
func Discover(source string) []string {
	home, _ := os.UserHomeDir()
	config, _ := os.UserConfigDir()
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		data = filepath.Join(home, ".local", "share")
	}
	codex := os.Getenv("CODEX_HOME")
	if codex == "" {
		codex = filepath.Join(home, ".codex")
	}
	claude := os.Getenv("CLAUDE_CONFIG_DIR")
	if claude == "" {
		claude = filepath.Join(home, ".claude")
	}
	switch source {
	case "codex":
		return []string{filepath.Join(codex, "sessions"), filepath.Join(codex, "archived_sessions")}
	case "claude", "claude-code":
		return []string{filepath.Join(claude, "projects")}
	case "opencode":
		if p := os.Getenv("OPENCODE_DB"); p != "" {
			return []string{p}
		}
		return []string{filepath.Join(data, "opencode", "opencode.db"), filepath.Join(data, "opencode", "storage")}
	case "cursor":
		return []string{filepath.Join(config, "Cursor", "User", "globalStorage", "state.vscdb"), filepath.Join(config, "Cursor", "User", "workspaceStorage")}
	case "cursor-agent":
		return cursorAgentPaths()
	case "claude-desktop":
		return []string{filepath.Join(config, "Claude", "local-agent-mode-sessions"), filepath.Join(claude, "projects")}
	case "vtvibe":
		return []string{filepath.Join(config, "f4", "ai", "dialog.json"), filepath.Join(config, "f4", "ai", "dialogs")}
	default:
		return nil
	}
}
