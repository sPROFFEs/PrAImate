package targets

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sPROFFEs/PrAImate/pkg/workpath"
)

// copyFile copies src → dst, creating dst's parent dir. Preserves the source
// file's mode bits so executable scripts stay executable.
func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode())
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return nil
}

// writeFile writes content to dst, creating parent dirs.
func writeFile(dst, content string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, []byte(content), 0o644)
}

// readFile is os.ReadFile that returns a string.
func readFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return string(raw), nil
}

// renderMissionBlock produces the markdown body shared by most targets:
// mission + optional playbook + optional rules. Each body is included
// verbatim if it already starts with a heading; otherwise it's wrapped in a
// generated H2 section header. This keeps hand-authored mission.md files
// with rich H1 structure intact while still giving plain-prose bodies a
// section title.
func renderMissionBlock(wp *workpath.Workpath) string {
	var b strings.Builder
	b.WriteString(section("Mission", wp.Mission))
	if wp.Playbook != "" {
		b.WriteString("\n")
		b.WriteString(section("Playbook", wp.Playbook))
	}
	if wp.Rules != "" {
		b.WriteString("\n")
		b.WriteString(section("Rules", wp.Rules))
	}
	return b.String()
}

func section(title, body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	if strings.HasPrefix(body, "#") {
		return body + "\n"
	}
	return "## " + title + "\n\n" + body + "\n"
}

// renderToolList returns a markdown bullet list of tools, or empty string if
// there are none. When a tool ships platform-paired scripts (.sh + .ps1)
// they're all listed so the agent knows both variants exist and can
// pick whichever matches its host shell.
func renderToolList(wp *workpath.Workpath) string {
	if len(wp.Tools) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## Tools\n\n")
	for _, t := range wp.Tools {
		desc := t.Description
		if desc == "" {
			desc = "(no description)"
		}
		scripts := t.AllScripts()
		switch len(scripts) {
		case 0:
			fmt.Fprintf(&b, "- `%s` — %s\n", t.Name, desc)
		case 1:
			fmt.Fprintf(&b, "- `%s` — %s (`%s`)\n", t.Name, desc, scripts[0])
		default:
			fmt.Fprintf(&b, "- `%s` — %s (`%s`)\n", t.Name, desc, strings.Join(scripts, "`, `"))
		}
	}
	return b.String()
}

// renderHookNote returns a "## Hooks" section noting that the
// workpath declares hooks but THIS target has no documented hook
// system yet, so they will NOT fire when launching this agent. Empty
// string when wp has no hooks. Used by every target except claude
// (claude has a real emitter — see writeClaudeHooks).
//
// The note lists each declared hook so authors can audit what's not
// wired. When the upstream agent grows a stable hook system, swap
// this call for a real emitter in that target's Compile().
func renderHookNote(wp *workpath.Workpath, targetName string) string {
	if len(wp.Hooks) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n## Hooks (declared, NOT wired for %s)\n\n", targetName)
	fmt.Fprintf(&b, "This workpath declares %d hook(s). The %s target has no "+
		"documented hook system yet, so these triggers will NOT fire when "+
		"launching this agent. They are wired for the `claude` target only "+
		"(via `.claude/settings.json`). See `hooks.json` in the workpath "+
		"source for the declarations.\n\n",
		len(wp.Hooks), targetName)
	for _, h := range wp.Hooks {
		matcher := h.Matcher
		if matcher == "" {
			matcher = "*"
		}
		desc := h.Description
		if desc == "" {
			desc = h.Command
		}
		fmt.Fprintf(&b, "- `%s` matcher=`%s` → %s\n", h.Event, matcher, desc)
	}
	return b.String()
}

