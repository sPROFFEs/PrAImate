package launcher

// Post-compile decoration. After wpc has written the workpath into the
// sandbox, the launcher layers per-workspace concerns on top:
//
//   - language directive (a one-line "Respond in X" preamble)
//   - persistent MEMORY.md (copied workspace → sandbox before launch,
//     synced back after the agent exits)
//   - online skills (cloned into the right per-target skills dir)
//   - a new chats/<timestamp>-<agent>/ session manifest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/skills"
)

// applyDecorations runs every per-workspace post-compile concern. Called
// from PrepareSandbox after wpc has run. Errors are aggregated but
// non-fatal — we don't want a broken online-skill clone to block the
// launch.
func applyDecorations(ws Workspace, agent Agent) []string {
	var notes []string

	// Personality is the highest-priority context — comes first so it
	// lands at the top of the agent's instructions, right after any
	// frontmatter.
	if err := prependPersonality(ws, agent); err != nil {
		notes = append(notes, "personality: "+err.Error())
	}

	if lang := strings.TrimSpace(ws.Settings.Language); lang != "" {
		if err := prependLanguage(ws, agent, lang); err != nil {
			notes = append(notes, "language: "+err.Error())
		}
	}

	if ws.Settings.MemoryEnabled {
		if err := stageMemory(ws); err != nil {
			notes = append(notes, "memory: "+err.Error())
		}
		// Tell the agent about the file it just got. Without this the
		// MEMORY.md sits in the sandbox unread — the agent doesn't read
		// arbitrary files in its cwd unless its instructions point at
		// them.
		if err := appendMemoryDirective(ws, agent); err != nil {
			notes = append(notes, "memory directive: "+err.Error())
		}
	}

	if len(ws.Settings.OnlineSkills) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		dir := onlineSkillsDir(ws, agent)
		var fetched []string
		for _, r := range skills.FetchAll(ctx, ws.Settings.OnlineSkills, dir) {
			if r.Err != nil {
				notes = append(notes, fmt.Sprintf("online-skill %s: %v", r.URL, r.Err))
				continue
			}
			fetched = append(fetched, r.Path)
		}
		// Tell the agent the skills exist + that they're required
		// reading. Without this, codex/opencode/gemini agents have no
		// idea what's under online-skills/ and never open it. Claude
		// auto-discovers .claude/skills/ so the directive is redundant
		// there but harmless — the same "scan and open if relevant"
		// instruction reinforces the auto-load.
		if len(fetched) > 0 {
			if err := appendOnlineSkillsDirective(ws, agent, dir, fetched); err != nil {
				notes = append(notes, "online-skills directive: "+err.Error())
			}
		}
	}

	if err := recordChatSession(ws, agent); err != nil {
		notes = append(notes, "chat-log: "+err.Error())
	}

	// Inject a "Recent sessions" digest of the last few summaries into
	// the compiled instructions. Runs after recordChatSession so the
	// brand-new (empty) session dir isn't included. No-op when there
	// are no prior summaries on disk yet.
	if err := appendRecentSessionsDirective(ws, agent); err != nil {
		notes = append(notes, "recent-sessions: "+err.Error())
	}

	return notes
}

// agentInstructionsPath returns the path of the compiled instruction
// file the agent reads on startup (SKILL.md / AGENTS.md / GEMINI.md),
// or "" if the target doesn't have one we know how to decorate.
func agentInstructionsPath(ws Workspace, agent Agent) string {
	switch agent.WpcTarget {
	case "claude":
		return filepath.Join(ws.SandboxDir, ".claude", "skills", ws.Name, "SKILL.md")
	case "codex":
		return filepath.Join(ws.SandboxDir, "AGENTS.md")
	case "gemini":
		return filepath.Join(ws.SandboxDir, "GEMINI.md")
	}
	return ""
}

