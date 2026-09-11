package launcher

// Native session resume. The naive idea ("always copy our captured
// JSONL back into the agent's store") turned out to be wrong:
//
//  1. Same-machine re-launch: the agent's native session files are STILL
//     in its store from when it ran — there's nothing to restore. Worse,
//     copying our captured copy back with a launcher-minted filename
//     made claude's --continue see a newer-mtime file that doesn't
//     match its <UUID>.jsonl naming + embedded sessionId convention,
//     so claude tried to load garbage and silently fell back to "no
//     resume." That's the bug the user hit.
//
//  2. Multi-session case: the user wants to PICK which session to
//     resume. Both claude (--resume without UUID) and codex (resume
//     without --last) already open native interactive pickers scoped
//     to the current cwd — way better UX than a PrAImate-side picker.
//
// So the real contract is:
//
//   native = count of sessions in the agent's own store matching this
//            chat's sandbox (slug for claude, cwd field for codex)
//
//   native >= 2 → pass the agent's "open picker" flag, no file ops.
//   native == 1 → pass --continue / --last, no file ops.
//   native == 0 → genuine cross-machine restore: extract the original
//                 session UUID from our captured JSONL, write it back
//                 with the correct name + embedded sessionId, then
//                 --continue / --last.
//
// OpenCode/Gemini/DeepSeek still skip (multi-file stores, no CLI
// resume, etc.) — handled in RestoreNativeSession's switch.

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ResumePlan is what Plan() merges into LaunchPlan.Args when a captured
// session for this chat/agent exists and can be restored.
type ResumePlan struct {
	// Args, if non-empty, are appended (or replaced — see openchat.go)
	// onto plan.Args by the launcher.
	Args []string

	// RestoredTo is the absolute path the rollout was written to, on
	// the genuine cross-machine restore path. Empty when no file write
	// happened (the common "native store already has matching sessions"
	// case, which is most of the time).
	RestoredTo string

	// Note is a one-liner explaining what the launcher decided. Always
	// safe to display next to the launch plan.
	Note string
}

// RestoreNativeSession is the entry point. Best-effort, never returns a
// hard error — failures land in Note and the launcher carries on.
func RestoreNativeSession(agent Agent, c Chat) ResumePlan {
	switch agent.ID {
	case AgentClaude:
		return resumeClaude(c)
	case AgentOpenClaude:
		return resumeOpenClaude(c)
	case AgentCodex:
		return resumeCodex(c)
	case AgentOpenCode:
		return ResumePlan{Note: "native resume not yet supported for opencode (multi-file session store); summary still injected as context"}
	case AgentGemini:
		return ResumePlan{Note: "native resume not yet supported for gemini-cli (no stable CLI resume flag); summary still injected as context"}
	case AgentDeepSeek:
		return ResumePlan{Note: "native resume not yet supported for deepseek-tui; summary still injected as context"}
	}
	return ResumePlan{Note: "native resume not implemented for " + string(agent.ID)}
}

// --- Claude ----------------------------------------------------------------

