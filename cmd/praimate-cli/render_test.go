package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestTextRendererMarkdownAndSplitChunks(t *testing.T) {
	var out bytes.Buffer
	r := newTextRenderer(&out, false, 80)
	for _, chunk := range []string{"# Capabilities\n\n- **Read** files\n- `git`, ", "status\n\n| Feature | Description |\n|---|---|\n| Git | Inspect **status** |\n\n```go\nfmt.Println(1)\n```\n"} {
		r.WriteChunk(chunk)
	}
	r.Flush()
	got := out.String()
	for _, want := range []string{"CAPABILITIES", "• Read files", "• ‹git›, status", "│ Feature", "│ Git", "Inspect status", "CODE · go", "  fmt.Println(1)"} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "**") || strings.Contains(got, "```") || strings.Contains(got, "|---|") {
		t.Fatalf("raw Markdown leaked: %q", got)
	}
}

func TestTextRendererStripsModelTerminalControls(t *testing.T) {
	var out bytes.Buffer
	r := newTextRenderer(&out, true, 80)
	r.WriteChunk("safe\x1b]52;c;payload\a **bold**\n")
	r.Flush()
	got := out.String()
	if strings.Contains(got, "\x1b]") || strings.Contains(got, "\a") || !strings.Contains(got, "\x1b[1mbold\x1b[0m") {
		t.Fatalf("unsafe or unstyled model output: %q", got)
	}
}
