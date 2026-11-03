package drain

// This is an independent, bounded reader for Cursor Agent's local SQLite store.
// Field numbers were checked against the generated agent.v1 descriptors in the
// official 2026.10.01-e373342 CLI. This does not describe Cursor IDE agentKv.

import (
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

func cursorAgentPaths() []string {
	root := strings.TrimSpace(os.Getenv("CURSOR_CONFIG_DIR"))
	if root == "" {
		if config := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); config != "" {
			root = filepath.Join(config, "cursor")
		} else {
			home, _ := os.UserHomeDir()
			root = filepath.Join(home, ".cursor")
		}
	}
	return []string{filepath.Join(root, "chats"), filepath.Join(root, "acp-sessions")}
}

type cursorAgentWire struct {
	kind int
	data []byte
	num  uint64
}
type cursorAgentProto map[int][]cursorAgentWire

func (p cursorAgentProto) data(field int) []byte {
	values := p[field]
	if len(values) == 0 {
		return nil
	}
	return values[len(values)-1].data
}
func (p cursorAgentProto) text(field int) string { return string(p.data(field)) }
func (p cursorAgentProto) number(field int) uint64 {
	values := p[field]
	if len(values) == 0 {
		return 0
	}
	return values[len(values)-1].num
}

type cursorAgentReader struct {
	b       *Bundle
	q       queryer
	cache   map[string][]byte
	read    int64
	records int
	active  map[string]bool
}

func (r *cursorAgentReader) proto(p []byte) (cursorAgentProto, error) {
	r.records++
	if r.records > 100000 {
		return nil, fmt.Errorf("Cursor Agent exceeds 100000 reachable record limit")
	}
	out := cursorAgentProto{}
	for len(p) > 0 {
		tag, n := binary.Uvarint(p)
		if n <= 0 || tag>>3 == 0 || tag>>3 >= 1<<29 {
			return nil, fmt.Errorf("invalid Cursor Agent protobuf tag")
		}
		p = p[n:]
		v := cursorAgentWire{kind: int(tag & 7)}
		switch v.kind {
		case 0:
			v.num, n = binary.Uvarint(p)
			if n <= 0 {
				return nil, fmt.Errorf("invalid Cursor Agent protobuf varint")
			}
			p = p[n:]
		case 1, 5:
			n = 8
			if v.kind == 5 {
				n = 4
			}
			if len(p) < n {
				return nil, fmt.Errorf("truncated Cursor Agent protobuf fixed value")
			}
			v.data, p = p[:n], p[n:]
		case 2:
			size, count := binary.Uvarint(p)
			if count <= 0 || size > uint64(len(p)-count) {
				return nil, fmt.Errorf("invalid Cursor Agent protobuf length")
			}
			p = p[count:]
			v.data, p = p[:int(size)], p[int(size):]
		default:
			return nil, fmt.Errorf("unsupported Cursor Agent protobuf wire type %d", v.kind)
		}
		field := int(tag >> 3)
		out[field] = append(out[field], v)
	}
	return out, nil
}

func (r *cursorAgentReader) blob(id []byte) ([]byte, error) {
	if len(id) != sha256.Size {
		return nil, fmt.Errorf("Cursor Agent blob reference must be a 32-byte SHA-256")
	}
	key := hex.EncodeToString(id)
	if data, exists := r.cache[key]; exists {
		return data, nil
	}
	rows, err := r.q.Query("SELECT length(data) FROM blobs WHERE id=?", key)
	if err != nil {
		return nil, err
	}
	if !rows.Next() {
		rows.Close()
		return nil, fmt.Errorf("missing Cursor Agent blob %s", key)
	}
	var size int64
	err = rows.Scan(&size)
	rows.Close()
	if err != nil {
		return nil, err
	}
	if size < 0 || size > maxSourceBytes-r.read {
		return nil, fmt.Errorf("Cursor Agent reachable blobs exceed %d-byte safety limit", maxSourceBytes)
	}
	rows, err = r.q.Query("SELECT data FROM blobs WHERE id=?", key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, fmt.Errorf("missing Cursor Agent blob %s", key)
	}
	var data []byte
	if err := rows.Scan(&data); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != key {
		return nil, fmt.Errorf("Cursor Agent blob checksum mismatch: %s", key)
	}
	r.read += int64(len(data))
	r.cache[key] = data
	return data, nil
}

func (r *cursorAgentReader) blobProto(id []byte) (cursorAgentProto, error) {
	data, err := r.blob(id)
	if err != nil {
		return nil, err
	}
	return r.proto(data)
}

