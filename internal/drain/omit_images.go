package drain

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// omitEmbeddedImages implements --images omit/describe for image bytes that
// live inside recorded JSON context rather than in image blocks: base64
// source objects ({"type":"base64","media_type":"image/...","data":...}) and
// data:image/...;base64 URL strings. Only image media types are touched.
// Each payload becomes {"$agendrain_omitted":{"bytes":N,"mime":M,"sha256":H}}.
func omitEmbeddedImages(b *Bundle) {
	n := 0
	for si := range b.Sessions {
		for ei := range b.Sessions[si].Events {
			for bi := range b.Sessions[si].Events[ei].Blocks {
				bl := &b.Sessions[si].Events[ei].Blocks[bi]
				if bl.Type != "context" || !strings.Contains(bl.Text, "base64") || !jsonContainer(bl.Text) {
					continue
				}
				var out []splice
				omitWalk(bl.Text, 0, &out)
				if len(out) > 0 {
					bl.Text = apply(bl.Text, out)
					n += len(out)
				}
			}
		}
	}
	if n > 0 {
		b.Changes = append(b.Changes, fmt.Sprintf("Images: %d base64 image payloads inside recorded JSON context replaced with {\"$agendrain_omitted\":{\"bytes\":N,\"mime\":M,\"sha256\":H}} markers", n))
	}
}

func omittedMarker(mime, encoded string) (string, bool) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		if raw, err = base64.RawStdEncoding.DecodeString(encoded); err != nil {
			return "", false
		}
	}
	sum := sha256.Sum256(raw)
	m, _ := json.Marshal(mime)
	return fmt.Sprintf(`{"$agendrain_omitted":{"bytes":%d,"mime":%s,"sha256":"%s"}}`, len(raw), m, hex.EncodeToString(sum[:])), true
}

func omitWalk(s string, i int, out *[]splice) {
	end := valueEnd(s, i)
	switch s[i] {
	case '"':
		if end-i < 40 || !strings.HasPrefix(s[i+1:], "data:image/") {
			return
		}
		var v string
		if json.Unmarshal([]byte(s[i:end]), &v) != nil {
			return
		}
		head, data, ok := strings.Cut(v, ",")
		if !ok || !strings.HasSuffix(head, ";base64") {
			return
		}
		mime := strings.TrimSuffix(strings.TrimPrefix(head, "data:"), ";base64")
		if mk, ok := omittedMarker(mime, data); ok {
			*out = append(*out, splice{i, end, mk})
		}
	case '{':
		members := children(s, i)
		var typ, mime string
		data := -1
		for n, m := range members {
			switch m.key {
			case "type":
				json.Unmarshal([]byte(s[m.start:m.end]), &typ)
			case "media_type", "mediaType", "mime_type":
				json.Unmarshal([]byte(s[m.start:m.end]), &mime)
			case "data":
				if s[m.start] == '"' {
					data = n
				}
			}
		}
		if typ == "base64" && data >= 0 && strings.HasPrefix(mime, "image/") {
			m := members[data]
			var enc string
			if json.Unmarshal([]byte(s[m.start:m.end]), &enc) == nil {
				if mk, ok := omittedMarker(mime, enc); ok {
					*out = append(*out, splice{m.start, m.end, mk})
				}
			}
		}
		for _, m := range members {
			omitWalk(s, m.start, out)
		}
	case '[':
		for _, m := range children(s, i) {
			omitWalk(s, m.start, out)
		}
	}
}
