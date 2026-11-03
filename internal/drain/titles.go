package drain

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// projectTitle uses the recorded working directory without opening that path.
// Slash normalization also handles histories copied from a different OS.
func projectTitle(cwd string) string {
	clean := strings.TrimRight(strings.ReplaceAll(strings.TrimSpace(cwd), "\\", "/"), "/")
	if clean == "" {
		return ""
	}
	base := path.Base(clean)
	if base == "." || base == ".." || strings.HasSuffix(base, ":") {
		return ""
	}
	return base
}

type claudeTitles struct{ custom, ai, summary string }

func (t *claudeTitles) observe(m obj) {
	switch str(m["type"]) {
	case "custom-title":
		if v := strings.TrimSpace(str(m["customTitle"])); v != "" {
			t.custom = v
		}
	case "ai-title":
		if v := strings.TrimSpace(str(m["aiTitle"])); v != "" {
			t.ai = v
		}
	case "summary":
		if v := strings.TrimSpace(str(m["summary"])); v != "" {
			t.summary = v
		}
	}
}
func (t *claudeTitles) merge(other claudeTitles) {
	if other.custom != "" {
		t.custom = other.custom
	}
	if other.ai != "" {
		t.ai = other.ai
	}
	if other.summary != "" {
		t.summary = other.summary
	}
}
func (t claudeTitles) title(cwd string) string {
	return first(t.custom, t.ai, t.summary, projectTitle(cwd))
}

// codexMetadataHome stays within the selected store. An explicitly copied
// transcript never causes a lookup in the operator's unrelated global home.
func codexMetadataHome(root string) string {
	for p := filepath.Clean(root); p != filepath.Dir(p); p = filepath.Dir(p) {
		if filepath.Base(p) == "sessions" || filepath.Base(p) == "archived_sessions" {
			return filepath.Dir(p)
		}
	}
	return root
}

func codexMetadataTitles(root string) (map[string]string, map[string]string) {
	home := codexMetadataHome(root)
	titles := map[string]string{}
	updated := map[string]string{}
	// Prefer the newest state schema; legacy snapshots must not override it.
	files, _ := filepath.Glob(filepath.Join(home, "state_*.sqlite"))
	version := func(p string) int {
		s := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(p), "state_"), ".sqlite")
		n, _ := strconv.Atoi(s)
		return n
	}
	sort.Slice(files, func(i, j int) bool { return version(files[i]) > version(files[j]) })
	if len(files) > 0 {
		// Read only the title columns, never previews or first_user_message.
		_ = snapshot(files[0], func(tx *sql.Tx) error {
			columns, err := records(tx, "PRAGMA table_info(threads)")
			if err != nil {
				return err
			}
			hasName, hasUpdated := false, false
			for _, c := range columns {
				if str(c["name"]) == "updated_at" {
					hasUpdated = true
				}
				if str(c["name"]) == "name" {
					hasName = true
				}
			}
			selectedColumns := "id,title"
			if hasName {
				selectedColumns += ",name"
			}
			if hasUpdated {
				selectedColumns += ",updated_at"
			}
			query := "SELECT " + selectedColumns + " FROM threads"
			rows, err := records(tx, query)
			if err != nil {
				return err
			}
			for _, r := range rows {
				if when := stamp(r["updated_at"]); when != "" {
					updated[str(r["id"])] = when
				}
				title := first(strings.TrimSpace(str(r["name"])), strings.TrimSpace(str(r["title"])))
				if title != "" {
					titles[str(r["id"])] = title
				}
			}
			return nil
		})
	}
	// The native index is append-only: the last valid nonempty name wins,
	// independent of timestamp ordering. Partial or damaged lines are optional
	// metadata and do not make otherwise-readable rollout headers unusable.
	f, err := os.Open(filepath.Join(home, "session_index.jsonl"))
	if err != nil {
		return titles, updated
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 64<<10), 1<<20)
	for scan.Scan() {
		var entry struct {
			ID      string `json:"id"`
			Name    string `json:"thread_name"`
			Updated string `json:"updated_at"`
		}
		if json.Unmarshal(scan.Bytes(), &entry) != nil {
			continue
		}
		if entry.ID != "" && entry.Updated != "" {
			updated[entry.ID] = entry.Updated
		}
		if title := strings.TrimSpace(entry.Name); entry.ID != "" && title != "" {
			titles[entry.ID] = title
		}
	}
	return titles, updated
}

func (b *Bundle) enrichCodexTitles(roots []string) {
	titles := map[string]string{}
	activity := map[string]string{}
	seen := map[string]bool{}
	for _, root := range roots {
		home := codexMetadataHome(root)
		if seen[home] {
			continue
		}
		seen[home] = true
		names, times := codexMetadataTitles(root)
		for id, when := range times {
			activity[id] = when
		}
		for id, title := range names {
			titles[id] = title
		}
	}
	for i := range b.Sessions {
		s := &b.Sessions[i]
		if s.Source == "codex" {
			s.Title = first(titles[s.ID], s.Title, projectTitle(s.CWD))
			s.UpdatedAt = first(activity[s.ID], s.UpdatedAt)
		}
	}
}
