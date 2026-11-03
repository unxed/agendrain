package drain

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
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
	seen := map[string]int{}
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
			s := Session{ID: first(meta["id"], meta["session_id"], fallback), ParentID: parentThread(meta), Source: "codex", CWD: str(meta["cwd"]), Title: first(meta["title"], meta["name"]), UpdatedAt: stamp(meta["timestamp"])}
			if inputFile && filepath.Clean(path) == filepath.Clean(input) {
				target = s.ID
			}
			if i, exists := seen[s.ID]; exists {
				if err := mergeCodexSession(&b.Sessions[i], s); err != nil {
					return nil, err
				}
				continue
			}
			seen[s.ID] = len(b.Sessions)
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
	if source == "claude" || source == "claude-code" {
		return loadSelectedClaude(input, id)
	}
	if source == "claude-desktop" {
		info, err := os.Stat(input)
		_, official := os.Stat(filepath.Join(input, "conversations.json"))
		if err == nil && ((info.IsDir() && official != nil) || strings.HasSuffix(input, ".jsonl") || strings.HasSuffix(input, ".jsonl.zst")) {
			b, err := loadSelectedClaude(input, id)
			if err != nil {
				return nil, err
			}
			for i := range b.Sessions {
				b.Sessions[i].Source = "claude-desktop"
			}
			b.warn("Desktop Code/Cowork: importing recorded local agent logs; cloud-only conversations require an offline data export")
			return b, nil
		}
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
	// File names and active/archive traversal do not define segment order.
	// Sort whole segments by their first known timestamp, preserving undated
	// records in each segment. If a segment is wholly undated, retain discovery
	// order rather than assigning it an invented date.
	allAnchored := true
	for _, session := range b.Sessions {
		if codexFirstEventTime(session).IsZero() {
			allAnchored = false
			break
		}
	}
	if allAnchored {
		sort.SliceStable(b.Sessions, func(i, j int) bool {
			return codexFirstEventTime(b.Sessions[i]).Before(codexFirstEventTime(b.Sessions[j]))
		})
	}
	// A paginated thread can span multiple rollouts, including archived or
	// compressed copies. Keep the union rather than choosing one file.
	seen := map[string]int{}
	sessions := make([]Session, 0, len(b.Sessions))
	for _, session := range b.Sessions {
		if i, exists := seen[session.ID]; exists {
			if err := mergeCodexSession(&sessions[i], session); err != nil {
				return nil, err
			}
			continue
		}
		seen[session.ID] = len(sessions)
		sessions = append(sessions, session)
	}
	b.Sessions = sessions
	b.enrichCodexTitles(roots)
	if err := b.Select(id); err != nil {
		return nil, err
	}
	return b, nil
}

// mergeCodexSession merges segments and duplicate copies without collapsing
// intentionally repeated identical records within an individual rollout.
func mergeCodexSession(dst *Session, src Session) error {
	if reflect.DeepEqual(*dst, src) {
		return nil
	}
	if dst.ParentID != "" && src.ParentID != "" && dst.ParentID != src.ParentID {
		return fmt.Errorf("conflicting parents for Codex session %q", src.ID)
	}
	// Working directories can change when a thread resumes. The display field
	// holds one value, so preserve any additional segment directory as context.
	if dst.CWD != "" && src.CWD != "" && dst.CWD != src.CWD {
		codexKeepDirectoryContext(dst)
		codexKeepDirectoryContext(&src)
	}
	if dst.ParentID == "" {
		dst.ParentID = src.ParentID
	}
	dstActivity, srcActivity := codexSessionActivity(*dst), codexSessionActivity(src)
	if !srcActivity.IsZero() && (dstActivity.IsZero() || srcActivity.After(dstActivity)) {
		dst.UpdatedAt = srcActivity.UTC().Format(time.RFC3339Nano)
		if src.Title != "" {
			dst.Title = src.Title
		}
		if src.CWD != "" {
			dst.CWD = src.CWD
		}
	}
	if dst.Title == "" {
		dst.Title = src.Title
	}
	if dst.CWD == "" {
		dst.CWD = src.CWD
	}
	for _, model := range src.Models {
		found := false
		for _, old := range dst.Models {
			if old == model {
				found = true
				break
			}
		}
		if !found {
			dst.Models = append(dst.Models, model)
		}
	}
	counts := map[string]int{}
	for _, event := range dst.Events {
		counts[jsonText(event)]++
	}
	incoming := map[string]int{}
	for _, event := range src.Events {
		key := jsonText(event)
		incoming[key]++
		if incoming[key] > counts[key] || (event.ID == "" && event.Time == "") {
			dst.Events = append(dst.Events, event)
		}
	}
	allDated := true
	for _, event := range dst.Events {
		if codexRecordTime(event.Time).IsZero() {
			allDated = false
			break
		}
	}
	if allDated {
		sort.SliceStable(dst.Events, func(i, j int) bool {
			return codexRecordTime(dst.Events[i].Time).Before(codexRecordTime(dst.Events[j].Time))
		})
	}
	return nil
}

func codexRecordTime(value string) time.Time {
	when, _ := time.Parse(time.RFC3339Nano, value)
	return when
}

func codexFirstEventTime(s Session) time.Time {
	for _, e := range s.Events {
		if when := codexRecordTime(e.Time); !when.IsZero() {
			return when
		}
	}
	return time.Time{}
}

func codexSessionActivity(s Session) time.Time {
	latest := codexRecordTime(s.UpdatedAt)
	for _, e := range s.Events {
		if when := codexRecordTime(e.Time); when.After(latest) {
			latest = when
		}
	}
	return latest
}

func codexKeepDirectoryContext(s *Session) {
	if len(s.Events) == 0 {
		return
	}
	e := Event{Role: "system", Kind: "session_context", Time: s.Events[0].Time, Blocks: []Block{{Type: "context", Text: jsonText(obj{"cwd": s.CWD})}}}
	for _, old := range s.Events {
		if reflect.DeepEqual(old, e) {
			return
		}
	}
	s.Events = append([]Event{e}, s.Events...)
}
