package targets

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sPROFFEs/PrAImate/pkg/workpath"
)

// claudeTarget emits a Claude Code skill bundle.
//
// Layout produced under outDir:
//
//	.claude/skills/<name>/SKILL.md          (frontmatter: name, description)
//	.claude/skills/<name>/scripts/<tool>.sh (one per tool, mode preserved)
//	.claude/agents/<name>__<agent>.md       (one per subagent, namespaced)
//
// Skill names are sanitized to kebab-case (Claude Code's frontmatter
// validator rejects underscores).
type claudeTarget struct{}

func (claudeTarget) Name() string { return "claude" }

func (claudeTarget) Description() string {
	return "Claude Code skill (.claude/skills/<name>/SKILL.md + scripts + agents)"
}

func (claudeTarget) Compile(wp *workpath.Workpath, outDir string) error {
	skillName := kebab(wp.Name)
	skillDir := filepath.Join(outDir, ".claude", "skills", skillName)

	// CLAUDE.md at sandbox root — claude reads this unconditionally
	// at session start, unlike SKILL.md which only loads when the
	// skill system decides it's relevant. We put the workpath's
	// mission / playbook / rules / required-reading directive here
	// so the agent has them in its system prompt from turn 1.
	if err := writeFile(filepath.Join(outDir, "CLAUDE.md"), claudeRootBody(wp)); err != nil {
		return err
	}

	body := claudeSkillBody(wp)
	if err := writeFile(filepath.Join(skillDir, "SKILL.md"), body); err != nil {
		return err
	}

	for _, t := range wp.Tools {
		for _, scriptRel := range t.AllScripts() {
			src := wp.ResolveToolScript(t, scriptRel)
			dst := filepath.Join(skillDir, "scripts", filepath.Base(scriptRel))
			if err := copyFile(src, dst); err != nil {
				return fmt.Errorf("copy tool %s: %w", t.Name, err)
			}
		}
	}

	for _, a := range wp.Agents {
		src := wp.ResolveAgentPrompt(a)
		agentName := fmt.Sprintf("%s__%s", skillName, kebab(a.Name))
		dst := filepath.Join(outDir, ".claude", "agents", agentName+".md")
		body, err := claudeAgentBody(wp, a, src)
		if err != nil {
			return fmt.Errorf("render agent %s: %w", a.Name, err)
		}
		if err := writeFile(dst, body); err != nil {
			return err
		}
	}

	// Stage knowledge/ at the sandbox root so the agent's file-reading
	// tools find it at the same relative path the manifest in SKILL.md
	// advertises.
	if err := copyKnowledge(wp, outDir); err != nil {
		return err
	}

	// Hooks: claude code reads them from .claude/settings.json at the
	// sandbox root. No-op when the workpath has no hooks.
	if err := writeClaudeHooks(wp, outDir); err != nil {
		return err
	}

	return nil
}

// claudeHookEvent maps the portable PrAImate event name to claude code's
// own event vocabulary. Returns ("", false) when no mapping exists —
// caller drops the hook for this target and emits a note.
func claudeHookEvent(e workpath.HookEvent) (string, bool) {
	switch e {
	case workpath.HookPreTool:
		return "PreToolUse", true
	case workpath.HookPostTool:
		return "PostToolUse", true
	case workpath.HookUserInput:
		return "UserPromptSubmit", true
	case workpath.HookSessionStart:
		return "SessionStart", true
	case workpath.HookSessionStop:
		return "Stop", true
	case workpath.HookSubagentStop:
		return "SubagentStop", true
	case workpath.HookNotification:
		return "Notification", true
	}
	return "", false
}

// writeClaudeHooks emits .claude/settings.json with the portable hooks
// translated to claude code's event vocabulary. Layout produced:
//
//	{
//	  "hooks": {
//	    "PreToolUse": [
//	      {"matcher": "Bash", "hooks": [{"type": "command", "command": "..."}]}
//	    ],
//	    "Stop": [
//	      {"hooks": [{"type": "command", "command": "..."}]}
//	    ]
//	  }
//	}
//
// No-op when wp has no hooks. Always overwrites — PrAImate owns the file
// (no target stages anything else into it today; if a future feature
// needs to coexist we'll add a merge step).
func writeClaudeHooks(wp *workpath.Workpath, outDir string) error {
	if len(wp.Hooks) == 0 {
		return nil
	}
	// Group portable hooks by their claude event name. Hooks whose
	// event has no claude mapping are dropped silently here — they
	// were already accepted by the loader's validator, so the only
	// way to land in the "no mapping" branch is if a new portable
	// event ships without a claude mapping line in claudeHookEvent.
	type claudeCmd struct {
		Type    string `json:"type"`
		Command string `json:"command"`
	}
	type claudeEntry struct {
		Matcher string      `json:"matcher,omitempty"`
		Hooks   []claudeCmd `json:"hooks"`
	}
	groups := map[string][]claudeEntry{}
	for _, h := range wp.Hooks {
		evt, ok := claudeHookEvent(h.Event)
		if !ok {
			continue
		}
		groups[evt] = append(groups[evt], claudeEntry{
			Matcher: h.Matcher,
			Hooks:   []claudeCmd{{Type: "command", Command: h.Command}},
		})
	}
	if len(groups) == 0 {
		return nil
	}
	settings := map[string]any{"hooks": groups}
	raw, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("encode .claude/settings.json: %w", err)
	}
	raw = append(raw, '\n')
	return writeFile(filepath.Join(outDir, ".claude", "settings.json"), string(raw))
}

