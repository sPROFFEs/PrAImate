package main

// Terminal sessions — host a real third-party CLI (claude / codex /
// opencode / …) live inside the GUI via a PTY. This is what makes the
// chat "fully functional for coding": you get the actual CLI's
// interactive terminal interface — streaming tokens, tool calls, file edits,
// approvals — running in a project folder, not a reimplemented loop.
//
// Lifecycle (all driven from the frontend over Wails bindings):
//   StartTerminal  → spawn CLI in a PTY, stream output via events
//   WriteTerminal  → forward keystrokes
//   ResizeTerminal → keep the PTY's window size in sync with xterm.js
//   CloseTerminal  → kill the child and clean up
//
// Output is emitted as Wails events named "term:data:<id>" (base64 so
// arbitrary bytes survive JSON) and a one-shot "term:exit:<id>".

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/aymanbagabas/go-pty"
)

type termSession struct {
	id           string
	chatID       string // empty for terminals not bound to a chat row
	cwd          string
	name         string
	pty          pty.Pty
	cmd          *pty.Cmd
	cleanup      func()
	once         sync.Once
	history      []byte
	historyStart int64
	outputEnd    int64
}

const terminalHistoryLimit = 4 << 20

// TerminalData is one offset-addressed PTY output chunk. Offsets let a newly
// mounted xterm replay the snapshot and de-duplicate output that arrived while
// the snapshot request was in flight.
type TerminalData struct {
	Data        string `json:"data"`
	StartOffset int64  `json:"startOffset"`
	EndOffset   int64  `json:"endOffset"`
}

// TerminalSnapshot is the retained tail of a live terminal's byte stream.
type TerminalSnapshot struct {
	Data        string `json:"data"`
	StartOffset int64  `json:"startOffset"`
	EndOffset   int64  `json:"endOffset"`
}

// TermInfo is a snapshot of one live PTY session for the Sessions panel.
type TermInfo struct {
	ID     string `json:"id"`
	ChatID string `json:"chatId,omitempty"`
	Cwd    string `json:"cwd,omitempty"`
	Name   string `json:"name,omitempty"`
}

type termManager struct {
	mu       sync.Mutex
	sessions map[string]*termSession
	seq      int
}

func newTermManager() *termManager {
	// Versions before log-free storage retained terminal output as plaintext
	// .log files. Remove that legacy cache once; live scrollback now stays in
	// memory and disappears with the process.
	cacheDir, err := os.UserCacheDir()
	if err != nil || strings.TrimSpace(cacheDir) == "" {
		cacheDir = os.TempDir()
	}
	_ = os.RemoveAll(filepath.Join(cacheDir, "praimate", "code-history"))
	return &termManager{
		sessions: map[string]*termSession{},
	}
}

// list returns a snapshot of every live PTY session so the Sessions
// panel can match chats to their running terminals and offer "resume"
// instead of "open new".
func (tm *termManager) list() []TermInfo {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	out := make([]TermInfo, 0, len(tm.sessions))
	for _, s := range tm.sessions {
		out = append(out, TermInfo{ID: s.id, ChatID: s.chatID, Cwd: s.cwd, Name: s.name})
	}
	return out
}

// bindChat associates a live PTY with a chat row. Called after a chat is
// created for a terminal session so the Sessions panel can resume the
// PTY by chat ID.
func (tm *termManager) bindChat(id, chatID string) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	s := tm.sessions[id]
	if s == nil {
		return fmt.Errorf("no terminal %q", id)
	}
	if s.chatID == chatID {
		return nil
	}
	if s.chatID != "" {
		return fmt.Errorf("terminal %q is already bound to chat %q", id, s.chatID)
	}
	if strings.TrimSpace(chatID) == "" {
		return fmt.Errorf("chat id is required")
	}
	s.chatID = chatID
	return nil
}

func (tm *termManager) recordOutput(id string, data []byte) (TerminalData, bool) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	s := tm.sessions[id]
	if s == nil {
		return TerminalData{}, false
	}
	start := s.outputEnd
	s.outputEnd += int64(len(data))
	s.history = append(s.history, data...)
	if len(s.history) > terminalHistoryLimit {
		// Drop a larger chunk (25% of the limit) when overflowing so we
		// don't incur a 4MB allocation on every subsequent 8KB read.
		dropChunk := terminalHistoryLimit / 4
		drop := len(s.history) - terminalHistoryLimit + dropChunk
		s.history = append([]byte(nil), s.history[drop:]...)
		s.historyStart += int64(drop)
	}
	return TerminalData{
		Data:        base64.StdEncoding.EncodeToString(data),
		StartOffset: start,
		EndOffset:   s.outputEnd,
	}, true
}

