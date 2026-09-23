package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jtsec.local/lab/PrAImate/internal/ollama"
)

func TestStartAndContinueChat_ResumesSession(t *testing.T) {
	mock := &mockAdapter{name: "claude", resumable: true, replies: []string{"hi there", "still here"}}
	withMockAdapter(t, mock)

	c, _ := New(Options{Store: openTempStore(t)})
	ctx := context.Background()
	if _, err := c.upsertAgent(ctx, &Agent{
		ID: "dev", Name: "Dev", Instructions: "be helpful", Supports: []string{"claude"},
		Workflows: []Workflow{{Name: "w", Steps: []WorkflowStep{{Kind: StepUserMessage, Template: "x"}}}},
	}); err != nil {
		t.Fatal(err)
	}

	chat, err := c.StartInteractiveChat(ctx, "dev", "claude", "/tmp")
	if err != nil {
		t.Fatalf("StartInteractiveChat: %v", err)
	}

	// First turn → SingleShot (no session yet), system prompt forwarded.
	t1, err := c.ContinueChat(ctx, chat.ID, "hello", "/tmp", "be helpful")
	if err != nil {
		t.Fatalf("ContinueChat 1: %v", err)
	}
	if t1.Reply != "hi there" {
		t.Fatalf("turn1 reply = %q", t1.Reply)
	}
	if len(mock.shots) != 1 || mock.shots[0].SystemPrompt != "be helpful" {
		t.Fatalf("first turn should be SingleShot with system prompt: shots=%d", len(mock.shots))
	}

	// Session id should now be stored on the chat.
	reloaded, _ := c.GetChat(ctx, chat.ID)
	if reloaded.SessionID == "" {
		t.Fatal("session id not persisted after first turn")
	}

	// Second turn → Resume.
	t2, err := c.ContinueChat(ctx, chat.ID, "again", "/tmp", "be helpful")
	if err != nil {
		t.Fatalf("ContinueChat 2: %v", err)
	}
	if t2.Reply != "still here" {
		t.Fatalf("turn2 reply = %q", t2.Reply)
	}
	if len(mock.resumes) != 1 {
		t.Fatalf("second turn should Resume, got %d resumes", len(mock.resumes))
	}

	// Four messages persisted: user/assistant × 2.
	msgs, _ := c.ListMessages(ctx, chat.ID, 0)
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(msgs))
	}
}

func TestContinueChat_RedactsOutboundRevealsReply(t *testing.T) {
	const secret = "sk-abcdefghijklmnopqrstuvwxyzz"
	// The mock echoes whatever placeholder it received back in the reply.
	mock := &mockAdapter{name: "claude", replies: []string{"you sent [OPENAI_KEY_1]"}}
	withMockAdapter(t, mock)

	c, _ := New(Options{Store: openTempStore(t)})
	ctx := context.Background()
	_, _ = c.upsertAgent(ctx, &Agent{
		ID: "dev", Name: "Dev", Instructions: "x", Supports: []string{"claude"},
		Workflows: []Workflow{{Name: "w", Steps: []WorkflowStep{{Kind: StepUserMessage, Template: "x"}}}},
	})
	chat, _ := c.StartInteractiveChat(ctx, "dev", "claude", "/tmp")

	turn, err := c.ContinueChat(ctx, chat.ID, "key is "+secret, "/tmp", "")
	if err != nil {
		t.Fatalf("ContinueChat: %v", err)
	}
	// Adapter saw the redacted placeholder, not the secret.
	if strings.Contains(mock.shots[0].Message, secret) {
		t.Fatalf("secret leaked to adapter: %q", mock.shots[0].Message)
	}
	if !strings.Contains(mock.shots[0].Message, "[OPENAI_KEY_1]") {
		t.Fatalf("expected placeholder in adapter message: %q", mock.shots[0].Message)
	}
	// Reply revealed back to the user's original secret.
	if !strings.Contains(turn.Reply, secret) {
		t.Fatalf("reply should reveal the secret: %q", turn.Reply)
	}
}

