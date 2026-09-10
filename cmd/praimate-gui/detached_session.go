package main

// Detached chat and terminal windows use a second, lightweight Wails process.
// The main process remains the only owner of the encrypted database, Core,
// streamed turns, and PTYs. A token-authenticated loopback broker carries a
// deliberately small RPC/event surface between the two processes.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"git.jtsec.local/lab/PrAImate/internal/core"
)

const (
	detachedWindowLimit   = 4
	detachedReadyTimeout  = 15 * time.Second
	detachedRequestLimit  = 16 << 20
	detachedResponseLimit = 32 << 20
	detachedEventLimit    = 4 << 20
)

type detachedMode struct {
	active    bool
	kind      string
	sessionID string
	windowID  string
	brokerURL string
	token     string
	title     string
	folder    string
}

var detachedProcessMode detachedMode

func detachedModeFromEnvironment() detachedMode {
	m := detachedMode{
		active:    os.Getenv("PRAIMATE_DETACHED_KIND") != "",
		kind:      os.Getenv("PRAIMATE_DETACHED_KIND"),
		sessionID: os.Getenv("PRAIMATE_DETACHED_SESSION"),
		windowID:  os.Getenv("PRAIMATE_DETACHED_WINDOW"),
		brokerURL: os.Getenv("PRAIMATE_DETACHED_BROKER"),
		token:     os.Getenv("PRAIMATE_DETACHED_TOKEN"),
		title:     os.Getenv("PRAIMATE_DETACHED_TITLE"),
		folder:    os.Getenv("PRAIMATE_DETACHED_FOLDER"),
	}
	broker, brokerErr := url.Parse(m.brokerURL)
	_, tokenErr := hex.DecodeString(m.token)
	_, windowErr := hex.DecodeString(m.windowID)
	validBroker := brokerErr == nil && broker.Scheme == "http" && broker.Hostname() == "127.0.0.1" &&
		broker.Port() != "" && broker.User == nil && broker.Path == "" && broker.RawQuery == "" && broker.Fragment == ""
	if (m.kind != "chat" && m.kind != "terminal" && m.kind != "studio") || m.sessionID == "" ||
		len(m.windowID) != 24 || windowErr != nil || !validBroker || len(m.token) != 64 || tokenErr != nil {
		return detachedMode{}
	}
	if strings.TrimSpace(m.title) == "" {
		m.title = "Detached " + m.kind
	}
	if m.kind == "studio" && strings.TrimSpace(m.folder) == "" {
		return detachedMode{}
	}
	return m
}

