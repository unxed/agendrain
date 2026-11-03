package drain

import (
	"bytes"
	"fmt"
	"path"
	"strings"
)

// The standard ZIP has a concise primary entry point and one document per
// subagent. The canonical manifest still contains every event in original order.
func renderSplit(b *Bundle, o Options) (rendered, error) {
	known := map[string]bool{}
	for _, s := range b.Sessions {
		known[s.ID] = true
	}
	main := *b
	main.Sessions = nil
	var children []Session
	for _, s := range b.Sessions {
		if s.ParentID != "" && known[s.ParentID] {
			children = append(children, s)
		} else {
			main.Sessions = append(main.Sessions, s)
		}
	}
	if len(main.Sessions) == 0 {
		main.Sessions = b.Sessions
		children = nil
	}
	r, err := render(&main, o, "images", "")
	if err != nil {
		return r, err
	}
	if len(children) == 0 {
		return r, nil
	}
	paths := map[string]string{}
	for i, s := range children {
		paths[s.ID] = fmt.Sprintf("subagents/%08d.md", i+1)
	}
	var index strings.Builder
	index.WriteString("## Subagent conversations\n\nRead these when their investigation or decisions matter to the next step. The main conversation below retains the original coordination messages.\n\n")
	for _, s := range children {
		fmt.Fprintf(&index, "- [%s](%s) — session %s; parent %s\n", escape(first(s.Title, "Subagent "+s.ID)), paths[s.ID], escape(s.ID), escape(s.ParentID))
	}
	index.WriteByte('\n')
	cut := bytes.Index(r.md, []byte("\n\n"))
	if cut < 0 {
		cut = 0
	} else {
		cut += 2
	}
	r.md = append(append(append([]byte{}, r.md[:cut]...), []byte(index.String())...), r.md[cut:]...)
	for _, s := range children {
		child := *b
		child.Sessions = []Session{s}
		child.Warnings = nil
		child.Changes = nil
		part, err := render(&child, o, "images", "../")
		if err != nil {
			return r, err
		}
		parentLink := "../conversation.md"
		if p := paths[s.ParentID]; p != "" {
			parentLink = path.Base(p)
		}
		part.md = append([]byte(fmt.Sprintf("[Main conversation](../conversation.md) · [Parent session %s](%s)\n\n", escape(s.ParentID), parentLink)), part.md...)
		r.files[paths[s.ID]] = part.md
		for name, data := range part.files {
			r.files[name] = data
		}
		for id, name := range part.assetFiles {
			r.assetFiles[id] = name
		}
	}
	return r, nil
}
