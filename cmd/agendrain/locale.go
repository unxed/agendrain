package main

import (
	"os"
	"strings"
)

// russianLocale follows the standard message-locale precedence. An explicit
// non-Russian locale also overrides a Russian native Windows setting.
func russianLocale(getenv func(string) string, native func() bool) bool {
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		value := strings.TrimSpace(getenv(key))
		if value == "" {
			continue
		}
		if end := strings.IndexAny(value, "_-.@"); end >= 0 {
			value = value[:end]
		}
		return strings.EqualFold(value, "ru")
	}
	return native()
}

func uiText(ru, en string) string {
	if russianLocale(os.Getenv, nativeRussianLocale) {
		return ru
	}
	return en
}
