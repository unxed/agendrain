package drain

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func TestImagesOmitRemovesEmbeddedImagePayloads(t *testing.T) {
	png := append([]byte{137, 80, 78, 71, 13, 10, 26, 10}, bytes.Repeat([]byte{7}, 300)...)
	pdf := bytes.Repeat([]byte{9, 1}, 300)
	pngB64 := base64.StdEncoding.EncodeToString(png)
	pdfB64 := base64.StdEncoding.EncodeToString(pdf)
	for _, mode := range []string{"omit", "describe"} {
		b := &Bundle{Version: 1, Assets: map[string][]byte{}, Sessions: []Session{{ID: "s", Events: []Event{
			{Role: "system", Blocks: []Block{{Type: "context", Text: jsonText(map[string]any{
				"content": []any{
					map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": pngB64}},
					map[string]any{"type": "document", "source": map[string]any{"type": "base64", "media_type": "application/pdf", "data": pdfB64}},
				},
				"url": "data:image/png;base64," + pngB64,
			})}}},
		}}}}
		if err := Prepare(b, Options{Images: mode}); err != nil {
			t.Fatal(err)
		}
		text := b.Sessions[0].Events[0].Blocks[0].Text
		if strings.Contains(text, pngB64[:40]) {
			t.Fatalf("%s: image base64 remains: %s", mode, text)
		}
		if !strings.Contains(text, pdfB64) {
			t.Fatalf("%s: non-image base64 was touched", mode)
		}
		if strings.Count(text, `"$agendrain_omitted"`) != 2 || !strings.Contains(text, `"mime":"image/png"`) || !strings.Contains(text, `"bytes":308`) {
			t.Fatalf("%s: marker missing: %s", mode, text)
		}
		n := 0
		for _, c := range b.Changes {
			if strings.Contains(c, "2 base64 image payloads") {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("%s: changes %v", mode, b.Changes)
		}
	}
	b := &Bundle{Version: 1, Sessions: []Session{{ID: "s", Events: []Event{{Role: "system", Blocks: []Block{{Type: "context", Text: jsonText(map[string]any{"url": "data:image/png;base64," + pngB64})}}}}}}}
	if err := Prepare(b, Options{}); err != nil || !strings.Contains(b.Sessions[0].Events[0].Blocks[0].Text, pngB64) {
		t.Fatal("default must keep images")
	}
}
