package main

// Chat extras — the bindings behind the composer's power features:
//
//   - per-chat Tools level (safe / ask / edits / plan / full) and model re-pinning
//   - "!" shell commands run in the chat's working directory
//   - file ingestion: attach images / PDFs / docs to a turn; the files
//     are copied into a per-chat attachments dir and their paths handed
//     to the CLI (file-tool-capable CLIs read them from disk)
//   - data-URL previews so the thread can render attached images inline
//
// All business logic lives in internal/core; this file only adds the
// dialog plumbing and the attachment storage location.

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"git.jtsec.local/lab/PrAImate/internal/appdata"
	"git.jtsec.local/lab/PrAImate/internal/core"
)

// SetChatTools persists the chat's Tools permission level ("", "ask",
// "edits", "plan", "full"). The next turn picks it up — resumed sessions re-pin
// the level on every invocation.
func (a *App) SetChatTools(chatID, tools string) error {
	switch tools {
	case "", "ask", "edits", "plan", "full":
	default:
		return fmt.Errorf("unknown tools level %q (want \"\", \"ask\", \"edits\", \"plan\" or \"full\")", tools)
	}
	c, err := a.requireCore()
	if err != nil {
		return err
	}
	return c.UpdateChatSettings(a.ctx, chatID, func(s *core.ChatSettings) {
		s.Tools = tools
		s.ToolsConfigured = true
	})
}

// SetChatMCPServers overwrites the per-chat MCP selection. Only connected,
// enabled servers can be selected; stale/disabled IDs are rejected here so a
// settings save cannot appear successful while exposing no tools.
func (a *App) SetChatMCPServers(chatID string, ids []string) error {
	c, err := a.requireCore()
	if err != nil {
		return err
	}
	seen := make(map[string]bool, len(ids))
	clean := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		s, err := c.GetMCPServer(a.ctx, id)
		if err != nil {
			return err
		}
		if !s.Enabled {
			return fmt.Errorf("MCP server %q is disabled", s.Name)
		}
		seen[id] = true
		clean = append(clean, id)
	}
	return c.UpdateChatSettings(a.ctx, chatID, func(s *core.ChatSettings) {
		s.MCPServers = append([]string(nil), clean...)
		s.MCPConfigured = true
	})
}

// UpdateChatWorkspace reconfigures an existing chat's working directory.
func (a *App) UpdateChatWorkspace(chatID, workspacePath string) error {
	if a.detachedClient != nil {
		return a.detachedClient.updateChatWorkspace(chatID, workspacePath)
	}
	c, err := a.requireCore()
	if err != nil {
		return err
	}
	return c.UpdateChatWorkspace(a.ctx, chatID, workspacePath)
}

// UpdateChatConfig reconfigures an existing chat (CLI / model / tools /
// per-chat local endpoint). Switching CLI starts a fresh session on the next turn
// (history stays). Empty localEndpoint clears the local route.
func (a *App) UpdateChatConfig(chatID, cli, model, tools, localEndpoint, _ string, localModel string) error {
	switch tools {
	case "", "ask", "edits", "plan", "full":
	default:
		return fmt.Errorf("unknown tools level %q", tools)
	}
	if strings.TrimSpace(localEndpoint) != "" {
		if err := core.ValidateLocalRoutingCLI(cli); err != nil {
			return err
		}
	}
	c, err := a.requireCore()
	if err != nil {
		return err
	}
	if err := c.UpdateChatConfig(a.ctx, chatID, cli, model, tools); err != nil {
		return err
	}
	return c.UpdateChatSettings(a.ctx, chatID, func(s *core.ChatSettings) {
		if localEndpoint == "" {
			s.Local = nil
			return
		}
		s.Local = &core.ChatLocalEndpoint{Endpoint: localEndpoint, Model: localModel}
	})
}

// SearchChats finds chats by title or message content.
func (a *App) SearchChats(query string) ([]core.Chat, error) {
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	chats, err := c.SearchChats(a.ctx, query, 50)
	if err != nil {
		return nil, err
	}
	return redactChatCredentials(chats), nil
}

