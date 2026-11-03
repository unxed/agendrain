package drain

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
)

type claudeCatalogEntry struct {
	path     string
	session  Session
	titles   claudeTitles
	explicit bool
}

// CatalogClaude indexes JSONL metadata and a short readable preview. Its Events
// contain only a preview, never the canonical history or an event count.
func CatalogClaude(input string) (*Bundle, error) {
	entries, _, warnings, err := claudeCatalogIndex(input)
	if err != nil {
		return nil, err
	}
	b := &Bundle{Version: Version, Assets: map[string][]byte{}, Warnings: warnings}
	seen := map[string]int{}
	titles := map[string]claudeTitles{}
	anonymous := map[string]bool{}
	target := ""
	for _, entry := range entries {
		s := entry.session
		if entry.path == input {
			target = s.ID
		}
		i, exists := seen[s.ID]
		if exists && (!entry.explicit || anonymous[s.ID]) {
			return nil, fmt.Errorf("Claude session identity collision %q", s.ID)
		}
		if !exists {
			i = len(b.Sessions)
			seen[s.ID] = i
			anonymous[s.ID] = !entry.explicit
			b.Sessions = append(b.Sessions, s)
		}
		dest := &b.Sessions[i]
		if dest.CWD == "" {
			dest.CWD = s.CWD
		}
		if s.UpdatedAt > dest.UpdatedAt {
			dest.UpdatedAt = s.UpdatedAt
		}
		if len(dest.Events) == 0 || len(s.Events) > 0 && dest.Events[0].Role != "user" && s.Events[0].Role == "user" {
			dest.Events = s.Events
		}
		title := titles[s.ID]
		title.merge(entry.titles)
		titles[s.ID] = title
		dest.Title = title.title(dest.CWD)
	}
	if len(b.Sessions) == 0 {
		return nil, fmt.Errorf("no sessions found in %s", input)
	}
	if target != "" {
		if err := b.Select(target); err != nil {
			return nil, err
		}
	}
	return b, nil
}

func claudeCatalogIndex(input string) ([]claudeCatalogEntry, string, []string, error) {
	ps, root, err := claudePaths(input)
	if err != nil {
		return nil, "", nil, err
	}
	var entries []claudeCatalogEntry
	var warnings []string
	for _, p := range ps {
		entry, damaged, err := claudeCatalogFile(root, p)
		if err != nil {
			return nil, "", nil, err
		}
		entries = append(entries, entry)
		if damaged {
			warnings = append(warnings, "Claude catalog skipped malformed records in "+p+"; canonical import validates selected transcripts")
		}
	}
	return entries, root, warnings, nil
}

