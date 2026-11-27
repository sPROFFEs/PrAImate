package main

// Streaming chat bindings — the GUI-side half of the desktop-app feel:
// SendChatStream forwards core StreamEvents over the Wails event bus as
// the turn runs (text deltas, tool activity), and CancelChatTurn is the
// Stop button. CLIs without an event stream silently degrade to the
// buffered behavior — the promise just resolves with the full reply and
// no events fire first.

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"


	"github.com/sPROFFEs/PrAImate/internal/core"
)

// ChatStreamEvent is what the frontend receives on the
// "praimate:chat-stream" event channel.
type ChatStreamEvent struct {
	ChatID   string `json:"chatId"`
	TurnID   uint64 `json:"turnId,omitempty"`
	Sequence uint64 `json:"sequence,omitempty"`
	Type     string `json:"type"` // "text" | "reasoning" | "step_*" | "tool_*" | "error"
	Text     string `json:"text,omitempty"`
	Tool     string `json:"tool,omitempty"`
	Detail   string `json:"detail,omitempty"`
	ID       string `json:"id,omitempty"`
	OK       bool   `json:"ok,omitempty"`
}

type ChatFinishedEvent struct {
	ChatID string `json:"chatId"`
	Error  string `json:"error,omitempty"`
}

// SendChatStream sends one message (with optional staged attachment
// paths) and streams progress events while the CLI runs. The returned
// turn is the persisted final state — after the promise resolves the
// frontend reloads messages from the DB exactly like the non-streaming
// path.
func (a *App) SendChatStream(chatID, message string, attachments []string) (*core.ChatTurn, error) {
	if a.detachedClient != nil {
		return a.detachedClient.sendChatStream(chatID, message, attachments)
	}
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	chat, err := c.GetChat(a.ctx, chatID)
	if err != nil {
		return nil, err
	}
	systemPrompt := ""
	if chat.AgentID != "" {
		if agent, err := c.GetAgent(a.ctx, chat.AgentID); err == nil {
			systemPrompt = core.AgentSystemPrompt(agent)
		}
	}
	// Prepend enabled skills (CLI-agnostic at the prompt level; the
	// catalogue's per-skill CLI tag is advisory in the UI). Skills add
	// to whatever the agent itself contributes, never replace it.
	if prefix := core.ResolveSkillsPrefix(chat.Settings.Skills); prefix != "" {
		if systemPrompt != "" {
			systemPrompt = prefix + "\n\n---\n\n" + systemPrompt
		} else {
			systemPrompt = prefix
		}
	}
	if chat.Settings.Surface == "workflow" {
		systemPrompt = appendPromptContext(systemPrompt, core.WorkflowSystemContext(chat.WorkspacePath))
	}

	ctx, cancel := context.WithCancel(a.ctx)
	a.chatCancelMu.Lock()
	if _, active := a.chatCancels[chatID]; active {
		a.chatCancelMu.Unlock()
		cancel()
		return nil, errors.New("this chat already has a turn in progress")
	}
	a.chatCancelSeq++
	runID := a.chatCancelSeq
	a.chatCancels[chatID] = cancel
	a.chatCancelIDs[chatID] = runID
	if a.chatLive == nil {
		a.chatLive = make(map[string]*liveChatTurn)
	}
	a.chatLive[chatID] = &liveChatTurn{turnID: runID}
	a.chatCancelMu.Unlock()
	cleanup := func() {
		cancel()
		a.chatCancelMu.Lock()
		if a.chatCancelIDs[chatID] == runID {
			delete(a.chatCancels, chatID)
			delete(a.chatCancelIDs, chatID)
			delete(a.chatLive, chatID)
		}
		a.chatCancelMu.Unlock()
	}

	emitImmediate := func(streamEvent ChatStreamEvent) {
		a.chatCancelMu.Lock()
		if live := a.chatLive[chatID]; live != nil && live.turnID == runID {
			live.record(&streamEvent)
		}
		a.chatCancelMu.Unlock()
		a.emitUIEvent("praimate:chat-stream", streamEvent)
		if a.detached != nil {
			a.detached.publish("chat", chatID, "praimate:chat-stream", streamEvent)
			a.detached.publish("studio", chatID, "praimate:chat-stream", streamEvent)
		}
	}
	emitImmediate(ChatStreamEvent{ChatID: chatID, Type: "started"})
	coalescer := newStreamCoalescer(20*time.Millisecond, emitImmediate)
	defer coalescer.Flush()

	onEvent := func(ev core.StreamEvent) {
		streamEvent := ChatStreamEvent{
			ChatID: chatID,
			Type:   ev.Type,
			Text:   ev.Text,
			Tool:   ev.Tool,
			Detail: ev.Detail,
			ID:     ev.ID,
			OK:     ev.OK,
		}
		coalescer.push(streamEvent)
	}
	turn, streamErr := c.ContinueChatStream(ctx, chatID, message, chat.WorkspacePath, systemPrompt, attachments, onEvent)
	coalescer.Flush()
	cleanup()
	finished := ChatFinishedEvent{ChatID: chatID}
	if streamErr != nil {
		finished.Error = streamErr.Error()
	}
	a.emitUIEvent("praimate:chat-finished", finished)
	if a.detached != nil {
		a.detached.publish("chat", chatID, "praimate:chat-finished", finished)
		a.detached.publish("studio", chatID, "praimate:chat-finished", finished)
	}
	return turn, streamErr
}