// claudeRootBody renders the CLAUDE.md that lives at the sandbox
// root. Claude auto-loads this on every session; the body therefore
// is the workpath's primary "you must follow this" surface. Kept
// compact (no full knowledge-base inventory, no agent rosters — those
// are in SKILL.md for skill-discovery use) so it doesn't blow out the
// system-prompt budget.
func claudeRootBody(wp *workpath.Workpath) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", wp.Name)
	if wp.Description != "" {
		b.WriteString(wp.Description + "\n\n")
	}
	b.WriteString("## How this chat operates\n\n")
	b.WriteString("This file is the contract for the current session. Read it on " +
		"every turn before answering. Do not improvise around the rules below; " +
		"if something here forbids what the user just asked for, say so and " +
		"propose an alternative.\n\n")

	if wp.Mission != "" {
		b.WriteString(section("Mission", wp.Mission))
		b.WriteString("\n")
	}
	if wp.Playbook != "" {
		b.WriteString(section("Playbook", wp.Playbook))
		b.WriteString("\n")
	}
	if wp.Rules != "" {
		b.WriteString(section("Rules", wp.Rules))
		b.WriteString("\n")
	}

	b.WriteString("## Required reading on every turn\n\n")
	b.WriteString("- `MEMORY.md` — the persistent log of prior sessions. If it's empty " +
		"or only headers, say so briefly; do not fabricate prior context.\n")
	if len(wp.Knowledge) > 0 {
		b.WriteString("- `knowledge/` — reference material indexed under the section " +
			"in `.claude/skills/" + kebab(wp.Name) + "/SKILL.md`. Open any file whose " +
			"title overlaps with the user's current question; cite the path inline.\n")
	}
	if len(wp.Tools) > 0 {
		b.WriteString("- `.claude/skills/" + kebab(wp.Name) + "/scripts/` — workpath tools " +
			"available via the Bash tool.\n")
	}
	if len(wp.Agents) > 0 {
		b.WriteString("- `.claude/agents/` — named subagent prompts you can dispatch when " +
			"a turn benefits from a focused persona.\n")
	}
	b.WriteString("\n")
	b.WriteString("If you write durable notes the next session should know about, " +
		"append them to `MEMORY.md` under a dated `### Title` subsection. Existing " +
		"entries are append-only — never rewrite or delete prior turns' notes.\n")
	return b.String()
}

func claudeSkillBody(wp *workpath.Workpath) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "name: %s\n", kebab(wp.Name))
	fmt.Fprintf(&b, "description: %s\n", yamlString(wp.Description))
	b.WriteString("---\n\n")
	fmt.Fprintf(&b, "# %s\n\n", wp.Name)
	b.WriteString(renderMissionBlock(wp))
	b.WriteString(renderToolList(wp))
	b.WriteString(renderAgentList(wp))
	b.WriteString(renderKnowledgeBlock(wp))
	if len(wp.Tools) > 0 {
		b.WriteString("\nScripts live in `scripts/` next to this SKILL.md and can be invoked via the Bash tool.\n")
	}
	return b.String()
}

func claudeAgentBody(wp *workpath.Workpath, a workpath.Agent, promptPath string) (string, error) {
	prompt, err := readFile(promptPath)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "name: %s__%s\n", kebab(wp.Name), kebab(a.Name))
	if a.Description != "" {
		fmt.Fprintf(&b, "description: %s\n", yamlString(a.Description))
	}
	if len(a.Tools) > 0 {
		fmt.Fprintf(&b, "tools: [%s]\n", strings.Join(a.Tools, ", "))
	}
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimSpace(prompt))
	b.WriteString("\n")
	return b.String(), nil
}

// kebab lowercases and replaces underscores/spaces with hyphens. Claude Code
// skill names must match ^[a-z0-9][a-z0-9-]*$.
func kebab(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "_", "-")
	s = strings.ReplaceAll(s, " ", "-")
	return s
}

// yamlString quotes a string for safe inclusion as a single-line YAML scalar.
// Quotes only when needed (presence of colon, leading/trailing space, etc.).
func yamlString(s string) string {
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, ":#\"'\n") || strings.HasPrefix(s, " ") || strings.HasSuffix(s, " ") {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}
