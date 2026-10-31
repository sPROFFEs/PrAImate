package launcher

// Per-chat slice snapshot of an agent's native session store.
//
// CaptureTranscript already copies one canonical JSONL into the
// chat's <ts>-<agent>/transcript.jsonl for search + summary. That's
// useful for cross-agent context injection but loses the agent-
// internal structure (claude's per-file format, opencode's
// info+messages split, codex's per-rollout files). MirrorOutSlice
// snapshots the FULL slice — every file belonging to this chat's
// sandbox — into <ts>-<agent>/native/ preserving the layout the
// agent's resume code expects.
//
// On a fresh launch of the same chat the native files are still in
// the agent's own home dir; the snapshot doesn't matter. But for:
//
//   1. Cross-machine portability (copy the chat dir elsewhere).
//   2. Cross-machine sync (Syncthing / git / cloud).
//   3. SIGKILL recovery when Step 3 mirror-in is enabled.
//   4. Backup / disaster recovery.
//
// …the slice is what makes the chat dir actually self-contained.
//
// MirrorInSlice is the reverse: copy the chat's native/ back into the
// agent's home dir. Used by Step 3's opt-in mirror flow and by the
// no-native-sessions branch in resume.go (cross-machine case).

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// MirrorResult describes what MirrorOutSlice / MirrorInSlice did. All
// fields are best-effort and may be empty.
type MirrorResult struct {
	// Files copied — used by callers for diagnostics + tests. Each
	// entry is the destination path (absolute).
	Files []string

	// Bytes copied (sum across all Files). Reported in the launching
	// screen's mirror diagnostics line.
	Bytes int64

	// Note is one of:
	//   ""                 — happy path, see Files for what landed
	//   "no native store"  — the agent's home dir has nothing matching this chat
	//   "skipped: <agent>" — agent isn't wired for slicing
	//   "error: <msg>"     — partial copy; Files holds what made it
	Note string
}

// sliceSubdir is the per-agent subdir under <ts>-<agent>/native/ where
// we put the slice. Keeping per-agent subdirs means a future
// composite-agent feature (e.g. a chat that ran with claude AND codex
// in different sessions) wouldn't have its slices stomp on each other.
func sliceSubdir(agent AgentID) string {
	switch agent {
	case AgentClaude:
		return "claude-projects"
	case AgentOpenClaude:
		return "openclaude-projects"
	case AgentCodex:
		return "codex-rollouts"
	case AgentOpenCode:
		return "opencode-session"
	case AgentGemini:
		return "gemini-tmp"
	case AgentDeepSeek:
		return "deepseek"
	}
	return string(agent)
}

// MirrorOutSlice copies the agent's per-chat native-store slice into
// <sessionDir>/native/<agent-subdir>/. Best-effort: failures inside
// individual file copies are skipped and noted, never propagated as a
// hard error to the launch flow.
//
// sandboxDir is the chat's sandbox path (the agent's cwd). sessionDir
// is the chat's per-launch <ts>-<agent>/ directory. The agent argument
// is the launched agent.
func MirrorOutSlice(agent Agent, sandboxDir, sessionDir string) MirrorResult {
	return MirrorOutSliceWithHome(agent, sandboxDir, sessionDir, "")
}

func MirrorOutSliceWithHome(agent Agent, sandboxDir, sessionDir, homeOverride string) MirrorResult {
	paths := agentHome(agent.ID, sandboxDir, homeOverride)
	if paths.SessionDir == "" {
		return MirrorResult{Note: "skipped: " + string(agent.ID) + " has no native store wired"}
	}
	dstRoot := filepath.Join(sessionDir, "native", sliceSubdir(agent.ID))
	if err := os.MkdirAll(dstRoot, 0o755); err != nil {
		return MirrorResult{Note: "error: mkdir " + dstRoot + ": " + err.Error()}
	}

	switch agent.ID {
	case AgentClaude:
		return mirrorOutClaude(paths.SessionDir, dstRoot)
	case AgentOpenClaude:
		// Same file layout as claude (UUID-named .jsonl files in a
		// slug dir) — mirrorOutClaude works verbatim.
		return mirrorOutClaude(paths.SessionDir, dstRoot)
	case AgentCodex:
		return mirrorOutCodex(paths.SessionDir, sandboxDir, dstRoot)
	case AgentOpenCode:
		return mirrorOutOpenCode(paths.SessionDir, paths.MessagesDir, sandboxDir, dstRoot)
	case AgentGemini:
		return mirrorOutGemini(paths.SessionDir, dstRoot)
	case AgentDeepSeek:
		return MirrorResult{Note: "skipped: deepseek has no documented session store"}
	}
	return MirrorResult{Note: "skipped: " + string(agent.ID) + " not wired"}
}