// resumeClaude decides how to launch claude based on what's already in
// its store for this chat's sandbox slug. See the package doc for the
// decision matrix.
func resumeClaude(c Chat) ResumePlan {
	home := homeDir()
	if home == "" {
		return ResumePlan{Note: "claude resume: no home dir resolved"}
	}
	slug := claudeProjectSlug(c.SandboxDir)
	storeDir := filepath.Join(home, ".claude", "projects", slug)
	native := claudeNativeSessionFiles(storeDir)

	switch {
	case len(native) >= 2:
		// Let claude's own picker handle it — scoped to this slug, so
		// the user sees only sessions for THIS chat. Way better than
		// us inventing one.
		return ResumePlan{
			Args: []string{"--resume"},
			Note: "found " + itoa(len(native)) + " native claude sessions for this chat — opening claude's picker",
		}
	case len(native) == 1:
		return ResumePlan{
			Args: []string{"--continue"},
			Note: "resuming the single native claude session for this chat",
		}
	}

	// No native sessions — check if OpenClaude has sessions for this project,
	// or fall back to restoring from our captured transcript.
	if ocSlug := openclaudeProjectSlug(c.SandboxDir); ocSlug != "" {
		ocStoreDir := filepath.Join(home, ".openclaude", "projects", ocSlug)
		if ocNative := claudeNativeSessionFiles(ocStoreDir); len(ocNative) > 0 {
			_ = os.MkdirAll(storeDir, 0o755)
			for _, src := range ocNative {
				dst := filepath.Join(storeDir, filepath.Base(src))
				if _, err := os.Stat(dst); os.IsNotExist(err) {
					if raw, err := os.ReadFile(src); err == nil {
						_ = writeFileAtomic(dst, raw, 0o644)
					}
				}
			}
			native = claudeNativeSessionFiles(storeDir)
			if len(native) == 1 {
				return ResumePlan{Args: []string{"--continue"}, Note: "resuming session migrated from openclaude"}
			} else if len(native) >= 2 {
				return ResumePlan{Args: []string{"--resume"}, Note: "opening picker with sessions migrated from openclaude"}
			}
		}
	}

	pick, ok := newestCaptureForAgent(c.SessionsDir, AgentClaude)
	if !ok {
		return ResumePlan{Note: "no previous claude session for this chat (no native store, no captured transcript)"}
	}
	src := filepath.Join(pick.dir, "transcript.jsonl")
	raw, err := os.ReadFile(src)
	if err != nil {
		return ResumePlan{Note: "claude resume: couldn't read captured transcript: " + err.Error()}
	}
	uuid := extractClaudeSessionID(raw)
	if uuid == "" {
		return ResumePlan{Note: "claude resume: captured transcript has no sessionId field — can't restore safely; launch will start fresh"}
	}
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		return ResumePlan{Note: "claude resume: mkdir " + storeDir + ": " + err.Error()}
	}
	dst := filepath.Join(storeDir, uuid+".jsonl")
	if err := writeFileAtomic(dst, raw, 0o644); err != nil {
		return ResumePlan{Note: "claude resume: write " + dst + ": " + err.Error()}
	}
	return ResumePlan{
		Args:       []string{"--continue"},
		RestoredTo: dst,
		Note:       "no native claude session on this machine; restored from captured transcript (uuid " + uuid + ")",
	}
}

// claudeNativeSessionFiles returns the absolute paths of every
// well-formed claude session file in storeDir. "Well-formed" = filename
// looks like a UUID and is a regular .jsonl. Skips legacy files we may
// have written previously with non-UUID names (those were the bug —
// claude can't load them, so they shouldn't count as "resumable").
func claudeNativeSessionFiles(storeDir string) []string {
	entries, err := os.ReadDir(storeDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		stem := strings.TrimSuffix(name, ".jsonl")
		if !looksLikeUUID(stem) {
			continue
		}
		out = append(out, filepath.Join(storeDir, name))
	}
	return out
}

// extractClaudeSessionID scans the first ~32KB of a claude rollout
// looking for a "sessionId":"<uuid>" field. Returns "" when missing.
// Claude's first line is typically `{"type":"permission-mode","sessionId":"<uuid>"}`
// but we tolerate any record carrying the field.
func extractClaudeSessionID(raw []byte) string {
	head := raw
	if len(head) > 32*1024 {
		head = head[:32*1024]
	}
	for _, line := range splitLines(head) {
		var rec struct {
			SessionID string `json:"sessionId"`
		}
		if err := json.Unmarshal(line, &rec); err == nil && rec.SessionID != "" {
			return rec.SessionID
		}
	}
	return ""
}

// --- OpenClaude ------------------------------------------------------------

