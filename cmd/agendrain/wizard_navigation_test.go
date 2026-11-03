package main

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

type navigationWizard struct {
	fakeWizardUI
	selects, optionCalls, inputCalls int
	backEnabled                      bool
	remembered                       []string
	t                                *testing.T
}

func (u *navigationWizard) SetBackEnabled(value bool)     { u.backEnabled = value }
func (u *navigationWizard) SetInitialSelection(id string) { u.remembered = append(u.remembered, id) }
func (u *navigationWizard) Select(title, help string, choices []wizardChoice) (int, error) {
	u.selects++
	// Back from sessions to environments, then from formats to sessions.
	if u.selects == 2 || u.selects == 5 {
		if !u.backEnabled {
			u.t.Fatal("Back disabled after first step")
		}
		return 0, errWizardBack
	}
	return u.fakeWizardUI.Select(title, help, choices)
}
func (u *navigationWizard) Options(title string, options []wizardOption) ([]wizardOption, error) {
	u.optionCalls++
	if u.optionCalls == 1 {
		options[0].Checked = true
		return options, errWizardBack
	}
	if !options[0].Checked {
		u.t.Fatal("Back lost checkbox state")
	}
	return options, nil
}
func (u *navigationWizard) Input(title, label, initial string) (string, error) {
	u.inputCalls++
	if u.inputCalls == 1 {
		return u.destination, errWizardBack
	}
	if initial != u.destination {
		u.t.Fatal("Back lost destination")
	}
	return u.destination, nil
}

func TestWizardBackRetainsStateAndBothLanguages(t *testing.T) {
	for _, locale := range []string{"ru_RU.UTF-8", "en_US.UTF-8"} {
		t.Run(locale, func(t *testing.T) {
			d := wizardTestHome(t)
			t.Setenv("LC_ALL", locale)
			wizardCodexFixture(t, d)
			u := &navigationWizard{t: t, fakeWizardUI: fakeWizardUI{picks: []string{"codex", "codex", "p", "p", "zip", "zip"}, destination: filepath.Join(d, "handoff.zip")}}
			if err := runWizard(u, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			if u.optionCalls != 3 || u.inputCalls != 2 {
				t.Fatal("unexpected Back navigation", u.optionCalls, u.inputCalls)
			}
			if !strings.Contains(strings.Join(u.remembered, ","), "codex") {
				t.Fatal("selected environment not restored")
			}
			if u.backEnabled {
				t.Fatal("Back enabled after saving")
			}
			last := u.messages[len(u.messages)-1]
			want := "Conversation saved"
			if locale == "ru_RU.UTF-8" {
				want = "Диалог сохранён"
			}
			if !strings.Contains(last, want) {
				t.Fatal(fmt.Sprintf("wrong interface language: %q", last))
			}
		})
	}
}
