package drain

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"
)

func runCommand(program string, args []string, input []byte, maxOutput int) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitedBuffer{buf: &stdout, max: maxOutput}
	cmd.Stderr = &limitedBuffer{buf: &stderr, max: 4096}
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s: %w: %s", program, err, stderr.String())
	}
	if len(bytes.TrimSpace(stdout.Bytes())) == 0 {
		return nil, fmt.Errorf("%s returned empty output", program)
	}
	if !utf8.Valid(stdout.Bytes()) {
		return nil, fmt.Errorf("%s returned invalid UTF-8", program)
	}
	return stdout.Bytes(), nil
}

func describe(o Options, kind, name, mime string, data []byte) (string, error) {
	request := struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
		MIME string `json:"mime"`
		Data []byte `json:"data_base64"`
	}{kind, name, mime, data}
	raw, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	out, err := runCommand(o.DescribeCommand, o.DescribeArgs, raw, 64<<10)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
