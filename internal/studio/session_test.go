package studio

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"git.jtsec.local/lab/PrAImate/internal/core"
)

type studioAdapter struct {
	mu    sync.Mutex
	shots []core.SingleShotOpts
	block bool
	probe func(context.Context) error
}

func (a *studioAdapter) Name() string { return "claude" }
func (a *studioAdapter) Available(ctx context.Context) error {
	if a.probe != nil {
		return a.probe(ctx)
	}
	return nil
}
func (a *studioAdapter) SupportsResume() bool { return false }
func (a *studioAdapter) SingleShot(ctx context.Context, o core.SingleShotOpts) (*core.Reply, error) {
	return a.SingleShotStream(ctx, o, nil)
}
func (a *studioAdapter) Resume(context.Context, string, core.ResumeOpts) (*core.Reply, error) {
	return nil, fmt.Errorf("unexpected resume")
}
func (a *studioAdapter) ResumeStream(ctx context.Context, id string, opts core.ResumeOpts, emit core.StreamHandler) (*core.Reply, error) {
	return a.Resume(ctx, id, opts)
}
func (a *studioAdapter) SingleShotStream(ctx context.Context, o core.SingleShotOpts, emit core.StreamHandler) (*core.Reply, error) {
	a.mu.Lock()
	a.shots = append(a.shots, o)
	a.mu.Unlock()
	if emit != nil {
		emit(core.StreamEvent{Type: "text", Text: "hello 🐒\n"})
	}
	if a.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &core.Reply{Text: "hello 🐒\n"}, nil
}
func fixture(t *testing.T) (*Server, *studioAdapter) {
	t.Helper()
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	c, cleanup := newTestCore(t)
	t.Cleanup(cleanup)
	ad := &studioAdapter{}
	old, _ := core.GetCLIAdapter(ad.Name())
	core.RegisterCLIAdapter(ad)
	t.Cleanup(func() {
		if old != nil {
			core.RegisterCLIAdapter(old)
		} else {
			core.UnregisterCLIAdapter(ad.Name())
		}
	})
	s := NewServer(c)
	t.Cleanup(func() { s.Close() })
	s.session.CLI = ad.Name()
	s.session.Workspace = t.TempDir()
	return s, ad
}
func rpcOK(t *testing.T, s *Server, method string, p any) any {
	t.Helper()
	r := s.dispatch(RPCRequest{JSONRPC: "2.0", ID: 1, Method: method, Params: p})
	if r.Error != nil {
		t.Fatalf("%s: %s", method, r.Error.Message)
	}
	return r.Result
}
func TestSessionClearAndPersist(t *testing.T) {
	s, _ := fixture(t)
	rpcOK(t, s, "session.update", map[string]any{"model": "custom", "tools": "edits"})
	rpcOK(t, s, "session.update", map[string]any{"model": "", "agentId": "", "tools": "safe", "mcpServers": []string{}})
	chat := rpcOK(t, s, "chats.create", map[string]string{"title": "test"}).(*core.Chat)
	if chat.Settings.Model != "" || chat.Settings.Tools != "" || !chat.Settings.ToolsConfigured || !chat.Settings.MCPConfigured {
		t.Fatalf("explicit clears lost: %+v", chat.Settings)
	}
	if chat.WorkspacePath != s.session.Workspace || chat.Settings.Surface != "studio-ide" {
		t.Fatal("chat not bound to workspace")
	}
}

