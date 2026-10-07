package studio

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/installer"
	"github.com/sPROFFEs/PrAImate/internal/orchestrator"
	"github.com/sPROFFEs/PrAImate/internal/version"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Each connection owns its session, notifications and cancellation. Core owns
// persistence and execution, shared with the desktop application.
type Server struct {
	core          *core.Core
	ctx           context.Context
	cancel        context.CancelFunc
	mu            sync.Mutex
	writeMu       sync.Mutex
	listener      net.Listener
	clients       map[net.Conn]*Server
	writer        io.Writer
	socketPath    string
	token         string
	authenticated bool
	session       sessionConfig
	running       context.CancelFunc
	approvals     map[string]pendingApproval
	approvalRules map[string]map[string]bool
	window        *DesktopWindowHooks
	workers       *orchestrator.Manager
	workerOwner   *Server
	assistant     *AssistantHooks
	voiceLease    string
	terminalUsage map[string]*core.TerminalUsage
}

type pendingApproval struct {
	reply       chan bool
	scope       string
	tool        string
	description string
}

func NewServer(c *core.Core) *Server {
	return newServer(c, true)
}

func newServer(c *core.Core, ownWorkers bool) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{core: c, ctx: ctx, cancel: cancel, clients: map[net.Conn]*Server{}, approvals: map[string]pendingApproval{}, approvalRules: map[string]map[string]bool{}, session: sessionConfig{CLI: "praimate-code", Tools: "safe"}}
	if ownWorkers {
		s.workers = orchestrator.NewManager(c, ctx)
	}
	return s
}

func (s *Server) newConnectionServer() *Server {
	child := newServer(s.core, false)
	child.cancel()
	child.ctx, child.cancel = context.WithCancel(s.ctx)
	child.workers = s.workers
	child.workerOwner = s
	child.token = s.token
	s.mu.Lock()
	child.window = s.window
	s.mu.Unlock()
	return child
}

// DesktopWindowHooks lets an authenticated Studio connection control only the
// main Desktop window. Core remains alive while hidden.
type DesktopWindowHooks struct {
	Hide     func()
	Show     func()
	IsHidden func() bool
}

func (s *Server) setDesktopWindowHooks(hooks *DesktopWindowHooks) {
	s.mu.Lock()
	s.window = hooks
	s.mu.Unlock()
}

// StartLocal opens an authenticated, ephemeral loopback endpoint on both Windows
// and Linux. Its token is passed through the launch environment and a private
// connection descriptor, never through command-line arguments or diagnostics.
func (s *Server) StartLocal() (string, string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", "", err
	}
	s.mu.Lock()
	s.token = hex.EncodeToString(b)
	s.listener = l
	s.mu.Unlock()
	go s.accept(l)
	return "tcp://" + l.Addr().String(), s.token, nil
}
func (s *Server) ServeSocket() error {
	if runtime.GOOS == "windows" {
		return errors.New("use serve --stdio on Windows; Studio opens its own authenticated connection")
	}
	name, err := SocketPath()
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return err
	}
	// Do not unlink a socket owned by a live daemon.
	l, err := net.Listen("unix", name)
	if err != nil {
		return err
	}
	if err = os.Chmod(name, 0600); err != nil {
		l.Close()
		return err
	}
	s.mu.Lock()
	s.listener, s.socketPath = l, name
	s.mu.Unlock()
	return s.accept(l)
}
func (s *Server) accept(l net.Listener) error {
	for {
		conn, err := l.Accept()
		if err != nil {
			if s.ctx.Err() != nil {
				return nil
			}
			return err
		}
		s.mu.Lock()
		s.clients[conn] = nil
		s.mu.Unlock()
		go func() {
			defer conn.Close()
			child := s.newConnectionServer()
			s.mu.Lock()
			s.clients[conn] = child
			s.mu.Unlock()
			defer child.Close()
			_ = child.ServeStdio(conn, conn)
			s.mu.Lock()
			delete(s.clients, conn)
			idle := len(s.clients) == 0
			s.mu.Unlock()
			if idle {
				go s.restoreDesktopWindowWhenIdle(1500 * time.Millisecond)
			}
		}()
	}
}

