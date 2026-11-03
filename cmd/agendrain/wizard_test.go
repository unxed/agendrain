package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/unxed/agendrain/internal/drain"
)

type fakeWizardUI struct {
	picks       []string
	checked     []map[string]bool
	destination string
	root        string
	cancel      bool
	choices     [][]wizardChoice
	options     [][]wizardOption
	messages    []string
}

func (f *fakeWizardUI) Select(title, help string, choices []wizardChoice) (int, error) {
	f.choices = append(f.choices, append([]wizardChoice(nil), choices...))
	if f.cancel {
		return 0, errWizardCanceled
	}
	if len(f.picks) == 0 {
		return 0, errors.New("unexpected selection: " + title)
	}
	id := f.picks[0]
	f.picks = f.picks[1:]
	for i, choice := range choices {
		if choice.ID == id {
			return i, nil
		}
	}
	return 0, errors.New("choice not offered: " + id)
}

func (f *fakeWizardUI) Options(title string, options []wizardOption) ([]wizardOption, error) {
	f.options = append(f.options, append([]wizardOption(nil), options...))
	selected := map[string]bool{}
	if len(f.checked) > 0 {
		selected = f.checked[0]
		f.checked = f.checked[1:]
	}
	result := append([]wizardOption(nil), options...)
	for i := range result {
		result[i].Checked = selected[result[i].ID]
	}
	return result, nil
}

func (f *fakeWizardUI) Input(title, label, initial string) (string, error) {
	if label == "Разрешённая папка" {
		return f.root, nil
	}
	return f.destination, nil
}

func (f *fakeWizardUI) Message(title, text string) error {
	f.messages = append(f.messages, title+": "+text)
	return nil
}

func wizardTestHome(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	for name, value := range map[string]string{
		"HOME": d, "USERPROFILE": d,
		"APPDATA": filepath.Join(d, "config"), "XDG_CONFIG_HOME": filepath.Join(d, "config"),
		"XDG_DATA_HOME": filepath.Join(d, "data"), "CODEX_HOME": filepath.Join(d, "codex"),
		"CLAUDE_CONFIG_DIR": filepath.Join(d, "claude"), "OPENCODE_DB": filepath.Join(d, "missing.db"),
	} {
		t.Setenv(name, value)
	}
	return d
}

func wizardCodexFixture(t *testing.T, d string) {
	t.Helper()
	cliPut(t, filepath.Join(d, "codex", "sessions", "p.jsonl"), `{"type":"session_meta","payload":{"id":"p","cwd":"/work/my-project","title":"Repair export"}}`+"\n"+`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Keep exact filenames"},{"type":"input_image","image_url":"data:image/png;base64,iVBORw=="}]}}`+"\n")
	cliPut(t, filepath.Join(d, "codex", "archived_sessions", "child.jsonl"), `{"type":"session_meta","payload":{"id":"child","parent_thread_id":"p"}}`+"\n")
}

func TestWizardDiscoversOnlyExistingEnvironmentsAndExportsChildren(t *testing.T) {
	d := wizardTestHome(t)
	wizardCodexFixture(t, d)
	ui := &fakeWizardUI{picks: []string{"codex", "p", "zip"}, destination: filepath.Join(d, "handoff.zip")}
	var out bytes.Buffer
	if err := runWizard(ui, &out); err != nil {
		t.Fatal(err)
	}
	if len(ui.choices[0]) != 1 || ui.choices[0][0].ID != "codex" || len(ui.choices[1]) != 1 {
		t.Fatal("offered missing environments or subagent rows", ui.choices)
	}
	if !strings.Contains(ui.choices[1][0].Title, "Repair export") || !strings.Contains(ui.choices[1][0].Detail, "my-project") {
		t.Fatal("session picker lacks human labels", ui.choices[1])
	}
	for _, option := range ui.options[0] {
		if option.Checked {
			t.Fatal("export modifications must be opt in")
		}
		if option.ID == "base64" {
			t.Fatal("ZIP should not offer redundant base64 conversion")
		}
	}
	b, err := drain.Load("agendrain", ui.destination)
	if err != nil || len(b.Sessions) != 2 || !strings.Contains(out.String(), "Saved") {
		t.Fatalf("missing exported family or success output: %v %s", err, out.String())
	}
}

