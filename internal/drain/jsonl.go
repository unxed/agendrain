package drain

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func parentThread(v any) string {
	m := object(v)
	if len(m) == 0 {
		return ""
	}
	if s := first(m["parent_thread_id"], m["parentThreadId"]); s != "" {
		return s
	}
	for _, k := range []string{"source", "subagent", "thread_spawn", "thread_source"} {
		if s := parentThread(m[k]); s != "" {
			return s
		}
	}
	return ""
}

func (b *Bundle) codex(input string) error {
	root := input
	single := false
	if i, e := os.Stat(input); e == nil && !i.IsDir() {
		single = true
		root = filepath.Dir(input)
		for p := root; p != filepath.Dir(p); p = filepath.Dir(p) {
			if filepath.Base(p) == "sessions" || filepath.Base(p) == "archived_sessions" {
				root = p
				break
			}
		}
	}
	ps, err := logPaths(root)
	if err != nil {
		return err
	}
	target := ""
	for _, p := range ps {
		s := Session{ID: strings.TrimSuffix(strings.TrimSuffix(filepath.Base(p), ".zst"), ".jsonl"), Source: "codex"}
		err := lines(p, func(m obj) error {
			payload := object(m["payload"])
			typ := str(m["type"])
			when := stamp(m["timestamp"])
			switch typ {
			case "session_meta":
				s.ID = first(payload["id"], payload["session_id"], s.ID)
				s.ParentID = parentThread(payload)
				s.CWD = str(payload["cwd"])
				s.model(payload["model"])
				context := obj{}
				for k, v := range payload {
					if k != "id" && k != "session_id" && k != "cwd" && k != "model" {
						context[k] = v
					}
				}
				if len(context) > 0 {
					s.Events = append(s.Events, Event{Role: "system", Kind: "session_context", Time: when, Blocks: []Block{{Type: "context", Text: jsonText(context)}}})
				}
			case "turn_context":
				s.model(payload["model"])
				s.Events = append(s.Events, Event{Role: "system", Kind: "turn_context", Time: when, Blocks: []Block{{Type: "context", Text: jsonText(payload)}}})
			case "response_item":
				e := Event{ID: str(payload["id"]), Time: when, Role: first(payload["role"], "assistant"), Kind: str(payload["type"]), CallID: str(payload["call_id"]), Name: str(payload["name"])}
				switch e.Kind {
				case "message":
					e.Blocks = b.blocks(payload["content"])
					e.Blocks = append(metadata(payload, "type", "id", "role", "content"), e.Blocks...)
				case "function_call", "custom_tool_call":
					e.Kind = "tool_call"
					e.Blocks = []Block{textBlock(jsonText(payload))}
				case "function_call_output", "custom_tool_call_output":
					e.Role = "tool"
					e.Kind = "tool_result"
					e.Blocks = b.blocks(payload["output"])
				case "reasoning":
					e.Blocks = b.blocks(payload["summary"])
					e.Blocks = append(e.Blocks, b.blocks(payload["content"])...)
					if payload["encrypted_content"] != nil {
						b.warn("Codex contains encrypted reasoning; only recorded plaintext is available")
					}
					if len(e.Blocks) == 0 {
						return nil
					}
				default:
					e.Blocks = []Block{{Type: "context", Text: jsonText(payload)}}
				}
				s.Events = append(s.Events, e)
			case "event_msg":
				pt := str(payload["type"])
				if pt == "user_message" || pt == "agent_message" {
					role := "user"
					if pt == "agent_message" {
						role = "assistant"
					}
					blocks := b.blocks(payload["message"])
					for _, im := range arr(payload["images"]) {
						blocks = append(blocks, b.blocks(obj{"type": "input_image", "image_url": im})...)
					}
					s.Events = append(s.Events, Event{Time: when, Role: role, Kind: "event_mirror", Blocks: blocks})
					return nil
				}
				if pt == "token_count" || strings.HasSuffix(pt, "_delta") {
					return nil
				}
				s.Events = append(s.Events, Event{Time: when, Role: "system", Kind: pt, Blocks: []Block{{Type: "context", Text: jsonText(payload)}}})
			case "compacted":
				s.Events = append(s.Events, Event{Time: when, Role: "system", Kind: "compaction", Blocks: []Block{{Type: "context", Text: jsonText(payload)}}})
			default:
				s.Events = append(s.Events, Event{Time: when, Role: "system", Kind: typ, Blocks: []Block{{Type: "context", Text: jsonText(payload)}}})
			}
			return nil
		})
		if err != nil {
			return err
		}
		// event_msg is a UI mirror of response_item; retain it only when no
		// corresponding recorded message exists (older or interrupted logs).
		counts := map[string]int{}
		for _, e := range s.Events {
			if e.Kind == "message" {
				counts[e.Role+jsonText(e.Blocks)]++
			}
		}
		ordered := s.Events[:0]
		for _, e := range s.Events {
			if e.Kind == "event_mirror" {
				k := e.Role + jsonText(e.Blocks)
				if counts[k] > 0 {
					counts[k]--
					continue
				}
				e.Kind = "message"
			}
			ordered = append(ordered, e)
		}
		s.Events = ordered
		if single && filepath.Clean(p) == filepath.Clean(input) {
			target = s.ID
		}
		b.Sessions = append(b.Sessions, s)
	}
	if single {
		return b.Select(target)
	}
	return nil
}

