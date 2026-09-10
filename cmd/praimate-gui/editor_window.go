package main

// Document studio (plan §14-P1) — the live markdown co-editing window.
//
// Window strategy: Wails v2 can't open a second window, so the studio
// is a SECOND PROCESS of this same binary (`praimate-gui -editor
// <folder> -editor-chat <id>`), the same hidden-mode pattern as the
// approval shim. The editor process binds the full App (same DB, same
// adapters, same chat bindings), and the frontend renders the Editor
// shell instead of the main app when EditorMode reports active.
//
// Real-time model: DISK IS THE SHARED DOCUMENT. The chat's CLI agent
// edits files with its own tools (Tools level edits/full or Ask
// approvals); an fsnotify watcher turns each external write into a
// "praimate:editor-fs" event, and the frontend merges the new content
// into the open tab with a cursor-preserving minimal diff. User
// keystrokes flush back to disk debounced; our own writes are
// remembered briefly so the watcher doesn't echo them back.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"git.jtsec.local/lab/PrAImate/internal/core"
)

// editorFolder / editorChatID are set by main() when the process was
// spawned in editor mode. Empty = normal main window.
var (
	editorFolder string
	editorChatID string
)

// EditorModeInfo tells the frontend which shell to render.
type EditorModeInfo struct {
	Active bool   `json:"active"`
	Folder string `json:"folder"`
	ChatID string `json:"chatId"`
}

func (a *App) EditorMode() EditorModeInfo {
	if a.detachedClient != nil && a.detachedClient.mode.kind == "studio" {
		return EditorModeInfo{Active: true, Folder: a.detachedClient.mode.folder, ChatID: a.detachedClient.mode.sessionID}
	}
	return EditorModeInfo{Active: editorFolder != "", Folder: editorFolder, ChatID: editorChatID}
}

// editableExts caps the studio to text formats; binaries and megafiles
// stay out of the tree.
var editableExts = map[string]bool{
	".md": true, ".markdown": true, ".txt": true, ".rst": true, ".adoc": true,
	".json": true, ".yaml": true, ".yml": true, ".toml": true, ".csv": true,
	".html": true, ".css": true, ".js": true, ".ts": true, ".svelte": true,
	".go": true, ".py": true, ".rs": true, ".sh": true, ".sql": true, ".xml": true,
}

const editorMaxFileSize = 2 << 20 // 2MB

func editorSkipDir(name string) bool {
	return strings.HasPrefix(name, ".") || name == "node_modules" || name == "dist" || name == "vendor"
}

// editorPath resolves rel inside the studio folder, refusing escapes —
// these bindings are callable from JS and must not become a filesystem
// primitive outside the scoped folder.
func editorPath(rel string) (string, error) {
	if editorFolder == "" {
		return "", errors.New("not an editor window")
	}
	abs := filepath.Join(editorFolder, filepath.FromSlash(rel))
	r, err := filepath.Rel(editorFolder, abs)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the studio folder", rel)
	}
	return abs, nil
}

