package targets

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sPROFFEs/PrAImate/pkg/workpath"
)

// codexTarget emits an AGENTS.md at the out-dir root, the file Codex CLI and
// OpenCode both load on every invocation. Tool scripts and subagent prompts
// go into AGENTS.assets/ so the model can shell out to them by relative path.
//
// Layout produced under outDir:
//
//	AGENTS.md
//	AGENTS.assets/tools/<tool>.sh
//	AGENTS.assets/agents/<agent>.md
//
// Unlike `generic`, the markdown file is always named `AGENTS.md` regardless
// of the workpath name — that is what the host CLIs scan for. If outDir
// already contains an AGENTS.md the compiler overwrites it; merge by hand if
// you need to preserve an existing one.
type codexTarget struct{}

func (codexTarget) Name() string { return "codex" }

func (codexTarget) Description() string {
	return "AGENTS.md at out-dir root (Codex CLI and OpenCode); assets in AGENTS.assets/"
}

func (codexTarget) Compile(wp *workpath.Workpath, outDir string) error {
	mdPath := filepath.Join(outDir, "AGENTS.md")
	if err := writeFile(mdPath, codexBody(wp)); err != nil {
		return err
	}

	// Knowledge always copies, even when there are no tools/agents,
	// since templates can ship knowledge-only (research notebooks,
	// reference packs, etc.).
	if err := copyKnowledge(wp, outDir); err != nil {
		return err
	}

	if len(wp.Tools) == 0 && len(wp.Agents) == 0 {
		return nil
	}

	assetsDir := filepath.Join(outDir, "AGENTS.assets")
	for _, t := range wp.Tools {
		for _, scriptRel := range t.AllScripts() {
			src := wp.ResolveToolScript(t, scriptRel)
			dst := filepath.Join(assetsDir, "tools", filepath.Base(scriptRel))
			if err := copyFile(src, dst); err != nil {
				return fmt.Errorf("copy tool %s: %w", t.Name, err)
			}
		}
	}
	for _, a := range wp.Agents {
		src := wp.ResolveAgentPrompt(a)
		dst := filepath.Join(assetsDir, "agents", filepath.Base(a.Prompt))
		if err := copyFile(src, dst); err != nil {
			return fmt.Errorf("copy agent %s: %w", a.Name, err)
		}
	}
	return nil
}

func codexBody(wp *workpath.Workpath) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", wp.Name)
	fmt.Fprintf(&b, "> %s\n\n", wp.Description)
	if wp.Version != "" {
		fmt.Fprintf(&b, "_Version %s", wp.Version)
		if wp.License != "" {
			fmt.Fprintf(&b, " · %s", wp.License)
		}
		b.WriteString("_\n\n")
	}
	b.WriteString(renderMissionBlock(wp))
	b.WriteString(renderToolList(wp))
	b.WriteString(renderAgentList(wp))
	b.WriteString(renderKnowledgeBlock(wp))
	b.WriteString(renderHookNote(wp, "codex"))
	if len(wp.Tools) > 0 {
		b.WriteString("\nTool scripts live under `AGENTS.assets/tools/`. " +
			"Invoke with the Bash tool using the relative path.\n")
	}
	if len(wp.Agents) > 0 {
		b.WriteString("\nSubagent prompts live under `AGENTS.assets/agents/`. " +
			"Read the file when you need to adopt that persona.\n")
	}
	return b.String()
}
