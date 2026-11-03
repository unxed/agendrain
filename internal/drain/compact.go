package drain

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Compaction is reference-based and lossless: a repeated value is kept once
// verbatim and later copies point to it. Recorded conversation blocks (user
// and assistant text, reasoning, tool calls and results) are never rewritten
// in place; only exact duplicate tool/system blocks and values nested inside
// recorded JSON context become references.
const (
	compactMinValue  = 256
	compactMinBase64 = 1024
)

// valueRef locates a kept value: the whole text of a block, or (inJSON) the
// value at a JSON Pointer inside that text, where "" is the document root.
type valueRef struct {
	session      string
	event, block int
	pointer      string
	inJSON       bool
}

type compactor struct {
	seen       map[[32]byte]valueRef
	usedAssets map[string]bool
	assets     bool
	b          *Bundle
	values     int
	valueBytes int
	inlined    int
	added      []Block
}

type splice struct {
	start, end int
	text       string
}

type member struct {
	key        string
	start, end int
}

func compactBundle(b *Bundle, o Options) {
	blocks := 0
	seen := map[[32]byte]string{}
	for si := range b.Sessions {
		s := &b.Sessions[si]
		for ei := range s.Events {
			e := &s.Events[ei]
			for bi := range e.Blocks {
				bl := &e.Blocks[bi]
				if !(e.Role == "tool" || e.Role == "system" || e.Role == "developer" || bl.Type == "tool_result") || bl.Asset != "" || bl.Text == "" || bl.Type == "reference" {
					continue
				}
				key := sha256.Sum256([]byte(e.Role + "\x00" + e.Kind + "\x00" + bl.Type + "\x00" + bl.Text))
				if prev, ok := seen[key]; ok {
					bl.Type = "reference"
					bl.Text = "Exact duplicate of " + prev
					blocks++
				} else {
					seen[key] = fmt.Sprintf("%s event %d", s.ID, ei+1)
				}
			}
		}
	}
	c := &compactor{seen: map[[32]byte]valueRef{}, usedAssets: map[string]bool{}, b: b, assets: o.Images == "" || o.Images == "keep"}
	for _, s := range b.Sessions {
		for _, e := range s.Events {
			for _, bl := range e.Blocks {
				if bl.Asset != "" {
					c.usedAssets[bl.Asset] = true
				}
			}
		}
	}
	// Recorded conversation blocks are anchors: copies inside context refer to
	// them, wherever they occur, so the readable wording stays in place.
	for _, s := range b.Sessions {
		for ei, e := range s.Events {
			for bi, bl := range e.Blocks {
				if bl.Type == "context" || bl.Type == "reference" || bl.Text == "" {
					continue
				}
				at := valueRef{session: s.ID, event: ei + 1, block: bi + 1}
				if len(bl.Text) >= compactMinValue {
					c.remember(stringKey(bl.Text), at)
				}
				if jsonContainer(bl.Text) {
					c.walk(bl.Text, 0, "", at, nil)
				}
			}
		}
	}
	for si := range b.Sessions {
		s := &b.Sessions[si]
		for ei := range s.Events {
			e := &s.Events[ei]
			c.added = nil
			for bi := range e.Blocks {
				bl := &e.Blocks[bi]
				if bl.Type != "context" || !jsonContainer(bl.Text) {
					continue
				}
				var out []splice
				c.walk(bl.Text, 0, "", valueRef{session: s.ID, event: ei + 1, block: bi + 1}, &out)
				if len(out) > 0 {
					bl.Text = apply(bl.Text, out)
				}
			}
			e.Blocks = append(e.Blocks, c.added...)
		}
	}
	if blocks > 0 {
		b.Changes = append(b.Changes, fmt.Sprintf("Compact: %d exact duplicate tool/system blocks replaced with references (\"Exact duplicate of <session> event <n>\")", blocks))
	}
	if c.values > 0 {
		b.Changes = append(b.Changes, fmt.Sprintf("Compact: %d repeated values (%d bytes) inside recorded JSON context replaced with {\"$agendrain_duplicate\":{block,event,pointer,session}} references to an identical value kept at that location", c.values, c.valueBytes))
	}
	if c.inlined > 0 {
		b.Changes = append(b.Changes, fmt.Sprintf("Compact: %d base64 payloads inside recorded JSON context replaced with {\"$agendrain_asset\":sha256} references to the same bytes in assets/", c.inlined))
	}
}

