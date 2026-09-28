package targets

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sPROFFEs/PrAImate/pkg/workpath"
)

// cursorTarget emits a Cursor rule file.
//
// Layout produced under outDir:
//
//	.cursor/rules/<name>.mdc
//
// Cursor rules support frontmatter (description, globs, alwaysApply) but have
// no native tool or subagent concept. Tools and agents are inlined into the
// body as bullet lists; their scripts are NOT copied (Cursor cannot execute
// them) — users are expected to also run `wpc compile --target generic` or
// keep the original source dir on disk.
type cursorTarget struct{}

func (cursorTarget) Name() string { return "cursor" }

func (cursorTarget) Description() string {
	return "Cursor rule (.cursor/rules/<name>.mdc); tools/agents inlined as references"
}

func (cursorTarget) Compile(wp *workpath.Workpath, outDir string) error {
	dst := filepath.Join(outDir, ".cursor", "rules", wp.Name+".mdc")
	return writeFile(dst, cursorBody(wp))
}

func cursorBody(wp *workpath.Workpath) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "description: %s\n", yamlString(wp.Description))
	b.WriteString("globs:\n")
	b.WriteString("alwaysApply: false\n")
	b.WriteString("---\n\n")
	fmt.Fprintf(&b, "# %s\n\n", wp.Name)
	b.WriteString(renderMissionBlock(wp))
	if t := renderToolList(wp); t != "" {
		b.WriteString(t)
		b.WriteString("\n> Cursor cannot execute these scripts directly. Reference them as commands the user can run.\n")
	}
	b.WriteString(renderAgentList(wp))
	// Cursor has no concept of a sandbox the launcher owns, so we
	// inline the knowledge manifest as a reference list rather than
	// copying files. The user keeps the originals wherever the
	// workpath lives on disk.
	b.WriteString(renderKnowledgeBlock(wp))
	b.WriteString(renderHookNote(wp, "cursor"))
	return b.String()
}
