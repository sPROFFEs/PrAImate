package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/store"
)

func TestChatLiveStateReplaysOneTurnAndBoundsPreview(t *testing.T) {
	current := &liveChatTurn{turnID: 17}
	a := &App{chatLive: map[string]*liveChatTurn{"active": current, "other": {turnID: 18}}}
	for _, text := range []string{"first", " second"} {
		event := ChatStreamEvent{ChatID: "active", Type: "text", Text: text}
		current.record(&event)
		if event.TurnID != 17 || event.Sequence != current.sequence {
			t.Fatalf("live event identity: %+v", event)
		}
	}
	snapshot := a.ChatLiveState("active")
	if snapshot.TurnID != 17 || len(snapshot.Events) != 1 || snapshot.Events[0].Text != "first second" || snapshot.Events[0].Sequence != 2 {
		t.Fatalf("snapshot: %+v", snapshot)
	}
	snapshot.Events[0].Text = "edited externally"
	if a.ChatLiveState("active").Events[0].Text != "first second" || len(a.ChatLiveState("other").Events) != 0 {
		t.Fatal("snapshot aliased live memory or mixed chats")
	}
	for range 40 {
		event := ChatStreamEvent{ChatID: "active", Type: "reasoning", Text: strings.Repeat("é", chatReplayChunk), Detail: "note"}
		current.record(&event)
	}
	bounded := a.ChatLiveState("active")
	if current.size > chatReplayBytes || len(bounded.Events) > chatReplayEvents || len(bounded.Events) == 0 {
		t.Fatalf("unbounded snapshot: %d bytes, %d events", current.size, len(bounded.Events))
	}
	for _, event := range bounded.Events {
		if !utf8.ValidString(event.Text) {
			t.Fatal("preview truncated inside a UTF-8 rune")
		}
	}
	delete(a.chatLive, "active")
	if a.ChatLiveState("active").TurnID != 0 {
		t.Fatal("finished chat still appears active")
	}
}

func TestActiveChatRejectsSecondSendWithoutCancellingFirst(t *testing.T) {
	core.RegisterAllCLIAdapters()
	st, err := store.InitializeWithPassword(filepath.Join(t.TempDir(), "profile.db"), "synthetic-regression-test")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, err := core.New(core.Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	chat, err := c.StartCleanChat(ctx, "praimate-cli", "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cancelled := false
	a := &App{
		ctx: ctx, core: c,
		chatCancels:   map[string]context.CancelFunc{chat.ID: func() { cancelled = true }},
		chatCancelIDs: map[string]uint64{chat.ID: 17},
		chatLive:      map[string]*liveChatTurn{chat.ID: {turnID: 17}},
	}
	if _, err := a.SendChatStream(chat.ID, "duplicate", nil); err == nil || !strings.Contains(err.Error(), "already has a turn") {
		t.Fatalf("duplicate send: %v", err)
	}
	if cancelled || a.ChatLiveState(chat.ID).TurnID != 17 {
		t.Fatal("second send disrupted the active turn")
	}
}