// codeSnapshot returns the in-memory tail of a live Code terminal together
// with its current offset. Terminal output is intentionally never written to
// disk. The frontend subscribes before this call, writes Data once, then
// drops queued chunks ending at EndOffset.
func (tm *termManager) codeSnapshot(chatID, termID string) (TerminalSnapshot, error) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if strings.TrimSpace(chatID) == "" {
		return TerminalSnapshot{}, fmt.Errorf("chat id is required")
	}
	if termID == "" {
		return TerminalSnapshot{}, nil
	}
	s := tm.sessions[termID]
	if s == nil {
		return TerminalSnapshot{}, fmt.Errorf("no terminal %q", termID)
	}
	if s.chatID != chatID {
		return TerminalSnapshot{}, fmt.Errorf("terminal %q is not bound to chat %q", termID, chatID)
	}
	return TerminalSnapshot{
		Data:        base64.StdEncoding.EncodeToString(s.history),
		StartOffset: s.historyStart,
		EndOffset:   s.outputEnd,
	}, nil
}

func (tm *termManager) snapshot(id string) (TerminalSnapshot, error) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	s := tm.sessions[id]
	if s == nil {
		return TerminalSnapshot{}, fmt.Errorf("no terminal %q", id)
	}
	return TerminalSnapshot{
		Data:        base64.StdEncoding.EncodeToString(s.history),
		StartOffset: s.historyStart,
		EndOffset:   s.outputEnd,
	}, nil
}

// start spawns name with args in cwd inside a new PTY. env adds to the
// inherited environment. emit fans PTY output into the main Wails window and
// any detached renderer until the child exits.
func (tm *termManager) start(name string, args []string, cwd string, env []string, emit func(string, any)) (string, error) {
	return tm.startWithCleanup(name, args, cwd, env, emit, nil)
}

// startWithCleanup owns a per-session resource alongside the PTY. Cleanup runs
// exactly once after either normal exit or an explicit Stop; it is never used
// for project/global configuration.
func (tm *termManager) startWithCleanup(name string, args []string, cwd string, env []string, emit func(string, any), cleanup func()) (string, error) {
	if cwd == "" {
		if home, err := os.UserHomeDir(); err == nil {
			cwd = home
		}
	}
	if cwd != "" {
		if abs, err := filepath.Abs(cwd); err == nil {
			cwd = abs
		}
	}
	// Resolve to an absolute path BEFORE handing it to go-pty / Cmd.
	// Go's exec on Windows joins Cmd.Dir + bare name first when
	// resolving — so a bare `opencode` with Cmd.Dir set to the project
	// folder produces errors like
	//   exec: "C:\…\<project>\opencode": not found in %PATH%
	// even though opencode IS on PATH, just not in <project>. Pre-
	// resolving collapses the ambiguity on every platform.
	resolved, lpErr := exec.LookPath(name)
	if lpErr != nil {
		return "", fmt.Errorf("%s not on PATH — install it (CLIs tab) or click 'Re-scan PATH' if you just installed it in another terminal", name)
	}
	if name == "opencode" || name == "praimate-code" {
		// Some OpenCode builds open their log file before creating its parent
		// directory. Prepare only the directory (PrAImate never writes logs)
		// so a first launch from the GUI is as reliable as a second launch.
		if err := ensureOpenCodeLogDir(); err != nil {
			return "", fmt.Errorf("prepare OpenCode state directory: %w", err)
		}
	}
	p, err := pty.New()
	if err != nil {
		return "", fmt.Errorf("open pty: %w", err)
	}
	c := p.Command(resolved, args...)
	c.Dir = cwd
	// Cmd.Env=nil inherits the parent, but assigning only our MCP/local-LLM
	// overlay replaces the environment completely. That used to discard
	// LANG/LC_* (and HOME/PATH), making interactive CLIs interpret UTF-8 input
	// such as accents as unrelated control/glyph bytes. Always merge overlays
	// into the inherited environment and guarantee a UTF-8 terminal locale.
	c.Env = terminalEnvironment(os.Environ(), env)
	if err := c.Start(); err != nil {
		_ = p.Close()
		return "", fmt.Errorf("start %s: %w", name, err)
	}

	tm.mu.Lock()
	tm.seq++
	id := fmt.Sprintf("term-%d", tm.seq)
	tm.sessions[id] = &termSession{id: id, pty: p, cmd: c, cwd: cwd, name: name, cleanup: cleanup}
	tm.mu.Unlock()

	go func() {
		buf := make([]byte, 8192)
		for {
			n, rerr := p.Read(buf)
			if n > 0 {
				if chunk, ok := tm.recordOutput(id, buf[:n]); ok {
					if emit != nil {
						emit("term:data:"+id, chunk)
					}
				}
			}
			if rerr != nil {
				break
			}
		}
		_ = c.Wait()
		if emit != nil {
			emit("term:exit:"+id, nil)
		}
		tm.close(id)
	}()

	return id, nil
}