func TestContinueChatStream_PersistsCompactOpenCodeActivity(t *testing.T) {
	mock := &mockAdapter{
		name:      "opencode",
		resumable: true,
		replies:   []string{"final"},
		streamEvents: []StreamEvent{
			{Type: "reasoning", Text: "checking\ncontext"},
			{Type: "step_start", Detail: "inspect workspace"},
			{Type: "tool_start", Tool: "read", Detail: "main.go", ID: "t1"},
			{Type: "tool_end", Tool: "read", Detail: "main.go", ID: "t1", OK: true},
			{Type: "error", Detail: "permission rejected"},
		},
	}
	withMockAdapter(t, mock)

	c, _ := New(Options{Store: openTempStore(t)})
	ctx := context.Background()
	chat, err := c.CreateChat(ctx, CreateChatRequest{Title: "oc", CLIAgent: "opencode", WorkspacePath: "/tmp"})
	if err != nil {
		t.Fatalf("CreateChat: %v", err)
	}
	if _, err := c.ContinueChatStream(ctx, chat.ID, "hello", "/tmp", "", nil, nil); err != nil {
		t.Fatalf("ContinueChatStream: %v", err)
	}
	msgs, err := c.ListMessages(ctx, chat.ID, 0)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	assistant := msgs[len(msgs)-1]
	activity, ok := assistant.Meta["activity"].([]any)
	if !ok || len(activity) < 4 {
		t.Fatalf("activity not persisted: %#v", assistant.Meta)
	}
	first, _ := activity[0].(map[string]any)
	if first["type"] != "reasoning" || first["text"] != "checking ⏎ context" {
		t.Fatalf("reasoning activity = %#v", first)
	}
	if _, ok := assistant.Meta["opencode"]; ok {
		t.Fatalf("raw opencode events should not be persisted: %#v", assistant.Meta)
	}
}

