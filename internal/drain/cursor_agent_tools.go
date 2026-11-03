package drain

import (
	"fmt"
	"sort"
	"strings"
)

// caSplitText moves long multi-line strings out of a JSON-ready tree into
// separate raw blocks so diffs, file contents and command output stay readable
// rather than being buried in escaped JSON. The JSON keeps a placeholder
// naming the block that carries the full text.
func caSplitText(v any, path string, blockType string, blocks *[]Block) any {
	switch x := v.(type) {
	case string:
		if len(x) >= 200 && strings.Contains(x, "\n") {
			*blocks = append(*blocks, Block{Type: blockType, Text: x, Description: path, Origin: blockType})
			return fmt.Sprintf("[%d bytes of text; full text in block %q]", len(x), path)
		}
		return x
	case obj:
		out := obj{}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			item := x[k]
			child := k
			if path != "" {
				child = path + "." + k
			}
			out[k] = caSplitText(item, child, blockType, blocks)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = caSplitText(item, fmt.Sprintf("%s[%d]", path, i), blockType, blocks)
		}
		return out
	}
	return v
}

func caU64(v any) uint64 { n, _ := v.(uint64); return n }

// toolCall decodes one agent.v1.ToolCall record into a tool_call event and, if
// the call finished, a tool_result event. depth bounds TruncatedToolCall chains.
func (r *cursorAgentReader) toolCall(s *Session, data []byte, depth int) error {
	if depth > 8 {
		return fmt.Errorf("Cursor Agent truncated tool call chain exceeds 8")
	}
	p, err := r.proto(data)
	if err != nil {
		return err
	}
	// A TruncatedToolCall points at the full original step stored as a blob.
	if truncated := p.data(34); len(truncated) > 0 {
		t, err := r.proto(truncated)
		if err != nil {
			return err
		}
		if ref := t.data(1); len(ref) > 0 {
			body, err := r.blob(ref)
			if err == nil {
				step, err := r.proto(body)
				if err != nil {
					return err
				}
				if original := step.data(2); len(original) > 0 {
					return r.toolCall(s, original, depth+1)
				}
				r.b.warn("Cursor Agent TruncatedToolCall original step has no tool call; the truncated record is decoded instead")
			} else if !strings.Contains(err.Error(), "missing Cursor Agent blob") {
				return err
			} else {
				r.b.warn("Cursor Agent TruncatedToolCall original step blob is missing; the truncated record is decoded instead")
			}
		}
	}
	d := &caDecoder{r: r}
	v, err := d.message("ToolCall", data, 0)
	if err != nil {
		return err
	}
	var keys []string
	for k := range v {
		if strings.HasSuffix(k, "_tool_call") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	name, inner := "unknown", obj{}
	if len(keys) > 0 {
		name, inner = strings.TrimSuffix(keys[0], "_tool_call"), object(v[keys[0]])
		if len(keys) > 1 {
			r.b.warn("Cursor Agent ToolCall has more than one tool variant; only the first is named")
		}
	} else {
		r.b.warn("Cursor Agent ToolCall has no known tool variant; its fields are retained as recorded JSON")
	}
	// Some ids are "call_x\nfc_y" pairs; the first line is the event key, the
	// full value stays in the JSON.
	callID := strings.SplitN(str(v["tool_call_id"]), "\n", 2)[0]
	call := obj{}
	for k, item := range inner {
		if k != "result" {
			call[k] = item
		}
	}
	// Shell parser output is derived entirely from the command text.
	if name == "shell" {
		if args := object(call["args"]); len(args) > 0 {
			delete(args, "parsing_result")
			delete(args, "simple_commands")
		}
	}
	if len(keys) == 0 {
		for k, item := range v {
			if k != "tool_call_id" && k != "started_at_ms" && k != "completed_at_ms" && k != "hook_additional_contexts" {
				call[k] = item
			}
		}
	}
	var callBlocks []Block
	args := caSplitText(call, "", "tool_call", &callBlocks)
	blocks := append([]Block{{Type: "tool_call", Name: name, CallID: callID, Text: jsonText(args)}}, callBlocks...)
	for _, hook := range arr(v["hook_additional_contexts"]) {
		blocks = append(blocks, Block{Type: "context", Text: jsonText(hook), Description: "Recorded hook additional context"})
	}
	for _, a := range d.attachments {
		blocks = append(blocks, Block{Type: "file", MIME: "application/octet-stream", Asset: r.b.asset(a.data), Description: "Recorded " + name + " tool binary value " + a.path})
	}
	s.Events = append(s.Events, Event{Role: "assistant", Kind: "tool_call", CallID: callID, Name: name, Time: cursorAgentTimestamp(caU64(v["started_at_ms"])), Blocks: blocks})
	if result, ok := inner["result"]; ok {
		var resultBlocks []Block
		text := caSplitText(result, "result", "tool_result", &resultBlocks)
		blocks := append([]Block{{Type: "tool_result", CallID: callID, Name: name, Text: jsonText(text)}}, resultBlocks...)
		when := caU64(v["completed_at_ms"])
		s.Events = append(s.Events, Event{Role: "tool", Kind: "tool_result", CallID: callID, Name: name, Time: cursorAgentTimestamp(when), Blocks: blocks})
	}
	return nil
}
