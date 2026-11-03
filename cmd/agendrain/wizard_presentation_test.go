package main

import (
	"strings"
	"testing"

	"github.com/unxed/agendrain/internal/drain"
)

func TestWizardRowsDistinguishSameTitlesForEverySource(t *testing.T) {
	for _, source := range []string{"codex", "claude", "claude-desktop", "opencode", "cursor", "cursor-agent", "vtvibe"} {
		t.Run(source, func(t *testing.T) {
			makeSession := func(id, when string) drain.Session {
				return drain.Session{ID: id, Source: source, Title: "Same title", CWD: "/work/runcity", UpdatedAt: when, Events: []drain.Event{{Role: "user", Blocks: []drain.Block{{Type: "text", Text: "Original requirement"}}}}}
			}
			b := &drain.Bundle{Sessions: []drain.Session{makeSession("11111111-aaaa", "2026-11-03T10:00:00Z"), makeSession("22222222-bbbb", "2026-11-03T11:00:00Z"), makeSession("33333333-cccc", "2026-11-03T11:00:00Z")}}
			choices := wizardSessionChoices(b)
			if len(choices) != 3 {
				t.Fatal(choices)
			}
			seen := map[string]bool{}
			for _, c := range choices {
				if seen[c.Title] {
					t.Fatal("indistinguishable rows", c.Title)
				}
				seen[c.Title] = true
				for _, want := range []string{"2026-11-03", "runcity", "Same title"} {
					if !strings.Contains(c.Title, want) {
						t.Fatal("missing row context", want, c.Title)
					}
				}
			}
		})
	}
}

func TestWizardHidesEmptyNativeRecordsAndSubagents(t *testing.T) {
	b := &drain.Bundle{Sessions: []drain.Session{
		{ID: "empty", Source: "cursor", Events: []drain.Event{{Role: "system", Kind: "context", Blocks: []drain.Block{{Type: "context", Text: "{}"}}}}},
		{ID: "real", Source: "cursor", Events: []drain.Event{{Role: "user", Blocks: []drain.Block{{Type: "text", Text: "Real request"}}}}},
		{ID: "child", ParentID: "real", Source: "cursor", Title: "Child"},
	}}
	c := wizardSessionChoices(b)
	if len(c) != 1 || c[0].ID != "real" || !strings.Contains(c[0].Title, "Real request") {
		t.Fatal("empty records or children offered as conversations", c)
	}
}
