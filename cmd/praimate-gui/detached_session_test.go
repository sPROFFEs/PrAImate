package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"git.jtsec.local/lab/PrAImate/internal/core"
	"git.jtsec.local/lab/PrAImate/internal/launcher"
	"git.jtsec.local/lab/PrAImate/internal/ollama"
	"git.jtsec.local/lab/PrAImate/internal/store"
)

func TestDetachedModeRequiresCompleteScopedEnvironment(t *testing.T) {
	t.Setenv("PRAIMATE_DETACHED_KIND", "terminal")
	t.Setenv("PRAIMATE_DETACHED_SESSION", "term-1")
	t.Setenv("PRAIMATE_DETACHED_WINDOW", "00112233445566778899aabb")
	t.Setenv("PRAIMATE_DETACHED_BROKER", "http://127.0.0.1:1234")
	t.Setenv("PRAIMATE_DETACHED_TOKEN", "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	t.Setenv("PRAIMATE_DETACHED_TITLE", "Build")

	mode := detachedModeFromEnvironment()
	if !mode.active || mode.kind != "terminal" || mode.sessionID != "term-1" || mode.title != "Build" {
		t.Fatalf("unexpected mode: %+v", mode)
	}

	t.Setenv("PRAIMATE_DETACHED_BROKER", "https://remote.invalid")
	if mode := detachedModeFromEnvironment(); mode.active {
		t.Fatalf("non-loopback broker was accepted: %+v", mode)
	}
	t.Setenv("PRAIMATE_DETACHED_BROKER", "http://127.0.0.1:1234@remote.invalid")
	if mode := detachedModeFromEnvironment(); mode.active {
		t.Fatalf("broker URL with a remote host was accepted: %+v", mode)
	}
}

func TestDetachedStudioModeCarriesFolder(t *testing.T) {
	t.Setenv("PRAIMATE_DETACHED_KIND", "studio")
	t.Setenv("PRAIMATE_DETACHED_SESSION", "chat-1")
	t.Setenv("PRAIMATE_DETACHED_WINDOW", "00112233445566778899aabb")
	t.Setenv("PRAIMATE_DETACHED_BROKER", "http://127.0.0.1:1234")
	t.Setenv("PRAIMATE_DETACHED_TOKEN", "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	t.Setenv("PRAIMATE_DETACHED_FOLDER", "/tmp/project")
	mode := detachedModeFromEnvironment()
	if !mode.active || mode.kind != "studio" || mode.folder != "/tmp/project" {
		t.Fatalf("unexpected studio mode: %+v", mode)
	}
}

func TestDetachedChildAppDoesNotOwnCoreOrTerminalManager(t *testing.T) {
	old := detachedProcessMode
	detachedProcessMode = detachedMode{
		active: true, kind: "chat", sessionID: "chat-1", windowID: "window-1",
		brokerURL: "http://127.0.0.1:1234", token: "secret", title: "Review",
	}
	t.Cleanup(func() { detachedProcessMode = old })

	a := NewApp()
	if a.detachedClient == nil || a.detached != nil || a.terms != nil || a.core != nil || a.st != nil {
		t.Fatalf("detached app owns main-process state: %+v", a)
	}
}

func TestDetachedStudioConfigUsesScopedMainCore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	// Keep this test deterministic and prove configured-file discovery rather
	// than accepting whatever CLI/model catalogue happens to be on PATH.
	t.Setenv("PATH", t.TempDir())
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	if _, err := ollama.ApplyOpenCodeModels("praimate_local", "Local", ollama.Settings{
		Endpoint: "http://127.0.0.1:11434", Model: "configured-opencode-model",
	}, []string{"configured-opencode-model"}, true); err != nil {
		t.Fatal(err)
	}
	if err := launcher.WriteOpenClaudeLocalProfile(launcher.OllamaSettings{
		Endpoint: "http://127.0.0.1:11434", Model: "configured-openclaude-model",
	}, "test-token"); err != nil {
		t.Fatal(err)
	}
	st, err := store.InitializeWithPassword(filepath.Join(t.TempDir(), "db.sqlite"), "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, err := core.New(core.Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	chat, err := c.CreateChat(ctx, core.CreateChatRequest{ID: "studio-config", Title: "Before", CLIAgent: "claude"})
	if err != nil {
		t.Fatal(err)
	}

	parent := &App{ctx: ctx, core: c}
	d := newDetachedCoordinator(parent)
	d.mu.Lock()
	if err := d.ensureServerLocked(); err != nil {
		d.mu.Unlock()
		t.Fatal(err)
	}
	w := &detachedWindow{id: "studio-config-window", kind: "studio", sessionID: chat.ID, events: make(chan detachedWireEvent, 2), ready: make(chan struct{})}
	d.windows[w.id] = w
	mode := detachedMode{active: true, kind: w.kind, sessionID: w.sessionID, windowID: w.id, brokerURL: d.addr, token: d.token}
	d.mu.Unlock()
	defer d.close()

	child := &App{detachedClient: newDetachedClient(mode)}
	models, err := child.StudioListCLIModels("claude")
	if err != nil || len(models) == 0 {
		t.Fatalf("Studio model options were not brokered: %v, %v", models, err)
	}
	models, err = child.StudioListCLIModels("opencode")
	if err != nil || !slices.Contains(models, "praimate_local/configured-opencode-model") {
		t.Fatalf("configured OpenCode model was not brokered: %v, %v", models, err)
	}
	models, err = child.StudioListCLIModels("openclaude")
	if err != nil || !slices.Contains(models, "configured-openclaude-model") {
		t.Fatalf("configured OpenClaude model was not brokered: %v, %v", models, err)
	}
	if _, err := child.StudioLocalLLMModels(); err != nil {
		t.Fatalf("Studio local-model options were not brokered: %v", err)
	}
	if _, err := child.StudioMCPServers(); err != nil {
		t.Fatalf("Studio MCP options were not brokered: %v", err)
	}
	if err := child.SaveStudioConfig(chat.ID, "After", "claude", "sonnet", "edits", "", "", nil); err != nil {
		t.Fatal(err)
	}
	updated, err := c.GetChat(ctx, chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != "After" || updated.CLIAgent != "claude" || updated.Settings.Model != "sonnet" || updated.Settings.Tools != "edits" {
		t.Fatalf("studio config was not saved through the parent: %+v", updated)
	}
	if err := child.SaveStudioConfig("another-chat", "No", "claude", "", "", "", "", nil); err == nil {
		t.Fatal("detached Studio changed a chat outside its assigned scope")
	}
	if _, err := d.call(&detachedWindow{kind: "terminal", sessionID: chat.ID}, detachedRPCRequest{Method: "studio.config.save"}); err == nil {
		t.Fatal("terminal window gained Studio configuration access")
	}
}

func TestDetachedTerminalRPCIsAuthenticatedAndScoped(t *testing.T) {
	a := &App{terms: newTermManager()}
	a.terms.sessions["term-1"] = &termSession{id: "term-1"}
	payload := bytes.Repeat([]byte("x"), 3<<20)
	_, _ = a.terms.recordOutput("term-1", payload)
	d := newDetachedCoordinator(a)
	a.detached = d
	d.mu.Lock()
	if err := d.ensureServerLocked(); err != nil {
		d.mu.Unlock()
		t.Fatal(err)
	}
	w := &detachedWindow{
		id: "window-1", kind: "terminal", sessionID: "term-1",
		events: make(chan detachedWireEvent, 2), ready: make(chan struct{}),
	}
	d.windows[w.id] = w
	mode := detachedMode{active: true, kind: w.kind, sessionID: w.sessionID, windowID: w.id, brokerURL: d.addr, token: d.token}
	d.mu.Unlock()
	t.Cleanup(d.close)

	client := newDetachedClient(mode)
	snapshot, err := client.terminalSnapshot("term-1")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(snapshot.Data)
	if err != nil || !bytes.Equal(raw, payload) {
		t.Fatalf("snapshot bytes = %d, want %d, err=%v", len(raw), len(payload), err)
	}
	if _, err := client.terminalSnapshot("term-2"); err == nil {
		t.Fatal("client accessed a terminal outside its assigned scope")
	}
	if err := client.rpc("window.ready", nil, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.ready:
	case <-time.After(time.Second):
		t.Fatal("renderer-ready handshake did not release the parent")
	}

	request, _ := http.NewRequest(http.MethodPost, d.addr+"/rpc?window=window-1", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("unauthenticated status = %d, want 403", response.StatusCode)
	}
}

func TestDetachedEventQueueIsBoundedAndRequestsResync(t *testing.T) {
	d := newDetachedCoordinator(&App{})
	w := &detachedWindow{
		id: "window-1", kind: "terminal", sessionID: "term-1",
		events: make(chan detachedWireEvent, 1), ready: make(chan struct{}),
	}
	d.windows[w.id] = w
	d.publish("terminal", "term-1", "term:data:term-1", TerminalData{Data: "YQ==", EndOffset: 1})
	d.publish("terminal", "term-1", "term:data:term-1", TerminalData{Data: "Yg==", StartOffset: 1, EndOffset: 2})

	select {
	case event := <-w.events:
		if event.Name != "praimate:detached-resync" {
			t.Fatalf("overflow event = %q, want resync", event.Name)
		}
	case <-time.After(time.Second):
		t.Fatal("no resync event after queue overflow")
	}
}

func TestDetachedChatQueueRetainsNewestEvent(t *testing.T) {
	d := newDetachedCoordinator(&App{})
	w := &detachedWindow{
		id: "window-1", kind: "chat", sessionID: "chat-1",
		events: make(chan detachedWireEvent, 1), ready: make(chan struct{}),
	}
	d.windows[w.id] = w
	d.publish("chat", "chat-1", "praimate:chat-stream", ChatStreamEvent{ChatID: "chat-1", Text: "old"})
	d.publish("chat", "chat-1", "praimate:chat-finished", ChatFinishedEvent{ChatID: "chat-1"})

	select {
	case event := <-w.events:
		if event.Name != "praimate:chat-finished" {
			t.Fatalf("overflow event = %q, want chat completion", event.Name)
		}
	case <-time.After(time.Second):
		t.Fatal("chat completion was lost after queue overflow")
	}
}

func TestDetachedClientBuffersEventsUntilRendererReady(t *testing.T) {
	var got []string
	client := &detachedClient{emit: func(name string, _ any) { got = append(got, name) }}
	client.dispatch("first", map[string]string{"value": "one"})
	client.dispatch("second", nil)
	if len(got) != 0 {
		t.Fatalf("events reached the frontend before it subscribed: %v", got)
	}
	client.markRendererReady()
	client.dispatch("third", nil)
	want := []string{"first", "second", "third"}
	if len(got) != len(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events = %v, want %v", got, want)
		}
	}
}

func TestDetachedWindowLimitIsBounded(t *testing.T) {
	d := newDetachedCoordinator(&App{})
	for i := 0; i < detachedWindowLimit; i++ {
		id := string(rune('a' + i))
		d.windows[id] = &detachedWindow{id: id}
	}
	if got := len(d.list()); got != detachedWindowLimit {
		t.Fatalf("window count = %d", got)
	}
}