// prependPersonality reads <workpath>/personality.md (if present) and
// injects it at the very top of the compiled instructions, framed as
// a persona / system-prompt block. Acts as the agent's "you are"
// directive — comes before everything else (mission, playbook, rules)
// so it influences tone and decision-making throughout the chat.
//
// No-op when personality.md doesn't exist or is empty.
func prependPersonality(ws Workspace, agent Agent) error {
	// Read from the chat's workpath (which is a clone of the template's
	// workpath at chat creation), so per-chat overrides of personality
	// work naturally.
	wpDir := filepath.Join(ws.Root, "workpath")
	if _, err := os.Stat(wpDir); err != nil {
		// Old layout: workpath was the chat root itself; fall back.
		wpDir = ws.Root
	}
	body, err := os.ReadFile(filepath.Join(wpDir, "personality.md"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	persona := strings.TrimSpace(stripHTMLComments(string(body)))
	if persona == "" {
		// File exists but only contains comments / whitespace — treat
		// as "no persona configured" so the auto-created placeholder
		// doesn't leak into the agent's instructions.
		return nil
	}

	path := agentInstructionsPath(ws, agent)
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	full := string(raw)

	directive := "\n## Persona\n\nAdopt the following persona for the entire session. " +
		"It overrides default tone defaults and applies to every reply.\n\n" +
		persona + "\n"

	// Insert AFTER YAML frontmatter for Claude's SKILL.md so the loader
	// stays happy; otherwise prepend to the body.
	if strings.HasPrefix(full, "---\n") {
		if end := strings.Index(full[4:], "\n---\n"); end >= 0 {
			cut := 4 + end + len("\n---\n")
			full = full[:cut] + directive + full[cut:]
		} else {
			full = directive + full
		}
	} else {
		full = directive + "\n" + full
	}
	return os.WriteFile(path, []byte(full), 0o644)
}

// stripHTMLComments returns s with every <!-- ... --> block removed.
// Used by personality processing so a comments-only personality.md
// (the auto-scaffolded placeholder) doesn't get treated as content.
func stripHTMLComments(s string) string {
	for {
		i := strings.Index(s, "<!--")
		if i < 0 {
			return s
		}
		j := strings.Index(s[i:], "-->")
		if j < 0 {
			return s[:i] // unclosed comment — drop the tail
		}
		s = s[:i] + s[i+j+3:]
	}
}

func prependLanguage(ws Workspace, agent Agent, lang string) error {
	path := agentInstructionsPath(ws, agent)
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	body := string(raw)
	directive := fmt.Sprintf("\n> **Language directive:** respond in %s.\n", lang)

	// For Claude SKILL.md we must keep YAML frontmatter intact, so insert
	// directive AFTER the second `---` line.
	if strings.HasPrefix(body, "---\n") {
		if end := strings.Index(body[4:], "\n---\n"); end >= 0 {
			cut := 4 + end + len("\n---\n")
			body = body[:cut] + directive + body[cut:]
		}
	} else {
		body = directive + "\n" + body
	}
	return os.WriteFile(path, []byte(body), 0o644)
}

// memoryDirectiveSection is the text appended to the compiled
// instructions when MemoryEnabled is true. The exact wording matters —
// it's the only signal the agent gets that MEMORY.md exists, and the
// previous softer version produced empty files because agents didn't
// actually treat "if the user asks, append..." as a reliable trigger.
const memoryDirectiveSection = `
## Persistent memory — required workflow

A file named ` + "`MEMORY.md`" + ` sits at the root of your current working
directory. It is YOUR notebook for this chat and the only state that
survives across launches. The launcher syncs it back to durable
storage when you exit.

You MUST follow this protocol:

**1. At session start (before responding to the first user message):**
   - Read ` + "`MEMORY.md`" + ` end-to-end.
   - If non-trivial notes exist, open your reply with a one-line
     "📒 Recalled: …" summary so the user sees the context carried over.
   - If the file only contains the starter header, say nothing about it.

**2. During the session, ` + "`MEMORY.md`" + ` MUST be updated when ANY of:**
   - The user says "remember", "save", "note this", "for later", or
     similar memory-intent phrasing.
   - You discover a durable fact about THIS project that a future
     session would benefit from knowing — build commands, key file
     paths, conventions, gotchas, user preferences, decisions already
     made and their rationale, in-flight tasks.
   - The user asks you to track a TODO list or a running summary.

**3. How to update:**
   - Use your file-write tool (Edit / Write — whichever your CLI
     exposes) to **append** to ` + "`MEMORY.md`" + `.
   - The launcher already wrote a "## YYYY-MM-DD HH:MM — Session
     opened" marker at the bottom of the file. Place new notes BELOW
     that most recent marker as ` + "`### Title`" + ` (H3) subsections so each
     session's contributions are visibly grouped.
   - Never overwrite or shorten existing entries unless asked.
   - After writing, tell the user "✓ saved to MEMORY.md: <title>" in
     one short line so they see the action.

**4. What NOT to do:**
   - Do not modify other files in the chat root.
   - Do not invent memory entries to look helpful — only save real,
     durable facts.
   - Do not skip step 1; the user can tell when you ignored prior
     context.
`

// appendMemoryDirective tacks the persistent-memory section onto the
// end of the compiled instructions so the agent learns MEMORY.md exists
// and how to use it. No-op for targets we don't know how to decorate.
func appendMemoryDirective(ws Workspace, agent Agent) error {
	path := agentInstructionsPath(ws, agent)
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	body := string(raw)
	// Idempotency: don't append twice if applyDecorations runs again.
	if strings.Contains(body, "## Persistent memory — required workflow") {
		return nil
	}
	body = strings.TrimRight(body, "\n") + "\n" + memoryDirectiveSection
	return os.WriteFile(path, []byte(body), 0o644)
}

// appendOnlineSkillsDirective tells the agent that cloned online
// skills live under <skillsDir> and that it must scan them at the
// start of every turn the same way it scans the knowledge inventory.
// Lists each skill by its directory name so the agent has a quick
// reference without having to ls first.
func appendOnlineSkillsDirective(ws Workspace, agent Agent, skillsDir string, fetched []string) error {
	path := agentInstructionsPath(ws, agent)
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	body := string(raw)
	if strings.Contains(body, "## Online skills — required reading workflow") {
		return nil // idempotent
	}

	// Render skill paths relative to the sandbox root for clarity.
	rels := make([]string, 0, len(fetched))
	for _, f := range fetched {
		if rel, err := filepath.Rel(ws.SandboxDir, f); err == nil {
			rels = append(rels, filepath.ToSlash(rel))
		} else {
			rels = append(rels, filepath.ToSlash(f))
		}
	}

	var b strings.Builder
	b.WriteString("\n## Online skills — required reading workflow\n\n")
	b.WriteString(
		"This chat has remote skill bundles cloned into your working " +
			"directory. Contents are NOT pre-loaded into your context; " +
			"you MUST consult them yourself before responding.\n\n")
	b.WriteString("**Required workflow on every user turn:**\n\n")
	b.WriteString(
		"1. Scan the list below for skills whose name overlaps with the " +
			"user's current question (domain, tool, technique).\n")
	b.WriteString(
		"2. Open the relevant skill's primary instruction file (typically " +
			"`SKILL.md`, `README.md`, or the only `.md` at its root) and " +
			"apply what it tells you. Cite the skill name when you draw " +
			"on it.\n")
	b.WriteString(
		"3. If nothing matches, note that briefly so the user sees you " +
			"checked. Do NOT silently skip.\n\n")
	b.WriteString("**Available skills:**\n\n")
	for _, r := range rels {
		fmt.Fprintf(&b, "- `%s`\n", r)
	}

	body = strings.TrimRight(body, "\n") + "\n" + b.String()
	return os.WriteFile(path, []byte(body), 0o644)
}

// recentSessionsLimit caps how many prior session summaries we replay
// into the compiled instructions. Three is enough to give the agent
// continuity without bloating the system prompt.
const recentSessionsLimit = 3

// appendRecentSessionsDirective scans <chat>/sessions/ for any prior
// summary.json files, sorts by StartedAt descending, and appends a
// "## Recent sessions" block to the compiled instructions so the
// agent has a deterministic recap before turn one — even if it's a
// different agent than ran the prior session(s).
//
// Idempotent (guarded by a section marker) and a no-op when there
// are no summaries yet.
func appendRecentSessionsDirective(ws Workspace, agent Agent) error {
	summaries, err := LoadRecentSummaries(ws.ChatsDir, recentSessionsLimit+1) // +1 because the slot we just created might already be in the list (it won't have summary.json yet, but be safe)
	if err != nil {
		return err
	}
	// Filter out any summary that belongs to the just-created session
	// dir (it has no transcript yet; including it would tell the
	// agent "look at yourself" which is unhelpful).
	filtered := summaries[:0]
	for _, s := range summaries {
		if s.SessionDir != LastSessionDir {
			filtered = append(filtered, s)
		}
	}
	summaries = filtered
	if len(summaries) > recentSessionsLimit {
		summaries = summaries[:recentSessionsLimit]
	}
	if len(summaries) == 0 {
		return nil
	}

	path := agentInstructionsPath(ws, agent)
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	body := string(raw)
	if strings.Contains(body, "## Recent sessions — required reading before turn 1") {
		return nil // idempotent
	}

	var b strings.Builder
	b.WriteString("\n## Recent sessions — required reading before turn 1\n\n")
	b.WriteString(
		"The launcher captured digests of the prior sessions in this chat. " +
			"Read them BEFORE responding so you have continuity with what was " +
			"already discussed — even if the previous turns were with a different " +
			"agent CLI. Files referenced below sit under `sessions/` in your cwd.\n\n")
	for _, s := range summaries {
		stamp := s.StartedAt
		if stamp.IsZero() {
			stamp = s.GeneratedAt
		}
		rel := s.SessionDir
		if r, err := filepath.Rel(ws.SandboxDir, s.SessionDir); err == nil {
			rel = filepath.ToSlash(r)
		}
		fmt.Fprintf(&b, "### %s · %s\n", stamp.Local().Format("2006-01-02 15:04"), s.Agent)
		if s.Headline != "" {
			fmt.Fprintf(&b, "_%s_\n\n", s.Headline)
		}
		if s.Duration != "" || s.UserTurns > 0 || s.AssistantTurns > 0 {
			fmt.Fprintf(&b, "- Duration: %s · %d user · %d assistant · %d tool calls\n",
				s.Duration, s.UserTurns, s.AssistantTurns, s.ToolCalls)
		}
		if s.FirstUserExcerpt != "" {
			fmt.Fprintf(&b, "- Opened with: %q\n", excerpt(s.FirstUserExcerpt, 160))
		}
		if s.LastAssistantExcerpt != "" {
			fmt.Fprintf(&b, "- Last reply: %q\n", excerpt(s.LastAssistantExcerpt, 160))
		}
		fmt.Fprintf(&b, "- Files: `%s/summary.md`, `%s/transcript.jsonl` (if captured)\n\n", rel, rel)
	}
	b.WriteString(
		"If anything above matches the user's question, open the matching " +
			"`summary.md` (and `transcript.jsonl` if you need detail) before " +
			"replying. Cite the session date when you draw on it.\n")

	body = strings.TrimRight(body, "\n") + "\n" + b.String()
	return os.WriteFile(path, []byte(body), 0o644)
}

// stageMemory copies workspace/MEMORY.md → sandbox/MEMORY.md before
// launch and APPENDS a session-start marker. The marker has two
// purposes:
//
//  1. It gives the user visible proof memory is alive — the file
//     grows every launch even if the agent itself writes nothing.
//  2. It gives the agent an anchor: the directive (see decorate.go's
//     memoryDirectiveSection) says "add notes BELOW the most recent
//     '## YYYY-MM-DD ...' marker", so each session's contributions
//     are clearly grouped.
//
// The workspace-level file is the canonical source of truth; sync-back
// happens via SyncMemoryBack after the agent exits.
func stageMemory(ws Workspace) error {
	src := filepath.Join(ws.Root, "MEMORY.md")
	if _, err := os.Stat(src); errors.Is(err, fs.ErrNotExist) {
		header := "# Memory\n\n" +
			"This file persists across agent sessions for this workspace.\n" +
			"Each session opens with a date-stamped marker; agents add notes\n" +
			"below the most recent marker.\n"
		if err := os.WriteFile(src, []byte(header), 0o644); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	// Append the marker to the workspace file FIRST so each launch
	// leaves a trace immediately (visible to the user even if the
	// agent exits abnormally and sync-back never runs), THEN copy to
	// the sandbox so the agent sees its own session marker as the
	// last entry in the file.
	if err := appendSessionStartMarker(src, ws); err != nil {
		return err
	}
	dst := filepath.Join(ws.SandboxDir, "MEMORY.md")
	return copyFileLib(src, dst)
}

// appendSessionStartMarker tacks "## YYYY-MM-DD HH:MM — Session opened"
// onto MEMORY.md so each launch leaves a visible trace and the agent
// has a clear anchor for that session's appends.
func appendSessionStartMarker(path string, ws Workspace) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	stamp := time.Now().Local().Format("2006-01-02 15:04")
	line := fmt.Sprintf("\n## %s — Session opened\n_(workspace: %s)_\n\n",
		stamp, ws.Name)
	_, err = f.WriteString(line)
	return err
}

// SyncMemoryBack copies sandbox/MEMORY.md → workspace/MEMORY.md if the
// sandbox version is newer. Called from main() after the agent exits.
func SyncMemoryBack(ws Workspace) error {
	if !ws.Settings.MemoryEnabled {
		return nil
	}
	sandboxPath := filepath.Join(ws.SandboxDir, "MEMORY.md")
	wsPath := filepath.Join(ws.Root, "MEMORY.md")
	sInfo, err := os.Stat(sandboxPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil // agent never touched it
	}
	if err != nil {
		return err
	}
	wInfo, wErr := os.Stat(wsPath)
	if wErr == nil && !sInfo.ModTime().After(wInfo.ModTime()) {
		return nil // no changes
	}
	return copyFileLib(sandboxPath, wsPath)
}

func onlineSkillsDir(ws Workspace, agent Agent) string {
	switch agent.WpcTarget {
	case "claude":
		// Claude Code auto-loads everything under .claude/skills/.
		return filepath.Join(ws.SandboxDir, ".claude", "skills")
	default:
		// For codex/opencode we drop them next to AGENTS.md and surface
		// the path in the orientation file. The agent can still cat them
		// on demand.
		return filepath.Join(ws.SandboxDir, "online-skills")
	}
}

// SessionRecord is the metadata blob written into
// <ws>/chats/<timestamp>-<agent>/session.json so the user can browse
// past sessions and see what each one was about. Actual transcript
// capture is a separate, agent-specific concern (Phase 3).
type SessionRecord struct {
	StartedAt   time.Time `json:"startedAt"`
	Agent       string    `json:"agent"`
	Binary      string    `json:"binary"`
	WpcTarget   string    `json:"wpcTarget"`
	WorkspaceID string    `json:"workspace"`
	SandboxDir  string    `json:"sandboxDir"`
}

func recordChatSession(ws Workspace, agent Agent) error {
	started := time.Now().UTC()
	stamp := started.Format("20060102-150405")
	dir := filepath.Join(ws.ChatsDir, stamp+"-"+string(agent.ID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	rec := SessionRecord{
		StartedAt:   started,
		Agent:       string(agent.ID),
		Binary:      agent.Binary,
		WpcTarget:   agent.WpcTarget,
		WorkspaceID: ws.Name,
		SandboxDir:  ws.SandboxDir,
	}
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(filepath.Join(dir, "session.json"), raw, 0o644); err != nil {
		return err
	}
	// Publish so main() can find this session's slot after the agent
	// exits and write transcript.jsonl + summary.md into it.
	LastSessionDir = dir
	LastSessionStartedAt = started
	return nil
}

// copyFileLib avoids name clash with the existing copyFile in workspaces.go.
func copyFileLib(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	raw, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, raw, 0o644)
}
