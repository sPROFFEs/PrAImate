package studio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"git.jtsec.local/lab/PrAImate/internal/core"
)

// JSON unmarshalling into a copy preserves omitted values and allows explicitly
// clearing model, persona and selections.
type sessionConfig struct {
	CLI           string                      `json:"cli"`
	Model         string                      `json:"model"`
	AgentID       string                      `json:"agentId"`
	Tools         string                      `json:"tools"`
	Workspace     string                      `json:"workspace"`
	MCPServers    []string                    `json:"mcpServers"`
	Skills        []core.InstalledSkillChoice `json:"skills"`
	LocalEndpoint string                      `json:"localEndpoint"`
	LocalModel    string                      `json:"localModel"`
}

func (s *Server) sessionSnapshot() sessionConfig { s.mu.Lock(); defer s.mu.Unlock(); return s.session }

func (s *Server) updateSession(ctx context.Context, body []byte) (any, error) {
	if s.isRunning() {
		return nil, errors.New("stop the active run before changing its configuration")
	}
	p := s.sessionSnapshot()
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, err
	}
	if _, err := core.GetCLIAdapter(p.CLI); err != nil {
		return nil, err
	}
	switch p.Tools {
	case "", "safe", "ask", "edits", "plan", "full":
	default:
		return nil, errors.New("invalid tool permission level")
	}
	if p.Workspace != "" {
		abs, err := filepath.Abs(p.Workspace)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(abs)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, errors.New("workspace must be a directory")
		}
		p.Workspace = abs
	}
	if p.AgentID != "" {
		a, err := s.core.GetAgent(ctx, p.AgentID)
		if err != nil {
			return nil, err
		}
		if !a.AllowsSurface("editor") {
			return nil, errors.New("agent is not enabled for the editor surface")
		}
		supported := false
		for _, cli := range a.Supports {
			if cli == p.CLI {
				supported = true
			}
		}
		if !supported {
			return nil, fmt.Errorf("agent does not support %s", p.CLI)
		}
	}
	if _, err := s.settings(ctx, p); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.session = p
	s.mu.Unlock()
	s.Broadcast("session.updated", p)
	return p, nil
}
func (s *Server) settings(ctx context.Context, p sessionConfig) (core.ChatSettings, error) {
	tools := p.Tools
	if tools == "safe" {
		tools = ""
	}
	st := core.ChatSettings{Surface: "studio-ide", Tools: tools, ToolsConfigured: true, Model: p.Model}
	if p.MCPServers != nil {
		for _, id := range p.MCPServers {
			if _, err := s.core.GetMCPServer(ctx, id); err != nil {
				return st, err
			}
		}
		st.MCPServers = append([]string{}, p.MCPServers...)
		st.MCPConfigured = true
	}
	if p.Skills != nil {
		selection, err := s.core.BuildInstalledSkillSelection(ctx, p.Skills)
		if err != nil {
			return st, err
		}
		st.SkillsV2, st.SkillsLock = selection.Config, selection.Lock
	}
	if p.LocalEndpoint != "" {
		if err := core.ValidateLocalRoutingCLI(p.CLI); err != nil {
			return st, err
		}
		st.Local = &core.ChatLocalEndpoint{Endpoint: p.LocalEndpoint, Model: p.LocalModel}
	}
	return st, nil
}
func (s *Server) createChat(ctx context.Context, title string) (*core.Chat, error) {
	p := s.sessionSnapshot()
	if p.Workspace == "" {
		return nil, errors.New("open a workspace folder before starting a chat")
	}
	settings, err := s.settings(ctx, p)
	if err != nil {
		return nil, err
	}
	if title == "" {
		title = "Studio Assistant"
	}
	return s.core.CreateChat(ctx, core.CreateChatRequest{Title: title, CLIAgent: p.CLI, AgentID: p.AgentID, WorkspacePath: p.Workspace, Settings: settings})
}
func (s *Server) ownedChat(ctx context.Context, id string) (*core.Chat, error) {
	chat, err := s.core.GetChat(ctx, id)
	if err != nil {
		return nil, err
	}
	if chat.Settings.Surface != "studio-ide" || filepath.Clean(chat.WorkspacePath) != filepath.Clean(s.sessionSnapshot().Workspace) {
		return nil, errors.New("chat belongs to a different surface or workspace")
	}
	return chat, nil
}
func (s *Server) sendChat(ctx context.Context, body []byte) (any, error) {
	var p struct {
		ChatID      string   `json:"chatId"`
		Prompt      string   `json:"prompt"`
		Attachments []string `json:"attachments"`
		Context     struct {
			ActiveFile string `json:"activeFile"`
			Language   string `json:"language"`
			Content    string `json:"content"`
			Selection  struct {
				Text      string
				StartLine int
				EndLine   int
			} `json:"selection"`
		} `json:"context"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.Prompt) == "" {
		return nil, errors.New("prompt is empty")
	}
	chat, err := s.ownedChat(ctx, p.ChatID)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(strings.TrimSpace(p.Prompt), "!") {
		if len(p.Attachments) != 0 {
			return nil, errors.New("shell commands cannot include attachments")
		}
		return s.core.RunChatCommand(ctx, chat.ID, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(p.Prompt), "!")))
	}
	cfg := s.sessionSnapshot()
	if chat.AgentID != cfg.AgentID {
		return nil, errors.New("start a new chat after changing agent persona")
	}
	st, err := s.settings(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err = s.core.UpdateChatConfig(ctx, chat.ID, cfg.CLI, cfg.Model, st.Tools); err != nil {
		return nil, err
	}
	if err = s.core.UpdateChatSettings(ctx, chat.ID, func(cs *core.ChatSettings) {
		cs.Local = st.Local
		cs.MCPServers = st.MCPServers
		cs.MCPConfigured = st.MCPConfigured
		if cfg.Skills != nil {
			cs.SkillsV2 = st.SkillsV2
			cs.SkillsLock = st.SkillsLock
			cs.Skills = nil
		}
	}); err != nil {
		return nil, err
	}
	system := ""
	if chat.AgentID != "" {
		agent, err := s.core.GetAgent(ctx, chat.AgentID)
		if err != nil {
			return nil, err
		}
		system = core.AgentSystemPrompt(agent)
	}
	if prefix := core.ResolveSkillsPrefix(chat.Settings.Skills); prefix != "" {
		system = prefix + "\n\n" + system
	}
	message := p.Prompt
	content := p.Context.Selection.Text
	if content == "" {
		content = p.Context.Content
	}
	if len(content) > 128*1024 {
		return nil, errors.New("editor context exceeds 128 KiB; select a smaller region")
	}
	if content != "" {
		if p.Context.ActiveFile == "" || !pathWithin(chat.WorkspacePath, p.Context.ActiveFile) {
			return nil, errors.New("editor context belongs to a different workspace")
		}
		// Context is untrusted project content, never injected into the system role.
		message = fmt.Sprintf("Editor context from %s (%s):\n%s\n\nRequest:\n%s", p.Context.ActiveFile, p.Context.Language, content, p.Prompt)
	}
	turn, err := s.core.ContinueChatStream(ctx, chat.ID, message, chat.WorkspacePath, system, p.Attachments, func(ev core.StreamEvent) {
		s.Broadcast("run.event", map[string]any{"chatId": chat.ID, "type": ev.Type, "text": ev.Text, "tool": ev.Tool, "detail": ev.Detail, "id": ev.ID, "ok": ev.OK})
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"chatId": chat.ID, "reply": turn.Reply, "status": "completed"}, nil
}

func pathWithin(root, name string) bool {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	nameAbs, err := filepath.Abs(name)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(rootAbs, nameAbs)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
func (s *Server) runWorkflow(ctx context.Context, body []byte) (any, error) {
	var p struct {
		AgentID string
		Name    string
		Inputs  map[string]string
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, err
	}
	agent, err := s.core.GetAgent(ctx, p.AgentID)
	if err != nil {
		return nil, err
	}
	cfg := s.sessionSnapshot()
	if cfg.Workspace == "" {
		return nil, errors.New("open a workspace first")
	}
	st, err := s.settings(ctx, cfg)
	if err != nil {
		return nil, err
	}
	result := s.core.RunWorkflow(ctx, core.RunOptions{
		Agent: agent, WorkflowName: p.Name, Inputs: p.Inputs, CLI: cfg.CLI, Model: cfg.Model, Tools: st.Tools,
		Cwd: cfg.Workspace, Persist: true, ChatSettings: st,
		OnEvent: func(ev core.WorkflowRunEvent) {
			s.Broadcast("run.event", map[string]any{"type": ev.Type, "text": ev.Text, "tool": ev.Tool, "detail": ev.Detail, "id": ev.ID, "ok": ev.OK})
		},
	})
	if result.Err != nil {
		return nil, result.Err
	}
	return result, nil
}