// DetachedModeInfo is read before secure-storage initialisation. Detached
// children use it to render their focused shell without prompting for the DB
// password or starting any main-process service.
type DetachedModeInfo struct {
	Active    bool   `json:"active"`
	Kind      string `json:"kind,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
	Title     string `json:"title,omitempty"`
	Folder    string `json:"folder,omitempty"`
}

func (a *App) DetachedMode() DetachedModeInfo {
	m := detachedProcessMode
	return DetachedModeInfo{Active: m.active, Kind: m.kind, SessionID: m.sessionID, Title: m.title, Folder: m.folder}
}

type detachedWireEvent struct {
	Name    string          `json:"name"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type detachedWindow struct {
	id        string
	kind      string
	sessionID string
	title     string
	folder    string
	cmd       *exec.Cmd
	events    chan detachedWireEvent
	ready     chan struct{}
	done      chan struct{}
	readyOnce sync.Once
	doneOnce  sync.Once
	resync    bool
}

type detachedCoordinator struct {
	app *App

	mu      sync.Mutex
	windows map[string]*detachedWindow
	server  *http.Server
	ln      net.Listener
	addr    string
	token   string
	closed  bool
}

func newDetachedCoordinator(app *App) *detachedCoordinator {
	return &detachedCoordinator{app: app, windows: map[string]*detachedWindow{}}
}

func randomHex(bytesLen int) (string, error) {
	raw := make([]byte, bytesLen)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func (d *detachedCoordinator) ensureServerLocked() error {
	if d.server != nil {
		return nil
	}
	token, err := randomHex(32)
	if err != nil {
		return fmt.Errorf("create detached-window token: %w", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("start detached-window broker: %w", err)
	}
	d.token = token
	d.addr = "http://" + ln.Addr().String()
	d.ln = ln
	mux := http.NewServeMux()
	mux.HandleFunc("/events", d.handleEvents)
	mux.HandleFunc("/rpc", d.handleRPC)
	d.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	go func() { _ = d.server.Serve(ln) }()
	return nil
}

func (d *detachedCoordinator) authorised(r *http.Request) bool {
	got := r.Header.Get("X-Praimate-Token")
	d.mu.Lock()
	want := d.token
	d.mu.Unlock()
	return len(got) == len(want) && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// DetachSession opens a focused child window after validating that the
// authoritative session is live. The call returns only after the child has
// connected to the broker, so the frontend can safely release its renderer.
func (a *App) DetachSession(kind, sessionID, title string) error {
	if a.detached == nil {
		return errors.New("detached windows can only be opened from the main PrAImate window")
	}
	return a.detached.open(kind, sessionID, title)
}

func (d *detachedCoordinator) open(kind, sessionID, title string) error {
	return d.openWithFolder(kind, sessionID, title, "")
}

func (d *detachedCoordinator) openWithFolder(kind, sessionID, title, folder string) error {
	if kind != "chat" && kind != "terminal" && kind != "studio" {
		return fmt.Errorf("unsupported detached session kind %q", kind)
	}
	if strings.TrimSpace(sessionID) == "" {
		return errors.New("session id is required")
	}
	if kind == "terminal" {
		if d.app.terms == nil {
			return errors.New("terminal manager is unavailable")
		}
		if _, err := d.app.terms.snapshot(sessionID); err != nil {
			return err
		}
	} else {
		c, err := d.app.requireCore()
		if err != nil {
			return err
		}
		if _, err := c.GetChat(d.app.ctx, sessionID); err != nil {
			return err
		}
		d.app.approvalMu.Lock()
		approval := d.app.approval
		d.app.approvalMu.Unlock()
		if approval != nil && approval.hasPending(sessionID) {
			return errors.New("answer the pending approval before detaching this chat")
		}
	}
	if kind == "studio" && strings.TrimSpace(folder) == "" {
		return errors.New("studio folder is required")
	}

	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return errors.New("detached-window broker is closed")
	}
	if len(d.windows) >= detachedWindowLimit {
		d.mu.Unlock()
		return fmt.Errorf("at most %d detached windows may be open", detachedWindowLimit)
	}
	for _, w := range d.windows {
		if w.kind == kind && w.sessionID == sessionID {
			d.mu.Unlock()
			return errors.New("this session is already detached")
		}
	}
	if err := d.ensureServerLocked(); err != nil {
		d.mu.Unlock()
		return err
	}
	windowID, err := randomHex(12)
	if err != nil {
		d.mu.Unlock()
		return err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Detached " + kind
	}
	titleRunes := []rune(title)
	if len(titleRunes) > 120 {
		title = string(titleRunes[:120])
	}
	w := &detachedWindow{
		id: windowID, kind: kind, sessionID: sessionID, title: title, folder: folder,
		events: make(chan detachedWireEvent, 256), ready: make(chan struct{}), done: make(chan struct{}),
	}
	d.windows[windowID] = w
	addr, token := d.addr, d.token
	d.mu.Unlock()

	exe, err := os.Executable()
	if err != nil {
		d.remove(windowID)
		return err
	}
	cmd := exec.Command(exe, "-detached-window")
	cmd.Env = append(os.Environ(),
		"PRAIMATE_DETACHED_KIND="+kind,
		"PRAIMATE_DETACHED_SESSION="+sessionID,
		"PRAIMATE_DETACHED_WINDOW="+windowID,
		"PRAIMATE_DETACHED_BROKER="+addr,
		"PRAIMATE_DETACHED_TOKEN="+token,
		"PRAIMATE_DETACHED_TITLE="+title,
		"PRAIMATE_DETACHED_FOLDER="+folder,
	)
	if err := cmd.Start(); err != nil {
		d.remove(windowID)
		return fmt.Errorf("open detached window: %w", err)
	}
	d.mu.Lock()
	if current := d.windows[windowID]; current != nil {
		current.cmd = cmd
	}
	d.mu.Unlock()
	go func() {
		_ = cmd.Wait()
		w.doneOnce.Do(func() { close(w.done) })
		d.remove(windowID)
		if d.app.ctx != nil {
			wailsruntime.EventsEmit(d.app.ctx, "praimate:detached-windows", d.list())
		}
	}()

	timer := time.NewTimer(detachedReadyTimeout)
	defer timer.Stop()
	select {
	case <-w.ready:
		wailsruntime.EventsEmit(d.app.ctx, "praimate:detached-windows", d.list())
		return nil
	case <-w.done:
		return errors.New("detached window exited before it became ready")
	case <-timer.C:
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		d.remove(windowID)
		return errors.New("detached window did not become ready in time")
	}
}

func (d *detachedCoordinator) openExternal(kind, sessionID, title string, cmd *exec.Cmd) error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return errors.New("window coordinator is closed")
	}
	if len(d.windows) >= detachedWindowLimit {
		d.mu.Unlock()
		return fmt.Errorf("at most %d secondary windows may be open", detachedWindowLimit)
	}
	for _, current := range d.windows {
		if current.kind == kind && current.sessionID == sessionID {
			d.mu.Unlock()
			return errors.New("this session already has an open window")
		}
	}
	id, err := randomHex(12)
	if err != nil {
		d.mu.Unlock()
		return err
	}
	w := &detachedWindow{id: id, kind: kind, sessionID: sessionID, title: title, cmd: cmd, done: make(chan struct{})}
	d.windows[id] = w
	d.mu.Unlock()
	if err := cmd.Start(); err != nil {
		d.remove(id)
		return err
	}
	go func() {
		_ = cmd.Wait()
		w.doneOnce.Do(func() { close(w.done) })
		d.remove(id)
		if d.app.ctx != nil {
			wailsruntime.EventsEmit(d.app.ctx, "praimate:detached-windows", d.list())
		}
	}()
	wailsruntime.EventsEmit(d.app.ctx, "praimate:detached-windows", d.list())
	return nil
}

func (d *detachedCoordinator) remove(id string) {
	d.mu.Lock()
	w := d.windows[id]
	delete(d.windows, id)
	d.mu.Unlock()
	if w != nil && (w.kind == "chat" || w.kind == "studio") {
		d.app.approvalMu.Lock()
		approval := d.app.approval
		d.app.approvalMu.Unlock()
		if approval != nil {
			approval.denyScope(w.sessionID)
		}
	}
}

type DetachedWindowInfo struct {
	Kind      string `json:"kind"`
	SessionID string `json:"sessionId"`
	Title     string `json:"title"`
}

func (d *detachedCoordinator) list() []DetachedWindowInfo {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]DetachedWindowInfo, 0, len(d.windows))
	for _, w := range d.windows {
		out = append(out, DetachedWindowInfo{Kind: w.kind, SessionID: w.sessionID, Title: w.title})
	}
	return out
}

