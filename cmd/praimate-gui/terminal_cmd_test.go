package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sPROFFEs/PrAImate/internal/core"
)

func TestTerminalResumeCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()

	// 1. Without native sessions, should launch clean without --continue
	name, args, supported, err := terminalResumeCommand("openclaude", "sonnet", cwd)
	if err != nil {
		t.Fatal(err)
	}
	if name != "openclaude" || !reflect.DeepEqual(args, []string{"--model", "sonnet"}) || !supported {
		t.Fatalf("expected clean launch without prior session, got name=%q args=%q supported=%v", name, args, supported)
	}
	if isNativeResumeCommand("openclaude", args) {
		t.Fatal("clean launch mistaken for native resume")
	}

	// 2. Create an openclaude native session file
	slug := openclaudeSlug(cwd)
	storeDir := filepath.Join(home, ".openclaude", "projects", slug)
	if err := os.MkdirAll(storeDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storeDir, "sess-1.jsonl"), []byte(`{"sessionId":"test"}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Now with native session, should append --continue
	name, args, supported, err = terminalResumeCommand("openclaude", "sonnet", cwd)
	if err != nil {
		t.Fatal(err)
	}
	if name != "openclaude" || !reflect.DeepEqual(args, []string{"--model", "sonnet", "--continue"}) || !supported {
		t.Fatalf("expected --continue with existing session, got name=%q args=%q supported=%v", name, args, supported)
	}
	if !isNativeResumeCommand("openclaude", args) || !isNativeResumeCommand("codex", []string{"resume", "--last"}) {
		t.Fatal("native resume command was not recognized")
	}

	// 3. Opencode test
	name, args, supported, err = terminalResumeCommand("opencode", "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	if name != "opencode" || !reflect.DeepEqual(args, []string{"--continue"}) || !supported {
		t.Fatalf("opencode got name=%q args=%q supported=%v", name, args, supported)
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

func TestCleanVersionedCodeChatsFallBackToMCPOnContextCollision(t *testing.T) {
	for _, cli := range []string{"codex", "opencode", "praimate-code", "claude", "openclaude"} {
		t.Run(cli, func(t *testing.T) {
			cwd := t.TempDir()
			file := "AGENTS.md"
			if cli == "claude" || cli == "openclaude" {
				file = "CLAUDE.md"
			}
			path := filepath.Join(cwd, file)
			cleanup, bridgeOnly, err := prepareTerminalContext(cwd, cli, nil, "SELECTED SKILL", true, false)
			if err != nil || bridgeOnly || cleanup == nil {
				t.Fatalf("clean chat: cleanup=%v bridgeOnly=%v err=%v", cleanup != nil, bridgeOnly, err)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("missing temporary context file: %v", err)
			}
			cleanup()
			if err := os.WriteFile(path, []byte("PROJECT INSTRUCTIONS"), 0600); err != nil {
				t.Fatal(err)
			}
			if fallbackCleanup, bridgeOnly, err := prepareTerminalContext(cwd, cli, nil, "SELECTED SKILL", true, false); err != nil || !bridgeOnly || fallbackCleanup != nil {
				t.Fatalf("existing project context blocked clean chat: %v", err)
			}
			if _, _, err := prepareTerminalContext(cwd, cli, nil, "LEGACY SKILL", false, false); !errors.Is(err, fs.ErrExist) {
				t.Fatalf("legacy skill bypassed existing project instructions: %v", err)
			}
			body, err := os.ReadFile(path)
			if err != nil || string(body) != "PROJECT INSTRUCTIONS" {
				t.Fatalf("project instructions changed: %q %v", body, err)
			}
			if _, _, err := prepareTerminalContext(cwd, cli, &core.Agent{Instructions: "PERSONA"}, "SELECTED SKILL", true, false); !errors.Is(err, fs.ErrExist) {
				t.Fatalf("agent persona bypassed existing project instructions: %v", err)
			}
			if resumedCleanup, bridgeOnly, err := prepareTerminalContext(cwd, cli, &core.Agent{Instructions: "PERSONA"}, "SELECTED SKILL", true, true); err != nil || !bridgeOnly || resumedCleanup != nil {
				t.Fatalf("native agent resume blocked by existing context: %v", err)
			}
		})
	}
}
