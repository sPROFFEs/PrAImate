package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"git.jtsec.local/lab/PrAImate/internal/core"
)

func TestTerminalResumeCommand(t *testing.T) {
	tests := []struct {
		cli       string
		model     string
		wantName  string
		wantArgs  []string
		supported bool
	}{
		{"praimate-code", "openai/gpt-5", "praimate-code", []string{"--model", "openai/gpt-5", "--continue"}, true},
		{"opencode", "", "opencode", []string{"--continue"}, true},
		{"claude", "sonnet", "claude", []string{"--model", "sonnet", "--continue"}, true},
		{"codex", "gpt-5", "codex", []string{"resume", "--last", "--model", "gpt-5"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.cli, func(t *testing.T) {
			name, args, supported, err := terminalResumeCommand(tt.cli, tt.model)
			if err != nil {
				t.Fatal(err)
			}
			if name != tt.wantName || !reflect.DeepEqual(args, tt.wantArgs) || supported != tt.supported {
				t.Fatalf("got name=%q args=%q supported=%v", name, args, supported)
			}
		})
	}
}

func TestLegacyTerminalContextExclusiveAndReversible(t *testing.T) {
	for _, cli := range []string{"claude", "openclaude", "codex", "opencode", "praimate-code"} {
		t.Run(cli, func(t *testing.T) {
			cwd := t.TempDir()
			file := "AGENTS.md"
			if cli == "claude" || cli == "openclaude" {
				file = "CLAUDE.md"
			}
			path := filepath.Join(cwd, file)
			agent := &core.Agent{Instructions: "PERSONA"}
			cleanup, err := prepareLegacyTerminalContext(cwd, cli, agent, "LEGACY SKILL")
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			body, err := os.ReadFile(path)
			if err != nil || !strings.Contains(string(body), "PERSONA") || !strings.Contains(string(body), "LEGACY SKILL") {
				t.Fatalf("context: %q %v", body, err)
			}
			if _, err := prepareLegacyTerminalContext(cwd, cli, agent, "OTHER"); err == nil {
				t.Fatal("another session overwrote context")
			}
			cleanup()
			cleanup()
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("context not cleaned: %v", err)
			}
			if err := os.WriteFile(path, []byte("USER FILE"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := prepareLegacyTerminalContext(cwd, cli, agent, ""); err == nil {
				t.Fatal("user file accepted for overwrite")
			}
			body, _ = os.ReadFile(path)
			if string(body) != "USER FILE" {
				t.Fatal("user context changed")
			}
		})
	}
}

func TestLegacyTerminalCleanupPreservesUserEdits(t *testing.T) {
	cwd := t.TempDir()
	cleanup, err := prepareLegacyTerminalContext(cwd, "codex", &core.Agent{Instructions: "PERSONA"}, "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cwd, "AGENTS.md")
	if err := os.WriteFile(path, []byte("USER EDIT"), 0600); err != nil {
		t.Fatal(err)
	}
	cleanup()
	body, err := os.ReadFile(path)
	if err != nil || string(body) != "USER EDIT" {
		t.Fatalf("user edit removed: %q %v", body, err)
	}
}