func (r *cursorAgentReader) opaque(s *Session, data []byte, typeName, role, kind, callID, name string) {
	sum := sha256.Sum256(data)
	meta := obj{"record_type": typeName, "sha256": hex.EncodeToString(sum[:]), "bytes": len(data)}
	mime := "application/octet-stream"
	if strings.HasPrefix(typeName, "agent.v1.") {
		mime = "application/x-protobuf"
		meta["protobuf_type"] = typeName
	}
	s.Events = append(s.Events, Event{Role: role, Kind: kind, CallID: callID, Name: name, Blocks: []Block{
		{Type: "context", Text: jsonText(meta)},
		{Type: "file", MIME: mime, Asset: r.b.asset(data), Description: "Recorded " + typeName + "; opaque bytes retained"},
	}})
	r.b.warn("Cursor Agent " + typeName + " is retained as an opaque protobuf attachment; tool-specific fields are not decoded (Markdown needs --base64 to include these bytes)")
}

func (r *cursorAgentReader) prompt(s *Session, id []byte) error {
	data, err := r.blob(id)
	if err != nil {
		return err
	}
	v, err := decode(data)
	if err != nil {
		return fmt.Errorf("Cursor Agent recorded prompt JSON: %w", err)
	}
	m := object(v)
	if len(m) == 0 {
		return fmt.Errorf("Cursor Agent recorded prompt must be a JSON object")
	}
	role := first(m["role"], "system")
	content := m["content"]
	if content == nil {
		content = m["text"]
	}
	blocks := r.b.blocks(content)
	blocks = append(metadata(m, "role", "content", "text"), blocks...)
	s.Events = append(s.Events, Event{Role: role, Kind: "recorded_prompt", Blocks: blocks})
	return nil
}

func (r *cursorAgentReader) selectedContext(s *Session, p []byte) ([]Block, error) {
	context, err := r.proto(p)
	if err != nil {
		return nil, err
	}
	var blocks []Block
	for _, image := range context[1] {
		im, err := r.proto(image.data)
		if err != nil {
			return nil, err
		}
		bl := Block{Type: "image", Path: im.text(3), MIME: im.text(7)}
		data := im.data(8)
		if ref := im.data(1); len(ref) > 0 {
			data, err = r.blob(ref)
			if err != nil {
				return nil, err
			}
		}
		if len(data) > 32<<20 {
			r.b.warn("Cursor Agent selected image exceeds 32 MiB; reference retained")
		} else if data != nil {
			bl.Asset = r.b.asset(data)
		}
		if len(im[9])+len(im[10]) > 0 {
			r.b.warn("Cursor Agent selected-image upload/reference variant is not decoded")
		}
		blocks = append(blocks, bl)
	}
	for _, item := range context[3] {
		blocks = append(blocks, Block{Type: "context", Text: string(item.data)})
	}
	// SelectedFile and SelectedCodeSelection both use field 2 for the path.
	// Their field 1 content is a project-file snapshot, excluded by default.
	for _, field := range []int{4, 5} {
		for _, item := range context[field] {
			file, err := r.proto(item.data)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, Block{Type: "file", Path: file.text(2), Description: "Recorded selected-file reference; snapshot contents excluded"})
		}
	}
	for _, item := range context[6] {
		terminal, err := r.proto(item.data)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, Block{Type: "context", Text: terminal.text(1), Description: terminal.text(2), Path: terminal.text(3)})
	}
	for _, item := range context[10] {
		selected, err := r.proto(item.data)
		if err != nil {
			return nil, err
		}
		rule, err := r.proto(selected.data(1))
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, Block{Type: "context", Text: rule.text(2), Path: rule.text(1), Description: "Recorded Cursor rule instruction"})
	}
	for field := range context {
		if field != 1 && field != 3 && field != 4 && field != 5 && field != 6 && field != 10 {
			r.b.warn(fmt.Sprintf("Cursor Agent SelectedContext field %d is not decoded; file/attachment snapshots are not exported implicitly", field))
		}
	}
	return blocks, nil
}

func (r *cursorAgentReader) user(s *Session, id []byte) error {
	p, err := r.blobProto(id)
	if err != nil {
		return err
	}
	text := p.text(1)
	if ref := p.data(18); len(ref) > 0 {
		data, err := r.blob(ref)
		if err != nil {
			return err
		}
		if !utf8.Valid(data) {
			return fmt.Errorf("Cursor Agent user text blob is not UTF-8")
		}
		text = string(data)
	}
	e := Event{ID: p.text(2), Role: "user", Kind: "message", Time: stamp(float64(p.number(25))), Blocks: r.b.blocks(text)}
	if p.number(25) == 0 {
		e.Time = ""
	}
	if context := p.data(3); len(context) > 0 {
		blocks, err := r.selectedContext(s, context)
		if err != nil {
			return err
		}
		e.Blocks = append(e.Blocks, blocks...)
	}
	for _, field := range []int{8, 11} {
		if text := p.text(field); text != "" {
			e.Blocks = append(e.Blocks, Block{Type: "context", Text: text})
		}
	}
	s.Events = append(s.Events, e)
	return nil
}