func jsonContainer(s string) bool {
	return (strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[")) && json.Valid([]byte(s))
}

func stringKey(s string) [32]byte { return sha256.Sum256([]byte("s" + s)) }

func (c *compactor) remember(key [32]byte, at valueRef) {
	if _, ok := c.seen[key]; !ok {
		c.seen[key] = at
	}
}

func marker(r valueRef) string {
	sess, _ := json.Marshal(r.session)
	ptr := ""
	if r.inJSON {
		p, _ := json.Marshal(r.pointer)
		ptr = `,"pointer":` + string(p)
	}
	return fmt.Sprintf(`{"$agendrain_duplicate":{"block":%d,"event":%d%s,"session":%s}}`, r.block, r.event, ptr, sess)
}

func pointerPart(p, key string) string {
	return p + "/" + strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
}

// walk visits the JSON value starting at i. With out == nil it only records
// value locations; otherwise it also emits replacements for repeated values.
func (c *compactor) walk(s string, i int, ptr string, at valueRef, out *[]splice) int {
	end := valueEnd(s, i)
	if end-i < compactMinValue {
		return end
	}
	at.pointer = ptr
	at.inJSON = true
	switch s[i] {
	case '"':
		var v string
		if json.Unmarshal([]byte(s[i:end]), &v) != nil || len(v) < compactMinValue {
			return end
		}
		c.check(stringKey(v), i, end, at, out)
		return end
	case '{', '[':
		// Values already holding references are not registered: their stored
		// form is not the value they stand for.
		if !strings.Contains(s[i:end], `"$agendrain_`) {
			if c.check(sha256.Sum256([]byte("c"+s[i:end])), i, end, at, out) {
				return end
			}
		}
	default:
		return end
	}
	members := children(s, i)
	if s[i] == '{' && out != nil && c.base64Object(s, members, at, out) {
		return end
	}
	for n, m := range members {
		if m.end-m.start < compactMinValue {
			continue
		}
		key := m.key
		if s[i] == '[' {
			key = strconv.Itoa(n)
		}
		c.walk(s, m.start, pointerPart(ptr, key), at, out)
	}
	return end
}

// check reports whether the value was replaced by a reference.
func (c *compactor) check(key [32]byte, start, end int, at valueRef, out *[]splice) bool {
	if prev, ok := c.seen[key]; ok {
		if out != nil && prev != at {
			*out = append(*out, splice{start, end, marker(prev)})
			c.values++
			c.valueBytes += end - start
			return true
		}
		return false
	}
	c.seen[key] = at
	return false
}

// base64Object moves {"type":"base64","media_type":...,"data":...} payloads to
// assets when the encoding round-trips exactly. The data value then becomes
// {"$agendrain_asset":"<sha256>"}; other members are walked normally.
func (c *compactor) base64Object(s string, members []member, at valueRef, out *[]splice) bool {
	if !c.assets {
		return false
	}
	var typ, mime string
	data := -1
	for n, m := range members {
		switch m.key {
		case "type":
			json.Unmarshal([]byte(s[m.start:m.end]), &typ)
		case "media_type":
			json.Unmarshal([]byte(s[m.start:m.end]), &mime)
		case "data":
			if s[m.start] == '"' && m.end-m.start >= compactMinBase64 {
				data = n
			}
		}
	}
	if typ != "base64" || data < 0 {
		return false
	}
	m := members[data]
	var encoded string
	if json.Unmarshal([]byte(s[m.start:m.end]), &encoded) != nil {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || base64.StdEncoding.EncodeToString(raw) != encoded {
		return false
	}
	id := c.b.asset(raw)
	if !c.usedAssets[id] {
		c.usedAssets[id] = true
		bl := Block{Type: "file", MIME: mime, Asset: id, Description: "Attachment decoded from base64 inside recorded context of this event; the context refers to it as $agendrain_asset"}
		if strings.HasPrefix(mime, "image/") {
			bl.Type = "image"
		}
		c.added = append(c.added, bl)
	}
	for n, other := range members {
		if n != data && other.end-other.start >= compactMinValue {
			c.walk(s, other.start, pointerPart(at.pointer, other.key), at, out)
		}
	}
	*out = append(*out, splice{m.start, m.end, `{"$agendrain_asset":"` + id + `"}`})
	c.inlined++
	return true
}

func apply(s string, edits []splice) string {
	sort.Slice(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
	var out strings.Builder
	last := 0
	for _, e := range edits {
		out.WriteString(s[last:e.start])
		out.WriteString(e.text)
		last = e.end
	}
	out.WriteString(s[last:])
	return out.String()
}

func skipSpace(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	return i
}

// valueEnd returns the end of the valid JSON value starting at i.
func valueEnd(s string, i int) int {
	switch s[i] {
	case '"':
		for j := i + 1; j < len(s); j++ {
			if s[j] == '\\' {
				j++
			} else if s[j] == '"' {
				return j + 1
			}
		}
		return len(s)
	case '{', '[':
		depth := 0
		for j := i; j < len(s); j++ {
			switch s[j] {
			case '"':
				j = valueEnd(s, j) - 1
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return j + 1
				}
			}
		}
		return len(s)
	}
	j := i
	for j < len(s) && !strings.ContainsRune(",]} \t\r\n", rune(s[j])) {
		j++
	}
	return j
}

// children lists the members of the object or array at i.
func children(s string, i int) []member {
	var out []member
	object := s[i] == '{'
	j := skipSpace(s, i+1)
	for j < len(s) && s[j] != '}' && s[j] != ']' {
		var key string
		if object {
			kEnd := valueEnd(s, j)
			json.Unmarshal([]byte(s[j:kEnd]), &key)
			j = skipSpace(s, kEnd)
			j = skipSpace(s, j+1) // ':'
		}
		end := valueEnd(s, j)
		out = append(out, member{key, j, end})
		j = skipSpace(s, end)
		if j < len(s) && s[j] == ',' {
			j = skipSpace(s, j+1)
		}
	}
	return out
}