// copyKnowledge stages every <workpath>/knowledge/** file into
// outDir at the same relative path. Hidden files and dirs were
// filtered by the loader already; here we just copy what made it
// into wp.Knowledge. No-op when there's no knowledge.
//
// All targets call this with their own outDir so the agent always
// finds reference material at `knowledge/...` relative to the
// sandbox root, regardless of which agent CLI is consuming it.
func copyKnowledge(wp *workpath.Workpath, outDir string) error {
	for _, k := range wp.Knowledge {
		src := wp.ResolveKnowledgePath(k)
		dst := filepath.Join(outDir, filepath.FromSlash(k.RelPath))
		if err := copyFile(src, dst); err != nil {
			return fmt.Errorf("copy knowledge %s: %w", k.RelPath, err)
		}
	}
	return nil
}

// renderKnowledgeBlock returns a markdown "Knowledge base" section
// listing every knowledge file with title + short summary. Returns
// empty string when wp.Knowledge is empty.
//
// The block is an ACTIVE directive: agents are required to scan the
// manifest at the start of every user turn and open any file whose
// title/summary suggests it's relevant to the current message before
// answering. We tried a passive "use these if helpful" wording first
// and observed that agents largely ignored the manifest — making the
// directive prescriptive (with explicit triggers) raises the hit
// rate from "almost never" to "reliably when relevant".
func renderKnowledgeBlock(wp *workpath.Workpath) string {
	if len(wp.Knowledge) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## Knowledge base — required reading workflow\n\n")
	b.WriteString(
		"Reference material lives under `knowledge/` in your current " +
			"working directory. Contents are NOT pre-loaded into your " +
			"context; you MUST consult them yourself before responding.\n\n")
	b.WriteString("**Required workflow on every user turn:**\n\n")
	b.WriteString(
		"1. Scan the inventory below for files whose title or summary " +
			"looks relevant to the user's current message — domain " +
			"jargon, named tools/protocols/APIs, file formats, " +
			"techniques, anything that overlaps.\n")
	b.WriteString(
		"2. Open every relevant file with your file-reading tool (Read, " +
			"view, etc.) and incorporate what you find into your reply. " +
			"Cite the file path inline when you draw on it so the user " +
			"can audit your sources.\n")
	b.WriteString(
		"3. If nothing in the inventory matches, say so briefly in your " +
			"reasoning (one short line) so the user knows you checked. " +
			"Do NOT silently skip this step.\n\n")
	b.WriteString("**Inventory:**\n\n")
	for _, k := range wp.Knowledge {
		size := humaniseBytes(k.Bytes)
		title := strings.TrimSpace(k.Title)
		if title == "" {
			title = filepath.Base(k.RelPath)
		}
		if k.IsText {
			fmt.Fprintf(&b, "- `%s` (%s) — **%s**", k.RelPath, size, title)
			if k.Summary != "" {
				fmt.Fprintf(&b, "  \n  %s", k.Summary)
			}
			b.WriteString("\n")
		} else {
			fmt.Fprintf(&b, "- `%s` (%s, binary) — %s "+
				"_(open with the appropriate parser; do not assume contents)_\n",
				k.RelPath, size, title)
		}
	}
	return b.String()
}

// humaniseBytes returns "12 B" / "3.4 KB" / "2.1 MB" style sizes for
// the manifest. Kept inline so the targets package doesn't take a
// dep on a units library.
func humaniseBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	case n < 1024*1024*1024:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	default:
		return fmt.Sprintf("%.1f GB", float64(n)/(1024*1024*1024))
	}
}

// renderAgentList returns a markdown bullet list of agents.
func renderAgentList(wp *workpath.Workpath) string {
	if len(wp.Agents) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## Subagents\n\n")
	for _, a := range wp.Agents {
		desc := a.Description
		if desc == "" {
			desc = "(no description)"
		}
		fmt.Fprintf(&b, "- `%s` — %s\n", a.Name, desc)
		if len(a.Tools) > 0 {
			fmt.Fprintf(&b, "  - tools: %s\n", strings.Join(a.Tools, ", "))
		}
	}
	return b.String()
}
