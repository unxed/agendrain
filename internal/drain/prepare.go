package drain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

var modelPattern = regexp.MustCompile(`(?i)\b(?:gpt-[a-z0-9][a-z0-9._-]*|chatgpt-[a-z0-9._-]+|claude-(?:opus|sonnet|haiku)[a-z0-9._-]*|claude-[0-9][a-z0-9._-]*|(?:opus|sonnet|haiku)-[0-9][a-z0-9._-]*|gemini-[a-z0-9._-]+|(?:o1|o3|o4)(?:-[a-z0-9._-]+)?|(?:llama|qwen|deepseek|mistral|codestral)[-_0-9][a-z0-9._:-]*)\b`)
var displayModelPattern = regexp.MustCompile(`(?i)\b(?:claude\s+(?:(?:sonnet|opus|haiku)\s+)?[0-9][a-z0-9.]*(?:\s+(?:sonnet|opus|haiku))?|gpt\s+[0-9][a-z0-9.]*|gemini\s+[0-9][a-z0-9.]*(?:\s+(?:pro|flash|lite))?)\b`)
var mdLink = regexp.MustCompile(`\[[^\]]*\]\(([^)]+)\)`)
var codePath = regexp.MustCompile("`([^`\\n]+)`")
var absPath = regexp.MustCompile(`(?:^|[\s"'])(/[^\s"'<>;,]+\.[A-Za-z0-9]{1,10})`)

// Prepare mutates a newly loaded bundle. It never rewrites the source store.
func Prepare(b *Bundle, o Options) error {
	if o.Files != "" && o.Files != "none" && o.Files != "summary" && o.Files != "full" {
		return fmt.Errorf("--files must be none, summary, or full")
	}
	if o.Files != "" && o.Files != "none" && len(o.Roots) == 0 {
		return fmt.Errorf("--files %s requires at least one --allow-root", o.Files)
	}
	if o.Images != "" && o.Images != "keep" && o.Images != "describe" && o.Images != "omit" {
		return fmt.Errorf("--images must be keep, describe, or omit")
	}
	if b.Assets == nil {
		b.Assets = map[string][]byte{}
	}
	descriptionCache := map[string]string{}
	var before time.Time
	var err error
	if o.PruneToolsBefore != "" {
		before, err = time.Parse(time.RFC3339, o.PruneToolsBefore)
		if err != nil {
			return fmt.Errorf("--prune-tools-before: %w", err)
		}
	}
	for si := range b.Sessions {
		s := &b.Sessions[si]
		if o.Files != "" && o.Files != "none" {
			seen := map[string]bool{}
			for ei := range s.Events {
				e := &s.Events[ei]
				for bi := range e.Blocks {
					bl := &e.Blocks[bi]
					if bl.storedAsset == "" {
						continue
					}
					data, ok := b.Assets[bl.storedAsset]
					if !ok {
						return fmt.Errorf("missing recorded file snapshot")
					}
					bl.Description = "Recorded attachment snapshot"
					bl.MIME = http.DetectContentType(data)
					if o.Files == "summary" {
						if o.DescribeCommand != "" {
							bl.Text, err = describe(o, "file", bl.Path, bl.MIME, data)
							if err != nil {
								return err
							}
						} else {
							bl.Text = fileSummary(data)
						}
					} else if utf8.Valid(data) && !bytes.ContainsRune(data, 0) && !strings.HasPrefix(bl.MIME, "image/") {
						bl.Type = "file_content"
						bl.Text = string(data)
					} else {
						bl.Asset = bl.storedAsset
						if strings.HasPrefix(bl.MIME, "image/") {
							bl.Type = "image"
						}
					}
					bl.storedAsset = ""
				}
				var refs []string
				for _, bl := range e.Blocks {
					if bl.Path != "" {
						refs = append(refs, bl.Path)
					}
					refs = append(refs, fileReferences(bl.Text)...)
				}
				for _, p := range refs {
					if seen[p] {
						continue
					}
					seen[p] = true
					bl, data, e2 := readReference(p, s.CWD, o)
					if e2 != nil {
						b.warn("File reference " + p + ": " + e2.Error())
						continue
					}
					if bl == nil {
						continue
					}
					if bl.Asset != "" {
						b.Assets[bl.Asset] = data
					}
					if o.Files == "summary" && o.DescribeCommand != "" {
						summary, e2 := describe(o, "file", bl.Path, bl.MIME, data)
						if e2 != nil {
							return e2
						}
						bl.Text = summary
						bl.Description += "; external semantic description"
					}
					e.Blocks = append(e.Blocks, *bl)
				}
			}
		}
		for ei := range s.Events {
			e := &s.Events[ei]
			label := fmt.Sprintf("%s event %d", s.ID, ei+1)
			pruneOld := false
			if !before.IsZero() {
				when, e2 := time.Parse(time.RFC3339Nano, e.Time)
				pruneOld = e2 == nil && when.Before(before)
			}

			for bi := range e.Blocks {
				bl := &e.Blocks[bi]
				if pruneOld && (e.Role == "tool" || e.Kind == "tool_result" || bl.Type == "tool_result" || bl.Origin == "tool_result") {
					sum := sha256.Sum256([]byte(jsonText(*bl)))
					bl.Type = "omitted"
					bl.Text = fmt.Sprintf("Tool result explicitly pruned; sha256:%x", sum)
					bl.Asset = ""
					bl.Description = ""
					b.Changes = append(b.Changes, label+": old tool result pruned")
					continue
				}

				if bl.Type == "image" && o.Images != "" && o.Images != "keep" {
					desc := first(o.Descriptions[bl.Asset], o.Descriptions[bl.Path], bl.Description)
					if o.Images == "describe" && desc == "" && o.DescribeCommand != "" {
						if cached := descriptionCache[bl.Asset]; cached != "" {
							desc = cached
						} else {
							data, ok := b.Assets[bl.Asset]
							if !ok {
								return fmt.Errorf("image %s has no recorded bytes to describe", first(bl.Path, bl.Asset))
							}
							desc, err = describe(o, "image", bl.Path, bl.MIME, data)
							if err != nil {
								return err
							}
							descriptionCache[bl.Asset] = desc
						}
					}
					if o.Images == "describe" && desc == "" {
						return fmt.Errorf("image %s in %s has no description; supply --descriptions or --describe-command", first(bl.Path, bl.Asset), label)
					}
					if desc == "" {
						desc = "Image explicitly omitted"
					}
					bl.Type = "image_description"
					bl.Text = desc
					bl.Asset = ""
					b.Changes = append(b.Changes, label+": image replaced with text")
				}
			}
		}
	}
	if o.StripModels {
		if err := stripModels(b, o.ModelNames); err != nil {
			return err
		}
		b.Changes = append(b.Changes, "Model identities removed; source client names identify storage formats")
	}
	// Compaction runs last so references point at the final exported values.
	if o.Compact {
		compactBundle(b, o)
	}
	// Unreferenced assets (including replaced images and other sessions) do not
	// leave the machine and cannot bloat a compact export.
	used := map[string]bool{}
	for _, s := range b.Sessions {
		for _, e := range s.Events {
			for _, bl := range e.Blocks {
				if bl.Asset != "" {
					used[bl.Asset] = true
				}
			}
		}
	}
	for id := range b.Assets {
		if !used[id] {
			delete(b.Assets, id)
		}
	}
	return nil
}