func TestContinueChat_PrAImateCodeInjectsEncryptedLocalLLMKey(t *testing.T) {
	mock := &mockAdapter{name: "praimate-code", replies: []string{"connected"}}
	withMockAdapter(t, mock)

	c, _ := New(Options{Store: openTempStore(t)})
	ctx := context.Background()
	if err := c.SetSetting(ctx, ScopeCLI, "local_llm.api_key", []byte(`"db-secret"`)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := ollama.ApplyOpenCode(ollama.Settings{
		Endpoint: "https://llm.example", Model: "qwen3", APIKey: "db-secret",
	}, true); err != nil {
		t.Fatal(err)
	}
	chat, err := c.CreateChat(ctx, CreateChatRequest{
		Title:    "local",
		CLIAgent: "praimate-code",
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := c.ContinueChat(ctx, chat.ID, "hello", t.TempDir(), ""); err != nil {
		t.Fatal(err)
	}
	if len(mock.shots) != 1 {
		t.Fatalf("shots = %d, want 1", len(mock.shots))
	}
	if got := mock.shots[0].Env["OPENAI_API_KEY"]; got != "db-secret" {
		t.Fatalf("OPENAI_API_KEY = %q, want encrypted DB credential", got)
	}
}

func TestContinueChat_OpenClaudeResolvesLocalKeyFromEncryptedSetting(t *testing.T) {
	mock := &mockAdapter{name: "openclaude", replies: []string{"connected"}}
	withMockAdapter(t, mock)

	c, _ := New(Options{Store: openTempStore(t)})
	ctx := context.Background()
	if err := c.SetSetting(ctx, ScopeCLI, "local_llm.api_key", []byte(`"db-secret"`)); err != nil {
		t.Fatal(err)
	}
	chat, err := c.CreateChat(ctx, CreateChatRequest{
		Title:    "local",
		CLIAgent: "openclaude",
		Settings: ChatSettings{Local: &ChatLocalEndpoint{
			Endpoint: "https://llm.example", Model: "qwen3",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ContinueChat(ctx, chat.ID, "hello", t.TempDir(), ""); err != nil {
		t.Fatal(err)
	}
	if got := mock.shots[0].Env["OPENAI_API_KEY"]; got != "db-secret" {
		t.Fatalf("OPENAI_API_KEY = %q, want encrypted DB credential", got)
	}
}

// UpdateChatConfig is the GUI's per-chat settings sheet: switching the
// CLI clears the session id (sessions belong to their CLI), and model +
// tools persist into the settings JSON.
func TestUpdateChatConfig_SwitchesCLIAndClearsSession(t *testing.T) {
	c, _ := New(Options{Store: openTempStore(t)})
	ctx := context.Background()
	RegisterAllCLIAdapters()

	ch, err := c.CreateChat(ctx, CreateChatRequest{Title: "cfg", CLIAgent: "claude"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := c.SetChatSessionID(ctx, ch.ID, "sess-old"); err != nil {
		t.Fatalf("set session: %v", err)
	}

	if err := c.UpdateChatConfig(ctx, ch.ID, "codex", "gpt-5.1-codex", "edits"); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ := c.GetChat(ctx, ch.ID)
	if got.CLIAgent != "codex" {
		t.Errorf("cli = %q", got.CLIAgent)
	}
	if got.SessionID != "" {
		t.Errorf("session must clear on CLI switch, got %q", got.SessionID)
	}
	if got.Settings.Model != "gpt-5.1-codex" || got.Settings.Tools != "edits" {
		t.Errorf("settings = %+v", got.Settings)
	}

	// Same CLI: session survives; unknown CLI: rejected.
	_ = c.SetChatSessionID(ctx, ch.ID, "sess-new")
	if err := c.UpdateChatConfig(ctx, ch.ID, "codex", "", ""); err != nil {
		t.Fatalf("same-cli update: %v", err)
	}
	got, _ = c.GetChat(ctx, ch.ID)
	if got.SessionID != "sess-new" {
		t.Errorf("session must survive same-CLI update, got %q", got.SessionID)
	}
	if err := c.UpdateChatConfig(ctx, ch.ID, "not-a-cli", "", ""); err == nil {
		t.Error("unknown CLI must be rejected")
	}
}

func TestContinueChat_PreparesOnlySelectedMCPServers(t *testing.T) {
	mock := &mockAdapter{name: "claude", replies: []string{"done"}}
	withMockAdapter(t, mock)
	c, _ := New(Options{Store: openTempStore(t)})
	ctx := context.Background()
	enabled := true
	for _, server := range []ConnectMCPRequest{
		{ID: "selected", Name: "Selected", Transport: MCPTransportHTTP, URL: "https://selected.example/mcp", Enabled: &enabled},
		{ID: "other", Name: "Other", Transport: MCPTransportHTTP, URL: "https://other.example/mcp", Enabled: &enabled},
	} {
		if _, err := c.ConnectMCP(ctx, server); err != nil {
			t.Fatal(err)
		}
	}
	cwd := t.TempDir()
	chat, err := c.CreateChat(ctx, CreateChatRequest{
		Title: "mcp", CLIAgent: "claude", WorkspacePath: cwd,
		Settings: ChatSettings{MCPServers: []string{"selected"}, MCPConfigured: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ContinueChat(ctx, chat.ID, "go", cwd, ""); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(cwd, ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, `"selected"`) || strings.Contains(text, `"other"`) {
		t.Fatalf("per-chat MCP config mismatch:\n%s", text)
	}
}

func TestContinueChat_ManualCompactCommandResetsSession(t *testing.T) {
	mock := &mockAdapter{name: "claude", resumable: true, replies: []string{"ok"}}
	withMockAdapter(t, mock)

	c, _ := New(Options{Store: openTempStore(t)})
	ctx := context.Background()
	chat, err := c.CreateChat(ctx, CreateChatRequest{Title: "compact-test", CLIAgent: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetChatSessionID(ctx, chat.ID, "session-to-clear")

	turn, err := c.ContinueChat(ctx, chat.ID, "/compact", t.TempDir(), "")
	if err != nil {
		t.Fatalf("ContinueChat /compact: %v", err)
	}
	if !strings.Contains(turn.Reply, "compacted") {
		t.Fatalf("unexpected compact reply: %q", turn.Reply)
	}
	reloaded, _ := c.GetChat(ctx, chat.ID)
	if reloaded.SessionID != "" {
		t.Fatalf("expected empty session id after /compact, got %q", reloaded.SessionID)
	}
}

func TestContinueChat_AutoCompactsOnContextOverflow(t *testing.T) {
	mock := &contextOverflowMockAdapter{
		name:      "praimate-code",
		resumable: true,
	}
	RegisterCLIAdapter(mock)
	t.Cleanup(func() { UnregisterCLIAdapter(mock.Name()) })

	c, _ := New(Options{Store: openTempStore(t)})
	ctx := context.Background()
	chat, err := c.CreateChat(ctx, CreateChatRequest{Title: "overflow-test", CLIAgent: "praimate-code"})
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetChatSessionID(ctx, chat.ID, "bloated-session-123")

	turn, err := c.ContinueChat(ctx, chat.ID, "tell me what's next", t.TempDir(), "")
	if err != nil {
		t.Fatalf("ContinueChat failed after auto-compaction: %v", err)
	}
	if turn.Reply != "recovered reply after compaction" {
		t.Fatalf("unexpected turn reply: %q", turn.Reply)
	}
	if turn.SessionID != "fresh-session-after-compact" {
		t.Fatalf("unexpected turn session ID: %q", turn.SessionID)
	}
}

type contextOverflowMockAdapter struct {
	name      string
	resumable bool
}

func (a *contextOverflowMockAdapter) Name() string           { return a.name }
func (a *contextOverflowMockAdapter) SupportsResume() bool  { return a.resumable }
func (a *contextOverflowMockAdapter) ManagedSafeMode() bool { return false }
func (a *contextOverflowMockAdapter) Available(context.Context) error {
	return nil
}

func (a *contextOverflowMockAdapter) SingleShot(_ context.Context, opts SingleShotOpts) (*Reply, error) {
	return &Reply{Text: "recovered reply after compaction", SessionID: "fresh-session-after-compact"}, nil
}

func (a *contextOverflowMockAdapter) Resume(_ context.Context, sessionID string, opts ResumeOpts) (*Reply, error) {
	return nil, errors.New("praimate-code stream: request (222011 tokens) exceeds the available context size (212992 tokens), try increasing it.")
}