func (s *Server) restoreDesktopWindowWhenIdle(delay time.Duration) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-s.ctx.Done():
		return
	}
	s.mu.Lock()
	idle, hooks := len(s.clients) == 0, s.window
	s.mu.Unlock()
	if idle && hooks != nil && hooks.Show != nil && hooks.IsHidden != nil && hooks.IsHidden() {
		hooks.Show()
	}
}
func (s *Server) Close() error {
	s.cancel()
	s.mu.Lock()
	lease := s.voiceLease
	s.voiceLease = ""
	defer func() {
		s.mu.Unlock()
		if lease != "" {
			raw, _ := json.Marshal(map[string]string{"id": lease})
			_, _ = s.assistantCall(context.Background(), "voice.end", raw)
		}
	}()
	if s.running != nil {
		s.running()
	}
	for id, usage := range s.terminalUsage {
		usage.Close()
		delete(s.terminalUsage, id)
	}
	if s.listener != nil {
		_ = s.listener.Close()
	}
	for conn := range s.clients {
		_ = conn.Close()
	}
	if s.socketPath != "" {
		_ = os.Remove(s.socketPath)
		s.socketPath = ""
	}
	return nil
}
func (s *Server) write(value any) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.writer != nil {
		_ = json.NewEncoder(s.writer).Encode(value)
	}
}
func (s *Server) Broadcast(method string, params any) {
	s.write(RPCNotification{JSONRPC: "2.0", Method: method, Params: params})
}
func (s *Server) ServeStdio(r io.Reader, w io.Writer) error {
	s.writer = w
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)
	var workers sync.WaitGroup
	probes := make(chan struct{}, 4)
	assistantWork := make(chan struct{}, 8)
	defer workers.Wait()
	defer s.cancel()
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		var req RPCRequest
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			s.write(RPCResponse{JSONRPC: "2.0", Error: &RPCError{Code: -32700, Message: "Parse error"}})
			continue
		}
		if req.JSONRPC != "2.0" || req.Method == "" {
			s.write(RPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &RPCError{Code: -32600, Message: "Invalid request"}})
			continue
		}
		// Execute long requests separately so Stop and approval replies can arrive.
		// Other requests keep their wire order.
		if req.ID != nil && (req.Method == "chats.send" || req.Method == "workflows.run" || req.Method == "runs.resume" || req.Method == "clis.install" || req.Method == "agents.knowledge.index") {
			ctx, err := s.beginRun()
			if err != nil {
				s.write(RPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &RPCError{Code: -32000, Message: err.Error()}})
				continue
			}
			workers.Add(1)
			go func(req RPCRequest) {
				defer workers.Done()
				response := s.dispatchContext(ctx, req)
				s.endRun()
				s.write(response)
			}(req)
		} else if req.ID != nil && (strings.HasPrefix(req.Method, "assistant.") || strings.HasPrefix(req.Method, "voice.")) {
			// Model work/downloads must leave the connection free for Stop and approvals.
			if req.Method == "assistant.cancel" || req.Method == "assistant.approve" || req.Method == "assistant.artifacts.cancel" || req.Method == "voice.cancel" || req.Method == "voice.end" {
				s.write(s.dispatch(req))
				continue
			}
			select {
			case assistantWork <- struct{}{}:
			default:
				s.write(RPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &RPCError{Code: -32000, Message: "Assistant requests are busy; wait for the current operation"}})
				continue
			}
			workers.Add(1)
			go func(req RPCRequest) {
				defer workers.Done()
				defer func() { <-assistantWork }()
				s.write(s.dispatch(req))
			}(req)
		} else if req.ID != nil && (req.Method == "clis.list" || req.Method == "models.list" || req.Method == "models.refresh" || req.Method == "tools.detect" || req.Method == "mcp.probe" || req.Method == "local.hosts.test") {
			// Version/model probes can take seconds. They must not block Stop,
			// approvals or history, and concurrent probes are bounded.
			select {
			case probes <- struct{}{}:
				workers.Add(1)
				go func(req RPCRequest) {
					defer workers.Done()
					defer func() { <-probes }()
					s.write(s.dispatch(req))
				}(req)
			default:
				s.write(RPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &RPCError{Code: -32000, Message: "CLI detection is already in progress; retry shortly"}})
			}
		} else if req.ID != nil {
			s.write(s.dispatch(req))
		}
	}
	return scanner.Err()
}
func (s *Server) beginRun() (context.Context, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token != "" && !s.authenticated {
		return nil, errors.New("initialize with launch token first")
	}
	if s.running != nil {
		return nil, errors.New("a run is already active; stop it first")
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.running = cancel
	return core.WithApprovalProvider(ctx, s.approvalProvider), nil
}
func (s *Server) endRun() {
	s.mu.Lock()
	if s.running != nil {
		s.running()
	}
	s.running = nil
	s.mu.Unlock()
	s.Broadcast("run.finished", nil)
}
func (s *Server) dispatch(req RPCRequest) RPCResponse { return s.dispatchContext(s.ctx, req) }
func (s *Server) dispatchContext(ctx context.Context, req RPCRequest) RPCResponse {
	resp := RPCResponse{JSONRPC: "2.0", ID: req.ID}
	s.mu.Lock()
	allowed := s.token == "" || s.authenticated || req.Method == "system.initialize"
	s.mu.Unlock()
	if !allowed {
		resp.Error = &RPCError{Code: -32001, Message: "initialize with launch token first"}
		return resp
	}
	body, err := json.Marshal(req.Params)
	if err == nil {
		resp.Result, err = s.execute(ctx, req.Method, body)
	}
	if err != nil {
		resp.Error = &RPCError{Code: -32000, Message: err.Error()}
	}
	return resp
}
func capabilities() ServerCapabilities {
	return ServerCapabilities{Agents: true, Workflows: true, Skills: true, MCP: true, LocalModels: true, Approvals: true, Streaming: true, Runs: true}
}
func (s *Server) execute(ctx context.Context, method string, body []byte) (any, error) {
	if strings.HasPrefix(method, "assistant.") || strings.HasPrefix(method, "voice.") {
		return s.assistantCall(ctx, method, body)
	}
	switch method {
	case "system.initialize":
		if s.isRunning() {
			return nil, errors.New("cannot reinitialize an active run")
		}
		var p struct {
			Token           string
			ProtocolVersion string
			Workspace       string
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		if p.ProtocolVersion != "" && p.ProtocolVersion != "1" {
			return nil, errors.New("unsupported protocol version")
		}
		if s.token != "" && subtle.ConstantTimeCompare([]byte(p.Token), []byte(s.token)) != 1 {
			return nil, errors.New("invalid launch token")
		}
		s.mu.Lock()
		s.authenticated = true
		s.session.Workspace = p.Workspace
		s.mu.Unlock()
		if s.workerOwner != nil {
			s.workerOwner.rebroadcastWorkerApprovals(s)
		}
		return map[string]any{"serverVersion": version.Current, "protocolVersion": "1", "capabilities": capabilities()}, nil
	case "system.version":
		return map[string]string{"name": version.Name, "version": version.Current}, nil
	case "system.capabilities":
		return capabilities(), nil
	case "system.status":
		return map[string]any{"version": version.Current, "backendRunning": true, "session": s.sessionSnapshot()}, nil
	case "desktop.window.status", "desktop.window.hide", "desktop.window.show", "desktop.window.toggle":
		s.mu.Lock()
		hooks := s.window
		s.mu.Unlock()
		if hooks == nil || hooks.Hide == nil || hooks.Show == nil || hooks.IsHidden == nil {
			return map[string]any{"available": false, "hidden": false}, nil
		}
		if method == "desktop.window.hide" || method == "desktop.window.toggle" && !hooks.IsHidden() {
			hooks.Hide()
		} else if method == "desktop.window.show" || method == "desktop.window.toggle" && hooks.IsHidden() {
			hooks.Show()
		}
		state := map[string]any{"available": true, "hidden": hooks.IsHidden()}
		s.Broadcast("desktop.window.changed", state)
		return state, nil
	case "session.get":
		p := s.sessionSnapshot()
		agents, err := s.core.ListAgents(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]any{"version": version.Current, "activeCLI": p.CLI, "activeModel": p.Model, "activeAgent": p.AgentID, "activeTools": p.Tools, "activeWorkspace": p.Workspace, "mcpServers": p.MCPServers, "skills": p.Skills, "localEndpoint": p.LocalEndpoint, "localModel": p.LocalModel, "localContextTokens": p.LocalContextTokens, "localOutputTokens": p.LocalOutputTokens, "agents": agents}, nil
	case "session.update":
		return s.updateSession(ctx, body)
	case "clis.list":
		return core.ListCLIs(ctx), nil
	case "clis.install.methods":
		var p struct{ CLI string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		methods, _ := cliInstallMethods(p.CLI)
		return methods, nil
	case "clis.install":
		var p struct{ CLI, MethodID string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return true, s.installCLI(ctx, p.CLI, p.MethodID)
	case "models.list", "models.refresh":
		var p struct{ CLI string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		if p.CLI == "" {
			p.CLI = s.sessionSnapshot().CLI
		}
		if p.CLI == "praimate-cli" {
			if assignments, err := s.core.NativeModelAssignments(ctx); err == nil && len(assignments) > 0 {
				models := make([]string, 0, len(assignments))
				for _, item := range assignments {
					models = append(models, item.HostID+"::"+item.Model)
				}
				return models, nil
			}
			if method == "models.refresh" {
				return s.core.RefreshNativeModels(ctx, s.sessionSnapshot().LocalEndpoint)
			}
			return s.core.NativeModels(ctx, s.sessionSnapshot().LocalEndpoint)
		}
		if method == "models.refresh" {
			return core.RefreshCLIModels(ctx, p.CLI), nil
		}
		return core.ListCLIModels(ctx, p.CLI), nil
	case "workers.config.get":
		return orchestrator.LoadConfig(ctx, s.core)
	case "workers.config.save":
		var config orchestrator.Config
		if err := json.Unmarshal(body, &config); err != nil {
			return nil, err
		}
		if workspace := s.sessionSnapshot().Workspace; workspace != "" {
			config.Workspace = workspace
		}
		return true, orchestrator.SaveConfig(ctx, s.core, config)
	case "workers.plan":
		var p struct {
			Task        string
			Config      orchestrator.Config
			MaxParallel int
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		p.Config.Workspace = s.sessionSnapshot().Workspace
		return s.workers.PlanDAG(p.Task, p.Config, p.MaxParallel)
	case "workers.run.config":
		var p struct {
			ID     string
			Config orchestrator.Config
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return true, s.workers.UpdateRunConfig(p.ID, p.Config)
	case "workers.plan.retry":
		var p struct{ ID string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return true, s.workers.RetryPlanning(p.ID)
	case "workers.graph.save":
		var p struct {
			ID          string
			Tasks       []orchestrator.DAGTask
			MaxParallel int
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return true, s.workers.UpdateDAG(p.ID, p.Tasks, p.MaxParallel)
	case "workers.graph.execute":
		var p struct{ ID string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return true, s.workers.ExecuteDAG(p.ID, s.workerApprovalProvider)
	case "workers.graph.review":
		var p struct{ ID, TaskID, Decision string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return true, s.workers.ReviewDAGTask(p.ID, p.TaskID, p.Decision)
	case "workers.graph.merge":
		var p struct{ ID string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return true, s.workers.MergeDAG(p.ID)
	case "workers.graph.reset":
		var p struct{ ID, TaskID string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return true, s.workers.ResetDAGTask(p.ID, p.TaskID)
	case "workers.graph.retry":
		var p struct{ ID, TaskID string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return true, s.workers.RetryDAGTask(p.ID, p.TaskID)
	case "workers.task.preview":
		var p struct{ ID, TaskID string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return s.workers.TaskPreview(p.ID, p.TaskID)
	case "workers.activity":
		var p struct {
			ID, WorkerID string
			Before       int64
			Limit        int
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return s.workers.Activity(p.ID, p.WorkerID, p.Before, p.Limit)
	case "workers.graph.profile":
		var p struct{ ID, TaskID string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return true, s.workers.UseDAGTaskProfile(p.ID, p.TaskID)
	case "workers.graph.cleanup":
		var p struct{ ID string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return true, s.workers.CleanupDAG(p.ID)
	case "workers.start":
		var p struct {
			Task   string
			Config *orchestrator.Config
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		workspace := s.sessionSnapshot().Workspace
		if workspace == "" {
			return nil, errors.New("select a workspace before starting workers")
		}
		if p.Config != nil {
			p.Config.Workspace = workspace
			return s.workers.StartWithConfigAndApproval(p.Task, *p.Config, s.workerApprovalProvider)
		}
		return s.workers.StartWithApproval(p.Task, workspace, s.workerApprovalProvider)
	case "workers.list":
		return s.workers.List()
	case "workers.get":
		var p struct{ ID string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return s.workers.Snapshot(p.ID)
	case "workers.cancel":
		var p struct{ ID string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return true, s.workers.Cancel(p.ID)
	case "workers.continue":
		var p struct{ ID, Task string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return true, s.workers.ContinueWithApproval(p.ID, p.Task, s.workerApprovalProvider)
	case "workers.rename":
		var p struct{ ID, Title string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return true, s.workers.Rename(p.ID, p.Title)
	case "workers.delete":
		var p struct{ ID string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return true, s.workers.Delete(p.ID)
	case "terminals.list":
		return terminalCLIs(), nil
	case "terminals.prepare":
		return s.prepareTerminal(body)
	case "terminals.closed":
		var params struct {
			UsageID string `json:"usageId"`
		}
		if err := json.Unmarshal(body, &params); err != nil {
			return nil, err
		}
		s.mu.Lock()
		usage := s.terminalUsage[params.UsageID]
		delete(s.terminalUsage, params.UsageID)
		s.mu.Unlock()
		usage.Close()
		return true, nil
	case "projects.list", "projects.recent":
		return NewManager(s.core).ListRecentProjects()
	case "workspace.register", "projects.open":
		var p struct{ Path string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return true, NewManager(s.core).SaveRecentProject(p.Path)
	case "usage.dashboard":
		var p struct {
			Month string `json:"month"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return s.core.UsageDashboard(ctx, p.Month)
	case "chats.list":
		chats, err := s.core.ListChats(ctx, 0)
		if err != nil {
			return nil, err
		}
		out := []map[string]any{}
		workspace := s.sessionSnapshot().Workspace
		for _, c := range chats {
			if c.Settings.Surface != "studio-ide" || filepath.Clean(c.WorkspacePath) != filepath.Clean(workspace) {
				continue
			}
			out = append(out, map[string]any{"id": c.ID, "title": c.Title, "cli": c.CLIAgent, "agentId": c.AgentID, "updatedAt": c.UpdatedAt})
		}
		return out, nil
	case "chats.create":
		var p struct{ Title string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return s.createChat(ctx, p.Title)
	case "chats.get", "chats.messages", "chats.delete", "chats.rename":
		var p struct {
			ID    string
			Title string
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		chat, err := s.ownedChat(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		switch method {
		case "chats.get":
			return chat, nil
		case "chats.messages":
			return s.core.ListMessages(ctx, p.ID, 0)
		case "chats.delete":
			if s.isRunning() {
				return nil, errors.New("stop the active run first")
			}
			return true, s.core.DeleteChat(ctx, p.ID)
		default:
			return true, s.core.RenameChat(ctx, p.ID, p.Title)
		}
	case "chats.send":
		return s.sendChat(ctx, body)
	case "attachments.stage":
		var p struct {
			ChatID  string
			Sources []string
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		if _, err := s.ownedChat(ctx, p.ChatID); err != nil {
			return nil, err
		}
		return stageAttachments(p.ChatID, p.Sources)
	case "runs.cancel":
		s.mu.Lock()
		if s.running != nil {
			s.running()
		}
		s.mu.Unlock()
		return true, nil
	case "agents.list":
		agents, err := s.core.ListAgents(ctx)
		if err != nil {
			return nil, err
		}
		out := []map[string]any{}
		for _, a := range agents {
			out = append(out, map[string]any{"id": a.ID, "name": a.Name, "description": a.Description, "supports": a.Supports, "surfaces": a.Surfaces})
		}
		return out, nil
	case "agents.get":
		var p struct{ ID string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return s.core.GetAgent(ctx, p.ID)
	case "agents.yaml":
		var p struct{ ID string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		agent, err := s.core.GetAgent(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		raw, err := core.MarshalAgentYAML(agent)
		return string(raw), err
	case "agents.save":
		var p struct{ YAML string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return s.core.ImportAgentYAML(ctx, []byte(p.YAML), "studio")
	case "agents.guided.preview", "agents.guided.create":
		var p core.GuidedAgentRequest
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		if method == "agents.guided.preview" {
			return core.PreviewGuidedAgent(p)
		}
		return s.core.CreateGuidedAgent(ctx, p)
	case "agents.delete":
		var p struct{ ID string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return true, s.core.DeleteAgent(ctx, p.ID)
	case "agents.pack.inspect":
		var p struct{ Path string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return s.core.InspectAgentPack(ctx, p.Path)
	case "agents.pack.import":
		var p struct{ Path, Review string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return s.core.ImportReviewedAgentPack(ctx, p.Path, p.Review)
	case "agents.pack.export":
		var p struct{ ID, Path string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return true, s.core.ExportAgentPack(ctx, p.ID, p.Path)
	case "agents.import":
		var p struct{ Path string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		switch strings.ToLower(filepath.Ext(p.Path)) {
		case ".md", ".markdown", ".yaml", ".yml":
			return s.core.ImportAgent(ctx, p.Path)
		default:
			return nil, fmt.Errorf("use reviewed pack import for archives")
		}
	case "agents.export":
		var p struct{ ID, Path string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return true, s.core.ExportAgent(ctx, p.ID, p.Path)
	case "agents.knowledge.config.get":
		var p struct{ ID string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		a, err := s.core.GetAgent(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"config": a.KnowledgeConfig, "hasAPIKey": a.HasKnowledgeAPIKey(), "mode": a.Knowledge}, nil
	case "agents.knowledge.config.save":
		var p struct {
			ID        string
			Config    core.AgentKnowledgeConfig
			APIKey    string
			RemoveKey bool
			Mode      *string
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		var mode []string
		if p.Mode != nil {
			mode = []string{*p.Mode}
		}
		_, err := s.core.SaveAgentKnowledgeConfig(ctx, p.ID, p.Config, p.APIKey, p.RemoveKey, mode...)
		return true, err
	case "agents.knowledge.config.test":
		var p struct{ ID string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return true, s.core.TestAgentKnowledgeRemote(ctx, p.ID)
	case "agents.knowledge.index":
		var p struct{ ID string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		idx, err := s.core.BuildAgentKnowledgeWithProgress(ctx, p.ID, func(done, total int) {
			s.Broadcast("knowledge.progress", map[string]any{"id": p.ID, "done": done, "total": total})
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{"documents": len(idx.Documents), "chunks": len(idx.Chunks), "nodes": len(idx.Graph.Nodes), "skipped": idx.Skipped}, nil
	case "agents.knowledge.list":
		var p struct{ ID string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return core.ListAgentKnowledge(p.ID)
	case "agents.knowledge.add":
		var p struct {
			ID      string
			Sources []string
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		if _, err := s.core.GetAgent(ctx, p.ID); err != nil {
			return nil, err
		}
		return core.AddAgentKnowledgeFiles(p.ID, p.Sources)
	case "agents.knowledge.delete":
		var p struct{ ID, Path string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		if _, err := s.core.GetAgent(ctx, p.ID); err != nil {
			return nil, err
		}
		return true, core.DeleteAgentKnowledgeFile(p.ID, p.Path)
	case "workflows.list":
		agents, err := s.core.ListAgents(ctx)
		if err != nil {
			return nil, err
		}
		out := []map[string]any{}
		for _, a := range agents {
			for _, w := range a.Workflows {
				out = append(out, map[string]any{"name": w.Name, "description": w.Description, "agentId": a.ID, "agentName": a.Name, "inputs": w.Inputs})
			}
		}
		return out, nil
	case "workflows.run":
		return s.runWorkflow(ctx, body)
	case "skills.list":
		return core.InstalledSkillSummaries(ctx)
	case "skills.library":
		var p core.SkillLibraryRequest
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return s.core.SkillLibrary(ctx, p)
	case "skills.rollout.get":
		return s.core.SkillsV2RolloutState(ctx)
	case "skills.rollout.set":
		var p struct{ Enabled bool }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return true, s.core.SetSkillsV2RolloutState(ctx, p.Enabled)
	case "mcp.list":
		servers, err := s.core.ListMCPServers(ctx, false)
		if err != nil {
			return nil, err
		}
		out := []map[string]any{}
		for _, server := range servers {
			out = append(out, map[string]any{"id": server.ID, "name": server.Name, "enabled": server.Enabled, "transport": server.Transport})
		}
		return out, nil
	case "mcp.catalogue":
		return core.ListMCPCatalogue(), nil
	case "mcp.get":
		var p struct{ ID string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return s.core.GetMCPServer(ctx, p.ID)
	case "mcp.connect":
		var p core.ConnectMCPRequest
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return s.core.ConnectMCP(ctx, p)
	case "mcp.add":
		var p core.AddCustomMCPRequest
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return s.core.AddCustomMCP(ctx, p)
	case "mcp.update":
		var p struct {
			ID      string
			Request core.AddCustomMCPRequest
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return s.core.UpdateMCPServer(ctx, p.ID, p.Request)
	case "mcp.enable":
		var p struct {
			ID      string
			Enabled bool
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return true, s.core.SetMCPEnabled(ctx, p.ID, p.Enabled)
	case "mcp.delete":
		var p struct{ ID string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return true, s.core.DeleteMCPServer(ctx, p.ID)
	case "mcp.probe":
		var p struct{ ID string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return s.core.ProbeMCPServer(ctx, p.ID)
	case "tools.list":
		return installer.KnownTools(), nil
	case "tools.detect":
		return installer.DetectTools(ctx), nil
	case "runs.list":
		return s.core.ListManagedRuns("")
	case "runs.get":
		var p struct{ ID string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return s.core.GetManagedRun(p.ID)
	case "runs.resume":
		var p struct{ ID string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return s.core.ResumeManagedRun(ctx, p.ID, func(event core.ManagedRunEvent) { s.Broadcast("managed-run.event", event) })
	case "runs.artifact":
		var p struct{ ID, Name string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		raw, err := s.core.ReadManagedArtifact(p.ID, p.Name)
		if err != nil {
			return nil, err
		}
		if !utf8.Valid(raw) {
			return nil, errors.New("artifact is binary and cannot be previewed as text")
		}
		return string(raw), nil
	case "privacy.list":
		return s.core.ListPrivacyPatterns(ctx)
	case "privacy.add":
		var p struct{ Pattern string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return true, s.core.AddPrivacyPattern(ctx, p.Pattern)
	case "privacy.delete":
		var p struct{ Index int }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return true, s.core.DeletePrivacyPattern(ctx, p.Index)
	case "local.hosts.list":
		return s.core.ListLocalHosts(ctx)
	case "local.hosts.save":
		var p core.LocalHost
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return s.core.SaveLocalHost(ctx, p)
	case "local.hosts.delete":
		var p struct{ ID string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return true, s.core.DeleteLocalHost(ctx, p.ID)
	case "local.hosts.test":
		var p struct{ ID, Endpoint, APIKey string }
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return s.core.TestLocalHost(ctx, p.ID, p.Endpoint, p.APIKey)
	case "runs.approve", "runs.deny":
		var p struct {
			ApprovalID string
			Remember   bool
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		pending, ok := s.takeApproval(p.ApprovalID, p.Remember && method == "runs.approve")
		if !ok && s.workerOwner != nil {
			pending, ok = s.workerOwner.takeApproval(p.ApprovalID, p.Remember && method == "runs.approve")
		}
		if !ok {
			return nil, errors.New("approval expired or belongs to another session")
		}
		pending.reply <- method == "runs.approve"
		return true, nil
	default:
		return nil, fmt.Errorf("method %q not found", method)
	}
}
func (s *Server) isRunning() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.running != nil }
func (s *Server) takeApproval(id string, remember bool) (pendingApproval, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending, ok := s.approvals[id]
	if ok {
		delete(s.approvals, id)
		if remember {
			if s.approvalRules[pending.scope] == nil {
				s.approvalRules[pending.scope] = map[string]bool{}
			}
			s.approvalRules[pending.scope][pending.tool] = true
		}
	}
	return pending, ok
}

func (s *Server) workerApprovalProvider(scope string) *core.ApprovalConfig {
	if s.workerOwner != nil {
		return s.workerOwner.approvalProvider(scope)
	}
	return s.approvalProvider(scope)
}

func (s *Server) broadcastApproval(payload map[string]any) {
	if s.workerOwner != nil {
		s.Broadcast("run.approval.required", payload)
		return
	}
	s.mu.Lock()
	children := make([]*Server, 0, len(s.clients))
	for _, child := range s.clients {
		if child != nil {
			child.mu.Lock()
			authenticated := child.authenticated
			child.mu.Unlock()
			if authenticated {
				children = append(children, child)
			}
		}
	}
	s.mu.Unlock()
	if len(children) == 0 {
		s.Broadcast("run.approval.required", payload)
		return
	}
	for _, child := range children {
		child.Broadcast("run.approval.required", payload)
	}
}

func (s *Server) rebroadcastWorkerApprovals(child *Server) {
	s.mu.Lock()
	pending := make([]map[string]any, 0)
	for id, approval := range s.approvals {
		if strings.HasPrefix(approval.scope, "worker-") || strings.HasPrefix(approval.scope, "workers-") {
			pending = append(pending, map[string]any{"runId": approval.scope, "approvalId": id, "description": approval.description})
		}
	}
	s.mu.Unlock()
	for _, payload := range pending {
		child.Broadcast("run.approval.required", payload)
	}
}

func (s *Server) approvalProvider(scope string) *core.ApprovalConfig {
	return &core.ApprovalConfig{Request: func(ctx context.Context, tool string, input map[string]any) (bool, error) {
		s.mu.Lock()
		remembered := s.approvalRules[scope][tool]
		s.mu.Unlock()
		if remembered {
			return true, nil
		}
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return false, err
		}
		id := hex.EncodeToString(b)
		ch := make(chan bool, 1)
		detail, _ := json.Marshal(input)
		description := tool + ": " + string(detail)
		s.mu.Lock()
		s.approvals[id] = pendingApproval{reply: ch, scope: scope, tool: tool, description: description}
		s.mu.Unlock()
		defer func() { s.mu.Lock(); delete(s.approvals, id); s.mu.Unlock() }()
		s.broadcastApproval(map[string]any{"runId": scope, "approvalId": id, "description": description})
		select {
		case allow := <-ch:
			return allow, nil
		case <-ctx.Done():
			return false, ctx.Err()
		case <-s.ctx.Done():
			return false, s.ctx.Err()
		case <-time.After(5 * time.Minute):
			return false, errors.New("approval timed out")
		}
	}}
}
