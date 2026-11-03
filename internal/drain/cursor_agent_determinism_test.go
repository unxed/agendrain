package drain

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestCursorAgentSplitTextIsDeterministic(t *testing.T) {
	build := func() obj {
		o := obj{}
		for i := 0; i < 12; i++ {
			o[fmt.Sprintf("field%02d", i)] = strings.Repeat(fmt.Sprintf("line %d\n", i), 40)
		}
		return o
	}
	var first []Block
	for n := 0; n < 50; n++ {
		var blocks []Block
		caSplitText(build(), "", "tool_call", &blocks)
		if n == 0 {
			first = blocks
			continue
		}
		if !reflect.DeepEqual(first, blocks) {
			t.Fatalf("run %d produced different block order", n)
		}
	}
}
