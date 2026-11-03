package drain

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCursorAgentPublicLoadAndAutoDetection(t *testing.T) {
	f := newCAFixture(t)
	prompt := f.blob([]byte(`{"role":"user","content":"Keep this exact requirement"}`))
	f.meta(f.blob(caBytes(1, prompt)))
	for _, tc := range []struct{ source, input string }{
		{"cursor-agent", f.path},
		{"cursor-agent", filepath.Dir(f.path)},
		{"auto", f.path},
		{"auto", filepath.Dir(f.path)},
	} {
		t.Run(tc.source+"/"+filepath.Base(tc.input), func(t *testing.T) {
			b, err := Load(tc.source, tc.input)
			if err != nil {
				t.Fatal(err)
			}
			if len(b.Sessions) != 1 || b.Sessions[0].Source != "cursor-agent" || !strings.Contains(joined(b), "Keep this exact requirement") {
				t.Fatal("public loading lost the selected source or recorded requirement", joined(b))
			}
		})
	}
}