// resumeOpenClaude is the resumeClaude twin for ~/.openclaude/projects/.
// CLI surface (-c / --continue, -r / --resume [UUID]) and on-disk JSONL
// schema (sessionId in the header records) are inherited from claude
// code, so the decision matrix is identical: ≥2 native → open picker,
// ==1 → --continue, 0 → restore from captured transcript if any.
func resumeOpenClaude(c Chat) ResumePlan {
	home := openClaudeHomeForChat(c)
	if home == "" {
		return ResumePlan{Note: "openclaude resume: no home dir resolved"}
	}
	slug := openclaudeProjectSlug(c.SandboxDir)
	storeDir := filepath.Join(home, ".openclaude", "projects", slug)
	native := claudeNativeSessionFiles(storeDir)

	switch {
	case len(native) >= 2:
		return ResumePlan{
			Args: []string{"--resume"},
			Note: "found " + itoa(len(native)) + " native openclaude sessions for this chat — opening openclaude's picker",
		}
	case len(native) == 1:
		return ResumePlan{
			Args: []string{"--continue"},
			Note: "resuming the single native openclaude session for this chat",
		}
	}

	// No native sessions — check if Claude Code has sessions for this project,
	// or fall back to captured transcript.
	if cSlug := claudeProjectSlug(c.SandboxDir); cSlug != "" {
		cStoreDir := filepath.Join(home, ".claude", "projects", cSlug)
		if cNative := claudeNativeSessionFiles(cStoreDir); len(cNative) > 0 {
			_ = os.MkdirAll(storeDir, 0o755)
			for _, src := range cNative {
				dst := filepath.Join(storeDir, filepath.Base(src))
				if _, err := os.Stat(dst); os.IsNotExist(err) {
					if raw, err := os.ReadFile(src); err == nil {
						_ = writeFileAtomic(dst, raw, 0o644)
					}
				}
			}
			native = claudeNativeSessionFiles(storeDir)
			if len(native) == 1 {
				return ResumePlan{Args: []string{"--continue"}, Note: "resuming session migrated from claude code"}
			} else if len(native) >= 2 {
				return ResumePlan{Args: []string{"--resume"}, Note: "opening picker with sessions migrated from claude code"}
			}
		}
	}

	pick, ok := newestCaptureForAgent(c.SessionsDir, AgentOpenClaude)
	if !ok {
		return ResumePlan{Note: "no previous openclaude session for this chat (no native store, no captured transcript)"}
	}
	src := filepath.Join(pick.dir, "transcript.jsonl")
	raw, err := os.ReadFile(src)
	if err != nil {
		return ResumePlan{Note: "openclaude resume: couldn't read captured transcript: " + err.Error()}
	}
	uuid := extractClaudeSessionID(raw)
	if uuid == "" {
		return ResumePlan{Note: "openclaude resume: captured transcript has no sessionId field — can't restore safely; launch will start fresh"}
	}
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		return ResumePlan{Note: "openclaude resume: mkdir " + storeDir + ": " + err.Error()}
	}
	dst := filepath.Join(storeDir, uuid+".jsonl")
	if err := writeFileAtomic(dst, raw, 0o644); err != nil {
		return ResumePlan{Note: "openclaude resume: write " + dst + ": " + err.Error()}
	}
	return ResumePlan{
		Args:       []string{"--continue"},
		RestoredTo: dst,
		Note:       "no native openclaude session on this machine; restored from captured transcript (uuid " + uuid + ")",
	}
}

// --- Codex -----------------------------------------------------------------

// resumeCodex mirrors resumeClaude's decision matrix against codex's
// own session store.
func resumeCodex(c Chat) ResumePlan {
	home := homeDir()
	if home == "" {
		return ResumePlan{Note: "codex resume: no home dir resolved"}
	}
	wantCwd := normaliseCwd(c.SandboxDir)
	native := codexNativeSessionFiles(home, wantCwd)

	switch {
	case len(native) >= 2:
		return ResumePlan{
			Args: []string{"resume"},
			Note: "found " + itoa(len(native)) + " native codex sessions for this chat — opening codex's picker",
		}
	case len(native) == 1:
		// Use explicit UUID when we can parse it — avoids "--last
		// picks a concurrent codex session from another terminal".
		if uuid := codexSessionIDFromFile(native[0]); uuid != "" {
			return ResumePlan{
				Args: []string{"resume", uuid},
				Note: "resuming codex session " + uuid,
			}
		}
		return ResumePlan{
			Args: []string{"resume", "--last"},
			Note: "resuming most-recent codex session (couldn't parse session UUID; if another codex runs concurrently outside Clade it could win the tie)",
		}
	}

	// No native sessions for this cwd — fall back to restoring from
	// our captured transcript.
	pick, ok := newestCaptureForAgent(c.SessionsDir, AgentCodex)
	if !ok {
		return ResumePlan{Note: "no previous codex session for this chat (no native store, no captured transcript)"}
	}
	src := filepath.Join(pick.dir, "transcript.jsonl")
	raw, err := os.ReadFile(src)
	if err != nil {
		return ResumePlan{Note: "codex resume: couldn't read captured transcript: " + err.Error()}
	}
	uuid := extractCodexSessionID(raw)
	now := time.Now()
	dstDir := filepath.Join(home, ".codex", "sessions",
		now.Format("2006"),
		now.Format("01"),
		now.Format("02"))
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return ResumePlan{Note: "codex resume: mkdir " + dstDir + ": " + err.Error()}
	}
	basename := "rollout-" + now.Format("20060102T150405") + "-" + safeStem(uuid, "praimate-"+filepath.Base(c.Root)) + ".jsonl"
	dst := filepath.Join(dstDir, basename)
	if err := writeFileAtomic(dst, raw, 0o644); err != nil {
		return ResumePlan{Note: "codex resume: write " + dst + ": " + err.Error()}
	}
	args := []string{"resume", "--last"}
	note := "no native codex session on this machine; restored from captured transcript (resume --last)"
	if uuid != "" {
		args = []string{"resume", uuid}
		note = "no native codex session on this machine; restored from captured transcript (uuid " + uuid + ")"
	}
	return ResumePlan{
		Args:       args,
		RestoredTo: dst,
		Note:       note,
	}
}

