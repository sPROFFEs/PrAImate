package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceChangeValidatesDirectoryAndResetsOnlyNativeSession(t *testing.T) {
	s := openTempStore(t)
	defer s.Close()
	c, _ := New(Options{Store: s})
	ctx := context.Background()
	folder, err := ValidateWorkspaceDirectory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	chat, err := c.CreateChat(ctx, CreateChatRequest{CLIAgent: "praimate-cli", WorkspacePath: folder})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.SetChatSessionID(ctx, chat.ID, "native-session"); err != nil {
		t.Fatal(err)
	}
	if err = c.UpdateChatWorkspace(ctx, chat.ID, folder); err != nil {
		t.Fatal(err)
	}
	got, _ := c.GetChat(ctx, chat.ID)
	if got.SessionID != "native-session" {
		t.Fatal("same folder lost native session")
	}
	file := filepath.Join(t.TempDir(), "file.txt")
	if err = os.WriteFile(file, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{file, filepath.Join(t.TempDir(), "missing")} {
		if err := c.UpdateChatWorkspace(ctx, chat.ID, invalid); err == nil {
			t.Fatal("invalid folder accepted")
		}
		got, _ = c.GetChat(ctx, chat.ID)
		if got.WorkspacePath != folder || got.SessionID != "native-session" {
			t.Fatal("invalid folder mutated conversation")
		}
	}
	next, _ := ValidateWorkspaceDirectory(t.TempDir())
	if err = c.UpdateChatWorkspace(ctx, chat.ID, next); err != nil {
		t.Fatal(err)
	}
	got, _ = c.GetChat(ctx, chat.ID)
	if got.SessionID != "" || got.WorkspacePath != next || got.ID != chat.ID {
		t.Fatal("new folder did not reset native session on the existing chat")
	}
}
