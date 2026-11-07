//go:build server

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/remote"
	"github.com/sPROFFEs/PrAImate/internal/skills"
	"github.com/sPROFFEs/PrAImate/internal/studio"
)

type remoteCoreState struct{ core *core.Core }

func (s remoteCoreState) Load(ctx context.Context, key string) ([]byte, error) {
	return s.core.GetSetting(ctx, core.ScopeGUI, "server.remote."+key)
}
func (s remoteCoreState) Save(ctx context.Context, key string, raw []byte) error {
	return s.core.SetSetting(ctx, core.ScopeGUI, "server.remote."+key, raw)
}

func (a *App) registerRemoteAdapters(r *remote.Registry) {
	r.Register("api.RemoteActivity", "read", false, a.RemoteActivity)
	r.Register("projects.prepare", "admin", true, a.prepareRemoteProject)
	r.Register("projects.open", "admin", true, func(path string) (string, error) {
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", err
		}
		info, err := os.Stat(abs)
		if err != nil {
			return "", err
		}
		if !info.IsDir() {
			return "", errors.New("project must be a directory")
		}
		return abs, studio.NewManager(a.core).SaveRecentProject(abs)
	})
	r.Register("api.ServerRestart", "admin", true, func() (bool, error) {
		if !a.headless || os.Getenv("PRAIMATE_SERVER_SUPERVISED") != "1" {
			return false, remote.Fail(409, "restart_requires_supervisor", "Restart this foreground server from its console")
		}
		a.edition.mu.Lock()
		a.edition.restartRequested = true
		a.edition.mu.Unlock()
		go func() { time.Sleep(2 * time.Second); a.quit(a.ctx) }()
		return true, nil
	})
	r.Register("api.ServerShutdown", "admin", true, func() bool { go func() { time.Sleep(2 * time.Second); a.quit(a.ctx) }(); return true })
	r.Register("files.watch", "admin", false, func(workspace string) (bool, error) { return true, a.edition.files.Watch(workspace) })
	r.Register("skills.query", "admin", false, func(body string) (core.SkillLibraryResult, error) {
		var request core.SkillLibraryRequest
		if err := json.Unmarshal([]byte(body), &request); err != nil {
			return core.SkillLibraryResult{}, err
		}
		switch request.Action {
		case "list", "read", "inspect", "legacy-inspect", "draft-read", "draft-preview":
			return a.SkillLibraryV2(body)
		default:
			return core.SkillLibraryResult{}, remote.Fail(400, "invalid_action", "Use the mutating skill operation for this action")
		}
	})
	a.registerRemoteTransfers(r)
	r.Register("api.StartTerminal", "control", true, a.StartTerminal)
	for _, method := range []string{"files.list", "files.tree", "files.read", "files.write", "files.mkdir", "files.rename", "files.remove"} {
		name := method
		r.Register(name, "admin", name != "files.list" && name != "files.tree" && name != "files.read", func(p remote.FileRequest) (any, error) { return a.edition.files.Call(name, p) })
	}
	r.Register("terminals.claim", "control", false, func(ctx context.Context, id string, force bool) (remote.Lease, error) {
		if _, err := a.GetTerminalSnapshot(id); err != nil {
			return remote.Lease{}, err
		}
		return a.edition.leases.Claim(ctx, id, force)
	})
	r.Register("api.WriteTerminal", "control", false, a.remoteWriteTerminal)
	r.Register("api.ResizeTerminal", "control", false, a.remoteResizeTerminal)
	r.Register("api.CloseTerminal", "control", true, func(ctx context.Context, id string) error {
		if _, err := a.edition.leases.Claim(ctx, id, false); err != nil {
			return err
		}
		a.CloseTerminal(id)
		return nil
	})
	r.Register("api.GetTerminalSnapshot", "read", false, a.GetTerminalSnapshot)
	r.Register("api.GetCodeSessionSnapshot", "read", false, a.GetCodeSessionSnapshot)
	r.Register("api.PickProjectFolder", "admin", false, func() (string, error) {
		return "", remote.Fail(409, "client_file_picker", "Select a server workspace using files.list")
	})
	r.Register("agents.import", "admin", true, func(path string) (*core.Agent, error) { return a.core.ImportAgentAuto(a.ctx, path) })
	r.Register("agents.inspect", "admin", false, func(path string) (*core.AgentPackReview, error) { return a.core.InspectAgentPack(a.ctx, path) })
	r.Register("agents.export", "admin", true, func(id, path string, pack bool) (string, error) {
		if pack {
			return path, a.core.ExportAgentPack(a.ctx, id, path)
		}
		return path, a.core.ExportAgent(a.ctx, id, path)
	})
	r.Register("skills.export", "admin", false, func(ref, digest string) (map[string]string, error) {
		host, err := core.OpenSkillStore(skills.PackageLimits{})
		if err != nil {
			return nil, err
		}
		defer host.Close()
		body, err := host.ExportPackageZIP(a.ctx, ref, digest)
		if err != nil {
			return nil, err
		}
		directory, err := a.remoteExportDirectory()
		if err != nil {
			return nil, err
		}
		if err = os.WriteFile(filepath.Join(directory, "skill.zip"), body, 0600); err != nil {
			return nil, err
		}
		return map[string]string{"name": "skill.zip", "workspace": directory, "path": "skill.zip"}, nil
	})
	r.Register("server.capabilities", "read", false, func(ctx context.Context) map[string]any {
		return map[string]any{"headless": a.headless, "supervised": a.headless && os.Getenv("PRAIMATE_SERVER_SUPERVISED") == "1", "nativeWindows": !a.headless, "fileTransferLimit": 512 << 20, "methods": r.Catalog(remote.Identity(ctx)), "desktopOnly": []string{"native file dialogs", "native external editor windows", "server microphone capture", "desktop window management"}}
	})
	r.Register("projects.validate", "control", false, func(path string) (string, error) {
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", err
		}
		info, err := os.Stat(abs)
		if err != nil {
			return "", err
		}
		if !info.IsDir() {
			return "", errors.New("project must be a directory")
		}
		return abs, nil
	})
}