// codexNativeSessionFiles walks ~/.codex/sessions and returns rollouts
// whose first record's cwd field matches wantCwd. Returns absolute
// paths. Pre-filters by mtime within the last 30 days to keep the walk
// bounded on long-running installs.
func codexNativeSessionFiles(home, wantCwd string) []string {
	base := filepath.Join(home, ".codex", "sessions")
	if _, err := os.Stat(base); err != nil {
		return nil
	}
	cutoff := time.Now().Add(-30 * 24 * time.Hour)
	var out []string
	_ = filepath.WalkDir(base, func(path string, d fs.DirEntry, walkErr error) error {
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
		if codexFileMatchesCwd(path, wantCwd) {
			out = append(out, path)
		}
		return nil
	})
	// Newest first — handy when callers (or future single-session code)
	// wants the most-recent without re-sorting.
	sort.SliceStable(out, func(i, j int) bool {
		ai, _ := os.Stat(out[i])
		aj, _ := os.Stat(out[j])
		if ai == nil || aj == nil {
			return false
		}
		return ai.ModTime().After(aj.ModTime())
	})
	return out
}

// codexSessionIDFromFile peeks at the first record of a codex rollout
// for payload.id. Returns "" when missing or unreadable.
func codexSessionIDFromFile(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return extractCodexSessionID(raw)
}

// extractCodexSessionID scans the first ~16KB of a codex rollout for
// `{"type":"session_meta","payload":{"id":"<uuid>",...}}`. Codex always
// emits this as the first record.
func extractCodexSessionID(raw []byte) string {
	head := raw
	if len(head) > 16*1024 {
		head = head[:16*1024]
	}
	for _, line := range splitLines(head) {
		var rec struct {
			Type    string `json:"type"`
			Payload struct {
				ID string `json:"id"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(line, &rec); err == nil && rec.Payload.ID != "" {
			return rec.Payload.ID
		}
	}
	return ""
}

// --- shared helpers --------------------------------------------------------

// capturePick names one session-dir under <chat>/sessions/. Kept for the
// cross-machine fallback path.
type capturePick struct {
	dir     string
	summary SessionSummary
}

// newestCaptureForAgent walks the chat's sessions/ dir and returns the
// newest one whose summary.json's Agent matches and whose
// transcript.jsonl exists on disk.
func newestCaptureForAgent(sessionsDir string, agent AgentID) (capturePick, bool) {
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return capturePick{}, false
	}
	type cand struct {
		dir string
		s   SessionSummary
	}
	var cands []cand
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		full := filepath.Join(sessionsDir, e.Name())
		raw, err := os.ReadFile(filepath.Join(full, "summary.json"))
		if err != nil {
			continue
		}
		var s SessionSummary
		if err := json.Unmarshal(raw, &s); err != nil {
			continue
		}
		if s.Agent != string(agent) {
			continue
		}
		if _, err := os.Stat(filepath.Join(full, "transcript.jsonl")); err != nil {
			continue
		}
		cands = append(cands, cand{full, s})
	}
	if len(cands) == 0 {
		return capturePick{}, false
	}
	sort.SliceStable(cands, func(i, j int) bool {
		ai, aj := cands[i].s.StartedAt, cands[j].s.StartedAt
		if ai.IsZero() {
			ai = cands[i].s.GeneratedAt
		}
		if aj.IsZero() {
			aj = cands[j].s.GeneratedAt
		}
		return ai.After(aj)
	})
	return capturePick{dir: cands[0].dir, summary: cands[0].s}, true
}

// looksLikeUUID returns true for the 8-4-4-4-12 hex pattern (case-
// insensitive). Used to filter out non-UUID filenames that previous
// versions of the launcher may have left in the claude store.
func looksLikeUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, ch := range s {
		switch i {
		case 8, 13, 18, 23:
			if ch != '-' {
				return false
			}
		default:
			if !isHex(byte(ch)) {
				return false
			}
		}
	}
	return true
}

func isHex(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

// safeStem returns id when non-empty, otherwise fallback. Used in
// minted codex basenames so we don't write "rollout-...-.jsonl".
func safeStem(id, fallback string) string {
	if id != "" {
		return id
	}
	return fallback
}

// itoa is a tiny strconv-free int formatter for the short counts we
// embed in Notes (avoids pulling in strconv just for this).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// writeFileAtomic writes data to path via tmp + rename. Refuses to
// replace an existing file with empty data.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	if len(data) == 0 {
		if _, err := os.Stat(path); err == nil {
			return errors.New("refusing to overwrite existing file with empty data")
		}
	}
	tmp := path + ".praimate-tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
