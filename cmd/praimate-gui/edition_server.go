//go:build server

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/cloudflared"
	"github.com/sPROFFEs/PrAImate/internal/remote"
)

type editionServices struct {
	listenerMu          sync.Mutex
	mu                  sync.Mutex
	settingsLoaded      bool
	startupDisabled     bool
	activeConfiguration *ServerConfiguration
	config              remote.Config
	gateway             *remote.Gateway
	registry            *remote.Registry
	leases              *remote.Leases
	files               *remote.Files
	startError          error
	address             string
	owner               *os.File
	restartRequested    bool
	tunnel              *cloudflared.Manager
}

func newEditionServices() editionServices {
	config := remote.Config{
		Listen: os.Getenv("PRAIMATE_REMOTE_LISTEN"), Token: os.Getenv("PRAIMATE_REMOTE_TOKEN"),
		TLSCert: os.Getenv("PRAIMATE_REMOTE_TLS_CERT"), TLSKey: os.Getenv("PRAIMATE_REMOTE_TLS_KEY"),
	}
	for _, origin := range strings.Split(os.Getenv("PRAIMATE_REMOTE_ORIGINS"), ",") {
		if origin = strings.TrimSpace(origin); origin != "" {
			config.AllowedOrigins = append(config.AllowedOrigins, origin)
		}
	}
	// CLIs, Studio windows and detached children must not inherit the admin
	// credential or attempt to bind a second copy of the server listener.
	for _, name := range []string{"PRAIMATE_REMOTE_LISTEN", "PRAIMATE_REMOTE_TOKEN", "PRAIMATE_REMOTE_TLS_CERT", "PRAIMATE_REMOTE_TLS_KEY", "PRAIMATE_REMOTE_ORIGINS"} {
		_ = os.Unsetenv(name)
	}
	return editionServices{config: config, leases: remote.NewLeases(), files: &remote.Files{}}
}

func (e *editionServices) start(a *App) {
	e.listenerMu.Lock()
	defer e.listenerMu.Unlock()
	e.startListener(a)
}

// Serialized by listenerMu. Closing a gateway must never hold edition.mu:
// HTTP handlers can need that mutex while Shutdown waits for them to return.
func (e *editionServices) startListener(a *App) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.gateway != nil || a.detachedClient != nil || editorFolder != "" {
		return
	}
	e.startError = nil
	if e.startupDisabled {
		return
	}
	if !e.settingsLoaded {
		ready, err := e.prepareServerSettingsLocked(a)
		if err != nil {
			e.startError = err
			return
		}
		if !ready {
			return
		}
	}
	if e.config.Listen == "" {
		publicURL, err := e.tunnelLocked(a).PublicURL(a.ctx)
		if err != nil {
			e.startError = err
			return
		}
		if publicURL == "" {
			return
		}
		e.config.Listen = "127.0.0.1:8844"
	}
	e.registry = a.remoteMethods()
	config := e.config
	config.Registry = e.registry
	config.State = remoteCoreState{a.core}
	config.Files = e.files
	config.Upload = a.storeRemoteUpload
	if frontend, err := fs.Sub(assets, "frontend/dist"); err == nil {
		config.Assets = http.FileServer(http.FS(frontend))
	}
	g, err := remote.New(config, a.remoteCall)
	if err == nil {
		publicURL, loadErr := e.tunnelLocked(a).PublicURL(a.ctx)
		if loadErr != nil {
			err = loadErr
		} else {
			err = g.SetPublicOrigin(publicURL)
		}
	}
	if err == nil {
		e.files.Configure(a.ctx, g.Publish)
		e.address, err = g.Start(a.ctx)
	}
	if err != nil {
		if g != nil {
			_ = g.Close()
		}
		e.startError = err
		fmt.Fprintln(os.Stderr, "PrAImate remote access could not start:", err)
		return
	}
	e.config.Token = ""
	e.config.Administrator = g.AdministratorCredential()
	e.gateway = g
	active := serverConfiguration(e.config)
	e.activeConfiguration = &active
	if a.workers != nil {
		changes, unsubscribe := a.workers.SubscribeChanges()
		go func() {
			defer unsubscribe()
			pending := map[string]bool{}
			ticker := time.NewTicker(250 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-a.ctx.Done():
					return
				case <-g.Done():
					return
				case id, ok := <-changes:
					if !ok {
						return
					}
					pending[id] = true
				case <-ticker.C:
					for id := range pending {
						g.Publish("praimate:worker-update", a.workers.Progress(id))
						delete(pending, id)
					}
				}
			}
		}()
	}
}