func (a *App) DetachedWindows() []DetachedWindowInfo {
	if a.detached == nil {
		return nil
	}
	return a.detached.list()
}

func (a *App) DetachedSessionActive() bool {
	if a.detachedClient == nil {
		return false
	}
	var active bool
	if err := a.detachedClient.rpc("session.active", nil, &active); err != nil {
		return false
	}
	return active
}

func (a *App) DetachedRendererReady() error {
	if a.detachedClient == nil {
		return errors.New("not a detached window")
	}
	a.detachedClient.markRendererReady()
	return a.detachedClient.rpc("window.ready", nil, nil)
}

func (a *App) beforeClose(ctx context.Context) bool {
	if a.detachedClient != nil || a.detached == nil {
		return false
	}
	windows := a.detached.list()
	if len(windows) == 0 {
		return false
	}
	wailsruntime.EventsEmit(ctx, "praimate:close-blocked", map[string]any{
		"count": len(windows), "windows": windows,
	})
	return true
}

func (a *App) emitTerminalEvent(name string, payload any) {
	wailsruntime.EventsEmit(a.ctx, name, payload)
	if a.detached == nil {
		return
	}
	id := strings.TrimPrefix(strings.TrimPrefix(name, "term:data:"), "term:exit:")
	a.detached.publish("terminal", id, name, payload)
}