func claudeCatalogFile(root, p string) (claudeCatalogEntry, bool, error) {
	fallback, err := claudeAnonymousID(root, p)
	if err != nil {
		return claudeCatalogEntry{}, false, err
	}
	entry := claudeCatalogEntry{path: p, session: Session{ID: fallback, Source: "claude"}}
	s := &entry.session
	base := strings.TrimSuffix(filepath.Base(p), ".jsonl")
	child := strings.HasPrefix(base, "agent-")
	if filepath.Base(filepath.Dir(p)) == "subagents" {
		s.ParentID = filepath.Base(filepath.Dir(filepath.Dir(p)))
	}
	f, err := os.Open(p)
	if err != nil {
		return entry, false, err
	}
	defer f.Close()
	var reader io.Reader = f
	if strings.HasSuffix(p, ".zst") {
		decoder, err := zstd.NewReader(f, zstd.WithDecoderMaxMemory(128<<20))
		if err != nil {
			return entry, false, err
		}
		defer decoder.Close()
		reader = decoder
	}
	scanner := bufio.NewScanner(io.LimitReader(reader, maxSourceBytes+1))
	scanner.Buffer(make([]byte, 64<<10), 64<<20)
	ids := map[string]bool{}
	damaged := false
	total := 0
	for scanner.Scan() {
		raw := scanner.Bytes()
		total += len(raw) + 1
		if total > maxSourceBytes {
			return entry, damaged, fmt.Errorf("%s exceeds source size limit", p)
		}
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		// Only metadata is decoded; tool inputs and attachments stay opaque.
		var m struct {
			Type        string          `json:"type"`
			SessionID   string          `json:"sessionId"`
			CWD         string          `json:"cwd"`
			Timestamp   json.RawMessage `json:"timestamp"`
			CustomTitle string          `json:"customTitle"`
			AITitle     string          `json:"aiTitle"`
			Summary     string          `json:"summary"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			damaged = true
			continue
		}
		if m.SessionID != "" {
			entry.explicit = true
			ids[m.SessionID] = true
			if child {
				if s.ParentID == "" {
					s.ParentID = m.SessionID
				}
				s.ID = s.ParentID + "/" + base
			} else {
				s.ID = m.SessionID
			}
		}
		if s.CWD == "" {
			s.CWD = m.CWD
		}
		entry.titles.observe(obj{"type": m.Type, "customTitle": m.CustomTitle, "aiTitle": m.AITitle, "summary": m.Summary})
		var timestamp any
		if json.Unmarshal(m.Timestamp, &timestamp) == nil {
			if when := stamp(timestamp); when > s.UpdatedAt {
				s.UpdatedAt = when
			}
		}
		if m.Type != "user" && m.Type != "assistant" || len(s.Events) > 0 && (s.Events[0].Role == "user" || m.Type != "user") {
			continue
		}
		var preview struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(raw, &preview) != nil {
			continue
		}
		content := preview.Content
		if len(preview.Message.Content) > 0 {
			content = preview.Message.Content
		}
		var blocks []Block
		if text := claudeCatalogText(content); text != "" {
			blocks = []Block{{Type: "text", Text: text}}
		} else {
			blocks = claudeCatalogAttachments(content)
		}
		if len(blocks) > 0 {
			s.Events = []Event{{Role: m.Type, Kind: "preview", Blocks: blocks}}
		}
	}
	if err := scanner.Err(); err != nil {
		return entry, damaged, err
	}
	if len(ids) > 1 {
		owner := base
		if child {
			owner = s.ParentID
		}
		matched := false
		for id := range ids {
			matched = matched || strings.EqualFold(id, owner)
		}
		if !claudeUUID(owner) || !matched {
			return entry, damaged, fmt.Errorf("Claude transcript %s has multiple sessionId values without a matching native UUID filename; cannot determine conversation ownership", p)
		}
		if !child {
			s.ID = owner
		}
	}
	return entry, damaged, nil
}

func claudeCatalogText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) != nil {
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &blocks) != nil {
			return ""
		}
		var parts []string
		for _, block := range blocks {
			if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
				parts = append(parts, block.Text)
			}
		}
		text = strings.Join(parts, "\n")
	}
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) > 1000 {
		text = string(runes[:1000]) + "…"
	}
	return text
}

// claudeCatalogAttachments keeps recorded attachment metadata without reading
// referenced paths or retaining encoded attachment payloads.
func claudeCatalogAttachments(raw json.RawMessage) []Block {
	var recorded []struct {
		Type        string `json:"type"`
		Path        string `json:"path"`
		FilePath    string `json:"file_path"`
		Filename    string `json:"filename"`
		FileName    string `json:"file_name"`
		MIME        string `json:"mime"`
		MIMEType    string `json:"mime_type"`
		MediaType   string `json:"media_type"`
		Alt         string `json:"alt"`
		Description string `json:"description"`
		Source      struct {
			MediaType string `json:"media_type"`
			MIMEType  string `json:"mime_type"`
		} `json:"source"`
	}
	if json.Unmarshal(raw, &recorded) != nil {
		return nil
	}
	var blocks []Block
	for _, item := range recorded {
		if item.Type != "image" && item.Type != "file" && item.Type != "document" {
			continue
		}
		kind := item.Type
		if kind == "document" {
			kind = "file"
		}
		blocks = append(blocks, Block{Type: kind, Path: first(item.Path, item.FilePath, item.Filename, item.FileName), MIME: first(item.MIME, item.MIMEType, item.MediaType, item.Source.MediaType, item.Source.MIMEType), Description: first(item.Alt, item.Description)})
	}
	return blocks
}

// loadSelectedClaude indexes all files, then canonically parses only the selected
// family. Malformed records in unrelated bodies cannot prevent an export.
func loadSelectedClaude(input, id string) (*Bundle, error) {
	entries, root, _, err := claudeCatalogIndex(input)
	if err != nil {
		return nil, err
	}
	selected := map[string]bool{id: true}
	found := false
	for _, e := range entries {
		found = found || e.session.ID == id
	}
	if !found {
		return nil, fmt.Errorf("session %q not found", id)
	}
	for changed := true; changed; {
		changed = false
		for _, e := range entries {
			if selected[e.session.ParentID] && !selected[e.session.ID] {
				selected[e.session.ID] = true
				changed = true
			}
		}
	}
	var ps []string
	for _, e := range entries {
		if selected[e.session.ID] {
			ps = append(ps, e.path)
		}
	}
	b := &Bundle{Version: Version, Assets: map[string][]byte{}}
	// An explicit transcript was already bounded by claudePaths. Suppress its
	// automatic root selection when the caller explicitly chose a child.
	if err := b.claudeFiles("", root, ps); err != nil {
		return nil, err
	}
	if err := b.Select(id); err != nil {
		return nil, err
	}
	return b, nil
}
