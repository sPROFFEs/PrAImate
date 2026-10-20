package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/store"
)

func documentFixture(t *testing.T) (*App, *App, *detachedWindow, *detachedCoordinator) {
	t.Helper()
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	st, err := store.InitializeWithPassword(filepath.Join(t.TempDir(), "docs.sqlite"), "test-password")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	c, err := core.New(core.Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	folder := t.TempDir()
	if err := os.WriteFile(filepath.Join(folder, "initial.md"), []byte("initial"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateChat(ctx, core.CreateChatRequest{ID: "document", CLIAgent: "praimate-cli", WorkspacePath: folder, Settings: core.ChatSettings{Surface: "studio"}}); err != nil {
		t.Fatal(err)
	}
	parent := &App{ctx: ctx, core: c}
	d := newDetachedCoordinator(parent)
	parent.detached = d
	d.mu.Lock()
	if err := d.ensureServerLocked(); err != nil {
		d.mu.Unlock()
		t.Fatal(err)
	}
	w := &detachedWindow{id: "document-window", kind: "studio", sessionID: "document", folder: folder, events: make(chan detachedWireEvent, 256), ready: make(chan struct{}), done: make(chan struct{})}
	d.windows[w.id] = w
	mode := detachedMode{active: true, kind: w.kind, sessionID: w.sessionID, windowID: w.id, brokerURL: d.addr, token: d.token, folder: folder}
	d.mu.Unlock()
	t.Cleanup(d.close)
	return parent, &App{detachedClient: newDetachedClient(mode)}, w, d
}

func TestDocumentFirstLoadListsFilesAndWorkspaceChangeMovesEditorScope(t *testing.T) {
	_, child, _, _ := documentFixture(t)
	files, err := child.EditorListFiles()
	if err != nil || len(files) != 1 || files[0] != "initial.md" {
		t.Fatalf("first snapshot is incorrect: %v %v", files, err)
	}
	next := t.TempDir()
	if err := os.WriteFile(filepath.Join(next, "next.md"), []byte("next"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := child.SaveStudioConfig("document", "Renamed", "praimate-cli", "", "edits", "", "", nil, next); err != nil {
		t.Fatal(err)
	}
	files, err = child.EditorListFiles()
	if err != nil || len(files) != 1 || files[0] != "next.md" {
		t.Fatalf("editor still points at old workspace: %v %v", files, err)
	}
	if _, err := child.EditorReadFile("initial.md"); err == nil {
		t.Fatal("old workspace must not remain accessible")
	}
}

func TestDocumentInvalidWorkspaceDoesNotChangeConfigurationOrCreateDirectories(t *testing.T) {
	parent, child, w, _ := documentFixture(t)
	missing := filepath.Join(t.TempDir(), "missing")
	if err := child.SaveStudioConfig("document", "Should not change", "codex", "other-model", "full", "", "", nil, missing); err == nil {
		t.Fatal("missing directory accepted")
	}
	chat, err := parent.core.GetChat(parent.ctx, "document")
	if err != nil || chat.WorkspacePath != w.folder || chat.CLIAgent != "praimate-cli" {
		t.Fatalf("invalid save changed session: %+v %v", chat, err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("missing workspace was created: %v", err)
	}
}

func TestDocumentWatcherPublishesInitialSnapshotAndExternalChanges(t *testing.T) {
	_, child, w, _ := documentFixture(t)
	if err := child.detachedClient.rpc("window.ready", nil, nil); err != nil {
		t.Fatal(err)
	}
	waitEvent := func(path string) {
		t.Helper()
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		for {
			select {
			case event := <-w.events:
				if event.Name == "praimate:editor-fs" && (path == "" || strings.Contains(string(event.Payload), path)) {
					return
				}
			case <-timer.C:
				t.Fatalf("no document event for %q", path)
			}
		}
	}
	waitEvent("")
	if err := os.WriteFile(filepath.Join(w.folder, "initial.md"), []byte("agent changed content"), 0600); err != nil {
		t.Fatal(err)
	}
	waitEvent("initial.md")
}

func TestDocumentWatcherRebindsAndKeepsSessionsIsolated(t *testing.T) {
	parent, child, w, d := documentFixture(t)
	if err := child.detachedClient.rpc("window.ready", nil, nil); err != nil {
		t.Fatal(err)
	}
	other := &detachedWindow{id: "other", kind: "studio", sessionID: "other", events: make(chan detachedWireEvent, 256), done: make(chan struct{})}
	d.mu.Lock()
	d.windows[other.id] = other
	d.mu.Unlock()
	wait := func(folder, path string) {
		t.Helper()
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		for {
			select {
			case event := <-w.events:
				var body map[string]string
				if event.Name == "praimate:editor-fs" && json.Unmarshal(event.Payload, &body) == nil && body["folder"] == folder && body["path"] == path {
					return
				}
			case <-timer.C:
				t.Fatalf("no event in new document scope for %s", path)
			}
		}
	}
	wait(d.documentFolder(w), "")
	if err := parent.UpdateChatWorkspace("document", t.TempDir()); err == nil {
		t.Fatal("dashboard changed a live document root")
	}
	if err := parent.UpdateChatWorkspace("document", ""); err == nil {
		t.Fatal("empty document root accepted")
	}
	next, _ := core.ValidateWorkspaceDirectory(t.TempDir())
	if err := child.SaveStudioConfig("document", "Doc", "praimate-cli", "", "full", "", "", nil, next); err != nil {
		t.Fatal(err)
	}
	wait(next, "")
	if err := os.WriteFile(filepath.Join(next, "new.md"), []byte("agent wrote this"), 0600); err != nil {
		t.Fatal(err)
	}
	wait(next, "new.md")
	select {
	case event := <-other.events:
		t.Fatalf("other document received event: %s", event.Name)
	default:
	}
}