func (b *Bundle) claude(input string) error {
	ps, err := logPaths(input)
	if err != nil {
		return err
	}
	if i, e := os.Stat(input); e == nil && !i.IsDir() {
		child := strings.TrimSuffix(input, ".jsonl")
		if i, e := os.Stat(filepath.Join(child, "subagents")); e == nil && i.IsDir() {
			extra, e := logPaths(filepath.Join(child, "subagents"))
			if e != nil {
				return e
			}
			ps = append(ps, extra...)
		}
		legacy, e := filepath.Glob(filepath.Join(filepath.Dir(input), "agent-*.jsonl"))
		if e != nil {
			return e
		}
		for _, p := range legacy {
			if p != input {
				ps = append(ps, p)
			}
		}
	}
	byID := map[string]int{}
	eventIDs := map[string]map[string]string{}
	target := ""
	for _, p := range ps {
		base := strings.TrimSuffix(filepath.Base(p), ".jsonl")
		s := Session{ID: base, Source: "claude"}
		child := strings.HasPrefix(base, "agent-")
		if filepath.Base(filepath.Dir(p)) == "subagents" {
			s.ParentID = filepath.Base(filepath.Dir(filepath.Dir(p)))
		}
		err = lines(p, func(m obj) error {
			if id := str(m["sessionId"]); id != "" {
				if child {
					if s.ParentID == "" {
						s.ParentID = id
					}
					s.ID = s.ParentID + "/" + base
				} else {
					s.ID = id
				}
			}
			if s.CWD == "" {
				s.CWD = str(m["cwd"])
			}
			msg := object(m["message"])
			s.model(msg["model"])
			typ := str(m["type"])
			e := Event{ID: str(m["uuid"]), Time: stamp(m["timestamp"]), Role: first(msg["role"], m["type"]), Kind: typ}
			if typ == "user" || typ == "assistant" || typ == "system" {
				if len(msg) > 0 {
					e.Blocks = b.blocks(msg["content"])
				} else {
					e.Blocks = b.blocks(m["content"])
				}
				if len(e.Blocks) == 0 {
					e.Blocks = []Block{{Type: "context", Text: jsonText(m)}}
				}
				// A single message may contain several ordered tool calls/results.
				allResults := len(arr(msg["content"])) > 0
				for _, item := range arr(msg["content"]) {
					x := object(item)
					bt := str(x["type"])
					if bt != "tool_result" {
						allResults = false
					}
					if bt == "tool_use" || bt == "tool_result" {
						e.CallID = first(x["id"], x["tool_use_id"])
						e.Name = str(x["name"])
						if bt != "tool_result" {
							e.Kind = "tool_call"
						}
					}
				}
				if allResults {
					e.Role = "tool"
					e.Kind = "tool_result"
				}
				e.Blocks = append(metadata(msg, "role", "content", "model"), e.Blocks...)
				e.Blocks = append(e.Blocks, metadata(m, "type", "uuid", "timestamp", "sessionId", "cwd", "message", "content")...)
			} else {
				e.Role = "system"
				e.Kind = typ
				e.Blocks = []Block{{Type: "context", Text: jsonText(m)}}
			}
			s.Events = append(s.Events, e)
			return nil
		})
		if err != nil {
			return err
		}
		if p == input {
			target = s.ID
		}
		idx, ok := byID[s.ID]
		if !ok {
			idx = len(b.Sessions)
			byID[s.ID] = idx
			b.Sessions = append(b.Sessions, Session{ID: s.ID, ParentID: s.ParentID, Source: s.Source, CWD: s.CWD})
			eventIDs[s.ID] = map[string]string{}
		}
		dest := &b.Sessions[idx]
		for _, m := range s.Models {
			dest.model(m)
		}
		for _, e := range s.Events {
			if e.ID != "" {
				old, ok := eventIDs[s.ID][e.ID]
				if ok && old == jsonText(e) {
					continue
				}
				eventIDs[s.ID][e.ID] = jsonText(e)
			}
			dest.Events = append(dest.Events, e)
		}
	}
	if i, e := os.Stat(input); e == nil && !i.IsDir() && target != "" {
		return b.Select(target)
	}
	return nil
}

