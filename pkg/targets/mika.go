package targets

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sPROFFEs/PrAImate/pkg/workpath"
)

// mikaTarget emits a mika-code workpath, the layout described in
// archive/go's internal/module loader.
//
// Layout produced under outDir:
//
//	modules/<name>/module.md         (description as H1 + mission body)
//	modules/<name>/playbook.md       (if non-empty)
//	modules/<name>/rules.md          (if non-empty)
//	modules/<name>/tools/<file>      (scripts copied verbatim, mode preserved)
//	modules/<name>/agents/<file>.md  (subagent prompts with frontmatter)
type mikaTarget struct{}

func (mikaTarget) Name() string { return "mika" }

func (mikaTarget) Description() string {
	return "mika-code workpath (modules/<name>/{module.md, playbook.md, rules.md, tools/, agents/})"
}

func (mikaTarget) Compile(wp *workpath.Workpath, outDir string) error {
	root := filepath.Join(outDir, "modules", wp.Name)

	module := fmt.Sprintf("# %s\n\n> %s\n\n%s\n", wp.Name, wp.Description, wp.Mission)
	if err := writeFile(filepath.Join(root, "module.md"), module); err != nil {
		return err
	}

	if wp.Playbook != "" {
		if err := writeFile(filepath.Join(root, "playbook.md"), wp.Playbook+"\n"); err != nil {
			return err
		}
	}
	if wp.Rules != "" {
		if err := writeFile(filepath.Join(root, "rules.md"), wp.Rules+"\n"); err != nil {
			return err
		}
	}

	for _, t := range wp.Tools {
		for _, scriptRel := range t.AllScripts() {
			src := wp.ResolveToolScript(t, scriptRel)
			dst := filepath.Join(root, "tools", filepath.Base(scriptRel))
			if err := copyFile(src, dst); err != nil {
				return fmt.Errorf("copy tool %s: %w", t.Name, err)
			}
		}
	}

	for _, a := range wp.Agents {
		src := wp.ResolveAgentPrompt(a)
		dst := filepath.Join(root, "agents", filepath.Base(a.Prompt))
		body, err := mikaAgentBody(a, src)
		if err != nil {
			return fmt.Errorf("render agent %s: %w", a.Name, err)
		}
		if err := writeFile(dst, body); err != nil {
			return err
		}
	}

	// Knowledge sits next to the module files so a mika module that
	// ships reference material has it co-located with module.md.
	for _, k := range wp.Knowledge {
		src := wp.ResolveKnowledgePath(k)
		dst := filepath.Join(root, filepath.FromSlash(k.RelPath))
		if err := copyFile(src, dst); err != nil {
			return fmt.Errorf("copy knowledge %s: %w", k.RelPath, err)
		}
	}
	// Append a manifest section to module.md so the reader / agent
	// sees the knowledge inventory next to the mission. The hook note
	// (if any) appends after, on the same module.md.
	tail := renderKnowledgeBlock(wp) + renderHookNote(wp, "mika")
	if tail != "" {
		modulePath := filepath.Join(root, "module.md")
		existing, err := readFile(modulePath)
		if err == nil {
			if err := writeFile(modulePath, strings.TrimRight(existing, "\n")+"\n"+tail); err != nil {
				return err
			}
		}
	}

	return nil
}

// mikaAgentBody preserves the original prompt body and prepends a frontmatter
// block carrying name/description/tools — matching the convention used in
// archive/go's internal/module loader.
func mikaAgentBody(a workpath.Agent, promptPath string) (string, error) {
	prompt, err := readFile(promptPath)
	if err != nil {
		return "", err
	}
	prompt = strings.TrimSpace(prompt)
	// If the source already has frontmatter, pass it through untouched —
	// the author already controls it.
	if strings.HasPrefix(prompt, "---\n") {
		return prompt + "\n", nil
	}
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "name: %s\n", a.Name)
	if a.Description != "" {
		fmt.Fprintf(&b, "description: %s\n", yamlString(a.Description))
	}
	if len(a.Tools) > 0 {
		fmt.Fprintf(&b, "tools: [%s]\n", strings.Join(a.Tools, ", "))
	}
	b.WriteString("---\n\n")
	b.WriteString(prompt)
	b.WriteString("\n")
	return b.String(), nil
}