// EditorListFiles walks the studio folder and returns the editable
// files as sorted slash-relative paths (the frontend builds the tree).
func (a *App) EditorListFiles() ([]string, error) {
	if a.detachedClient != nil {
		return a.detachedClient.editorListFiles()
	}
	if editorFolder == "" {
		return nil, errors.New("not an editor window")
	}
	var out []string
	err := filepath.WalkDir(editorFolder, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable subtree — skip, don't fail the tree
		}
		if d.IsDir() {
			if path != editorFolder && editorSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !editableExts[strings.ToLower(filepath.Ext(d.Name()))] {
			return nil
		}
		if info, err := d.Info(); err != nil || info.Size() > editorMaxFileSize {
			return nil
		}
		rel, err := filepath.Rel(editorFolder, path)
		if err == nil {
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

func (a *App) EditorReadFile(rel string) (string, error) {
	if a.detachedClient != nil {
		return a.detachedClient.editorReadFile(rel)
	}
	abs, err := editorPath(rel)
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// EditorWriteFile flushes editor content to disk. The write is recorded
// so the fsnotify watcher suppresses the echo event.
func (a *App) EditorWriteFile(rel, content string) error {
	if a.detachedClient != nil {
		return a.detachedClient.editorWriteFile(rel, content)
	}
	abs, err := editorPath(rel)
	if err != nil {
		return err
	}
	a.editorMarkOwnWrite(abs)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	tmp := abs + ".praimate-tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, abs)
}

// EditorCreateFile creates an empty file (errors when it exists) and
// returns the normalized rel path.
func (a *App) EditorCreateFile(rel string) (string, error) {
	if a.detachedClient != nil {
		return a.detachedClient.editorCreateFile(rel)
	}
	abs, err := editorPath(rel)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(abs); err == nil {
		return "", fmt.Errorf("%s already exists", rel)
	}
	a.editorMarkOwnWrite(abs)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(abs, nil, 0o644); err != nil {
		return "", err
	}
	r, _ := filepath.Rel(editorFolder, abs)
	return filepath.ToSlash(r), nil
}

// EditorDeleteFile deletes a file inside the studio folder. The path
// is safety-checked; refusing to descend through a symlink avoids
// turning these bindings into a generic filesystem primitive.
func (a *App) EditorDeleteFile(rel string) error {
	if a.detachedClient != nil {
		return a.detachedClient.editorDeleteFile(rel)
	}
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return errors.New("path required")
	}
	abs, err := editorPath(rel)
	if err != nil {
		return err
	}
	fi, err := os.Lstat(abs)
	if err != nil {
		return fmt.Errorf("%s: %w", rel, err)
	}
	if fi.IsDir() {
		return fmt.Errorf("%s is a directory — refusing to recursively delete from here", rel)
	}
	a.editorMarkOwnWrite(abs)
	return os.Remove(abs)
}

// EditorRenameFile renames/moves a file inside the studio folder.
// src and dst are slash-relative paths; both are path-safety checked.
// Returns the normalized destination rel.
func (a *App) EditorRenameFile(src, dst string) (string, error) {
	if a.detachedClient != nil {
		return a.detachedClient.editorRenameFile(src, dst)
	}
	src = strings.TrimSpace(src)
	dst = strings.TrimSpace(dst)
	if src == "" || dst == "" {
		return "", errors.New("source and destination names are required")
	}
	srcAbs, err := editorPath(src)
	if err != nil {
		return "", err
	}
	dstAbs, err := editorPath(dst)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(srcAbs); err != nil {
		return "", fmt.Errorf("%s: %w", src, err)
	}
	if _, err := os.Stat(dstAbs); err == nil {
		return "", fmt.Errorf("%s already exists", dst)
	}
	a.editorMarkOwnWrite(srcAbs)
	a.editorMarkOwnWrite(dstAbs)
	if err := os.MkdirAll(filepath.Dir(dstAbs), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(srcAbs, dstAbs); err != nil {
		return "", err
	}
	r, _ := filepath.Rel(editorFolder, dstAbs)
	return filepath.ToSlash(r), nil
}

// --- own-write echo suppression ---------------------------------------------

func (a *App) editorMarkOwnWrite(abs string) {
	a.editorMu.Lock()
	if a.editorOwnWrites == nil {
		a.editorOwnWrites = map[string]time.Time{}
	}
	a.editorOwnWrites[abs] = time.Now()
	a.editorMu.Unlock()
}

func (a *App) editorIsOwnWrite(abs string) bool {
	a.editorMu.Lock()
	defer a.editorMu.Unlock()
	t, ok := a.editorOwnWrites[abs]
	if ok && time.Since(t) < 2*time.Second {
		return true
	}
	delete(a.editorOwnWrites, abs)
	return false
}

// --- fsnotify watcher --------------------------------------------------------

// startEditorWatcher watches the studio folder tree and emits
// "praimate:editor-fs" {path, op} for editable-file changes that we
// didn't write ourselves. New subdirectories are added to the watch as
// they appear (fsnotify is not recursive).
func (a *App) startEditorWatcher() {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return
	}
	addTree := func(root string) {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return nil
			}
			if path != root && editorSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			_ = w.Add(path)
			return nil
		})
	}
	addTree(editorFolder)
	go func() {
		defer w.Close()
		for {
			select {
			case <-a.ctx.Done():
				return
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				if ev.Op&fsnotify.Create != 0 {
					if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
						if !editorSkipDir(filepath.Base(ev.Name)) {
							addTree(ev.Name)
						}
						continue
					}
				}
				if !editableExts[strings.ToLower(filepath.Ext(ev.Name))] {
					continue
				}
				if a.editorIsOwnWrite(ev.Name) {
					continue
				}
				rel, err := filepath.Rel(editorFolder, ev.Name)
				if err != nil {
					continue
				}
				wruntime.EventsEmit(a.ctx, "praimate:editor-fs", map[string]string{
					"path": filepath.ToSlash(rel),
					"op":   ev.Op.String(),
				})
			case _, ok := <-w.Errors:
				if !ok {
					return
				}
			}
		}
	}()
}