func (e *editionServices) close() {
	e.listenerMu.Lock()
	defer e.listenerMu.Unlock()
	e.closeListener()
}

func (e *editionServices) closeListener() {
	e.mu.Lock()
	g := e.gateway
	e.gateway = nil
	e.address = ""
	e.activeConfiguration = nil
	e.mu.Unlock()
	if g != nil {
		_ = g.Close()
	}
	if e.files != nil {
		e.files.Close()
	}
}
func (e *editionServices) openOwner(path string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if path == "" || e.owner != nil {
		return nil
	}
	owner, err := remote.LockProfile(path)
	if err != nil {
		return err
	}
	e.owner = owner
	return nil
}
func (e *editionServices) releaseOwner() {
	e.mu.Lock()
	owner := e.owner
	e.owner = nil
	e.mu.Unlock()
	if owner != nil {
		_ = owner.Close()
	}
}

func (e *editionServices) publish(name string, payload any) {
	e.mu.Lock()
	g := e.gateway
	e.mu.Unlock()
	if g != nil {
		if strings.HasPrefix(name, "term:exit:") {
			e.leases.Release(strings.TrimPrefix(name, "term:exit:"))
		}
		g.Publish(name, payload)
	}
}

// Explicitly map transport operations to the same services the desktop uses.
// Never expose Wails bindings or Studio's credential-bearing terminal plans
// through reflection or a generic "invoke application method" endpoint.
func (a *App) remoteCall(ctx context.Context, method string, body json.RawMessage) (any, error) {
	if _, err := a.requireCore(); err != nil {
		return nil, err
	}
	var p struct {
		ID        string `json:"id"`
		ChatID    string `json:"chatId"`
		AgentID   string `json:"agentId"`
		CLI       string `json:"cli"`
		Model     string `json:"model"`
		Workspace string `json:"workspace"`
		Message   string `json:"message"`
		Data      string `json:"data"`
		Cols      int    `json:"cols"`
		Rows      int    `json:"rows"`
		Task      string `json:"task"`
		TaskID    string `json:"taskId"`
	}
	if len(body) > 0 && string(body) != "null" {
		d := json.NewDecoder(strings.NewReader(string(body)))
		d.DisallowUnknownFields()
		if err := d.Decode(&p); err != nil {
			return nil, err
		}
	}
	switch method {
	case "chats.list":
		return a.ListChats()
	case "chats.messages":
		return a.ChatMessages(p.ID)
	case "chats.create":
		return a.StartCleanChat(p.CLI, p.Model, p.Workspace)
	case "chats.send":
		return a.SendChatStream(p.ID, p.Message, nil)
	case "chats.cancel":
		a.CancelChatTurn(p.ID)
		return true, nil
	case "agents.list":
		return a.ListAgents()
	case "terminals.list":
		return a.ListTerminalSessions(), nil
	case "terminals.start":
		return a.StartCodeSessionWithSkills(p.AgentID, p.CLI, p.Model, p.Workspace, "", "", "")
	case "terminals.resume":
		return a.StartTerminalForChat(p.ChatID, true)
	case "terminals.snapshot":
		return a.GetTerminalSnapshot(p.ID)
	case "terminals.write":
		return true, a.remoteWriteTerminal(ctx, p.ID, p.Data)
	case "terminals.resize":
		if p.Cols < 1 || p.Cols > 4096 || p.Rows < 1 || p.Rows > 2048 {
			return nil, errors.New("invalid terminal dimensions")
		}
		return true, a.remoteResizeTerminal(ctx, p.ID, p.Cols, p.Rows)
	case "terminals.close":
		if _, err := a.edition.leases.Claim(ctx, p.ID, false); err != nil {
			return nil, err
		}
		a.CloseTerminal(p.ID)
		return true, nil
	case "workers.list":
		return a.WorkerRuns()
	case "workers.get":
		return a.WorkerRunSnapshot(p.ID)
	case "workers.start":
		return a.StartWorkerRun(p.Task)
	case "workers.cancel":
		return true, a.CancelWorkerRun(p.ID)
	case "workers.continue":
		return true, a.ContinueWorkerRun(p.ID, p.Task)
	case "workers.task.retry":
		return true, a.RetryWorkerDAGTask(p.ID, p.TaskID)
	default:
		return nil, errors.New("remote method unavailable")
	}
}