func TestWizardCodexOrdersByMetadataTimeRatherThanID(t *testing.T) {
	d := wizardTestHome(t)
	cliPut(t, filepath.Join(d, "codex", "sessions", "old.jsonl"), `{"type":"session_meta","payload":{"id":"zz-old","timestamp":"2026-11-02T00:00:00Z","cwd":"/work/Old"}}`+"\n")
	cliPut(t, filepath.Join(d, "codex", "sessions", "new.jsonl"), `{"type":"session_meta","payload":{"id":"aa-new","timestamp":"2026-11-03T00:00:00Z","cwd":"/work/Runcity"}}`+"\n")
	ui := &fakeWizardUI{picks: []string{"codex", "aa-new", "zip"}, destination: filepath.Join(d, "new.zip")}
	if err := runWizard(ui, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if ui.choices[1][0].ID != "aa-new" || !strings.Contains(ui.choices[1][0].Detail, "2026-11-03") {
		t.Fatal(ui.choices[1])
	}
}

func TestWizardSortsSessionsAndUsesUserText(t *testing.T) {
	d := wizardTestHome(t)
	cliPut(t, filepath.Join(d, "claude", "projects", "project", "old.jsonl"), `{"type":"user","sessionId":"old","cwd":"/work/project","timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","content":"Old task"}}`+"\n")
	cliPut(t, filepath.Join(d, "claude", "projects", "project", "new.jsonl"), `{"type":"user","sessionId":"new","cwd":"/work/project","timestamp":"2026-11-03T12:00:00Z","message":{"role":"user","content":"Fix the newest failure"}}`+"\n")
	ui := &fakeWizardUI{picks: []string{"claude", "new", "zip"}, destination: filepath.Join(d, "new.zip")}
	if err := runWizard(ui, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if len(ui.choices[0]) != 1 || ui.choices[1][0].ID != "new" || !strings.Contains(ui.choices[1][0].Title, "Fix the newest failure") || !strings.Contains(ui.choices[1][0].Detail, "2026-11-03") {
		t.Fatal("sessions not human-readable/recent-first", ui.choices)
	}
}

func TestWizardMarkdownMediaModes(t *testing.T) {
	for _, format := range []string{"md-images", "md-image-zip"} {
		t.Run(format, func(t *testing.T) {
			d := wizardTestHome(t)
			wizardCodexFixture(t, d)
			ui := &fakeWizardUI{picks: []string{"codex", "p", format}, destination: filepath.Join(d, "handoff.md")}
			if err := runWizard(ui, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			asset := filepath.Join(d, "handoff_images", "00000001.png")
			if format == "md-image-zip" {
				asset = filepath.Join(d, "handoff_images.zip")
			}
			if _, err := os.Stat(asset); err != nil {
				t.Fatal("missing numbered image output", err)
			}
		})
	}
}

func TestWizardRejectsConflictingFileModesAndReadsExplicitRoot(t *testing.T) {
	d := wizardTestHome(t)
	file := filepath.Join(d, "project", "source.txt")
	cliPut(t, file, "EXPLICITLY INCLUDED FILE")
	// An explicit structured reference is portable across platform separators.
	cliPut(t, filepath.Join(d, "claude", "projects", "project", "p.jsonl"), `{"type":"user","sessionId":"p","message":{"role":"user","content":[{"type":"file","path":`+strconv.Quote(file)+`}]}}`+"\n")
	ui := &fakeWizardUI{picks: []string{"claude", "p", "zip"}, checked: []map[string]bool{{"files-summary": true, "files-full": true}, {"files-full": true}}, root: filepath.Dir(file), destination: filepath.Join(d, "full.zip")}
	if err := runWizard(ui, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	b, err := drain.Load("agendrain", ui.destination)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(b)
	if len(ui.options) != 2 || !strings.Contains(string(raw), "EXPLICITLY INCLUDED FILE") {
		t.Fatal("file mode conflict or explicit inclusion failed")
	}
}

func TestWizardCancellationAndNoSources(t *testing.T) {
	d := wizardTestHome(t)
	ui := &fakeWizardUI{}
	if err := runWizard(ui, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "--input") || len(ui.choices) != 0 {
		t.Fatalf("no-source result is not actionable: %v", err)
	}
	wizardCodexFixture(t, d)
	ui = &fakeWizardUI{cancel: true}
	if err := runWizard(ui, &bytes.Buffer{}); !errors.Is(err, errWizardCanceled) || len(ui.messages) != 0 {
		t.Fatalf("cancellation was noisy: %v %v", err, ui.messages)
	}
}

func TestWizardNeverOverwritesExistingDestination(t *testing.T) {
	d := wizardTestHome(t)
	wizardCodexFixture(t, d)
	destination := filepath.Join(d, "keep.zip")
	cliPut(t, destination, "KEEP EXISTING OUTPUT")
	ui := &fakeWizardUI{picks: []string{"codex", "p", "zip"}, destination: destination}
	if err := runWizard(ui, &bytes.Buffer{}); err == nil {
		t.Fatal("accepted existing destination")
	}
	data, err := os.ReadFile(destination)
	if err != nil || string(data) != "KEEP EXISTING OUTPUT" {
		t.Fatal("existing destination changed", err)
	}
}

func TestWizardAppliesModelAndImageOptions(t *testing.T) {
	d := wizardTestHome(t)
	wizardCodexFixture(t, d)
	cliPut(t, filepath.Join(d, "codex", "sessions", "p.jsonl"), `{"type":"session_meta","payload":{"id":"p","model":"bespoke-v12"}}`+"\n"+`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"bespoke-v12 must keep exact filenames"},{"type":"input_image","image_url":"data:image/png;base64,iVBORw=="}]}}`+"\n")
	ui := &fakeWizardUI{picks: []string{"codex", "p", "zip"}, checked: []map[string]bool{{"strip-models": true, "images-omit": true, "compact": true}}, destination: filepath.Join(d, "clean.zip")}
	if err := runWizard(ui, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	b, err := drain.Load("agendrain", ui.destination)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(b)
	if len(b.Assets) != 0 || strings.Contains(string(raw), "bespoke-v12") || !strings.Contains(string(raw), "must keep exact filenames") {
		t.Fatal("wizard options were not applied")
	}
}
