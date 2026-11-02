package drain

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

type queryer interface {
	Query(string, ...any) (*sql.Rows, error)
}

func openDB(path string) (*sql.DB, error) {
	abs, e := filepath.Abs(path)
	if e != nil {
		return nil, e
	}
	if _, e = os.Stat(abs); e != nil {
		return nil, e
	}
	uriPath := filepath.ToSlash(abs)
	// file://C:/... treats the drive letter as a URI authority. SQLite
	// requires file:///C:/... for an absolute Windows drive path.
	if len(uriPath) > 1 && uriPath[1] == ':' {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath}
	q := u.Query()
	q.Set("mode", "ro")
	q.Add("_pragma", "query_only(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	u.RawQuery = q.Encode()
	db, e := sql.Open("sqlite", u.String())
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	if e = db.Ping(); e != nil {
		db.Close()
		return nil, e
	}
	return db, nil
}
func tableExists(q queryer, name string) bool {
	rows, e := q.Query("SELECT name FROM sqlite_master WHERE type='table' AND name=?", name)
	if e != nil {
		return false
	}
	defer rows.Close()
	return rows.Next()
}
func records(q queryer, query string, args ...any) ([]obj, error) {
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []obj
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		m := obj{}
		for i, k := range cols {
			if p, ok := vals[i].([]byte); ok {
				m[k] = string(p)
			} else if n, ok := vals[i].(int64); ok {
				m[k] = float64(n)
			} else {
				m[k] = vals[i]
			}
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
func snapshot(path string, fn func(*sql.Tx) error) error {
	db, e := openDB(path)
	if e != nil {
		return e
	}
	defer db.Close()
	tx, e := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	return fn(tx)
}

func (b *Bundle) opencode(input string) error {
	i, err := os.Stat(input)
	if err != nil {
		return err
	}
	if i.IsDir() {
		if _, e := os.Stat(filepath.Join(input, "opencode.db")); e == nil {
			return b.openCodeDB(filepath.Join(input, "opencode.db"))
		}
		return b.openCodeTree(input)
	}
	if strings.HasSuffix(input, ".db") {
		return b.openCodeDB(input)
	}
	raw, err := readLimited(input)
	if err != nil {
		return err
	}
	v, err := decode(raw)
	if err != nil {
		return err
	}
	var docs []any
	if a, ok := v.([]any); ok {
		docs = a
	} else {
		docs = []any{v}
	}
	for _, doc := range docs {
		m := object(doc)
		info := object(m["info"])
		if len(info) == 0 {
			info = object(m["session"])
		}
		s := sessionInfo(info, "opencode")
		sessionMetadata(&s, info)
		if s.ID == "" {
			return fmt.Errorf("OpenCode export has no info.id")
		}
		for _, msg := range arr(m["messages"]) {
			x := object(msg)
			mi := object(x["info"])
			if len(mi) == 0 {
				mi = x
			}
			s.model(mi["model"])
			s.model(mi["modelID"])
			e := Event{ID: str(mi["id"]), Role: first(mi["role"], mi["type"], "system"), Time: stamp(object(mi["time"])["created"]), Kind: "message"}
			content := x["parts"]
			if content == nil {
				content = x["content"]
			}
			e.Blocks = b.openCodeParts(content)
			if len(e.Blocks) == 0 {
				e.Blocks = b.blocks(mi["text"])
			}
			e.Blocks = append(metadata(mi, "id", "role", "type", "time", "model", "modelID", "text", "content", "parts", "files"), e.Blocks...)
			for _, file := range arr(mi["files"]) {
				e.Blocks = append(e.Blocks, b.blocks(file)...)
			}
			s.Events = append(s.Events, e)
		}
		b.Sessions = append(b.Sessions, s)
	}
	return nil
}
func sessionInfo(m obj, source string) Session {
	return Session{ID: str(m["id"]), ParentID: first(m["parent_id"], m["parentID"]), Source: source, Title: str(m["title"]), CWD: first(m["directory"], object(m["path"])["cwd"])}
}
func (b *Bundle) openCodeParts(v any) []Block {
	var out []Block
	for _, part := range arr(v) {
		m := object(part)
		typ := str(m["type"])
		if typ == "tool" {
			meta := obj{}
			for k, v := range m {
				if k != "state" {
					meta[k] = v
				}
			}
			state := object(m["state"])
			stateMeta := obj{}
			for k, v := range state {
				if k != "content" && k != "output" && k != "attachments" {
					stateMeta[k] = v
				}
			}
			meta["state"] = stateMeta
			out = append(out, Block{Type: "tool_call", Text: jsonText(meta)})
			content := state["content"]
			if content == nil {
				content = state["output"]
			}
			results := b.blocks(content)
			for _, a := range arr(state["attachments"]) {
				results = append(results, b.blocks(a)...)
			}
			for _, bl := range results {
				if bl.Type == "text" {
					bl.Type = "tool_result"
				}
				bl.Origin = "tool_result"
				bl.CallID = first(m["callID"], m["id"])
				bl.Name = first(m["tool"], m["name"])
				out = append(out, bl)
			}
		} else {
			out = append(out, b.blocks(m)...)
		}
	}
	return out
}
func (b *Bundle) openCodeDB(input string) error {
	return snapshot(input, func(q *sql.Tx) error {
		seen := map[string]bool{}
		for _, table := range []string{"session_v2", "session"} {
			if !tableExists(q, table) {
				continue
			}
			sessions, err := records(q, "SELECT * FROM "+table+" ORDER BY id")
			if err != nil {
				return err
			}
			for _, row := range sessions {
				s := sessionInfo(row, "opencode")
				if seen[s.ID] {
					continue
				}
				seen[s.ID] = true
				sessionMetadata(&s, row)
				s.model(row["model"])
				if table == "session_v2" && tableExists(q, "session_message") {
					msgs, err := records(q, "SELECT * FROM session_message WHERE session_id=? ORDER BY seq, id", s.ID)
					if err != nil {
						return err
					}
					for _, msg := range msgs {
						v, err := decode([]byte(str(msg["data"])))
						if err != nil {
							return fmt.Errorf("message %s: %w", str(msg["id"]), err)
						}
						m := object(v)
						s.model(m["model"])
						e := Event{ID: str(msg["id"]), Role: first(m["role"], msg["type"], "system"), Kind: str(msg["type"]), Time: stamp(msg["time_created"])}
						if e.Role != "user" && e.Role != "assistant" {
							e.Role = "system"
						}
						e.Blocks = b.openCodeParts(m["content"])
						if len(e.Blocks) == 0 {
							e.Blocks = b.blocks(m["text"])
						}
						for _, file := range arr(m["files"]) {
							e.Blocks = append(e.Blocks, b.blocks(file)...)
						}
						if tableExists(q, "session_part") {
							parts, err := records(q, "SELECT * FROM session_part WHERE message_id=? ORDER BY ordinal", e.ID)
							if err != nil {
								return err
							}
							for _, part := range parts {
								pv, err := decode([]byte(str(part["data"])))
								if err != nil {
									return err
								}
								pm := object(pv)
								if pm["type"] == nil {
									pm["type"] = part["type"]
								}
								e.Blocks = append(e.Blocks, b.openCodeParts([]any{pm})...)
							}
						}
						if len(e.Blocks) == 0 {
							e.Blocks = []Block{{Type: "context", Text: jsonText(m)}}
						}
						e.Blocks = append(metadata(m, "id", "role", "type", "time", "model", "modelID", "text", "content", "parts", "files"), e.Blocks...)
						s.Events = append(s.Events, e)
					}
				} else if tableExists(q, "message") {
					msgs, err := records(q, "SELECT * FROM message WHERE session_id=? ORDER BY time_created, id", s.ID)
					if err != nil {
						return err
					}
					for _, msg := range msgs {
						v, err := decode([]byte(str(msg["data"])))
						if err != nil {
							return err
						}
						m := object(v)
						s.model(m["model"])
						s.model(m["modelID"])
						e := Event{ID: str(msg["id"]), Role: first(m["role"], "system"), Kind: "message", Time: stamp(msg["time_created"])}
						if tableExists(q, "part") {
							parts, err := records(q, "SELECT * FROM part WHERE message_id=? ORDER BY time_created, id", e.ID)
							if err != nil {
								return err
							}
							for _, part := range parts {
								pv, err := decode([]byte(str(part["data"])))
								if err != nil {
									return err
								}
								e.Blocks = append(e.Blocks, b.openCodeParts([]any{pv})...)
							}
						}
						if len(e.Blocks) == 0 {
							e.Blocks = []Block{{Type: "context", Text: jsonText(m)}}
						}
						e.Blocks = append(metadata(m, "id", "role", "type", "time", "model", "modelID", "text", "content", "parts", "files"), e.Blocks...)
						s.Events = append(s.Events, e)
					}
				}
				b.Sessions = append(b.Sessions, s)
			}
		}
		return nil
	})
}
func (b *Bundle) openCodeTree(root string) error {
	if _, e := os.Stat(filepath.Join(root, "storage")); e == nil {
		root = filepath.Join(root, "storage")
	}
	ps, err := paths(filepath.Join(root, "session"), func(p string) bool { return strings.HasSuffix(p, ".json") })
	if err != nil {
		return err
	}
	for _, p := range ps {
		raw, err := readLimited(p)
		if err != nil {
			return err
		}
		v, err := decode(raw)
		if err != nil {
			return err
		}
		s := sessionInfo(object(v), "opencode")
		sessionMetadata(&s, object(v))
		if s.ID == "" {
			continue
		}
		msgs, err := paths(filepath.Join(root, "message", s.ID), func(p string) bool { return strings.HasSuffix(p, ".json") })
		if os.IsNotExist(err) {
			msgs = nil
			err = nil
		}
		if err != nil {
			return err
		}
		for _, mp := range msgs {
			raw, err := readLimited(mp)
			if err != nil {
				return err
			}
			v, err := decode(raw)
			if err != nil {
				return err
			}
			m := object(v)
			e := Event{ID: str(m["id"]), Role: first(m["role"], "system"), Kind: "message", Time: stamp(object(m["time"])["created"])}
			s.model(m["model"])
			parts, err := paths(filepath.Join(root, "part", e.ID), func(p string) bool { return strings.HasSuffix(p, ".json") })
			if os.IsNotExist(err) {
				parts, err = paths(filepath.Join(root, "part", s.ID, e.ID), func(p string) bool { return strings.HasSuffix(p, ".json") })
			}
			if os.IsNotExist(err) {
				err = nil
			}
			if err != nil {
				return err
			}
			for _, pp := range parts {
				raw, err := readLimited(pp)
				if err != nil {
					return err
				}
				v, err := decode(raw)
				if err != nil {
					return err
				}
				e.Blocks = append(e.Blocks, b.openCodeParts([]any{v})...)
			}
			e.Blocks = append(metadata(m, "id", "role", "type", "time", "model", "modelID", "text", "content", "parts", "files"), e.Blocks...)
			s.Events = append(s.Events, e)
		}
		b.Sessions = append(b.Sessions, s)
	}
	return nil
}

func (b *Bundle) cursor(input string) error {
	ps, err := paths(input, func(p string) bool { return filepath.Base(p) == "state.vscdb" })
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, p := range ps {
		err = snapshot(p, func(q *sql.Tx) error {
			table := "cursorDiskKV"
			if !tableExists(q, table) {
				table = "ItemTable"
			}
			if !tableExists(q, table) {
				return fmt.Errorf("Cursor store has no cursorDiskKV or ItemTable")
			}
			rows, err := records(q, "SELECT key,value FROM "+table+" WHERE key LIKE 'composerData:%' ORDER BY key")
			if err != nil {
				return err
			}
			for _, row := range rows {
				v, err := decode([]byte(str(row["value"])))
				if err != nil {
					return fmt.Errorf("Cursor %s: %w", str(row["key"]), err)
				}
				m := object(v)
				id := strings.TrimPrefix(str(row["key"]), "composerData:")
				if seen[id] {
					continue
				}
				seen[id] = true
				s := Session{ID: id, Source: "cursor", Title: first(m["name"], m["title"]), ParentID: first(m["parentComposerId"], m["parentComposerID"], m["parentId"])}
				s.model(object(m["modelConfig"])["modelName"])
				s.model(m["model"])
				bubbles := map[string]obj{}
				stored, err := records(q, "SELECT key,value FROM "+table+" WHERE substr(key,1,?)=? ORDER BY key", len("bubbleId:"+id+":"), "bubbleId:"+id+":")
				if err != nil {
					return err
				}
				for _, r := range stored {
					x, e := decode([]byte(str(r["value"])))
					if e != nil {
						return e
					}
					bid := strings.TrimPrefix(str(r["key"]), "bubbleId:"+id+":")
					bubbles[bid] = object(x)
				}
				for bid, x := range object(m["conversationMap"]) {
					if bubbles[bid] == nil {
						bubbles[bid] = object(x)
					}
				}
				var order []string
				for _, h := range arr(m["fullConversationHeadersOnly"]) {
					x := object(h)
					bid := str(x["bubbleId"])
					if bubbles[bid] != nil {
						if bubbles[bid]["type"] == nil {
							bubbles[bid]["type"] = x["type"]
						}
						order = append(order, bid)
					} else {
						b.warn("Cursor bubble missing: " + id + "/" + bid)
					}
				}
				if len(order) == 0 {
					for _, x := range arr(m["conversation"]) {
						bm := object(x)
						s.Events = append(s.Events, b.cursorEvent(bm))
					}
					var keys []string
					for k := range bubbles {
						keys = append(keys, k)
					}
					sort.Strings(keys)
					order = keys
					if len(keys) > 1 {
						b.warn("Cursor message headers absent; bubble order falls back to IDs")
					}
				}
				used := map[string]bool{}
				for _, bid := range order {
					e := b.cursorEvent(bubbles[bid])
					e.ID = bid
					s.Events = append(s.Events, e)
					used[bid] = true
				}
				var extra []string
				for bid := range bubbles {
					if !used[bid] {
						extra = append(extra, bid)
					}
				}
				sort.Strings(extra)
				for _, bid := range extra {
					e := b.cursorEvent(bubbles[bid])
					e.ID = bid
					s.Events = append(s.Events, e)
					b.warn("Cursor has unindexed bubbles; appended after ordered messages")
				}
				for _, field := range []string{"context", "rules", "systemPrompt"} {
					if x := m[field]; x != nil {
						s.Events = append([]Event{{Role: "system", Kind: field, Blocks: []Block{{Type: "context", Text: jsonText(x)}}}}, s.Events...)
					}
				}
				ctx, err := records(q, "SELECT key,value FROM "+table+" WHERE substr(key,1,?)=? ORDER BY key", len("messageRequestContext:"+id+":"), "messageRequestContext:"+id+":")
				if err != nil {
					return err
				}
				for _, c := range ctx {
					s.Events = append(s.Events, Event{Role: "system", Kind: "request_context", ID: str(c["key"]), Blocks: []Block{{Type: "context", Text: str(c["value"])}}})
				}
				b.Sessions = append(b.Sessions, s)
			}
			legacy, err := records(q, "SELECT key,value FROM "+table+" WHERE key IN ('workbench.panel.aichat.view.aichat.chatdata','aiService.prompts') ORDER BY key")
			if err != nil {
				return err
			}
			for _, row := range legacy {
				b.Sessions = append(b.Sessions, Session{ID: filepath.Base(filepath.Dir(p)) + ":" + str(row["key"]), Source: "cursor", Events: []Event{{Role: "system", Kind: "legacy_chat", Blocks: []Block{{Type: "context", Text: str(row["value"])}}}}})
			}
			if tableExists(q, "cursorDiskKV") {
				r, e := records(q, "SELECT key FROM cursorDiskKV WHERE key LIKE 'agentKv:%' LIMIT 1")
				if e != nil {
					return e
				}
				if len(r) > 0 {
					b.warn("Cursor agentKv binary state exists; only readable composer/bubble/request context is exported")
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}
func (b *Bundle) cursorEvent(m obj) Event {
	role := str(m["role"])
	if role == "" {
		role = "assistant"
		if n, ok := m["type"].(float64); ok && n == 1 {
			role = "user"
		}
	}
	e := Event{ID: first(m["bubbleId"], m["id"]), Role: role, Kind: "message", Time: stamp(m["createdAt"])}
	e.Blocks = b.blocks(m["text"])
	for _, k := range []string{"content", "images", "attachments"} {
		e.Blocks = append(e.Blocks, b.blocks(m[k])...)
	}
	for _, k := range []string{"toolFormerData", "thinking", "thinkingText", "context", "codeBlocks", "capabilities", "toolResults"} {
		if m[k] != nil {
			e.Blocks = append(e.Blocks, Block{Type: k, Text: jsonText(m[k])})
		}
	}
	if len(e.Blocks) == 0 {
		e.Blocks = []Block{{Type: "context", Text: jsonText(m)}}
	}
	return e
}