func fileReferences(text string) []string {
	var out []string
	var visit func(any)
	visit = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, v := range x {
				if k == "file_path" || k == "path" || k == "filename" || k == "filePath" {
					if s := str(v); s != "" {
						out = append(out, s)
					}
				}
				visit(v)
			}
		case []any:
			for _, v := range x {
				visit(v)
			}
		case string:
			if strings.HasPrefix(x, "{") || strings.HasPrefix(x, "[") {
				if v, e := decode([]byte(x)); e == nil {
					visit(v)
				}
			}
		}
	}
	if v, e := decode([]byte(text)); e == nil {
		visit(v)
	}
	for _, m := range mdLink.FindAllStringSubmatch(text, -1) {
		out = append(out, strings.Trim(m[1], "<>"))
	}
	for _, m := range codePath.FindAllStringSubmatch(text, -1) {
		if !strings.ContainsAny(m[1], " \n\t") && filepath.Ext(m[1]) != "" {
			out = append(out, m[1])
		}
	}
	for _, m := range absPath.FindAllStringSubmatch(text, -1) {
		out = append(out, strings.TrimRight(m[1], ").]"))
	}
	return out
}
func readReference(p, cwd string, o Options) (*Block, []byte, error) {
	if strings.HasPrefix(p, "file://") {
		u, e := url.Parse(p)
		if e != nil {
			return nil, nil, e
		}
		p = u.Path
	} else if strings.Contains(p, "://") || strings.HasPrefix(p, "data:") {
		return nil, nil, nil
	}
	if !filepath.IsAbs(p) {
		if cwd == "" {
			return nil, nil, fmt.Errorf("relative path has no recorded working directory")
		}
		p = filepath.Join(cwd, p)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return nil, nil, err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, nil, err
	}
	allowed := false
	for _, root := range o.Roots {
		r, e := filepath.Abs(root)
		if e != nil {
			continue
		}
		r, e = filepath.EvalSymlinks(r)
		if e != nil {
			continue
		}
		rel, e := filepath.Rel(r, real)
		if e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, nil, fmt.Errorf("outside --allow-root (including symlink resolution)")
	}
	f, err := os.Open(real)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("not a regular file")
	}
	limit := o.MaxFileBytes
	if limit == 0 {
		limit = 8 << 20
	}
	if info.Size() > limit {
		return nil, nil, fmt.Errorf("exceeds --max-file-bytes (%d)", limit)
	}
	data, err := bounded(f, limit)
	if err != nil {
		return nil, nil, err
	}
	sum := sha256.Sum256(data)
	mime := http.DetectContentType(data)
	bl := &Block{Type: "file", Path: p, MIME: mime, Description: fmt.Sprintf("Current local file: %d bytes; sha256:%s; not a historical snapshot", len(data), hex.EncodeToString(sum[:]))}
	if o.Files == "summary" {
		bl.Text = fileSummary(data)
		return bl, data, nil
	}
	if utf8.Valid(data) && !bytes.ContainsRune(data, 0) && !strings.HasPrefix(mime, "image/") {
		bl.Type = "file_content"
		bl.Text = string(data)
	} else {
		if strings.HasPrefix(mime, "image/") {
			bl.Type = "image"
		}
		bl.Asset = hex.EncodeToString(sum[:])
	}
	return bl, data, nil
}
func fileSummary(data []byte) string {
	if !utf8.Valid(data) || bytes.ContainsRune(data, 0) {
		return "Binary file; structural text summary unavailable"
	}
	lines := strings.Split(string(data), "\n")
	var out []string
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "#") || strings.HasPrefix(t, "//") || strings.HasPrefix(t, "package ") || strings.HasPrefix(t, "func ") || strings.HasPrefix(t, "type ") || strings.HasPrefix(t, "class ") || strings.HasPrefix(t, "def ") || strings.HasPrefix(t, "export ") {
			if len(t) > 200 {
				t = t[:200]
			}
			out = append(out, t)
			if len(out) >= 20 {
				break
			}
		}
	}
	if len(out) == 0 {
		for _, l := range lines {
			l = strings.TrimSpace(l)
			if l != "" {
				if len(l) > 200 {
					l = l[:200]
				}
				out = append(out, l)
				if len(out) == 3 {
					break
				}
			}
		}
	}
	return fmt.Sprintf("Structural excerpt (%d lines in file; not a semantic summary):\n%s", len(lines), strings.Join(out, "\n"))
}

