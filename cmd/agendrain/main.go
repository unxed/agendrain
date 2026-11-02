package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/unxed/agendrain/internal/drain"
)

var version = "dev"

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ", ") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

const help = `agendrain — portable recorded context for another AI agent

Usage:
  agendrain sources
  agendrain list   --source CLIENT [--input PATH]
  agendrain export --source CLIENT [--input PATH] --session ID --output handoff.zip

Clients: codex, claude (Claude Code), claude-desktop, opencode, cursor,
         vtvibe, agendrain (previous portable export), auto

Export defaults to a compressed ZIP with conversation.md, manifest.json and
recorded attachments. Referenced local files are never read by default.
Use 'agendrain export --help' for all options. No network requests are made
unless you explicitly provide an external --rewrite-command.
`

func run(args []string, out, stderr io.Writer) int {
	fail := func(err error) int { fmt.Fprintln(stderr, "agendrain:", err); return 1 }
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(out, help)
		return 0
	}
	if args[0] == "version" || args[0] == "--version" {
		fmt.Fprintln(out, "agendrain", version)
		return 0
	}
	if args[0] == "sources" {
		for _, source := range []string{"codex", "claude", "claude-desktop", "opencode", "cursor", "vtvibe"} {
			fmt.Fprintln(out, source+":")
			for _, p := range drain.Discover(source) {
				mark := "not found"
				if _, err := os.Stat(p); err == nil {
					mark = "found"
				}
				fmt.Fprintf(out, "  [%s] %s\n", mark, p)
			}
		}
		fmt.Fprintln(out, "claude-desktop cloud chats: Settings > Privacy > Export data; import conversations.json or export ZIP.")
		fmt.Fprintln(out, "vtvibe: copy chat/ and session.md from ai:// with F5 to a local directory; optionally copy ctx/.")
		return 0
	}
	command := args[0]
	if command != "list" && command != "export" {
		return fail(fmt.Errorf("unknown command %q", command))
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(stderr)
	source := fs.String("source", "auto", "source client (see agendrain --help)")
	input := fs.String("input", "", "native store, transcript, or exported JSON/ZIP")
	id := fs.String("session", "", "session ID; includes descendants")
	output := fs.String("output", "", "destination path (must not exist)")
	all := fs.Bool("all", false, "export all sessions in a multi-session store")
	strict := fs.Bool("strict", false, "fail if the source has any completeness warnings")
	jsonList := fs.Bool("json", false, "list sessions as JSON")
	o := drain.Options{}
	fs.StringVar(&o.Format, "format", "zip", "zip, md (plus image directory), or md-zip (Markdown and numbered images only)")
	fs.BoolVar(&o.StripModels, "strip-models", false, "remove recorded/recognized model identities from text and metadata")
	var models, roots, histories stringList
	fs.Var(&models, "model-name", "additional model identity to remove; repeatable")
	fs.Var(&roots, "allow-root", "allow reading referenced files under this directory; repeatable")
	fs.Var(&histories, "history", "include sessions from a previous agendrain ZIP; repeatable")
	fs.StringVar(&o.Files, "files", "none", "referenced local files: none, summary (structural excerpts), full")
	fs.Int64Var(&o.MaxFileBytes, "max-file-bytes", 8<<20, "maximum referenced file size")
	fs.BoolVar(&o.Compact, "compact", false, "replace exact repeated system/tool blocks with references")
	fs.StringVar(&o.Images, "images", "keep", "keep, describe (requires descriptions), or omit")
	descriptions := fs.String("descriptions", "", "JSON object mapping image asset ID or original path to description")
	fs.BoolVar(&o.Base64, "base64", false, "include non-image attachment bytes as base64 in Markdown modes")
	fs.StringVar(&o.PruneToolsBefore, "prune-tools-before", "", "explicitly omit old tool results before RFC3339 time; preserves decisions")
	fs.StringVar(&o.RewriteCommand, "rewrite-command", "", "optional executable: read Markdown on stdin, emit a lossy rewrite on stdout (no shell)")
	if err := fs.Parse(args[1:]); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		return fail(fmt.Errorf("unexpected positional arguments: %s", strings.Join(fs.Args(), " ")))
	}
	if o.MaxFileBytes <= 0 {
		return fail(fmt.Errorf("--max-file-bytes must be positive"))
	}
	if command == "export" && *output == "" {
		return fail(fmt.Errorf("--output is required"))
	}
	if *input == "" {
		for _, p := range drain.Discover(*source) {
			if _, err := os.Stat(p); err == nil {
				*input = p
				break
			}
		}
	}
	b, err := drain.Load(*source, *input)
	if err != nil {
		return fail(err)
	}
	if err = b.Select(*id); err != nil {
		return fail(err)
	}
	if command == "list" {
		if *jsonList {
			type row struct {
				ID     string `json:"id"`
				Parent string `json:"parent_id,omitempty"`
				Source string `json:"source"`
				Title  string `json:"title,omitempty"`
				Events int    `json:"events"`
			}
			rows := make([]row, 0, len(b.Sessions))
			for _, s := range b.Sessions {
				rows = append(rows, row{s.ID, s.ParentID, s.Source, s.Title, len(s.Events)})
			}
			if err = json.NewEncoder(out).Encode(rows); err != nil {
				return fail(err)
			}
		} else {
			for _, s := range b.Sessions {
				fmt.Fprintf(out, "%s\t%s\t%d events\t%s\tparent=%s\n", s.ID, s.Source, len(s.Events), s.Title, s.ParentID)
			}
		}
		for _, w := range b.Warnings {
			fmt.Fprintln(stderr, "warning:", w)
		}
		return 0
	}
	if *id == "" && !*all {
		roots := 0
		known := map[string]bool{}
		for _, s := range b.Sessions {
			known[s.ID] = true
		}
		for _, s := range b.Sessions {
			if !known[s.ParentID] {
				roots++
			}
		}
		if roots > 1 {
			return fail(fmt.Errorf("store contains %d root sessions; select --session ID or explicitly use --all", roots))
		}
	}
	for _, p := range histories {
		history, e := drain.Load("agendrain", p)
		if e != nil {
			return fail(e)
		}
		known := map[string]bool{}
		for _, s := range b.Sessions {
			known[s.ID] = true
		}
		for _, s := range history.Sessions {
			if known[s.ID] {
				return fail(fmt.Errorf("history has duplicate session %q", s.ID))
			}
		}
		b.Sessions = append(history.Sessions, b.Sessions...)
		b.Warnings = append(history.Warnings, b.Warnings...)
		b.Changes = append(history.Changes, b.Changes...)
		for k, v := range history.Assets {
			b.Assets[k] = v
		}
	}
	o.Roots = roots
	o.ModelNames = models
	if *descriptions != "" {
		raw, e := os.ReadFile(*descriptions)
		if e != nil {
			return fail(e)
		}
		if e = json.Unmarshal(raw, &o.Descriptions); e != nil {
			return fail(e)
		}
	}
	if err = drain.Prepare(b, o); err != nil {
		return fail(err)
	}
	for _, w := range b.Warnings {
		fmt.Fprintln(stderr, "warning:", w)
	}
	if *strict && len(b.Warnings) > 0 {
		return fail(fmt.Errorf("--strict: %d completeness warnings", len(b.Warnings)))
	}
	if err = drain.Write(b, *output, o); err != nil {
		return fail(err)
	}
	info, err := os.Stat(*output)
	if err != nil {
		return fail(err)
	}
	fmt.Fprintf(out, "Saved %s (%d bytes, %d sessions)\n", *output, info.Size(), len(b.Sessions))
	return 0
}