// --- launching the studio from the main window -------------------------------

// PrepareStudioChat creates the backing chat and freezes an optional explicit
// skill selection before any detached Studio process can send a turn.
func (a *App) PrepareStudioChat(folder, agentID, cli, model, localEndpoint, localModel, choices string) (string, error) {
	c, err := a.requireCore()
	if err != nil {
		return "", err
	}
	if folder == "" {
		return "", errors.New("a folder is required")
	}
	var chat *core.Chat
	if agentID != "" {
		agent, getErr := c.GetAgent(a.ctx, agentID)
		if getErr != nil {
			return "", getErr
		}
		if !agent.AllowsSurface("editor") {
			return "", fmt.Errorf("agent %q is not allowed on the editor surface", agent.Name)
		}
		chat, err = c.StartInteractiveChat(a.ctx, agentID, cli, folder)
	} else {
		if cli == "" {
			cli = "claude"
		}
		startModel := model
		if localEndpoint != "" {
			startModel = ""
		}
		chat, err = c.StartCleanChat(a.ctx, cli, startModel, folder)
	}
	if err != nil {
		return "", err
	}
	fail := func(cause error) (string, error) {
		_ = c.DeleteChat(a.ctx, chat.ID)
		return "", cause
	}
	if choices != "" {
		if _, err := a.SaveChatSkillChoicesV2(chat.ID, choices); err != nil {
			return fail(err)
		}
	}
	if err := c.UpdateChatSettings(a.ctx, chat.ID, func(s *core.ChatSettings) {
		s.Tools = "edits"
		s.Surface = "studio"
		if localEndpoint != "" {
			s.Local = &core.ChatLocalEndpoint{Endpoint: localEndpoint, Model: localModel}
		} else if model != "" {
			s.Model = model
		}
	}); err != nil {
		return fail(err)
	}
	return chat.ID, nil
}

// OpenEditorWindow creates (or reuses) a chat scoped to folder and
// spawns the studio window as a second process of this binary. model,
// when non-empty, pins the chat's model. Returns the chat id backing
// the studio's chat pane.
func (a *App) OpenEditorWindow(folder, agentID, cli, model, chatID, localEndpoint, _ string, localModel string) (string, error) {
	c, err := a.requireCore()
	if err != nil {
		return "", err
	}
	if folder == "" {
		return "", errors.New("a folder is required")
	}
	if chatID == "" {
		if agentID != "" {
			agent, err := c.GetAgent(a.ctx, agentID)
			if err != nil {
				return "", err
			}
			if !agent.AllowsSurface("editor") {
				return "", fmt.Errorf("agent %q is not allowed on the editor surface", agent.Name)
			}
			chat, err := c.StartInteractiveChat(a.ctx, agentID, cli, folder)
			if err != nil {
				return "", err
			}
			chatID = chat.ID
		} else {
			if cli == "" {
				cli = "claude"
			}
			startModel := model
			if localEndpoint != "" {
				startModel = "" // the local route carries the model
			}
			chat, err := c.StartCleanChat(a.ctx, cli, startModel, folder)
			if err != nil {
				return "", err
			}
			chatID = chat.ID
		}
		// Documents-first defaults: tag the chat as a studio session and
		// let the agent edit files without a per-edit prompt; the user can
		// lower it from the chat header. A local endpoint, when given,
		// routes the studio chat through the same per-CLI machinery as a
		// regular chat (every CLI supported).
		_ = c.UpdateChatSettings(a.ctx, chatID, func(s *core.ChatSettings) {
			s.Tools = "edits"
			s.Surface = "studio"
			if localEndpoint != "" {
				s.Local = &core.ChatLocalEndpoint{Endpoint: localEndpoint, Model: localModel}
			} else if model != "" {
				s.Model = model
			}
		})
	}
	if a.detached == nil {
		return "", errors.New("window coordinator is unavailable")
	}
	if err := a.detached.openWithFolder("studio", chatID, "Studio — "+filepath.Base(folder), folder); err != nil {
		return "", fmt.Errorf("open studio window: %w", err)
	}
	return chatID, nil
}
