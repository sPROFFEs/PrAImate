package main

import (
	"bufio"
	"context"
	"os"
	"strings"
	"testing"

	"git.jtsec.local/lab/PrAImate/internal/core"
)

func TestNativeCLIHelpAndValidationDoNotOpenStorage(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PRAIMATE_HOME", dir)
	for _, args := range [][]string{{"--version"}, {"--help"}} {
		if err := run(context.Background(), args); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"--tools", "unsafe"}, {"--format", "broken"}, {"--mcp-config", "untrusted.json"}, {"--chat", "id", "--continue"}} {
		if err := run(context.Background(), args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("help/invalid options touched storage")
	}
}

func TestNativeCLIOpensSameEncryptedCoreAndPreservesInput(t *testing.T) {
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	input := bufio.NewReader(strings.NewReader("correct horse battery staple\nuser prompt\n"))
	c, closeCore, err := openCore(input, true, false)
	if err != nil {
		t.Fatal(err)
	}
	chat, err := c.CreateChat(context.Background(), core.CreateChatRequest{CLIAgent: "praimate-cli", WorkspacePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	remaining, err := input.ReadString('\n')
	if err != nil || remaining != "user prompt\n" {
		t.Fatalf("password consumed prompt: %q %v", remaining, err)
	}
	closeCore()
	c, closeCore, err = openCore(bufio.NewReader(strings.NewReader("correct horse battery staple\n")), true, false)
	if err != nil {
		t.Fatal(err)
	}
	defer closeCore()
	if _, err := c.GetChat(context.Background(), chat.ID); err != nil {
		t.Fatal("terminal did not reopen core history:", err)
	}
	if _, _, err := openCore(bufio.NewReader(strings.NewReader("wrong password\n")), true, false); err == nil {
		t.Fatal("accepted wrong database password")
	}
}

func TestChatSystemPromptDoesNotLeakBetweenSessions(t *testing.T) {
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	c, closeCore, err := openCore(bufio.NewReader(strings.NewReader("test-password\n")), true, false)
	if err != nil {
		t.Fatal(err)
	}
	defer closeCore()
	ctx := context.Background()
	first, err := c.CreateChat(ctx, core.CreateChatRequest{ID: "terminal-first", CLIAgent: "praimate-cli", WorkspacePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.CreateChat(ctx, core.CreateChatRequest{ID: "terminal-second", CLIAgent: "praimate-cli", WorkspacePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.AddMessage(ctx, first.ID, "system", "first only", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AddMessage(ctx, second.ID, "system", "second only", nil); err != nil {
		t.Fatal(err)
	}
	firstSystem, _, _, err := chatSystemPrompt(ctx, c, first)
	if err != nil {
		t.Fatal(err)
	}
	secondSystem, _, _, err := chatSystemPrompt(ctx, c, second)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(firstSystem, "first only") || strings.Contains(secondSystem, "first only") || !strings.Contains(secondSystem, "second only") {
		t.Fatalf("session system context leaked: first=%q second=%q", firstSystem, secondSystem)
	}
}
