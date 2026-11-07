//go:build server

package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/remote"
	"github.com/sPROFFEs/PrAImate/internal/store"
)

func TestServerDoesNotPassAdminCredentialsToChildCLIs(t *testing.T) {
	t.Setenv("PRAIMATE_REMOTE_LISTEN", "127.0.0.1:8844")
	t.Setenv("PRAIMATE_REMOTE_TOKEN", "synthetic-test-credential-with-32-characters")
	e := newEditionServices()
	if e.config.Listen == "" || e.config.Token == "" {
		t.Fatal("configuration not captured")
	}
	if os.Getenv("PRAIMATE_REMOTE_TOKEN") != "" || os.Getenv("PRAIMATE_REMOTE_LISTEN") != "" {
		t.Fatal("remote credentials/configuration inherited by child processes")
	}
}

func TestRemoteActivityRestoresPendingApprovalsWithoutStartingServices(t *testing.T) {
	a := &App{chatCancels: map[string]context.CancelFunc{"chat-live": func() {}}, approval: &approvalBroker{requests: map[string]ApprovalRequest{"ap-1": {ID: "ap-1", ChatID: "chat-live", Tool: "command"}}}}
	state := a.RemoteActivity()
	if len(state["chats"].([]string)) != 1 || len(state["approvals"].([]ApprovalRequest)) != 1 {
		t.Fatal("active state was not recovered")
	}
	if len(a.approval.requests) != 1 {
		t.Fatal("reading state consumed the pending approval")
	}
	fresh := &App{}
	fresh.RemoteActivity()
	if fresh.approval != nil {
		t.Fatal("read-only status started a broker")
	}
}

