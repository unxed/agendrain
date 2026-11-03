package main

import "testing"

func TestRussianLocalePrecedence(t *testing.T) {
	tests := []struct {
		name, all, messages, lang string
		native, want              bool
	}{
		{name: "English fallback"},
		{name: "native Russian", native: true, want: true},
		{name: "Russian language", lang: "ru", want: true},
		{name: "Russian Unix locale", lang: "ru_RU.UTF-8", want: true},
		{name: "Russian BCP47", lang: "RU-ru", want: true},
		{name: "Russian language encoding", lang: "ru.UTF-8", want: true},
		{name: "non Russian", lang: "de_DE.UTF-8"},
		{name: "prefix is not language", lang: "russian"},
		{name: "messages overrides language", messages: "en_US", lang: "ru_RU"},
		{name: "all overrides messages", all: "C", messages: "ru", lang: "ru"},
		{name: "all Russian", all: "ru_RU", messages: "en_US", want: true},
		{name: "explicit English overrides native", lang: "en_US", native: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := map[string]string{"LC_ALL": tt.all, "LC_MESSAGES": tt.messages, "LANG": tt.lang}
			if got := russianLocale(func(key string) string { return env[key] }, func() bool { return tt.native }); got != tt.want {
				t.Fatalf("Russian = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUITextLocale(t *testing.T) {
	t.Setenv("LC_ALL", "en_US.UTF-8")
	if got := uiText("Русский", "English"); got != "English" {
		t.Fatal(got)
	}
	t.Setenv("LC_ALL", "ru_RU.UTF-8")
	if got := uiText("Русский", "English"); got != "Русский" {
		t.Fatal(got)
	}
}