func (d *detachedCoordinator) publish(kind, sessionID, name string, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	event := detachedWireEvent{Name: name, Payload: raw}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, w := range d.windows {
		if w.kind != kind || w.sessionID != sessionID {
			continue
		}
		if w.resync {
			continue
		}
		select {
		case w.events <- event:
		default:
			// A stalled renderer must never block a CLI or grow memory without
			// bound. Terminals can reconstruct their byte stream from the
			// offset-addressed snapshot. Chats retain the newest event so a
			// completion or approval cannot be hidden behind stale text chunks.
			if kind == "terminal" {
				for len(w.events) > 0 {
					<-w.events
				}
				w.events <- detachedWireEvent{Name: "praimate:detached-resync"}
				w.resync = true
				continue
			}
			select {
			case <-w.events:
			default:
			}
			select {
			case w.events <- event:
			default:
			}
		}
	}
}

func (d *detachedCoordinator) windowForRequest(r *http.Request) (*detachedWindow, error) {
	if !d.authorised(r) {
		return nil, errors.New("forbidden")
	}
	id := r.URL.Query().Get("window")
	d.mu.Lock()
	w := d.windows[id]
	d.mu.Unlock()
	if w == nil {
		return nil, errors.New("unknown detached window")
	}
	return w, nil
}

