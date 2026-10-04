package main

// Approval broker — the GUI-side half of mid-turn approvals. A loopback
// HTTP server (127.0.0.1, random port, bearer token) that the MCP shim
// posts permission requests to. Each request surfaces as an
// "praimate:approval" event; the frontend's Allow/Deny card answers via
// the ResolveApproval binding, which unblocks the waiting HTTP response
// (and therefore the claude turn).
//
// Decisions:
//   allow once      — this request only
//   allow always    — auto-allow this tool name for this chat until the
//                     GUI restarts (kept in-process, never persisted)
//   deny / timeout  — fail-closed; 10 minutes of silence is a deny
//
// Started lazily on the first "ask"-level turn; one broker serves all
// chats (the chat id rides in the URL path).

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/studio"
)

const approvalTimeout = 10 * time.Minute

// ApprovalRequest is what the frontend receives on "praimate:approval".
type ApprovalRequest struct {
	ID     string `json:"id"`
	ChatID string `json:"chatId"`
	Tool   string `json:"tool"`
	Detail string `json:"detail"`
}

type approvalDecision struct {
	allow  bool
	always bool
}

type approvalBroker struct {
	addr  string
	token string

	mu       sync.Mutex
	pending  map[string]chan approvalDecision
	scopes   map[string]string // request ID -> chat ID
	requests map[string]ApprovalRequest
	always   map[string]map[string]bool // chatID → tool name → allowed
	seq      int

	emit     func(ApprovalRequest)
	resolved func(ApprovalRequest)
}

