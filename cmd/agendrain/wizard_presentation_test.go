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

func TestWizardKeepsImageOnlyCatalogConversations(t *testing.T) {
	b := &drain.Bundle{Sessions: []drain.Session{{ID: "p", Source: "claude", Events: []drain.Event{{Role: "user", Kind: "preview", Blocks: []drain.Block{{Type: "image", MIME: "image/png"}}}}}}}
	c := wizardSessionChoices(b)
	if len(c) != 1 || c[0].ID != "p" {
		t.Fatal("image-only conversation hidden", c)
	}
}

func TestWizardRowShowsRequestCountWhenKnown(t *testing.T) {
	t.Setenv("LC_ALL", "en_US.UTF-8")
	user := func(text string) drain.Event {
		return drain.Event{Role: "user", Blocks: []drain.Block{{Type: "text", Text: text}}}
	}
	asst := drain.Event{Role: "assistant", Blocks: []drain.Block{{Type: "text", Text: "ok"}}}
	b := &drain.Bundle{Sessions: []drain.Session{
		{ID: "full", Source: "opencode", Title: "Full", UpdatedAt: "2026-11-03T10:00:00Z", Events: []drain.Event{user("a"), asst, user("b"), asst, user("c")}},
		{ID: "prev", Source: "claude", Title: "Prev", UpdatedAt: "2026-11-03T09:00:00Z", Requests: 168, Events: []drain.Event{{Role: "user", Kind: "preview", Blocks: []drain.Block{{Type: "text", Text: "first"}}}}},
		{ID: "unk", Source: "claude", Title: "Unknown", UpdatedAt: "2026-11-03T08:00:00Z", Events: []drain.Event{{Role: "user", Kind: "preview", Blocks: []drain.Block{{Type: "text", Text: "first"}}}}},
	}}
	c := wizardSessionChoices(b)
	if len(c) != 3 || !strings.Contains(c[0].Title, " · 3 requests") || !strings.Contains(c[1].Title, " · 168 requests") || strings.Contains(c[2].Title, "requests") {
		t.Fatal(c)
	}
	if !strings.HasPrefix(c[1].Title, "2026-11-03 09:00 · 168 requests") {
		t.Fatal(c[1].Title)
	}
}

func TestWizardProjectLabel(t *testing.T) {
	cases := []struct{ home, cwd, want string }{
		{"/home/unxed", "/home/unxed", "~"},
		{"/home/unxed", "/home/unxed/4", "~/4"},
		{"/home/unxed", "/home/unxed/dev/5", "~/dev/5"},
		{"/home/unxed", "/home/unxed/dev/agendrain", "agendrain"},
		{"/home/unxed", "/work/runcity", "runcity"},
		{`C:\Users\Ann`, `c:\users\ann`, "~"},
		{`C:\Users\Ann`, `C:\Users\Ann\7`, "~/7"},
		{"/home/unxed", "", ""},
	}
	for _, c := range cases {
		if got := wizardProjectLabel(c.cwd, c.home); got != c.want {
			t.Fatalf("%q in %q: got %q want %q", c.cwd, c.home, got, c.want)
		}
	}
}

func TestWizardRussianRequestPlurals(t *testing.T) {
	t.Setenv("LC_ALL", "ru_RU.UTF-8")
	want := map[int]string{1: "1 запрос", 2: "2 запроса", 5: "5 запросов", 11: "11 запросов", 14: "14 запросов", 21: "21 запрос", 22: "22 запроса", 168: "168 запросов", 111: "111 запросов"}
	for n, w := range want {
		if got := wizardRequests(n); got != w {
			t.Fatal(n, got, w)
		}
	}
}

func TestWizardUntitledRowQuotesFirstRequest(t *testing.T) {
	t.Setenv("LC_ALL", "en_US.UTF-8")
	b := &drain.Bundle{Sessions: []drain.Session{{ID: "u", Source: "opencode", CWD: "/work/p", Events: []drain.Event{{Role: "user", Blocks: []drain.Block{{Type: "text", Text: "which model?"}}}}}}}
	c := wizardSessionChoices(b)
	if !strings.Contains(c[0].Title, "Untitled: “which model?”") || !strings.Contains(c[0].Detail, "First request:") || !strings.Contains(c[0].Detail, "\n") {
		t.Fatal(c)
	}
}