func opencodeLogDir(goos, home, xdgDataHome, localAppData string) string {
	base := xdgDataHome
	if goos == "windows" {
		base = localAppData
	} else if strings.TrimSpace(base) == "" {
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "opencode", "log")
}

func ensureOpenCodeLogDir() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	localAppData := ""
	if runtime.GOOS == "windows" {
		localAppData, err = os.UserCacheDir()
		if err != nil {
			return err
		}
	}
	dir := opencodeLogDir(runtime.GOOS, home, os.Getenv("XDG_DATA_HOME"), localAppData)
	return os.MkdirAll(dir, 0o700)
}

// terminalEnvironment merges a small launch overlay into the host process
// environment. Overrides win without duplicating keys. Interactive terminal
// programs need a UTF-8 locale even when PrAImate was launched from a desktop
// entry with a sparse or legacy C locale.
func terminalEnvironment(base, overrides []string) []string {
	out := make([]string, 0, len(base)+len(overrides)+3)
	index := make(map[string]int, len(base)+len(overrides))
	put := func(kv string) {
		key, _, ok := strings.Cut(kv, "=")
		if !ok || key == "" {
			return
		}
		if i, exists := index[key]; exists {
			out[i] = kv
			return
		}
		index[key] = len(out)
		out = append(out, kv)
	}
	for _, kv := range base {
		put(kv)
	}
	for _, kv := range overrides {
		put(kv)
	}
	value := func(key string) string {
		i, ok := index[key]
		if !ok {
			return ""
		}
		_, v, _ := strings.Cut(out[i], "=")
		return v
	}
	if value("TERM") == "" {
		put("TERM=xterm-256color")
	}
	if value("COLORTERM") == "" {
		put("COLORTERM=truecolor")
	}

	locale := value("LC_ALL")
	if locale == "" {
		locale = value("LC_CTYPE")
	}
	if locale == "" {
		locale = value("LANG")
	}
	normalized := strings.ReplaceAll(strings.ToUpper(locale), "-", "")
	if !strings.Contains(normalized, "UTF8") {
		if value("LC_ALL") != "" {
			put("LC_ALL=C.UTF-8")
		} else {
			put("LC_CTYPE=C.UTF-8")
		}
		if value("LANG") == "" {
			put("LANG=C.UTF-8")
		}
	}
	return out
}

func (tm *termManager) write(id string, data []byte) error {
	tm.mu.Lock()
	s := tm.sessions[id]
	tm.mu.Unlock()
	if s == nil {
		return fmt.Errorf("no terminal %q", id)
	}
	_, err := s.pty.Write(data)
	return err
}

func (tm *termManager) resize(id string, cols, rows int) error {
	tm.mu.Lock()
	s := tm.sessions[id]
	tm.mu.Unlock()
	if s == nil {
		return fmt.Errorf("no terminal %q", id)
	}
	return s.pty.Resize(cols, rows)
}

func (tm *termManager) close(id string) {
	tm.mu.Lock()
	s := tm.sessions[id]
	delete(tm.sessions, id)
	tm.mu.Unlock()
	if s == nil {
		return
	}
	s.once.Do(func() {
		if s.cmd != nil && s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
		if s.pty != nil {
			_ = s.pty.Close()
		}
		if s.cleanup != nil {
			s.cleanup()
		}
	})
}

func (tm *termManager) closeAll() {
	tm.mu.Lock()
	ids := make([]string, 0, len(tm.sessions))
	for id := range tm.sessions {
		ids = append(ids, id)
	}
	tm.mu.Unlock()
	for _, id := range ids {
		tm.close(id)
	}
}
