package targets

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sPROFFEs/PrAImate/pkg/workpath"
)

// genericTarget emits a single self-contained markdown file with every part
// of the workpath inlined. Suitable for any CLI agent that reads an
// AGENTS.md-style instruction file, or for hand-pasting into a system prompt.
//
// Layout produced under outDir:
//
//	<name>.md            (everything inlined)
//	<name>.assets/       (tool scripts + agent prompts copied verbatim)
//
// The asset directory is only written when there are tools or agents.
type genericTarget struct{}

func (genericTarget) Name() string { return "generic" }

func (genericTarget) Description() string {
	return "Single-file AGENTS.md-style markdown for any CLI agent; assets in <name>.assets/"
}

func (genericTarget) Compile(wp *workpath.Workpath, outDir string) error {
	mdPath := filepath.Join(outDir, wp.Name+".md")
	if err := writeFile(mdPath, genericBody(wp)); err != nil {
		return err
	}

	if err := copyKnowledge(wp, outDir); err != nil {
		return err
	}

	if len(wp.Tools) == 0 && len(wp.Agents) == 0 {
		return nil
	}

	assetsDir := filepath.Join(outDir, wp.Name+".assets")
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

func genericBody(wp *workpath.Workpath) string {
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
	b.WriteString(renderHookNote(wp, "generic"))
	if len(wp.Tools) > 0 || len(wp.Agents) > 0 {
		fmt.Fprintf(&b, "\nAssets live under `%s.assets/`.\n", wp.Name)
	}
	return b.String()
}
