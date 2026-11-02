package drain

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandHelperProcess(t *testing.T) {
	if os.Getenv("AGENDRAIN_TEST_HELPER") != "1" {
		return
	}
	var request struct {
		Kind string `json:"kind"`
		Data []byte `json:"data_base64"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil || len(request.Data) == 0 {
		os.Exit(2)
	}
	fmt.Print("Evidence description: " + request.Kind)
	os.Exit(0)
}

func TestExternalImageDescription(t *testing.T) {
	t.Setenv("AGENDRAIN_TEST_HELPER", "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b := &Bundle{Assets: map[string][]byte{"i": {137, 80, 78, 71}}, Sessions: []Session{{ID: "p", Events: []Event{{Role: "user", Blocks: []Block{{Type: "image", MIME: "image/png", Asset: "i"}}}}}}}
	if err := Prepare(b, Options{Images: "describe", DescribeCommand: exe, DescribeArgs: []string{"-test.run=TestCommandHelperProcess"}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(joined(b), "Evidence description: image") || len(b.Assets) != 0 {
		t.Fatal(joined(b))
	}
}

func TestMarkdownWithSeparateImageArchive(t *testing.T) {
	d := t.TempDir()
	b := &Bundle{Version: 1, Assets: map[string][]byte{"i": {137, 80, 78, 71}}, Sessions: []Session{{ID: "p", Events: []Event{{Role: "user", Blocks: []Block{{Type: "image", MIME: "image/png", Asset: "i"}}}}}}}
	md := filepath.Join(d, "handoff.md")
	images := filepath.Join(d, "pictures.zip")
	if err := Write(b, md, Options{Format: "md", ImageArchive: images}); err != nil {
		t.Fatal(err)
	}
	files, err := zipData(images)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files["handoff_images/00000001.png"] == nil {
		t.Fatal(files)
	}
	if _, err := os.Stat(filepath.Join(d, "handoff_images")); !os.IsNotExist(err) {
		t.Fatal("unexpected unpacked directory")
	}
}