func (d *detachedCoordinator) handleEvents(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w, err := d.windowForRequest(r)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusForbidden)
		return
	}
	flusher, ok := rw.(http.Flusher)
	if !ok {
		http.Error(rw, "streaming unavailable", http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "application/x-ndjson")
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("X-Content-Type-Options", "nosniff")
	enc := json.NewEncoder(rw)
	_ = enc.Encode(detachedWireEvent{Name: "praimate:detached-connected"})
	flusher.Flush()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case event := <-w.events:
			if err := enc.Encode(event); err != nil {
				return
			}
			if event.Name == "praimate:detached-resync" {
				d.mu.Lock()
				w.resync = false
				d.mu.Unlock()
			}
			flusher.Flush()
		case <-ticker.C:
			if err := enc.Encode(detachedWireEvent{Name: "praimate:detached-heartbeat"}); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

type detachedRPCRequest struct {
	Method string          `json:"method"`
	Body   json.RawMessage `json:"body,omitempty"`
}

type detachedRPCResponse struct {
	OK    bool   `json:"ok"`
	Data  any    `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
}

func (d *detachedCoordinator) handleRPC(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w, err := d.windowForRequest(r)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(rw, r.Body, detachedRequestLimit)
	var req detachedRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		d.writeRPC(rw, nil, fmt.Errorf("decode request: %w", err))
		return
	}
	data, callErr := d.call(w, req)
	d.writeRPC(rw, data, callErr)
}

func (d *detachedCoordinator) writeRPC(rw http.ResponseWriter, data any, err error) {
	rw.Header().Set("Content-Type", "application/json")
	rw.Header().Set("Cache-Control", "no-store")
	res := detachedRPCResponse{OK: err == nil, Data: data}
	if err != nil {
		res.Error = err.Error()
	}
	_ = json.NewEncoder(rw).Encode(res)
}

func decodeRPCBody(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, dst)
}

func (d *detachedCoordinator) call(w *detachedWindow, req detachedRPCRequest) (any, error) {
	switch req.Method {
	case "window.ready":
		w.readyOnce.Do(func() { close(w.ready) })
		return nil, nil
	case "session.active":
		if w.kind == "terminal" {
			_, err := d.app.terms.snapshot(w.sessionID)
			return err == nil, nil
		}
		d.app.chatCancelMu.Lock()
		_, active := d.app.chatCancels[w.sessionID]
		d.app.chatCancelMu.Unlock()
		return active, nil
	case "chat.messages":
		if w.kind != "chat" && w.kind != "studio" {
			return nil, errors.New("operation is outside this window's scope")
		}
		return d.app.ChatMessages(w.sessionID)
	case "chat.list":
		if w.kind != "studio" {
			return nil, errors.New("operation is outside this window's scope")
		}
		return d.app.ListChats()
	case "agent.list":
		if w.kind != "studio" {
			return nil, errors.New("operation is outside this window's scope")
		}
		return d.app.ListAgents()
	case "chat.skills":
		if w.kind != "studio" {
			return nil, errors.New("operation is outside this window's scope")
		}
		return d.app.ChatSkills(w.sessionID), nil
	case "chat.skills.set":
		if w.kind != "studio" {
			return nil, errors.New("operation is outside this window's scope")
		}
		var ids []string
		if err := decodeRPCBody(req.Body, &ids); err != nil {
			return nil, err
		}
		return nil, d.app.SetChatSkills(w.sessionID, ids)
	case "skills.v2.versions", "skills.v2.build", "chat.skills.v2", "chat.skills.v2.preview", "chat.skills.v2.set":
		if w.kind != "chat" && w.kind != "studio" {
			return nil, errors.New("operation is outside this window's scope")
		}
		if req.Method == "skills.v2.versions" {
			return d.app.InstalledSkillVersionsV2()
		}
		if req.Method == "chat.skills.v2" {
			return d.app.ChatSkillsV2(w.sessionID)
		}
		var body string
		if err := decodeRPCBody(req.Body, &body); err != nil {
			return nil, err
		}
		switch req.Method {
		case "skills.v2.build":
			return d.app.BuildInstalledSkillSelectionV2(body)
		case "chat.skills.v2.preview":
			return d.app.PreviewChatSkillsV2(w.sessionID, body)
		default:
			return nil, d.app.SetChatSkillsV2(w.sessionID, body)
		}
	case "chat.send":
		if w.kind != "chat" && w.kind != "studio" {
			return nil, errors.New("operation is outside this window's scope")
		}
		var body struct {
			Message     string   `json:"message"`
			Attachments []string `json:"attachments"`
		}
		if err := decodeRPCBody(req.Body, &body); err != nil {
			return nil, err
		}
		return d.app.SendChatStream(w.sessionID, body.Message, body.Attachments)
	case "chat.cancel":
		if w.kind != "chat" && w.kind != "studio" {
			return nil, errors.New("operation is outside this window's scope")
		}
		d.app.CancelChatTurn(w.sessionID)
		return nil, nil
	case "chat.command":
		if w.kind != "chat" && w.kind != "studio" {
			return nil, errors.New("operation is outside this window's scope")
		}
		var body struct {
			Command string `json:"command"`
		}
		if err := decodeRPCBody(req.Body, &body); err != nil {
			return nil, err
		}
		return d.app.RunChatCommand(w.sessionID, body.Command)
	case "chat.approval":
		if w.kind != "chat" && w.kind != "studio" {
			return nil, errors.New("operation is outside this window's scope")
		}
		var body struct {
			ID     string `json:"id"`
			Allow  bool   `json:"allow"`
			Always bool   `json:"always"`
		}
		if err := decodeRPCBody(req.Body, &body); err != nil {
			return nil, err
		}
		d.app.approvalMu.Lock()
		approval := d.app.approval
		d.app.approvalMu.Unlock()
		if approval == nil || !approval.resolveScoped(w.sessionID, body.ID, body.Allow, body.Always) {
			return nil, errors.New("approval is no longer pending for this chat")
		}
		return nil, nil
	case "editor.list":
		if w.kind != "studio" {
			return nil, errors.New("operation is outside this window's scope")
		}
		return d.withEditorFolder(w.folder, func() (any, error) { return d.app.EditorListFiles() })
	case "editor.read":
		if w.kind != "studio" {
			return nil, errors.New("operation is outside this window's scope")
		}
		var body struct {
			Path string `json:"path"`
		}
		if err := decodeRPCBody(req.Body, &body); err != nil {
			return nil, err
		}
		return d.withEditorFolder(w.folder, func() (any, error) { return d.app.EditorReadFile(body.Path) })
	case "editor.write":
		if w.kind != "studio" {
			return nil, errors.New("operation is outside this window's scope")
		}
		var body struct{ Path, Content string }
		if err := decodeRPCBody(req.Body, &body); err != nil {
			return nil, err
		}
		return nil, d.withEditorFolderErr(w.folder, func() error { return d.app.EditorWriteFile(body.Path, body.Content) })
	case "editor.create":
		if w.kind != "studio" {
			return nil, errors.New("operation is outside this window's scope")
		}
		var body struct {
			Path string `json:"path"`
		}
		if err := decodeRPCBody(req.Body, &body); err != nil {
			return nil, err
		}
		return d.withEditorFolder(w.folder, func() (any, error) { return d.app.EditorCreateFile(body.Path) })
	case "editor.delete":
		if w.kind != "studio" {
			return nil, errors.New("operation is outside this window's scope")
		}
		var body struct {
			Path string `json:"path"`
		}
		if err := decodeRPCBody(req.Body, &body); err != nil {
			return nil, err
		}
		return nil, d.withEditorFolderErr(w.folder, func() error { return d.app.EditorDeleteFile(body.Path) })
	case "editor.rename":
		if w.kind != "studio" {
			return nil, errors.New("operation is outside this window's scope")
		}
		var body struct{ Src, Dst string }
		if err := decodeRPCBody(req.Body, &body); err != nil {
			return nil, err
		}
		return d.withEditorFolder(w.folder, func() (any, error) { return d.app.EditorRenameFile(body.Src, body.Dst) })
	case "editor.open-folder":
		if w.kind != "studio" {
			return nil, errors.New("operation is outside this window's scope")
		}
		return nil, openPathInFileManager(w.folder)
	case "terminal.snapshot":
		if w.kind != "terminal" {
			return nil, errors.New("operation is outside this window's scope")
		}
		return d.app.GetTerminalSnapshot(w.sessionID)
	case "terminal.write":
		if w.kind != "terminal" {
			return nil, errors.New("operation is outside this window's scope")
		}
		var body struct {
			Data string `json:"data"`
		}
		if err := decodeRPCBody(req.Body, &body); err != nil {
			return nil, err
		}
		return nil, d.app.WriteTerminal(w.sessionID, body.Data)
	case "terminal.resize":
		if w.kind != "terminal" {
			return nil, errors.New("operation is outside this window's scope")
		}
		var body struct {
			Cols int `json:"cols"`
			Rows int `json:"rows"`
		}
		if err := decodeRPCBody(req.Body, &body); err != nil {
			return nil, err
		}
		return nil, d.app.ResizeTerminal(w.sessionID, body.Cols, body.Rows)
	case "terminal.close":
		if w.kind != "terminal" {
			return nil, errors.New("operation is outside this window's scope")
		}
		d.app.CloseTerminal(w.sessionID)
		return nil, nil
	default:
		return nil, fmt.Errorf("unsupported detached RPC method %q", req.Method)
	}
}

func (d *detachedCoordinator) withEditorFolder(folder string, fn func() (any, error)) (any, error) {
	d.app.editorScopeMu.Lock()
	defer d.app.editorScopeMu.Unlock()
	old := editorFolder
	editorFolder = folder
	defer func() { editorFolder = old }()
	return fn()
}
func (d *detachedCoordinator) withEditorFolderErr(folder string, fn func() error) error {
	_, err := d.withEditorFolder(folder, func() (any, error) { return nil, fn() })
	return err
}

func (d *detachedCoordinator) close() {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	server := d.server
	d.mu.Unlock()
	if server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}
}

// detachedClient is the child-process side of the broker. It intentionally
// has no Store/Core/PTY references.
type detachedClient struct {
	mode    detachedMode
	http    *http.Client
	cancel  context.CancelFunc
	mu      sync.Mutex
	closed  bool
	ready   bool
	emit    func(string, any)
	pending []detachedWireEvent
}

func newDetachedClient(mode detachedMode) *detachedClient {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil // loopback IPC must never traverse a configured proxy
	return &detachedClient{mode: mode, http: &http.Client{
		Transport: transport,
		Timeout:   0, // the event stream and model turns are intentionally long-lived
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

func (c *detachedClient) start(ctx context.Context, emit func(string, any)) {
	streamCtx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.cancel = cancel
	c.emit = emit
	c.mu.Unlock()
	go c.eventLoop(streamCtx)
}

func (c *detachedClient) eventLoop(ctx context.Context) {
	backoff := 100 * time.Millisecond
	for {
		if ctx.Err() != nil {
			return
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
			c.mode.brokerURL+"/events?window="+c.mode.windowID, nil)
		req.Header.Set("X-Praimate-Token", c.mode.token)
		res, err := c.http.Do(req)
		if err == nil && res.StatusCode == http.StatusOK {
			backoff = 100 * time.Millisecond
			scanner := bufio.NewScanner(res.Body)
			scanner.Buffer(make([]byte, 4096), detachedEventLimit)
			for scanner.Scan() {
				var event detachedWireEvent
				if json.Unmarshal(scanner.Bytes(), &event) != nil {
					continue
				}
				if event.Name == "praimate:detached-heartbeat" {
					continue
				}
				var payload any
				if len(event.Payload) > 0 && string(event.Payload) != "null" {
					_ = json.Unmarshal(event.Payload, &payload)
				}
				c.dispatch(event.Name, payload)
			}
			_ = res.Body.Close()
		} else if res != nil {
			_ = res.Body.Close()
		}
		if ctx.Err() != nil {
			return
		}
		c.dispatch("praimate:detached-disconnected", map[string]string{"message": "Connection to the main PrAImate window was lost. Keep the main app open."})
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if backoff < 2*time.Second {
			backoff *= 2
		}
	}
}

func (c *detachedClient) dispatch(name string, payload any) {
	c.mu.Lock()
	if !c.ready {
		raw, _ := json.Marshal(payload)
		if len(c.pending) == 256 {
			c.pending = c.pending[1:]
		}
		c.pending = append(c.pending, detachedWireEvent{Name: name, Payload: raw})
		c.mu.Unlock()
		return
	}
	emit := c.emit
	c.mu.Unlock()
	if emit != nil {
		emit(name, payload)
	}
}

func (c *detachedClient) markRendererReady() {
	for {
		c.mu.Lock()
		if c.ready {
			c.mu.Unlock()
			return
		}
		if len(c.pending) == 0 {
			c.ready = true
			c.mu.Unlock()
			return
		}
		pending := append([]detachedWireEvent(nil), c.pending...)
		c.pending = nil
		emit := c.emit
		c.mu.Unlock()
		if emit == nil {
			continue
		}
		for _, event := range pending {
			var payload any
			if len(event.Payload) > 0 && string(event.Payload) != "null" {
				_ = json.Unmarshal(event.Payload, &payload)
			}
			emit(event.Name, payload)
		}
	}
}

func (c *detachedClient) rpc(method string, body any, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	reqBody, err := json.Marshal(detachedRPCRequest{Method: method, Body: raw})
	if err != nil {
		return err
	}
	requestCtx := context.Background()
	cancel := func() {}
	if method != "chat.send" {
		requestCtx, cancel = context.WithTimeout(requestCtx, 5*time.Second)
	}
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, c.mode.brokerURL+"/rpc?window="+c.mode.windowID, bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Praimate-Token", c.mode.token)
	res, err := c.http.Do(req)
	if err != nil {
		return errors.New("main PrAImate window is unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return fmt.Errorf("detached broker: %s", strings.TrimSpace(string(body)))
	}
	var envelope struct {
		OK    bool            `json:"ok"`
		Data  json.RawMessage `json:"data"`
		Error string          `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, detachedResponseLimit)).Decode(&envelope); err != nil {
		return err
	}
	if !envelope.OK {
		return errors.New(envelope.Error)
	}
	if out != nil && len(envelope.Data) > 0 && string(envelope.Data) != "null" {
		return json.Unmarshal(envelope.Data, out)
	}
	return nil
}