func TestRemoteStudioCreatesAPersistentChatWithTheSelectedExecutionSettings(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PRAIMATE_HOME", root)
	st, err := store.InitializeWithPassword(filepath.Join(root, "db.sqlite"), "remote-studio-test-password")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, err := core.New(core.Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	core.RegisterAllCLIAdapters()
	a := &App{ctx: context.Background(), core: c}
	result, err := a.prepareRemoteProject(root, "", "codex", "gpt-6.1-sol", "full", "", "")
	if err != nil {
		t.Fatal(err)
	}
	chat, err := c.GetChat(a.ctx, result["chatId"])
	if err != nil {
		t.Fatal(err)
	}
	if chat.CLIAgent != "codex" || chat.Settings.Model != "gpt-6.1-sol" || chat.Settings.Tools != "full" || chat.Settings.Surface != "studio" || chat.WorkspacePath != root {
		t.Fatalf("Studio configuration lost: %#v", chat)
	}
}

func TestRemoteMCPCatalogHidesCredentialsFromNonAdminDevices(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PRAIMATE_HOME", root)
	st, err := store.InitializeWithPassword(filepath.Join(root, "db.sqlite"), "remote-mcp-test-password")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, err := core.New(core.Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, err = c.ConnectMCP(ctx, core.ConnectMCPRequest{ID: "example", Name: "Example MCP", Transport: core.MCPTransport("http"), URL: "https://mcp.example/?token=synthetic-secret", Env: map[string]string{"KEY": "synthetic-secret"}, Auth: map[string]string{"Authorization": "Bearer synthetic-secret"}})
	if err != nil {
		t.Fatal(err)
	}
	a := &App{ctx: ctx, core: c}
	for _, role := range []string{"viewer", "control", "admin"} {
		servers, err := a.remoteMCPServers(remote.WithPrincipal(ctx, remote.Principal{ID: "device", Role: role}))
		if err != nil || len(servers) != 1 {
			t.Fatalf("%s catalog: %v", role, err)
		}
		raw, _ := json.Marshal(servers)
		if strings.Contains(string(raw), "synthetic-secret") != (role == "admin") {
			t.Fatalf("%s credential visibility is incorrect", role)
		}
		if servers[0].ID != "example" {
			t.Fatal("model selection metadata disappeared")
		}
	}
}

func TestRemoteHTTPControlsTheRealPTYAndRetainsOutputOnReconnect(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell fixture; Windows server is cross-built separately")
	}
	root := t.TempDir()
	t.Setenv("PRAIMATE_HOME", root)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	a := &App{ctx: context.Background(), terms: newTermManager(), edition: newEditionServices(), emit: func(context.Context, string, ...any) {}}
	registry := remote.NewRegistry()
	registry.Register("api.WriteTerminal", "control", false, a.remoteWriteTerminal)
	registry.Register("api.GetTerminalSnapshot", "read", false, a.GetTerminalSnapshot)
	const token = "test-terminal-only-administrator-token"
	g, err := remote.New(remote.Config{Token: token, Registry: registry}, nil)
	if err != nil {
		t.Fatal(err)
	}
	a.edition.gateway = g
	defer g.Close()
	server := httptest.NewServer(g)
	defer server.Close()
	id, err := a.terms.start("/bin/sh", []string{"-c", `while IFS= read -r line; do printf 'remote:%s\n' "$line"; done`}, root, nil, a.emitTerminalEvent)
	if err != nil {
		t.Fatal(err)
	}
	defer a.terms.close(id)
	client := &http.Client{Timeout: 5 * time.Second}
	request, _ := http.NewRequest("GET", server.URL+"/v1/events", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	call := func(method string, args []string) []byte {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"method": method, "params": args})
		req, _ := http.NewRequest("POST", server.URL+"/v1/call", strings.NewReader(string(raw)))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Praimate-Client", "browser1")
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		if res.StatusCode != 200 {
			t.Fatalf("%s: %d %s", method, res.StatusCode, body)
		}
		return body
	}
	call("api.WriteTerminal", []string{id, base64.StdEncoding.EncodeToString([]byte("ping\n"))})
	scanner := bufio.NewScanner(response.Body)
	output := ""
	for scanner.Scan() {
		if !strings.HasPrefix(scanner.Text(), "data: ") {
			continue
		}
		var event struct {
			Name    string       `json:"name"`
			Payload TerminalData `json:"payload"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "data: ")), &event) != nil || event.Name != "term:data:"+id {
			continue
		}
		body, _ := base64.StdEncoding.DecodeString(event.Payload.Data)
		output += string(body)
		if strings.Contains(output, "remote:ping") {
			break
		}
	}
	if !strings.Contains(output, "remote:ping") {
		t.Fatalf("PTY response not streamed: %q (%v)", output, scanner.Err())
	}
	response.Body.Close()
	var snapshot struct {
		Result TerminalSnapshot `json:"result"`
	}
	if err = json.Unmarshal(call("api.GetTerminalSnapshot", []string{id}), &snapshot); err != nil {
		t.Fatal(err)
	}
	body, _ := base64.StdEncoding.DecodeString(snapshot.Result.Data)
	if !strings.Contains(string(body), "remote:ping") {
		t.Fatal("new HTTP connection lost terminal output")
	}
}

func TestRemoteTerminalReadsTheDesktopSession(t *testing.T) {
	c, err := core.New(core.Options{WorkspacesRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	tm := newTermManager()
	tm.sessions["term-shared"] = &termSession{id: "term-shared", chatID: "chat-shared", cwd: "/example", name: "codex"}
	tm.recordOutput("term-shared", []byte("shared output"))
	a := &App{core: c, terms: tm, ctx: context.Background()}
	result, err := a.remoteCall(context.Background(), "terminals.snapshot", []byte(`{"id":"term-shared"}`))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, ok := result.(TerminalSnapshot)
	if !ok || snapshot.Data != base64.StdEncoding.EncodeToString([]byte("shared output")) {
		t.Fatalf("remote client received a separate session: %#v", result)
	}
	if _, err := a.remoteCall(context.Background(), "InstallPraimateCode", nil); err == nil {
		t.Fatal("arbitrary Wails method exposed")
	}
}
