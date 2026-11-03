package drain

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

var stopCodexHeader = errors.New("Codex metadata read")

// codexHeader reads just the first nonempty record, not the conversation body.
// Codex writes session_meta first in both CLI and Desktop rollout files.
func codexHeader(path string) (obj, error) {
	var header obj
	err := lines(path, func(m obj) error {
		if str(m["type"]) == "session_meta" {
			header = object(m["payload"])
		}
		return stopCodexHeader
	})
	if err != nil && !errors.Is(err, stopCodexHeader) {
		return nil, err
	}
	if len(header) == 0 {
		return nil, fmt.Errorf("%s has no initial session_meta record", path)
	}
	return header, nil
}

func codexRoots(input string) ([]string, bool, error) {
	var roots []string
	if input == "" {
		for _, path := range Discover("codex") {
			if _, err := os.Stat(path); os.IsNotExist(err) {
				continue
			} else if err != nil {
				return nil, false, err
			}
			roots = append(roots, path)
		}
		return roots, false, nil
	}
	info, err := os.Stat(input)
	if err != nil {
		return nil, false, err
	}
	root := input
	if !info.IsDir() {
		root = filepath.Dir(input)
		for path := root; path != filepath.Dir(path); path = filepath.Dir(path) {
			if filepath.Base(path) == "sessions" || filepath.Base(path) == "archived_sessions" {
				root = path
				break
			}
		}
	}
	return []string{root}, !info.IsDir(), nil
}

// CatalogCodex lists recorded session metadata without reading conversation
// bodies. Events remain nil because their count is unknown until import.
func CatalogCodex(input string) (*Bundle, error) {
	roots, inputFile, err := codexRoots(input)
	if err != nil {
		return nil, err
	}
	b := &Bundle{Version: Version, Assets: map[string][]byte{}}
	seen := map[string]Session{}
	target := ""
	for _, root := range roots {
		paths, err := logPaths(root)
		if err != nil {
			return nil, err
		}
		for _, path := range paths {
			meta, err := codexHeader(path)
			if err != nil {
				b.warn("Codex rollout could not be indexed: " + err.Error())
				continue
			}
			fallback := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(path), ".zst"), ".jsonl")
			s := Session{ID: first(meta["id"], meta["session_id"], fallback), ParentID: parentThread(meta), Source: "codex", CWD: str(meta["cwd"]), Title: first(meta["title"], meta["name"])}
			if inputFile && filepath.Clean(path) == filepath.Clean(input) {
				target = s.ID
			}
			if old, exists := seen[s.ID]; exists {
				if !reflect.DeepEqual(old, s) {
					return nil, fmt.Errorf("conflicting session metadata %q in Codex stores; select an explicit --input", s.ID)
				}
				b.warn("Codex session " + s.ID + " appears in multiple rollouts; body equality is checked during export")
				continue
			}
			seen[s.ID] = s
			b.Sessions = append(b.Sessions, s)
		}
	}
	if len(b.Sessions) == 0 {
		return nil, fmt.Errorf("no usable Codex session metadata found: %s", strings.Join(b.Warnings, "; "))
	}
	b.enrichCodexTitles(roots)
	if inputFile {
		if target == "" {
			return nil, fmt.Errorf("selected Codex transcript has no usable metadata")
		}
		if err := b.Select(target); err != nil {
			return nil, err
		}
	}
	return b, nil
}

// LoadSelected imports a session and its descendants. For Codex it indexes only
// rollout headers, then opens bodies belonging to the selected family. An empty
// Codex input searches both active and archived native stores. Explicit inputs
// retain their normal store boundary; an explicit transcript searches siblings
// within its enclosing sessions or archived_sessions directory.
func LoadSelected(source, input, id string) (*Bundle, error) {
	if id == "" {
		return Load(source, input)
	}
	if source == "" || source == "auto" {
		// Auto-detection normally examines the entire JSONL. Recognize a Codex
		// header first so selection does not accidentally decode unrelated data.
		if info, err := os.Stat(input); err == nil && !info.IsDir() && (strings.HasSuffix(input, ".jsonl") || strings.HasSuffix(input, ".jsonl.zst")) {
			if _, err := codexHeader(input); err == nil {
				source = "codex"
			}
		}
	}
	if source != "codex" {
		b, err := Load(source, input)
		if err != nil {
			return nil, err
		}
		if err := b.Select(id); err != nil {
			return nil, err
		}
		return b, nil
	}
	roots, inputFile, err := codexRoots(input)
	if err != nil {
		return nil, err
	}
	type entry struct{ path, id, parent string }
	var index []entry
	b := &Bundle{Version: Version, Assets: map[string][]byte{}}
	seenPaths := map[string]bool{}
	for _, root := range roots {
		paths, err := logPaths(root)
		if err != nil {
			return nil, err
		}
		for _, path := range paths {
			if seenPaths[path] {
				continue
			}
			seenPaths[path] = true
			meta, err := codexHeader(path)
			if err != nil {
				b.warn("Codex rollout could not be indexed: " + err.Error())
				continue
			}
			fallback := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(path), ".zst"), ".jsonl")
			index = append(index, entry{path, first(meta["id"], meta["session_id"], fallback), parentThread(meta)})
		}
	}
	if len(index) == 0 {
		return nil, fmt.Errorf("no usable Codex session metadata found: %s", strings.Join(b.Warnings, "; "))
	}
	if inputFile {
		// An explicit transcript retains its normal family boundary even when
		// another requested ID happens to exist in a neighboring rollout.
		scope := map[string]bool{}
		for _, item := range index {
			if filepath.Clean(item.path) == filepath.Clean(input) {
				scope[item.id] = true
			}
		}
		for changed := true; changed; {
			changed = false
			for _, item := range index {
				if scope[item.parent] && !scope[item.id] {
					scope[item.id] = true
					changed = true
				}
			}
		}
		if !scope[id] {
			return nil, fmt.Errorf("session %q is outside the selected Codex transcript family", id)
		}
	}
	selected := map[string]bool{id: true}
	found := false
	for _, item := range index {
		if item.id == id {
			found = true
		}
	}
	if !found {
		return nil, fmt.Errorf("session %q not found in Codex metadata index", id)
	}
	for changed := true; changed; {
		changed = false
		for _, item := range index {
			if selected[item.parent] && !selected[item.id] {
				selected[item.id] = true
				changed = true
			}
		}
	}
	var paths []string
	for _, item := range index {
		if selected[item.id] {
			paths = append(paths, item.path)
		}
	}
	if err := b.codexFiles(paths, "", false); err != nil {
		return nil, err
	}
	// Archival or migration can leave exact copies in both native stores. A
	// different history under the same identity requires an explicit input.
	seen := map[string]Session{}
	sessions := b.Sessions[:0]
	for _, session := range b.Sessions {
		if old, exists := seen[session.ID]; exists {
			if !reflect.DeepEqual(old, session) {
				return nil, fmt.Errorf("conflicting session %q in Codex stores; select an explicit --input", session.ID)
			}
			continue
		}
		seen[session.ID] = session
		sessions = append(sessions, session)
	}
	b.Sessions = sessions
	if err := b.Select(id); err != nil {
		return nil, err
	}
	return b, nil
}