func (b *Bundle) vibeTree(input string) error {
	i, err := os.Stat(input)
	if err != nil {
		return err
	}
	if !i.IsDir() {
		return fmt.Errorf("vtvibe expects an ai:// tree copied with F5 (directory with chat/)")
	}
	chat := filepath.Join(input, "chat")
	if _, e := os.Stat(chat); e != nil {
		chat = input
	}
	ps, err := paths(chat, func(p string) bool { return strings.HasSuffix(p, ".md") })
	if err != nil {
		return err
	}
	s := Session{ID: filepath.Base(input), Source: "vtvibe", CWD: input, Title: "vtvibe"}
	if raw, e := os.ReadFile(filepath.Join(input, "session.md")); e == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "model ") {
				_, v, _ := strings.Cut(line, ":")
				s.model(strings.TrimSpace(v))
			}
		}
	}
	for _, p := range ps {
		raw, e := readLimited(p)
		if e != nil {
			return e
		}
		text := string(raw)
		role := ""
		when := ""
		if strings.HasPrefix(text, "<!-- ") {
			head, body, ok := strings.Cut(text, "-->\n")
			if ok {
				meta := strings.SplitN(strings.TrimPrefix(head, "<!-- "), ",", 2)
				role = strings.TrimSpace(meta[0])
				if len(meta) > 1 {
					when = strings.TrimSpace(meta[1])
				}
				text = strings.TrimPrefix(body, "\n")
			}
		}
		if role == "" {
			continue
		}
		if strings.TrimSpace(text) == "RCtrl+A to hide" {
			continue
		}
		s.Events = append(s.Events, Event{ID: filepath.Base(p), Role: role, Time: when, Kind: "message", Blocks: []Block{textBlock(strings.TrimSuffix(text, "\n"))}})
	}
	if len(s.Events) == 0 {
		return fmt.Errorf("no vtvibe chat messages found")
	}
	// Context files remain references; reading them requires explicit --files.
	ctx := filepath.Join(input, "ctx")
	if _, e := os.Stat(ctx); e == nil {
		refs, e := paths(ctx, func(string) bool { return true })
		if e != nil {
			return e
		}
		var blocks []Block
		for _, p := range refs {
			blocks = append(blocks, Block{Type: "file", Path: p})
		}
		if len(blocks) > 0 {
			s.Events = append([]Event{{Role: "system", Kind: "attached_context", Blocks: blocks}}, s.Events...)
		}
	}
	b.warn("vtvibe's ai:// tree does not record historical system prompts or removed context files; only copied data is available")
	b.Sessions = append(b.Sessions, s)
	return nil
}
