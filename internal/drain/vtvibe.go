package drain

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func (b *Bundle) vtvibe(input string) error {
	i, err := os.Stat(input)
	if err != nil {
		return err
	}
	if i.IsDir() {
		if _, err := os.Stat(filepath.Join(input, "agendrain.json")); err == nil {
			return b.vtvibe(filepath.Join(input, "agendrain.json"))
		}
		if _, err := os.Stat(filepath.Join(input, "chat")); err == nil {
			return b.vibeTree(input)
		}
		if _, err := os.Stat(filepath.Join(input, "dialog.json")); err == nil {
			return b.vibeSavedTree(input)
		}
		ps, err := paths(input, func(p string) bool { return strings.HasSuffix(p, ".json") })
		if err != nil {
			return err
		}
		if len(ps) > 0 {
			return b.vibeSavedTree(input)
		}
		return b.vibeTree(input)
	}
	raw, err := readLimited(input)
	if err != nil {
		return err
	}
	v, err := decode(raw)
	if err != nil {
		return err
	}
	m := object(v)
	if m["sessions"] != nil {
		if err := b.portable(input); err != nil {
			return err
		}
		for si := range b.Sessions {
			if b.Sessions[si].CWD == "" {
				b.Sessions[si].CWD = filepath.Dir(input)
			}
		}
		return nil
	}
	if m["turns"] == nil {
		return fmt.Errorf("not a saved vtvibe dialog: %s", input)
	}
	s := Session{ID: "vtvibe:" + strings.TrimSuffix(filepath.Base(input), ".json"), Title: str(m["title"]), Source: "vtvibe", CWD: filepath.Dir(input)}
	for model := range object(m["spent"]) {
		s.model(model)
	}
	for _, turn := range arr(m["turns"]) {
		t := object(turn)
		role := first(t["Role"], t["role"])
		text := first(t["Text"], t["text"])
		if strings.TrimSpace(text) == "RCtrl+A to hide" {
			continue
		}
		s.Events = append(s.Events, Event{Role: role, Kind: "message", Time: first(t["Time"], t["time"]), Blocks: []Block{textBlock(text)}})
	}
	context := object(m["context"])
	var names []string
	for name := range context {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) > 0 {
		var refs []Block
		for _, name := range names {
			bl := Block{Type: "file", Path: "ai://" + strings.TrimPrefix(name, "/"), Description: "Recorded context file; bytes excluded unless --files is requested"}
			if encoded := str(context[name]); encoded != "" {
				data, e := base64.StdEncoding.DecodeString(encoded)
				if e != nil {
					return fmt.Errorf("vtvibe context %s: %w", name, e)
				}
				if len(data) > 32<<20 {
					b.warn("vtvibe context file exceeds 32 MiB: " + name)
				} else {
					bl.storedAsset = b.asset(data)
				}
			}
			refs = append(refs, bl)
		}
		s.Events = append([]Event{{Role: "system", Kind: "attached_context", Blocks: refs}}, s.Events...)
	}
	if draft := str(m["draft"]); strings.TrimSpace(draft) != "" {
		s.Events = append(s.Events, Event{Role: "user", Kind: "unsent_draft", Blocks: []Block{textBlock(draft)}})
	}
	// Deliberately whitelist contextual fields. savedDialog also stores a GitHub
	// token; credentials must never be copied via a catch-all metadata block.
	extra := obj{}
	for _, k := range []string{"patch_mode", "orders", "mode", "applied"} {
		if m[k] != nil {
			extra[k] = m[k]
		}
	}
	if len(extra) > 0 {
		s.Events = append(s.Events, Event{Role: "system", Kind: "saved_state", Blocks: []Block{{Type: "context", Text: jsonText(extra)}}})
	}
	b.Sessions = append(b.Sessions, s)
	b.warn("Saved vtvibe dialogs do not contain historical system prompts or transient bot tool histories unless recorded by the optional f4 export patch")
	return nil
}
func (b *Bundle) vibeSavedTree(input string) error {
	ps, err := paths(input, func(p string) bool { return strings.HasSuffix(p, ".json") })
	if err != nil {
		return err
	}
	for _, p := range ps {
		if err := b.vtvibe(p); err != nil {
			return err
		}
	}
	return nil
}