func (r *cursorAgentReader) turn(s *Session, id []byte) error {
	p, err := r.blobProto(id)
	if err != nil {
		return err
	}
	if turn := p.data(1); len(turn) > 0 {
		a, err := r.proto(turn)
		if err != nil {
			return err
		}
		if ref := a.data(1); len(ref) > 0 {
			if err := r.user(s, ref); err != nil {
				return err
			}
		}
		for _, ref := range a[2] {
			step, err := r.blobProto(ref.data)
			if err != nil {
				return err
			}
			if tool := step.data(2); len(tool) > 0 {
				meta, err := r.proto(tool)
				if err != nil {
					return err
				}
				name := ""
				// Names are verified ToolCall oneof tags, never guessed from data.
				for _, tool := range []struct {
					field int
					name  string
				}{{1, "shell"}, {3, "delete"}, {4, "glob"}, {5, "grep"}, {8, "read"}, {9, "update_todos"}, {10, "read_todos"}, {12, "edit"}, {13, "ls"}, {15, "mcp"}, {19, "task"}} {
					if len(meta[tool.field]) > 0 {
						name = tool.name
					}
				}
				r.opaque(s, tool, "agent.v1.ToolCall", "assistant", "tool_call", meta.text(57), name)
				continue
			}
			field, kind := 1, "message"
			if len(step[3]) > 0 {
				field, kind = 3, "reasoning"
			}
			if len(step[field]) == 0 {
				return fmt.Errorf("unsupported Cursor Agent ConversationStep variant")
			}
			message, err := r.proto(step.data(field))
			if err != nil {
				return err
			}
			e := Event{Role: "assistant", Kind: kind, Blocks: []Block{{Type: "text", Text: message.text(1)}}}
			if kind == "reasoning" {
				e.Blocks[0].Type = "reasoning"
			}
			s.Events = append(s.Events, e)
		}
		return nil
	}
	if turn := p.data(2); len(turn) > 0 {
		a, err := r.proto(turn)
		if err != nil {
			return err
		}
		command, err := r.blobProto(a.data(1))
		if err != nil {
			return err
		}
		output, err := r.blobProto(a.data(2))
		if err != nil {
			return err
		}
		s.Events = append(s.Events, Event{Role: "user", Kind: "shell_command", Blocks: []Block{textBlock(command.text(1))}}, Event{Role: "tool", Kind: "tool_result", Name: "shell", Blocks: []Block{{Type: "tool_result", Text: jsonText(obj{"stdout": output.text(1), "stderr": output.text(2), "exit_code": int32(output.number(3))})}}})
		return nil
	}
	return fmt.Errorf("unsupported Cursor Agent ConversationTurnStructure variant")
}

func (r *cursorAgentReader) state(data []byte, s Session, depth int) error {
	if depth > 64 {
		return fmt.Errorf("Cursor Agent child state nesting exceeds 64")
	}
	p, err := r.proto(data)
	if err != nil {
		return err
	}
	for _, uri := range p[9] {
		if u, err := url.Parse(string(uri.data)); err == nil && u.Scheme == "file" && s.CWD == "" {
			s.CWD = u.Path
		}
	}
	for _, ref := range p[1] {
		if err := r.prompt(&s, ref.data); err != nil {
			return err
		}
	}
	for _, ref := range p[8] {
		if err := r.turn(&s, ref.data); err != nil {
			return err
		}
	}
	for _, text := range p[35] {
		s.Events = append(s.Events, Event{Role: "system", Kind: "durable_skill", Blocks: []Block{{Type: "context", Text: string(text.data)}}})
	}
	for _, field := range []int{3, 6, 7, 11, 13} {
		for _, ref := range p[field] {
			body, err := r.blob(ref.data)
			if err != nil {
				return err
			}
			if field == 6 {
				summary, err := r.proto(body)
				if err != nil {
					return err
				}
				s.Events = append(s.Events, Event{Role: "system", Kind: "compaction", Blocks: []Block{{Type: "context", Text: summary.text(1)}}})
			} else {
				typeName := map[int]string{3: "agent.v1.TodoItem", 7: "Cursor Agent plan blob (type not verified)", 11: "agent.v1.ConversationSummaryArchive", 13: "agent.v1.ConversationSummaryArchive"}[field]
				r.opaque(&s, body, typeName, "system", "recorded_state", "", "")
			}
		}
	}
	// No content/initial_content, fileStates or fileStatesV2 blob is followed.
	for _, field := range []int{12, 15} {
		for _, entry := range p[field] {
			item, err := r.proto(entry.data)
			if err != nil {
				return err
			}
			s.Events = append(s.Events, Event{Role: "system", Kind: "file_reference", Blocks: []Block{{Type: "file", Path: item.text(1), Description: "Cursor Agent file snapshot cache excluded"}}})
		}
	}
	r.b.Sessions = append(r.b.Sessions, s)
	children := map[string][]byte{}
	for _, entry := range p[16] {
		item, err := r.proto(entry.data)
		if err != nil {
			return err
		}
		children[item.text(1)] = item.data(2)
	}
	for _, entry := range p[31] {
		item, err := r.proto(entry.data)
		if err != nil {
			return err
		}
		key, ref := item.text(1), item.data(2)
		body, err := r.blob(ref)
		if err != nil {
			if _, inline := children[key]; inline && strings.Contains(err.Error(), "missing Cursor Agent blob") {
				r.b.warn("Cursor Agent child state reference missing; recorded inline state used")
				continue
			}
			return err
		}
		children[key] = body
	}
	var keys []string
	for key := range children {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if key == "" {
			return fmt.Errorf("Cursor Agent child state has empty identity")
		}
		body := children[key]
		hash := sha256.Sum256(body)
		activeKey := hex.EncodeToString(hash[:])
		if r.active[activeKey] {
			return fmt.Errorf("Cursor Agent child state cycle detected")
		}
		child, err := r.proto(body)
		if err != nil {
			return err
		}
		if len(child[1]) == 0 {
			r.b.warn("Cursor Agent child " + key + " has no locally recorded conversation state")
			continue
		}
		cs := Session{ID: s.ID + "/" + key, ParentID: s.ID, Source: "cursor-agent", CWD: s.CWD, Title: "Subagent " + key}
		cs.model(child.text(5))
		r.active[activeKey] = true
		err = r.state(child.data(1), cs, depth+1)
		delete(r.active, activeKey)
		if err != nil {
			return err
		}
	}
	return nil
}