func (c *detachedClient) chatMessages(id string) ([]core.Message, error) {
	if id != c.mode.sessionID {
		return nil, errors.New("chat is outside this detached window")
	}
	var out []core.Message
	return out, c.rpc("chat.messages", nil, &out)
}
func (c *detachedClient) listChats() ([]core.Chat, error) {
	var out []core.Chat
	return out, c.rpc("chat.list", nil, &out)
}
func (c *detachedClient) listAgents() ([]core.Agent, error) {
	var out []core.Agent
	return out, c.rpc("agent.list", nil, &out)
}
func (c *detachedClient) chatSkills(id string) []string {
	if id != c.mode.sessionID {
		return nil
	}
	var out []string
	_ = c.rpc("chat.skills", nil, &out)
	return out
}
func (c *detachedClient) setChatSkills(id string, ids []string) error {
	if id != c.mode.sessionID {
		return errors.New("chat is outside this detached window")
	}
	return c.rpc("chat.skills.set", ids, nil)
}

func (c *detachedClient) sendChatStream(id, message string, attachments []string) (*core.ChatTurn, error) {
	if id != c.mode.sessionID {
		return nil, errors.New("chat is outside this detached window")
	}
	var out core.ChatTurn
	err := c.rpc("chat.send", map[string]any{"message": message, "attachments": attachments}, &out)
	return &out, err
}