func appendPromptContext(systemPrompt, context string) string {
	if context == "" {
		return systemPrompt
	}
	if systemPrompt == "" {
		return context
	}
	return systemPrompt + "\n\n---\n\n" + context
}

// ActiveChatIDs returns the IDs of every chat that currently has an
// in-flight streamed turn (something the user can switch back to without
// losing it). Used by the Sessions panel's "live" indicator.
func (a *App) ActiveChatIDs() []string {
	a.chatCancelMu.Lock()
	defer a.chatCancelMu.Unlock()
	out := make([]string, 0, len(a.chatCancels))
	for id := range a.chatCancels {
		out = append(out, id)
	}
	return out
}

// ChatLiveSnapshot restores an active turn when a renderer opens its chat.
// The replay is transient and capped. The transcript remains authoritative
// once the final message is saved.
type ChatLiveSnapshot struct {
	TurnID uint64            `json:"turnId"`
	Events []ChatStreamEvent `json:"events"`
}

type liveChatTurn struct {
	turnID   uint64
	sequence uint64
	events   []ChatStreamEvent
	size     int
}

func (a *App) ChatLiveState(chatID string) ChatLiveSnapshot {
	a.chatCancelMu.Lock()
	defer a.chatCancelMu.Unlock()
	if turn := a.chatLive[chatID]; turn != nil {
		return ChatLiveSnapshot{TurnID: turn.turnID, Events: slices.Clone(turn.events)}
	}
	return ChatLiveSnapshot{}
}

const (
	chatReplayBytes  = 512 << 10
	chatReplayEvents = 2048
	chatReplayChunk  = 16 << 10
)

func (t *liveChatTurn) record(event *ChatStreamEvent) {
	t.sequence++
	event.TurnID, event.Sequence = t.turnID, t.sequence
	replay := *event
	replay.Text = replayTail(replay.Text)
	replay.Detail = replayTail(replay.Detail)
	t.size += len(replay.Text) + len(replay.Detail)
	if n := len(t.events); n > 0 && (replay.Type == "text" || replay.Type == "reasoning") &&
		t.events[n-1].Type == replay.Type && replay.Detail == "" && t.events[n-1].Detail == "" && len(t.events[n-1].Text)+len(replay.Text) <= chatReplayChunk {
		t.events[n-1].Text += replay.Text
		t.events[n-1].Sequence = replay.Sequence
	} else {
		t.events = append(t.events, replay)
	}
	for t.size > chatReplayBytes || len(t.events) > chatReplayEvents {
		t.size -= len(t.events[0].Text) + len(t.events[0].Detail)
		t.events[0] = ChatStreamEvent{}
		t.events = t.events[1:]
	}
}

func replayTail(text string) string {
	if len(text) <= chatReplayChunk {
		return text
	}
	return "… " + strings.ToValidUTF8(text[len(text)-chatReplayChunk:], "")
}

// CancelChatTurn interrupts the chat's in-flight streamed turn. No-op
// when nothing is running. The interrupted turn persists whatever text
// already streamed (flagged in the message meta).
func (a *App) CancelChatTurn(chatID string) {
	if a.detachedClient != nil {
		_ = a.detachedClient.cancelChatTurn(chatID)
		return
	}
	a.chatCancelMu.Lock()
	cancel, ok := a.chatCancels[chatID]
	a.chatCancelMu.Unlock()
	if ok {
		cancel()
	}
}

// streamCoalescer aggregates rapid sequential text deltas over a small duration
// (e.g. 20ms) to avoid overwhelming the frontend event bus and IPC serialization.
type streamCoalescer struct {
	emitFn   func(ChatStreamEvent)
	mu       sync.Mutex
	pending  ChatStreamEvent
	timer    *time.Timer
	interval time.Duration
}

func newStreamCoalescer(interval time.Duration, emitFn func(ChatStreamEvent)) *streamCoalescer {
	return &streamCoalescer{
		emitFn:   emitFn,
		interval: interval,
	}
}

func (c *streamCoalescer) push(ev ChatStreamEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if ev.Type == "text" || ev.Type == "reasoning" {
		if c.pending.Type == ev.Type {
			c.pending.Text += ev.Text
			return
		}
		c.flushLocked()
		c.pending = ev
		if c.timer == nil {
			c.timer = time.AfterFunc(c.interval, func() {
				c.mu.Lock()
				c.flushLocked()
				c.mu.Unlock()
			})
		} else {
			c.timer.Reset(c.interval)
		}
		return
	}

	c.flushLocked()
	c.emitFn(ev)
}

func (c *streamCoalescer) flushLocked() {
	if c.timer != nil {
		c.timer.Stop()
	}
	if c.pending.Type != "" {
		c.emitFn(c.pending)
		c.pending = ChatStreamEvent{}
	}
}

func (c *streamCoalescer) Flush() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.flushLocked()
}