func (a *App) prepareRemoteProject(folder, agentID, cli, model, tools, localEndpoint, localModel string) (map[string]string, error) {
	abs, err := filepath.Abs(folder)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("project must be a directory")
	}
	id, err := a.PrepareStudioChat(abs, agentID, cli, model, localEndpoint, localModel, "")
	if err != nil {
		return nil, err
	}
	if tools != "" {
		err = a.SetChatTools(id, tools)
	}
	if err == nil {
		err = studio.NewManager(a.core).SaveRecentProject(abs)
	}
	if err != nil {
		_ = a.core.DeleteChat(a.ctx, id)
		return nil, err
	}
	return map[string]string{"workspace": abs, "chatId": id}, nil
}

// RemoteActivity restores active turns and pending approvals after a browser
// reconnect. Reading it never starts an approval broker or a new model call.
func (a *App) RemoteActivity() map[string]any {
	a.approvalMu.Lock()
	broker := a.approval
	a.approvalMu.Unlock()
	requests := []ApprovalRequest{}
	if broker != nil {
		broker.mu.Lock()
		for _, request := range broker.requests {
			requests = append(requests, request)
		}
		broker.mu.Unlock()
	}
	return map[string]any{"chats": a.ActiveChatIDs(), "approvals": requests}
}

func (a *App) remoteMCPServers(ctx context.Context) ([]core.MCPServer, error) {
	servers, err := a.MCPServers()
	if err != nil || remote.Identity(ctx).Allows("admin") {
		return servers, err
	}
	for i, server := range servers {
		servers[i] = core.MCPServer{ID: server.ID, Name: server.Name, Transport: server.Transport, Enabled: server.Enabled, CatalogueKey: server.CatalogueKey, CreatedAt: server.CreatedAt}
	}
	return servers, nil
}
func (a *App) remoteWriteTerminal(ctx context.Context, id, data string) error {
	if len(data) > 64<<10 {
		return errors.New("terminal input exceeds limit")
	}
	if _, err := a.GetTerminalSnapshot(id); err != nil {
		return err
	}
	if _, err := a.edition.leases.Claim(ctx, id, false); err != nil {
		return err
	}
	bytes, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return err
	}
	return a.terms.write(id, bytes)
}
func (a *App) remoteResizeTerminal(ctx context.Context, id string, cols, rows int) error {
	if cols < 1 || cols > 4096 || rows < 1 || rows > 2048 {
		return errors.New("invalid terminal dimensions")
	}
	if _, err := a.GetTerminalSnapshot(id); err != nil {
		return err
	}
	if _, err := a.edition.leases.Claim(ctx, id, false); err != nil {
		return err
	}
	return a.terms.resize(id, cols, rows)
}
func (e *editionServices) localTerminalInput(id string) {
	if e.leases != nil {
		e.leases.TakeLocal(id)
	}
}
func (e *editionServices) skipLocalTerminalResize(id string) bool {
	return e.leases != nil && e.leases.RemoteControls(id)
}
