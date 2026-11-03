package drain

import (
	"strings"
	"testing"
)

func TestCursorAgentToolDecodingKeepsLimitsAndIntegrity(t *testing.T) {
	// Malformed tool bytes are an integrity error, not a silent skip.
	f := newCAFixture(t)
	user := f.blob(caText(1, "x"))
	f.rootWithTurns(f.toolTurn(user, []byte{0xff}))
	if err := (&Bundle{Assets: map[string][]byte{}}).cursorAgent(f.path); err == nil {
		t.Fatal("malformed tool call accepted")
	}
	// google.protobuf.Value nesting is bounded.
	g := newCAFixture(t)
	user = g.blob(caText(1, "x"))
	value := caText(3, "leaf")
	for i := 0; i < 80; i++ {
		value = caBytes(6, caBytes(1, value))
	}
	args := caJoin(caText(1, "srv"), caBytes(2, caJoin(caText(1, "k"), caBytes(2, value))))
	g.rootWithTurns(g.toolTurn(user, caBytes(15, caBytes(1, args))))
	if err := (&Bundle{Assets: map[string][]byte{}}).cursorAgent(g.path); err == nil || !strings.Contains(err.Error(), "nesting") {
		t.Fatalf("unbounded Value nesting: %v", err)
	}
}