// MirrorInSlice copies the chat's previously-snapshotted slice back
// into the agent's home dir. Used by:
//
//   - The no-native-sessions branch in resume.go (cross-machine restore).
//   - Step 3's opt-in mirror-in (when chat.Settings.MirrorAgentState).
//
// IsNewerHome, when true, signals that we should compare mtimes and
// preserve the home-dir version if it's newer than the slice (SIGKILL
// recovery: agent ran, wrote new turns, praimate got killed before mirror-
// out completed → next launch should NOT clobber those turns).
func MirrorInSlice(agent Agent, sandboxDir, sliceRoot string, preserveNewerHome bool) MirrorResult {
	return MirrorInSliceWithHome(agent, sandboxDir, sliceRoot, preserveNewerHome, "")
}

func MirrorInSliceWithHome(agent Agent, sandboxDir, sliceRoot string, preserveNewerHome bool, homeOverride string) MirrorResult {
	paths := agentHome(agent.ID, sandboxDir, homeOverride)
	if paths.SessionDir == "" {
		return MirrorResult{Note: "skipped: " + string(agent.ID) + " has no native store wired"}
	}
	switch agent.ID {
	case AgentClaude:
		return mirrorInClaude(paths.SessionDir, filepath.Join(sliceRoot, sliceSubdir(agent.ID)), preserveNewerHome)
	case AgentOpenClaude:
		// Same layout as claude — reuse the helper.
		return mirrorInClaude(paths.SessionDir, filepath.Join(sliceRoot, sliceSubdir(agent.ID)), preserveNewerHome)
	case AgentCodex:
		return mirrorInCodex(paths.SessionDir, filepath.Join(sliceRoot, sliceSubdir(agent.ID)), preserveNewerHome)
	case AgentOpenCode:
		return mirrorInOpenCode(paths.SessionDir, paths.MessagesDir, filepath.Join(sliceRoot, sliceSubdir(agent.ID)), preserveNewerHome)
	case AgentGemini:
		return mirrorInGemini(paths.SessionDir, filepath.Join(sliceRoot, sliceSubdir(agent.ID)), preserveNewerHome)
	}
	return MirrorResult{Note: "skipped: " + string(agent.ID) + " not wired"}
}

// --- Claude ----------------------------------------------------------------

func mirrorOutClaude(srcDir, dstDir string) MirrorResult {
	// claude's project dir is already cwd-scoped (slug). Copy every
	// .jsonl. Skip the broken legacy files my earlier prototype wrote
	// (non-UUID stems) — they're not real claude sessions.
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return MirrorResult{Note: "no native store"}
		}
		return MirrorResult{Note: "error: read " + srcDir + ": " + err.Error()}
	}
	var out MirrorResult
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		stem := strings.TrimSuffix(e.Name(), ".jsonl")
		if !looksLikeUUID(stem) {
			continue
		}
		src := filepath.Join(srcDir, e.Name())
		dst := filepath.Join(dstDir, e.Name())
		n, err := copyFileWithMtime(src, dst)
		if err != nil {
			out.Note = "error: copy " + src + ": " + err.Error()
			continue
		}
		out.Files = append(out.Files, dst)
		out.Bytes += n
	}
	return out
}

func mirrorInClaude(srcDir, sliceDir string, preserveNewerHome bool) MirrorResult {
	entries, err := os.ReadDir(sliceDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return MirrorResult{Note: "no slice on disk"}
		}
		return MirrorResult{Note: "error: read " + sliceDir + ": " + err.Error()}
	}
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		return MirrorResult{Note: "error: mkdir " + srcDir + ": " + err.Error()}
	}
	var out MirrorResult
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		src := filepath.Join(sliceDir, e.Name())
		dst := filepath.Join(srcDir, e.Name())
		if preserveNewerHome {
			if homeIsNewer(dst, src) {
				continue
			}
		}
		n, err := copyFileWithMtime(src, dst)
		if err != nil {
			out.Note = "error: copy " + src + ": " + err.Error()
			continue
		}
		out.Files = append(out.Files, dst)
		out.Bytes += n
	}
	return out
}

// --- Codex -----------------------------------------------------------------

