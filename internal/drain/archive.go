package drain

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func zipData(input string) (map[string][]byte, error) {
	z, err := zip.OpenReader(input)
	if err != nil {
		return nil, err
	}
	defer z.Close()
	out := map[string][]byte{}
	var total int64
	for _, f := range z.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := strings.ReplaceAll(f.Name, "\\", "/")
		if path.IsAbs(name) || path.Clean(name) != name || strings.Contains(name, ":") || strings.HasPrefix(name, "../") {
			return nil, fmt.Errorf("unsafe ZIP entry %q", f.Name)
		}
		if _, ok := out[name]; ok {
			return nil, fmt.Errorf("duplicate ZIP entry %q", name)
		}
		if f.UncompressedSize64 > maxSourceBytes {
			return nil, fmt.Errorf("oversized ZIP entry %q", name)
		}
		r, e := f.Open()
		if e != nil {
			return nil, e
		}
		data, e := bounded(r, maxSourceBytes-total)
		r.Close()
		if e != nil {
			return nil, e
		}
		total += int64(len(data))
		out[name] = data
	}
	return out, nil
}
func (b *Bundle) desktop(input string) error {
	if i, e := os.Stat(input); e == nil && i.IsDir() {
		if _, e := os.Stat(filepath.Join(input, "conversations.json")); e == nil {
			return b.desktop(filepath.Join(input, "conversations.json"))
		}
		b.warn("Desktop Code/Cowork: importing recorded JSONL context; cloud conversations require Claude's data export")
		if err := b.claude(input); err != nil {
			return err
		}
		for i := range b.Sessions {
			b.Sessions[i].Source = "claude-desktop"
		}
		return nil
	}
	var raw []byte
	assets := map[string][]byte{}
	if strings.HasSuffix(strings.ToLower(input), ".zip") {
		files, err := zipData(input)
		if err != nil {
			return err
		}
		for name, data := range files {
			if path.Base(name) == "conversations.json" {
				if raw != nil {
					return fmt.Errorf("multiple conversations.json entries")
				}
				raw = data
			} else {
				assets[name] = data
			}
		}
		if raw == nil {
			return fmt.Errorf("Claude export ZIP has no conversations.json")
		}
	} else {
		var err error
		raw, err = readLimited(input)
		if err != nil {
			return err
		}
	}
	v, err := decode(raw)
	if err != nil {
		return err
	}
	docs := arr(v)
	if docs == nil {
		if a := arr(object(v)["conversations"]); a != nil {
			docs = a
		} else {
			docs = []any{v}
		}
	}
	for _, doc := range docs {
		m := object(doc)
		s := Session{ID: first(m["uuid"], m["id"]), Title: first(m["name"], m["title"]), Source: "claude-desktop"}
		s.model(m["model"])
		if s.ID == "" {
			return fmt.Errorf("Claude export conversation has no uuid")
		}
		for _, field := range []string{"summary", "system_prompt", "instructions"} {
			if v := m[field]; v != nil {
				s.Events = append(s.Events, Event{Role: "system", Kind: field, Blocks: []Block{{Type: "context", Text: jsonText(v)}}})
			}
		}
		for _, message := range arr(m["chat_messages"]) {
			msg := object(message)
			s.model(msg["model"])
			role := first(msg["sender"], msg["role"])
			if role == "human" {
				role = "user"
			}
			if role == "" {
				role = "assistant"
			}
			e := Event{ID: first(msg["uuid"], msg["id"]), Role: role, Time: stamp(msg["created_at"]), Kind: "message"}
			e.Blocks = b.blocks(msg["content"])
			if len(e.Blocks) == 0 {
				e.Blocks = b.blocks(msg["text"])
			}
			for _, field := range []string{"attachments", "files"} {
				for _, a := range arr(msg[field]) {
					x := object(a)
					name := first(x["file_name"], x["filename"], x["name"], x["path"])
					bl := Block{Type: "file", Path: name, MIME: first(x["file_type"], x["mime_type"])}
					if strings.HasPrefix(bl.MIME, "image/") {
						bl.Type = "image"
					}
					if data, ok := assets[name]; ok {
						bl.Asset = b.asset(data)
					}
					if x["extracted_content"] != nil {
						b.warn("Claude attachment extracted_content omitted by default: " + name)
					}
					e.Blocks = append(e.Blocks, bl)
				}
			}
			s.Events = append(s.Events, e)
		}
		b.Sessions = append(b.Sessions, s)
	}
	b.warn("Claude data exports may omit uploaded file bytes and hidden system/reasoning context; only supplied records are available")
	return nil
}

func (b *Bundle) portable(input string) error {
	files := map[string][]byte{}
	var raw []byte
	var err error
	if strings.HasSuffix(strings.ToLower(input), ".zip") {
		files, err = zipData(input)
		if err != nil {
			return err
		}
		raw = files["manifest.json"]
		if raw == nil {
			return fmt.Errorf("missing manifest.json")
		}
	} else {
		raw, err = readLimited(input)
		if err != nil {
			return err
		}
	}
	if err = json.Unmarshal(raw, b); err != nil {
		return err
	}
	if b.Version != Version {
		return fmt.Errorf("unsupported format version %d", b.Version)
	}
	b.Assets = map[string][]byte{}
	for id, p := range b.AssetFiles {
		data, ok := files[p]
		if !ok {
			return fmt.Errorf("missing asset %s; import the ZIP containing manifest and assets", p)
		}
		sum := sha256.Sum256(data)
		if strings.HasPrefix(path.Base(p), hex.EncodeToString(sum[:])) {
			b.Assets[id] = data
		} else {
			return fmt.Errorf("asset checksum mismatch: %s", p)
		}
	}
	return nil
}

func writeBytes(w io.Writer, p []byte) error { _, err := w.Write(p); return err }
