package launcher

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/sPROFFEs/PrAImate/internal/installer"
	"github.com/sPROFFEs/PrAImate/pkg/workpath"
)

// missingImportedToolNotes surfaces a one-line hint when a workpath
// imports a `_common/<bundle>` whose underlying tool isn't reachable
// from PATH or the PrAImate-managed prefix. Each item in returned slice
// is rendered by the TUI's launching screen on the next launch so the
// user sees the one command to fix it.
//
// The bundle → tool name mapping is by convention: a bundle named
// `_common/<name>` wraps the tool whose installer.ToolID is also
// `<name>`. This keeps the settings Bundles toggle and Tools installer
// loosely coupled: adding a known tool plus a matching _common bundle
// is enough for launch-time missing-tool hints.
//
// Best-effort: probes are cheap (exec.LookPath + os.Stat), so this
// adds at most a few ms per launch. Returns nil when every imported
// bundle's tool is available, or when no imported bundles reference
// a known tool.
func missingImportedToolNotes(wp *workpath.Workpath) []string {
	if wp == nil {
		return nil
	}
	bundles := map[string]bool{}
	collect := func(p string) {
		if p == "" {
			return
		}
		bundles[filepath.Base(p)] = true
	}
	for _, t := range wp.Tools {
		collect(t.ImportedFrom)
	}
	for _, a := range wp.Agents {
		collect(a.ImportedFrom)
	}
	for _, k := range wp.Knowledge {
		collect(k.ImportedFrom)
	}
	for _, h := range wp.Hooks {
		collect(h.ImportedFrom)
	}
	if len(bundles) == 0 {
		return nil
	}

	known := map[string]installer.Tool{}
	for _, t := range installer.KnownTools() {
		known[string(t.ID)] = t
	}

	var notes []string
	for bundle := range bundles {
		tool, ok := known[bundle]
		if !ok {
			continue
		}
		if toolReachable(tool) {
			continue
		}
		notes = append(notes, fmt.Sprintf(
			"Imported bundle %q needs the %s tool which isn't reachable. "+
				"Install: `praimate -install-tool %s`",
			bundle, tool.ID, tool.ID))
	}
	return notes
}

// toolReachable reports whether the named tool is callable, by PATH
// lookup OR by direct stat in the PrAImate-managed prefix bin dir.
func toolReachable(t installer.Tool) bool {
	if _, err := exec.LookPath(t.Binary); err == nil {
		return true
	}
	binDir, err := installer.ManagedToolBinDir(string(t.ID))
	if err != nil {
		return false
	}
	candidates := []string{filepath.Join(binDir, t.Binary)}
	if runtime.GOOS == "windows" {
		for _, ext := range []string{".exe", ".cmd", ".bat"} {
			candidates = append(candidates, filepath.Join(binDir, t.Binary+ext))
		}
	}
	for _, cand := range candidates {
		if st, err := os.Stat(cand); err == nil && !st.IsDir() {
			return true
		}
	}
	return false
}