func mirrorOutCodex(srcDir, sandboxDir, dstDir string) MirrorResult {
	wantCwd := normaliseCwd(sandboxDir)
	cutoff := time.Now().Add(-90 * 24 * time.Hour) // ignore archaic rollouts
	var out MirrorResult
	_ = filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil || info.ModTime().Before(cutoff) {
			return nil
		}
		if !codexFileMatchesCwd(path, wantCwd) {
			return nil
		}
		dst := filepath.Join(dstDir, name)
		n, err := copyFileWithMtime(path, dst)
		if err != nil {
			out.Note = "error: copy " + path + ": " + err.Error()
			return nil
		}
		out.Files = append(out.Files, dst)
		out.Bytes += n
		return nil
	})
	if out.Note == "" && len(out.Files) == 0 {
		out.Note = "no native store"
	}
	return out
}

func mirrorInCodex(homeSessionsDir, sliceDir string, preserveNewerHome bool) MirrorResult {
	entries, err := os.ReadDir(sliceDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return MirrorResult{Note: "no slice on disk"}
		}
		return MirrorResult{Note: "error: read " + sliceDir + ": " + err.Error()}
	}
	// Codex needs each rollout under its date-stamped dir. Re-derive
	// the date from the rollout's mtime when we put it back so the
	// agent's `resume` walker finds it where it expects.
	var out MirrorResult
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		src := filepath.Join(sliceDir, e.Name())
		info, err := os.Stat(src)
		if err != nil {
			continue
		}
		mt := info.ModTime()
		dstDir := filepath.Join(homeSessionsDir,
			mt.Format("2006"), mt.Format("01"), mt.Format("02"))
		if err := os.MkdirAll(dstDir, 0o755); err != nil {
			out.Note = "error: mkdir " + dstDir + ": " + err.Error()
			continue
		}
		dst := filepath.Join(dstDir, e.Name())
		if preserveNewerHome && homeIsNewer(dst, src) {
			continue
		}
		n, err := copyFileWithMtime(src, dst)
		if err != nil {
			out.Note = "error: copy " + src + ": " + err.Error()
			continue
		}
		out.Files = append(out.Files, dst)
		out.Bytes += n
	}
	return out
}

// --- OpenCode --------------------------------------------------------------

// opencodeSliceLayout: under sliceSubdir we keep:
//
//	slice/opencode-session/info/<id>.json
//	slice/opencode-session/message/<id>/*.json
//
// matching the source layout 1:1 so MirrorIn can blat it back.
func mirrorOutOpenCode(infoDir, msgDir, sandboxDir, dstRoot string) MirrorResult {
	wantCwd := normaliseCwd(sandboxDir)
	if _, err := os.Stat(infoDir); err != nil {
		return MirrorResult{Note: "no native store"}
	}
	entries, err := os.ReadDir(infoDir)
	if err != nil {
		return MirrorResult{Note: "error: read " + infoDir + ": " + err.Error()}
	}
	var out MirrorResult
	dstInfo := filepath.Join(dstRoot, "info")
	dstMsg := filepath.Join(dstRoot, "message")
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(infoDir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var meta struct {
			ID        string `json:"id"`
			Directory string `json:"directory"`
		}
		if json.Unmarshal(raw, &meta) != nil {
			continue
		}
		if meta.ID == "" || normaliseCwd(meta.Directory) != wantCwd {
			continue
		}
		// Copy the info file itself.
		if err := os.MkdirAll(dstInfo, 0o755); err != nil {
			out.Note = "error: mkdir " + dstInfo + ": " + err.Error()
			continue
		}
		dst := filepath.Join(dstInfo, e.Name())
		n, err := copyFileWithMtime(path, dst)
		if err != nil {
			out.Note = "error: copy " + path + ": " + err.Error()
			continue
		}
		out.Files = append(out.Files, dst)
		out.Bytes += n
		// Copy the message dir verbatim if present.
		srcMsgDir := filepath.Join(msgDir, meta.ID)
		if files, err := os.ReadDir(srcMsgDir); err == nil {
			dstMsgID := filepath.Join(dstMsg, meta.ID)
			if mkErr := os.MkdirAll(dstMsgID, 0o755); mkErr != nil {
				out.Note = "error: mkdir " + dstMsgID + ": " + mkErr.Error()
				continue
			}
			for _, f := range files {
				if f.IsDir() {
					continue
				}
				s := filepath.Join(srcMsgDir, f.Name())
				d := filepath.Join(dstMsgID, f.Name())
				nn, err := copyFileWithMtime(s, d)
				if err != nil {
					out.Note = "error: copy " + s + ": " + err.Error()
					continue
				}
				out.Files = append(out.Files, d)
				out.Bytes += nn
			}
		}
	}
	if out.Note == "" && len(out.Files) == 0 {
		out.Note = "no native store"
	}
	return out
}