func TestSlowCLIProbeDoesNotBlockSessionOrStop(t *testing.T) {
	s, ad := fixture(t)
	started := make(chan struct{})
	release := make(chan struct{})
	ad.probe = func(ctx context.Context) error {
		close(started)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	server, client := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- s.ServeStdio(server, server) }()
	t.Cleanup(func() { client.Close(); server.Close(); <-done })
	defer close(release)
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	encoder, decoder := json.NewEncoder(client), json.NewDecoder(client)
	if err := encoder.Encode(RPCRequest{JSONRPC: "2.0", ID: 1, Method: "clis.list"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("probe did not start")
	}
	for _, method := range []string{"system.status", "runs.cancel"} {
		if err := encoder.Encode(RPCRequest{JSONRPC: "2.0", ID: 2, Method: method}); err != nil {
			t.Fatal(err)
		}
		var response RPCResponse
		if err := decoder.Decode(&response); err != nil {
			t.Fatalf("%s blocked behind CLI probe: %v", method, err)
		}
		if response.ID != float64(2) || response.Error != nil {
			t.Fatalf("bad response: %+v", response)
		}
	}
}

func TestMCPSelectionCanReturnToInherited(t *testing.T) {
	s, _ := fixture(t)
	rpcOK(t, s, "session.update", map[string]any{"mcpServers": []string{}})
	chat := rpcOK(t, s, "chats.create", map[string]string{"title": "inheritance"}).(*core.Chat)
	rpcOK(t, s, "session.update", map[string]any{"mcpServers": nil})
	rpcOK(t, s, "chats.send", map[string]any{"chatId": chat.ID, "prompt": "hello"})
	stored, err := s.core.GetChat(context.Background(), chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Settings.MCPConfigured {
		t.Fatal("Inherit silently retained explicit None")
	}
}
func TestChatSendUsesCoreConfigurationAndHistory(t *testing.T) {
	s, ad := fixture(t)
	rpcOK(t, s, "session.update", map[string]any{"model": "custom", "tools": "edits"})
	chat := rpcOK(t, s, "chats.create", map[string]string{"title": "test"}).(*core.Chat)
	rpcOK(t, s, "chats.send", map[string]any{"chatId": chat.ID, "prompt": "explain", "context": map[string]string{"activeFile": filepath.Join(s.session.Workspace, "hello.go"), "content": "unsaved editor contents"}})
	ad.mu.Lock()
	shot := ad.shots[0]
	ad.mu.Unlock()
	if shot.Cwd != s.session.Workspace || shot.Model != "custom" || shot.Tools != "edits" || !strings.Contains(shot.Message, "unsaved editor contents") {
		t.Fatalf("incorrect launch: %+v", shot)
	}
	rpcOK(t, s, "session.update", map[string]any{"model": "", "tools": "safe"})
	rpcOK(t, s, "chats.send", map[string]any{"chatId": chat.ID, "prompt": "again"})
	messages := rpcOK(t, s, "chats.messages", map[string]string{"id": chat.ID}).([]core.Message)
	if len(messages) != 4 {
		t.Fatalf("want 4 persisted messages, got %d", len(messages))
	}
	stored, err := s.core.GetChat(context.Background(), chat.ID)
	if err != nil || stored.Settings.Model != "" || stored.Settings.Tools != "" {
		t.Fatalf("settings not updated: %v", err)
	}
}
func TestChatRejectsEditorContextOutsideSessionWorkspace(t *testing.T) {
	s, _ := fixture(t)
	chat := rpcOK(t, s, "chats.create", map[string]string{"title": "workspace boundary"}).(*core.Chat)
	response := s.dispatch(RPCRequest{JSONRPC: "2.0", ID: 1, Method: "chats.send", Params: map[string]any{
		"chatId": chat.ID, "prompt": "inspect", "context": map[string]string{"activeFile": filepath.Join(t.TempDir(), "outside.go"), "content": "secret"},
	}})
	if response.Error == nil || !strings.Contains(response.Error.Message, "different workspace") {
		t.Fatalf("outside context was accepted: %+v", response)
	}
}
func TestChatDoesNotReuseOtherSurfaceOrWorkspace(t *testing.T) {
	s, _ := fixture(t)
	other, err := s.core.CreateChat(context.Background(), core.CreateChatRequest{Title: "desktop", CLIAgent: "studio-test", WorkspacePath: s.session.Workspace})
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"chats.get", "chats.messages", "chats.delete", "chats.send"} {
		r := s.dispatch(RPCRequest{Method: method, Params: map[string]string{"id": other.ID, "chatId": other.ID, "prompt": "no"}})
		if r.Error == nil {
			t.Fatalf("%s accepted desktop chat", method)
		}
	}
	chat, err := s.createChat(context.Background(), "one")
	if err != nil {
		t.Fatal(err)
	}
	s.session.Workspace = t.TempDir()
	if _, err := s.ownedChat(context.Background(), chat.ID); err == nil {
		t.Fatal("accepted another workspace")
	}
	r := s.dispatch(RPCRequest{Method: "chats.send", Params: map[string]string{"prompt": "no implicit reuse"}})
	if r.Error == nil {
		t.Fatal("send without chat id must fail")
	}
}
func TestStudioStopDuringStream(t *testing.T) {
	s, ad := fixture(t)
	ad.block = true
	chat, err := s.createChat(context.Background(), "cancel")
	if err != nil {
		t.Fatal(err)
	}
	server, client := net.Pipe()
	defer client.Close()
	done := make(chan struct{})
	go func() { defer close(done); defer server.Close(); _ = s.ServeStdio(server, server) }()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	enc := json.NewEncoder(client)
	scanner := bufio.NewScanner(client)
	if err := enc.Encode(RPCRequest{JSONRPC: "2.0", ID: 1, Method: "chats.send", Params: map[string]string{"chatId": chat.ID, "prompt": "wait"}}); err != nil {
		t.Fatal(err)
	}
	if !scanner.Scan() || !strings.Contains(scanner.Text(), "run.event") {
		t.Fatal("no live event before response", scanner.Err())
	}
	if err := enc.Encode(RPCRequest{JSONRPC: "2.0", ID: 2, Method: "runs.cancel"}); err != nil {
		t.Fatal(err)
	}
	responses := map[float64]bool{}
	for len(responses) < 2 && scanner.Scan() {
		var msg map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			t.Fatal("interleaved JSON", err)
		}
		if id, ok := msg["id"].(float64); ok {
			responses[id] = true
		}
	}
	if !responses[1] || !responses[2] {
		t.Fatal("Stop did not unblock the run", scanner.Err())
	}
	client.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("connection did not close")
	}
}
func TestApprovalIsRealScopedAndFailsClosed(t *testing.T) {
	s, _ := fixture(t)
	// Capture the approval notification without blocking its sender.
	s.writer = io.Discard
	cfg := s.approvalProvider("run")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	decision := make(chan bool, 1)
	go func() { allow, _ := cfg.Request(ctx, "write", map[string]any{"path": "example"}); decision <- allow }()
	var id string
	deadline := time.After(time.Second)
	for id == "" {
		s.mu.Lock()
		for key := range s.approvals {
			id = key
		}
		s.mu.Unlock()
		select {
		case <-deadline:
			t.Fatal("no pending approval")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	other := NewServer(s.core)
	defer other.Close()
	if other.dispatch(RPCRequest{Method: "runs.approve", Params: map[string]string{"approvalId": id}}).Error == nil {
		t.Fatal("foreign session approved")
	}
	rpcOK(t, s, "runs.approve", map[string]string{"approvalId": id})
	select {
	case allow := <-decision:
		if !allow {
			t.Fatal("approval did not reach execution")
		}
	case <-time.After(time.Second):
		t.Fatal("blocked")
	}
	if s.dispatch(RPCRequest{Method: "runs.approve", Params: map[string]string{"approvalId": id}}).Error == nil {
		t.Fatal("approval replayed")
	}
	cancel()
	allow, err := cfg.Request(ctx, "write", nil)
	if allow || err == nil {
		t.Fatal("cancelled request must deny")
	}
}
func TestLoopbackAuthenticationAndSessionIsolation(t *testing.T) {
	s, _ := fixture(t)
	endpoint, token, err := s.StartLocal()
	if err != nil {
		t.Fatal(err)
	}
	address := strings.TrimPrefix(endpoint, "tcp://")
	request := func(conn net.Conn, id int, method string, params any) RPCResponse {
		t.Helper()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		if err := json.NewEncoder(conn).Encode(RPCRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}); err != nil {
			t.Fatal(err)
		}
		var resp RPCResponse
		if err := json.NewDecoder(conn).Decode(&resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}
	first, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if request(first, 1, "system.version", nil).Error == nil {
		t.Fatal("unauthenticated request succeeded")
	}
	if request(first, 2, "system.initialize", map[string]string{"token": "wrong"}).Error == nil {
		t.Fatal("bad token accepted")
	}
	if request(first, 3, "system.initialize", map[string]string{"token": token, "workspace": s.session.Workspace}).Error != nil {
		t.Fatal("authentication failed")
	}
	second, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if request(second, 4, "system.version", nil).Error == nil {
		t.Fatal("authentication leaked to another client")
	}
	// Closing a stdio server must not delete the daemon socket.
	sentinel := filepath.Join(t.TempDir(), "praimate.sock")
	if err := os.WriteFile(sentinel, []byte("owned by another server"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PRAIMATE_SOCK", sentinel)
	child := NewServer(s.core)
	child.Close()
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatal("unowned socket removed")
	}
}
func TestLaunchEnvironmentReplacesWindowsCaseVariants(t *testing.T) {
	out := studioEnvironment([]string{"Path=C:\\Tools", "praimate_sock=old", "PRAIMATE_BIN=old"}, map[string]string{"PRAIMATE_SOCK": "new", "PRAIMATE_BIN": `C:\Program Files\PrAImate\praimate.exe`})
	if len(out) != 3 || out[0] != "Path=C:\\Tools" {
		t.Fatalf("duplicate or lost environment: %v", out)
	}
}