func stripModels(b *Bundle, extra []string) error {
	names := append(append([]string{}, b.redactedModels...), extra...)
	for _, s := range b.Sessions {
		names = append(names, s.Models...)
	}
	var collect func(any)
	collect = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, v := range x {
				if k == "model" || k == "modelID" || k == "model_id" || k == "modelName" || k == "model_name" {
					if s := str(v); s != "" {
						if strings.HasPrefix(s, "{") {
							if v, e := decode([]byte(s)); e == nil {
								collect(v)
							}
						} else {
							names = append(names, s)
						}
					}
				}
				collect(v)
			}
		case []any:
			for _, v := range x {
				collect(v)
			}
		case string:
			if strings.HasPrefix(x, "{") || strings.HasPrefix(x, "[") {
				if v, e := decode([]byte(x)); e == nil {
					collect(v)
				}
			}
		}
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	var v any
	json.Unmarshal(raw, &v)
	collect(v)
	// Context/tool blocks themselves can contain JSON documents as text.
	for _, s := range b.Sessions {
		for _, e := range s.Events {
			for _, bl := range e.Blocks {
				if x, e := decode([]byte(bl.Text)); e == nil {
					collect(x)
				}
			}
		}
	}
	b.redactedModels = names
	sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })
	replace := func(s string) string {
		for _, name := range names {
			if name != "" {
				re := regexp.MustCompile("(?i)" + regexp.QuoteMeta(name))
				s = re.ReplaceAllString(s, "[model]")
			}
		}
		return displayModelPattern.ReplaceAllString(modelPattern.ReplaceAllString(s, "[model]"), "[model]")
	}
	var transform func(any) any
	transform = func(v any) any {
		switch x := v.(type) {
		case string:
			return replace(x)
		case []any:
			for i, v := range x {
				x[i] = transform(v)
			}
			return x
		case map[string]any:
			out := map[string]any{}
			for k, v := range x {
				out[replace(k)] = transform(v)
			}
			return out
		}
		return v
	}
	v = transform(v)
	raw, err = json.Marshal(v)
	if err != nil {
		return err
	}
	assets := b.Assets
	if err = json.Unmarshal(raw, b); err != nil {
		return err
	}
	b.Assets = assets
	for i := range b.Sessions {
		b.Sessions[i].Models = nil
	}
	for id, p := range assets {
		if utf8.Valid(p) && !bytes.ContainsRune(p, 0) {
			assets[id] = []byte(replace(string(p)))
		}
	}
	return nil
}