func mirrorInOpenCode(infoDir, msgDir, sliceDir string, preserveNewerHome bool) MirrorResult {
	sliceInfo := filepath.Join(sliceDir, "info")
	sliceMsg := filepath.Join(sliceDir, "message")
	entries, err := os.ReadDir(sliceInfo)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return MirrorResult{Note: "no slice on disk"}
		}
		return MirrorResult{Note: "error: read " + sliceInfo + ": " + err.Error()}
	}
	if err := os.MkdirAll(infoDir, 0o755); err != nil {
		return MirrorResult{Note: "error: mkdir " + infoDir + ": " + err.Error()}
	}
	var out MirrorResult
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		src := filepath.Join(sliceInfo, e.Name())
		dst := filepath.Join(infoDir, e.Name())
		if !preserveNewerHome || !homeIsNewer(dst, src) {
			n, err := copyFileWithMtime(src, dst)
			if err != nil {
				out.Note = "error: copy " + src + ": " + err.Error()
				continue
			}
			out.Files = append(out.Files, dst)
			out.Bytes += n
		}
		// Mirror in the matching message dir verbatim.
		id := strings.TrimSuffix(e.Name(), ".json")
		srcMsgID := filepath.Join(sliceMsg, id)
		if files, err := os.ReadDir(srcMsgID); err == nil {
			dstMsgID := filepath.Join(msgDir, id)
			_ = os.MkdirAll(dstMsgID, 0o755)
			for _, f := range files {
				if f.IsDir() {
					continue
				}
				s := filepath.Join(srcMsgID, f.Name())
				d := filepath.Join(dstMsgID, f.Name())
				if preserveNewerHome && homeIsNewer(d, s) {
					continue
				}
				nn, err := copyFileWithMtime(s, d)
				if err != nil {
					out.Note = "error: copy " + s + ": " + err.Error()
					continue
				}
				out.Files = append(out.Files, d)
				out.Bytes += nn
			}
		}
	}
	return out
}

// --- Gemini ----------------------------------------------------------------

func mirrorOutGemini(srcDir, dstDir string) MirrorResult {
	if _, err := os.Stat(srcDir); err != nil {
		return MirrorResult{Note: "no native store"}
	}
	var out MirrorResult
	_ = filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return nil
		}
		dst := filepath.Join(dstDir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			out.Note = "error: mkdir " + filepath.Dir(dst) + ": " + err.Error()
			return nil
		}
		n, err := copyFileWithMtime(path, dst)
		if err != nil {
			out.Note = "error: copy " + path + ": " + err.Error()
			return nil
		}
		out.Files = append(out.Files, dst)
		out.Bytes += n
		return nil
	})
	if out.Note == "" && len(out.Files) == 0 {
		out.Note = "no native store"
	}
	return out
}

func mirrorInGemini(homeDir, sliceDir string, preserveNewerHome bool) MirrorResult {
	if _, err := os.Stat(sliceDir); err != nil {
		return MirrorResult{Note: "no slice on disk"}
	}
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		return MirrorResult{Note: "error: mkdir " + homeDir + ": " + err.Error()}
	}
	var out MirrorResult
	_ = filepath.WalkDir(sliceDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(sliceDir, path)
		if err != nil {
			return nil
		}
		dst := filepath.Join(homeDir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			out.Note = "error: mkdir " + filepath.Dir(dst) + ": " + err.Error()
			return nil
		}
		if preserveNewerHome && homeIsNewer(dst, path) {
			return nil
		}
		n, err := copyFileWithMtime(path, dst)
		if err != nil {
			out.Note = "error: copy " + path + ": " + err.Error()
			return nil
		}
		out.Files = append(out.Files, dst)
		out.Bytes += n
		return nil
	})
	return out
}

// --- shared helpers --------------------------------------------------------

// copyFileWithMtime copies src to dst byte-for-byte and propagates the
// source mtime onto the destination. The mtime matters for resume
// flows where the agent picks the newest file by mtime (claude
// --continue, codex resume --last).
func copyFileWithMtime(src, dst string) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, err
	}
	tmp := dst + ".praimate-tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, err
	}
	n, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return n, copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return n, closeErr
	}
	if err := os.Rename(tmp, dst); err != nil {
		return n, err
	}
	_ = os.Chtimes(dst, st.ModTime(), st.ModTime())
	return n, nil
}

// homeIsNewer reports whether the home-dir file is newer than the
// slice file. Used by Step 3's SIGKILL recovery: if the agent wrote
// new turns after the last mirror-out, we don't want a stale slice
// to clobber them on the next mirror-in.
//
// Returns false when home doesn't exist (nothing to preserve).
// Returns true on tie too, just to be conservative — we'd rather
// preserve home in the ambiguous case than overwrite live turns.
func homeIsNewer(homePath, slicePath string) bool {
	hi, herr := os.Stat(homePath)
	if herr != nil {
		return false
	}
	si, serr := os.Stat(slicePath)
	if serr != nil {
		return true
	}
	return !hi.ModTime().Before(si.ModTime())
}