func (c *detachedClient) cancelChatTurn(id string) error {
	if id != c.mode.sessionID {
		return errors.New("chat is outside this detached window")
	}
	return c.rpc("chat.cancel", nil, nil)
}

func (c *detachedClient) runChatCommand(id, command string) (*core.ChatTurn, error) {
	if id != c.mode.sessionID {
		return nil, errors.New("chat is outside this detached window")
	}
	var out core.ChatTurn
	err := c.rpc("chat.command", map[string]string{"command": command}, &out)
	return &out, err
}

func (c *detachedClient) resolveApproval(id string, allow, always bool) error {
	return c.rpc("chat.approval", map[string]any{"id": id, "allow": allow, "always": always}, nil)
}

func (c *detachedClient) editorListFiles() ([]string, error) {
	var out []string
	return out, c.rpc("editor.list", nil, &out)
}
func (c *detachedClient) editorReadFile(path string) (string, error) {
	var out string
	return out, c.rpc("editor.read", map[string]string{"path": path}, &out)
}
func (c *detachedClient) editorWriteFile(path, content string) error {
	return c.rpc("editor.write", map[string]string{"Path": path, "Content": content}, nil)
}
func (c *detachedClient) editorCreateFile(path string) (string, error) {
	var out string
	return out, c.rpc("editor.create", map[string]string{"path": path}, &out)
}
func (c *detachedClient) editorDeleteFile(path string) error {
	return c.rpc("editor.delete", map[string]string{"path": path}, nil)
}
func (c *detachedClient) editorRenameFile(src, dst string) (string, error) {
	var out string
	return out, c.rpc("editor.rename", map[string]string{"Src": src, "Dst": dst}, &out)
}
func (c *detachedClient) openEditorFolder() error { return c.rpc("editor.open-folder", nil, nil) }

func (c *detachedClient) terminalSnapshot(id string) (TerminalSnapshot, error) {
	if id != c.mode.sessionID {
		return TerminalSnapshot{}, errors.New("terminal is outside this detached window")
	}
	var out TerminalSnapshot
	return out, c.rpc("terminal.snapshot", nil, &out)
}

func (c *detachedClient) terminalWrite(id, data string) error {
	if id != c.mode.sessionID {
		return errors.New("terminal is outside this detached window")
	}
	return c.rpc("terminal.write", map[string]string{"data": data}, nil)
}

func (c *detachedClient) terminalResize(id string, cols, rows int) error {
	if id != c.mode.sessionID {
		return errors.New("terminal is outside this detached window")
	}
	return c.rpc("terminal.resize", map[string]int{"cols": cols, "rows": rows}, nil)
}

func (c *detachedClient) terminalClose(id string) error {
	if id != c.mode.sessionID {
		return errors.New("terminal is outside this detached window")
	}
	return c.rpc("terminal.close", nil, nil)
}

func (c *detachedClient) close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	cancel := c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