// RunChatCommand executes a "!" composer command in the chat's working
// directory and returns the persisted output turn.
func (a *App) RunChatCommand(chatID, command string) (*core.ChatTurn, error) {
	if a.detachedClient != nil {
		return a.detachedClient.runChatCommand(chatID, command)
	}
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	return c.RunChatCommand(a.ctx, chatID, command)
}

// ChatAttachment describes one file staged for the next turn.
type ChatAttachment struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Image bool   `json:"image"`
}

// attachmentsRoot is where attached files are copied so they outlive
// the source location (Downloads cleanups, USB drives, …) and so the
// preview binding can safely restrict reads to a praimate-owned dir.
func attachmentsRoot() (string, error) {
	base, err := appdata.Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "attachments"), nil
}

func isImagePath(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".svg":
		return true
	}
	return false
}

// PickChatAttachments opens the system file dialog and copies the
// selection into the chat's attachments dir. Returns the staged files;
// the frontend holds them until Send and passes the paths to
// SendChatWithAttachments.
func (a *App) PickChatAttachments(chatID string) ([]ChatAttachment, error) {
	if chatID == "" {
		return nil, errors.New("PickChatAttachments: empty chatID")
	}
	paths, err := wruntime.OpenMultipleFilesDialog(a.ctx, wruntime.OpenDialogOptions{
		Title: "Attach files to this chat",
		Filters: []wruntime.FileFilter{
			{DisplayName: "Images", Pattern: "*.png;*.jpg;*.jpeg;*.gif;*.webp;*.bmp;*.svg"},
			{DisplayName: "Documents", Pattern: "*.pdf;*.md;*.txt;*.doc;*.docx;*.csv;*.json;*.xml;*.html"},
			{DisplayName: "All files", Pattern: "*.*"},
		},
	})
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return []ChatAttachment{}, nil
	}
	root, err := attachmentsRoot()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(root, chatID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	out := make([]ChatAttachment, 0, len(paths))
	for _, src := range paths {
		name := filepath.Base(src)
		dst := filepath.Join(dir, name)
		// Avoid clobbering a same-named earlier attachment.
		for i := 2; ; i++ {
			if _, err := os.Stat(dst); os.IsNotExist(err) {
				break
			}
			ext := filepath.Ext(name)
			dst = filepath.Join(dir, strings.TrimSuffix(name, ext)+fmt.Sprintf("-%d", i)+ext)
		}
		if err := copyFile(src, dst); err != nil {
			return nil, fmt.Errorf("copy %s: %w", name, err)
		}
		out = append(out, ChatAttachment{Name: filepath.Base(dst), Path: dst, Image: isImagePath(dst)})
	}
	return out, nil
}

// SendChatWithAttachments is SendChat plus staged attachment paths.
func (a *App) SendChatWithAttachments(chatID, message string, attachments []string) (*core.ChatTurn, error) {
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
	return c.ContinueChatWithAttachments(a.ctx, chatID, message, chat.WorkspacePath, systemPrompt, attachments)
}

// AttachmentDataURL returns a data: URL for an attached image so the
// thread can render it inline. Reads are restricted to the praimate
// attachments dir — message meta is the only place these paths come
// from, but the binding is callable from JS, so it must not become an
// arbitrary-file-read primitive.
func (a *App) AttachmentDataURL(path string) (string, error) {
	root, err := attachmentsRoot()
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if rel, err := filepath.Rel(root, abs); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("path is outside the attachments directory")
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	const maxPreview = 8 << 20
	if fi.Size() > maxPreview {
		return "", fmt.Errorf("file too large for inline preview (%d bytes)", fi.Size())
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	mt := mime.TypeByExtension(filepath.Ext(abs))
	if mt == "" {
		mt = "application/octet-stream"
	}
	return "data:" + mt + ";base64," + base64.StdEncoding.EncodeToString(raw), nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