func (b *Bundle) cursorAgent(input string) error {
	info, err := os.Stat(input)
	if err != nil {
		return err
	}
	files := []string{input}
	if info.IsDir() {
		files, err = paths(input, func(path string) bool { return filepath.Base(path) == "store.db" })
		if err != nil {
			return err
		}
	}
	if b.Assets == nil {
		b.Assets = map[string][]byte{}
	}
	var totalRead int64
	totalRecords := 0
	for _, path := range files {
		err := snapshot(path, func(q *sql.Tx) error {
			if !tableExists(q, "meta") || !tableExists(q, "blobs") {
				return fmt.Errorf("Cursor Agent store requires meta and blobs tables")
			}
			rows, err := records(q, "SELECT length(value) AS size FROM meta WHERE key='0'")
			if err != nil {
				return err
			}
			if len(rows) != 1 {
				return fmt.Errorf("Cursor Agent store has no meta/0")
			}
			if size, _ := rows[0]["size"].(float64); size > 8<<20 {
				return fmt.Errorf("Cursor Agent metadata exceeds safety limit")
			}
			rows, err = records(q, "SELECT value FROM meta WHERE key='0'")
			if err != nil {
				return err
			}
			raw, err := hex.DecodeString(str(rows[0]["value"]))
			if err != nil {
				return fmt.Errorf("Cursor Agent meta/0 is not hexadecimal JSON: %w", err)
			}
			var meta obj
			if err := json.Unmarshal(raw, &meta); err != nil {
				return fmt.Errorf("Cursor Agent metadata JSON: %w", err)
			}
			root, err := hex.DecodeString(str(meta["latestRootBlobId"]))
			if err != nil {
				return fmt.Errorf("Cursor Agent root reference: %w", err)
			}
			if len(root) == 0 {
				b.warn("Cursor Agent store has no recorded conversation: " + path)
				return nil
			}
			s := Session{ID: first(meta["agentId"], filepath.Base(filepath.Dir(path))), ParentID: str(object(meta["subagentInfo"])["parentAgentId"]), Title: str(meta["name"]), Source: "cursor-agent"}
			s.model(meta["lastUsedModel"])
			r := &cursorAgentReader{b: b, q: q, cache: map[string][]byte{}, active: map[string]bool{}, read: totalRead, records: totalRecords}
			body, err := r.blob(root)
			if err != nil {
				return err
			}
			err = r.state(body, s, 0)
			totalRead, totalRecords = r.read, r.records
			return err
		})
		if err != nil {
			return fmt.Errorf("Cursor Agent %s: %w", path, err)
		}
	}
	b.warn("Experimental Cursor Agent CLI schema: recorded prompts, user/assistant/thinking/shell and local child states are decoded; other typed records remain protobuf attachments")
	b.warn("Cursor Agent file snapshot caches and encryption/authentication metadata are excluded; referenced project files are never opened by this importer")
	return nil
}
