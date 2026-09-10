package main

import (
	"context"
	"path/filepath"
	"testing"

	"git.jtsec.local/lab/PrAImate/internal/core"
	"git.jtsec.local/lab/PrAImate/internal/skills"
	"git.jtsec.local/lab/PrAImate/internal/store"
)

func TestSkillBindingPreviewPreservesStructuredFailure(t *testing.T) {
	err := &skills.SkillResolutionError{Diagnostics: []skills.SkillDiagnostic{{Code: "untrusted_source", Ref: "local/example", Remedy: "Review this version"}}}
	preview := bindingPreview("agent", skills.ResolvedSkillSet{}, err)
	if preview.Error == "" || len(preview.Resolution.Diagnostics) != 1 || preview.Resolution.Diagnostics[0].Ref != "local/example" {
		t.Fatal("structured resolution failure lost")
	}
}

func TestSkillBindingsGUIUsesPersistedChatAndStrictInput(t *testing.T) {
	t.Setenv("PRAIMATE_HOME", t.TempDir())
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
	app := &App{ctx: ctx, core: c}
	rollout, err := app.SkillsV2RolloutState()
	if err != nil || rollout.Enabled {
		t.Fatalf("initial local rollout: %+v %v", rollout, err)
	}
	if err := app.SetSkillsV2RolloutState(true); err != nil {
		t.Fatal(err)
	}
	rollout, err = app.SkillsV2RolloutState()
	if err != nil || !rollout.Enabled {
		t.Fatalf("enabled local rollout: %+v %v", rollout, err)
	}
	chat, err := c.CreateChat(ctx, core.CreateChatRequest{ID: "preview-chat", CLIAgent: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{}`, `{"config":null,"lock":null,"trusted":true}`, `{"config":null,"config":null,"lock":null}`} {
		if err := app.SetChatSkillsV2(chat.ID, body); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	const legacy = `{"config":null,"lock":null}`
	if err := app.SetChatSkillsV2(chat.ID, legacy); err != nil {
		t.Fatal(err)
	}
	preview, err := app.PreviewChatSkillsV2(chat.ID, legacy)
	if err != nil || !preview.Resolution.Legacy || preview.Error != "" {
		t.Fatalf("legacy preview: %+v %v", preview, err)
	}
	selection, err := app.ChatSkillsV2(chat.ID)
	if err != nil || selection.Config != nil || selection.Lock != nil {
		t.Fatalf("selection: %+v %v", selection, err)
	}
}

func TestDetachedStudioVersionedSkillsUseScopedMainCore(t *testing.T) {
	t.Setenv("PRAIMATE_HOME", t.TempDir())
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
	if err := c.SetSkillsV2RolloutState(ctx, true); err != nil {
		t.Fatal(err)
	}
	chat, err := c.CreateChat(ctx, core.CreateChatRequest{ID: "studio-v2", CLIAgent: "claude"})
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
	w := &detachedWindow{id: "skills-window", kind: "studio", sessionID: chat.ID, events: make(chan detachedWireEvent, 2), ready: make(chan struct{})}
	d.windows[w.id] = w
	mode := detachedMode{active: true, kind: w.kind, sessionID: w.sessionID, windowID: w.id, brokerURL: d.addr, token: d.token}
	d.mu.Unlock()
	defer d.close()
	child := &App{detachedClient: newDetachedClient(mode)}
	if _, err := child.InstalledSkillVersionsV2(); err != nil {
		t.Fatal(err)
	}
	if _, err := child.BuildInstalledSkillSelectionV2("[]"); err != nil {
		t.Fatal(err)
	}
	const body = `{"config":null,"lock":null}`
	if err := child.SetChatSkillsV2(chat.ID, body); err != nil {
		t.Fatal(err)
	}
	if _, err := child.PreviewChatSkillsV2(chat.ID, body); err != nil {
		t.Fatal(err)
	}
	if _, err := child.ChatSkillsV2(chat.ID); err != nil {
		t.Fatal(err)
	}
	if err := child.SetChatSkillsV2("another-chat", body); err == nil {
		t.Fatal("window changed another chat")
	}
	if _, err := d.call(&detachedWindow{kind: "terminal"}, detachedRPCRequest{Method: "skills.v2.versions"}); err == nil {
		t.Fatal("terminal gained skill-library access")
	}
}