// --- async wrapper ---------------------------------------------------------

// mirrorInFlight is the package-level guard around the background
// mirror-out goroutine. The TUI reads it via MirrorInProgress() to
// render "mirroring..." while the copy is still running, and uses
// WaitForMirror() if it ever needs to be sure the snapshot completed
// before reading the slice (e.g. before mirror-IN on the next launch
// of the same chat).
var mirrorInFlight struct {
	mu      sync.Mutex
	count   int
	done    chan struct{} // re-created each time count goes 0 → 1+
	current string        // sessionDir of the most-recent in-flight mirror, for diagnostics
}

// StartMirrorOutAsync fires MirrorOutSlice on a background goroutine
// and updates LastMirrorResult when it completes. Safe to call from
// the tea.ExecProcess callback — returns immediately so the Bubbletea
// program redraws without waiting on disk I/O.
//
// Multiple calls in flight are tolerated (e.g. user closes one chat,
// opens another immediately) — the in-flight count tracks all of them
// and the WaitForMirror() helper can drain them all before a sensitive
// operation like mirror-in.
func StartMirrorOutAsync(agent Agent, sandboxDir, sessionDir string) {
	StartMirrorOutAsyncWithHome(agent, sandboxDir, sessionDir, "")
}

func StartMirrorOutAsyncWithHome(agent Agent, sandboxDir, sessionDir, homeOverride string) {
	mirrorInFlight.mu.Lock()
	if mirrorInFlight.count == 0 {
		mirrorInFlight.done = make(chan struct{})
	}
	mirrorInFlight.count++
	mirrorInFlight.current = sessionDir
	mirrorInFlight.mu.Unlock()

	go func() {
		res := MirrorOutSliceWithHome(agent, sandboxDir, sessionDir, homeOverride)
		mirrorInFlight.mu.Lock()
		launcherMu.Lock()
		LastMirrorResult = res
		launcherMu.Unlock()
		mirrorInFlight.count--
		if mirrorInFlight.count == 0 {
			// Wake any WaitForMirror callers and reset the channel
			// so the NEXT in-flight cycle gets a fresh signal.
			close(mirrorInFlight.done)
			mirrorInFlight.done = nil
			mirrorInFlight.current = ""
		}
		mirrorInFlight.mu.Unlock()
	}()
}

// MirrorInProgress reports whether at least one MirrorOutSlice
// goroutine is still running. The TUI uses this to render a
// "mirroring..." note next to the chat-list session diagnostics
// without blocking redraws.
func MirrorInProgress() (active bool, sessionDir string) {
	mirrorInFlight.mu.Lock()
	defer mirrorInFlight.mu.Unlock()
	return mirrorInFlight.count > 0, mirrorInFlight.current
}

// WaitForMirror blocks until every in-flight mirror has completed.
// Called from sensitive operations that need a consistent on-disk
// slice — e.g. OpenChat's mirror-IN must not race a mirror-OUT from
// the just-exited session of the SAME chat. Returns immediately when
// nothing's in flight. Honors the context for timeout.
func WaitForMirror(ctx context.Context) error {
	mirrorInFlight.mu.Lock()
	if mirrorInFlight.count == 0 {
		mirrorInFlight.mu.Unlock()
		return nil
	}
	done := mirrorInFlight.done
	mirrorInFlight.mu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// LatestSliceDir returns the chat's most-recent <ts>-<agent>/native/
// dir, or "" when none exists. Used by Step 3's mirror-in launch path
// and by resume.go's no-native-sessions branch.
func LatestSliceDir(sessionsDir string, agent AgentID) string {
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return ""
	}
	type cand struct {
		path string
		mt   time.Time
	}
	var cands []cand
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Match <ts>-<agent> names — agent suffix tells us this was
		// captured by that agent.
		if !strings.HasSuffix(e.Name(), "-"+string(agent)) {
			continue
		}
		full := filepath.Join(sessionsDir, e.Name(), "native")
		st, err := os.Stat(full)
		if err != nil || !st.IsDir() {
			continue
		}
		cands = append(cands, cand{full, st.ModTime()})
	}
	if len(cands) == 0 {
		return ""
	}
	sort.SliceStable(cands, func(i, j int) bool {
		return cands[i].mt.After(cands[j].mt)
	})
	return cands[0].path
}