func newApprovalBroker(emit func(ApprovalRequest)) (*approvalBroker, error) {
	tok := make([]byte, 24)
	if _, err := rand.Read(tok); err != nil {
		return nil, err
	}
	b := &approvalBroker{
		token:   hex.EncodeToString(tok),
		pending: map[string]chan approvalDecision{},
		scopes:  map[string]string{},
		always:  map[string]map[string]bool{},
		emit:    emit,
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	b.addr = ln.Addr().String()
	mux := http.NewServeMux()
	mux.HandleFunc("/approve/", b.handleApprove)
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	return b, nil
}

func (b *approvalBroker) handleApprove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.Header.Get("X-Praimate-Token") != b.token {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	chatID := strings.TrimPrefix(r.URL.Path, "/approve/")
	var req struct {
		ToolName string         `json:"tool_name"`
		Input    map[string]any `json:"input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	respond := func(allow bool, msg string) {
		w.Header().Set("Content-Type", "application/json")
		behavior := "deny"
		if allow {
			behavior = "allow"
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"behavior": behavior, "message": msg})
	}

	allow, err := b.request(r.Context(), chatID, req.ToolName, req.Input)
	if err != nil {
		respond(false, err.Error())
		return
	}
	respond(allow, "")
}

func (b *approvalBroker) request(ctx context.Context, scope, tool string, input map[string]any) (bool, error) {
	b.mu.Lock()
	if b.always[scope][tool] {
		b.mu.Unlock()
		return true, nil
	}
	b.seq++
	id := fmt.Sprintf("ap-%d", b.seq)
	ch := make(chan approvalDecision, 1)
	b.pending[id] = ch
	b.scopes[id] = scope
	if b.requests == nil {
		b.requests = map[string]ApprovalRequest{}
	}
	request := ApprovalRequest{ID: id, ChatID: scope, Tool: tool, Detail: summarizeApprovalInput(input)}
	b.requests[id] = request
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.pending, id)
		delete(b.scopes, id)
		delete(b.requests, id)
		b.mu.Unlock()
		if b.resolved != nil {
			b.resolved(request)
		}
	}()

	b.emit(request)
	timer := time.NewTimer(approvalTimeout)
	defer timer.Stop()
	select {
	case d := <-ch:
		if d.allow && d.always {
			b.mu.Lock()
			if b.always[scope] == nil {
				b.always[scope] = map[string]bool{}
			}
			b.always[scope][tool] = true
			b.mu.Unlock()
		}
		return d.allow, nil
	case <-timer.C:
		return false, errors.New("approval timed out in PrAImate GUI")
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

func (b *approvalBroker) hasPending(scope string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, pendingScope := range b.scopes {
		if pendingScope == scope {
			return true
		}
	}
	return false
}

func (b *approvalBroker) denyScope(scope string) {
	b.mu.Lock()
	channels := make([]chan approvalDecision, 0)
	for id, pendingScope := range b.scopes {
		if pendingScope == scope {
			if ch := b.pending[id]; ch != nil {
				channels = append(channels, ch)
			}
		}
	}
	b.mu.Unlock()
	for _, ch := range channels {
		select {
		case ch <- approvalDecision{}:
		default:
		}
	}
}

func (b *approvalBroker) resolve(id string, allow, always bool) {
	b.mu.Lock()
	ch, ok := b.pending[id]
	b.mu.Unlock()
	if ok {
		select {
		case ch <- approvalDecision{allow: allow, always: always}:
		default:
		}
	}
}

func (b *approvalBroker) resolveScoped(scope, id string, allow, always bool) bool {
	b.mu.Lock()
	ch, ok := b.pending[id]
	if b.scopes[id] != scope {
		ok = false
	}
	b.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- approvalDecision{allow: allow, always: always}:
		return true
	default:
		return false
	}
}

// summarizeApprovalInput mirrors core's tool-input summary for the
// approval card (command / file path first, compact JSON fallback).
func summarizeApprovalInput(input map[string]any) string {
	for _, key := range []string{"command", "file_path", "path", "pattern", "url", "query", "description"} {
		if s, ok := input[key].(string); ok && strings.TrimSpace(s) != "" {
			if len(s) > 200 {
				s = s[:200] + "…"
			}
			return s
		}
	}
	if len(input) == 0 {
		return ""
	}
	raw, _ := json.Marshal(input)
	s := string(raw)
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// --- App wiring --------------------------------------------------------------

// ensureApprovalBroker lazily starts the broker (first "ask" turn).
func (a *App) ensureApprovalBroker() (*approvalBroker, error) {
	a.approvalMu.Lock()
	defer a.approvalMu.Unlock()
	if a.approval != nil {
		return a.approval, nil
	}
	b, err := newApprovalBroker(func(req ApprovalRequest) {
		if strings.HasPrefix(req.ChatID, "assistant-") {
			studio.PublishDesktopAssistant(a.core, "assistant.approval", req)
		}
		wruntime.EventsEmit(a.ctx, "praimate:approval", req)
		if a.detached != nil {
			a.detached.publish("chat", req.ChatID, "praimate:approval", req)
			a.detached.publish("studio", req.ChatID, "praimate:approval", req)
			a.detached.publish("workers", req.ChatID, "praimate:approval", req)
		}
	})
	if err != nil {
		return nil, err
	}
	b.resolved = func(req ApprovalRequest) {
		a.emitUIEvent("praimate:approval-resolved", req)
		if a.detached != nil {
			a.detached.publish("workers", req.ChatID, "praimate:approval-resolved", req)
		}
	}
	a.approval = b
	return b, nil
}

// approvalProvider is registered on Core at startup: it hands the chat
// layer the shim command line for an "ask"-level turn. Errors degrade
// to nil (the turn runs safe instead of wedging).
func (a *App) approvalProvider(chatID string) *core.ApprovalConfig {
	b, err := a.ensureApprovalBroker()
	if err != nil {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	return &core.ApprovalConfig{
		Command: exe,
		Args: []string{
			"-mcp-approve", "http://" + b.addr + "/approve/" + chatID,
			"-mcp-token", b.token,
		},
		Request: func(ctx context.Context, tool string, input map[string]any) (bool, error) {
			return b.request(ctx, chatID, tool, input)
		},
	}
}

// ResolveApproval answers a pending approval card. always=true
// remembers "allow" for this tool in this chat until the GUI exits.
func (a *App) ResolveApproval(id string, allow, always bool) {
	if a.detachedClient != nil {
		_ = a.detachedClient.resolveApproval(id, allow, always)
		return
	}
	a.approvalMu.Lock()
	b := a.approval
	a.approvalMu.Unlock()
	if b != nil {
		b.resolve(id, allow, always)
	}
}
