// Package targets defines per-CLI-agent output formats and the registry that
// lets the wpc CLI list and dispatch them.
//
// Each Target reads a *workpath.Workpath and writes files under an output
// directory. Targets MUST be idempotent: calling Compile twice into the same
// outDir produces the same tree.
package targets

import (
	"fmt"
	"sort"

	"github.com/sPROFFEs/PrAImate/pkg/workpath"
)

// Target is the contract for a per-CLI compiler.
type Target interface {
	Name() string
	Description() string
	Compile(wp *workpath.Workpath, outDir string) error
}

var registry = map[string]Target{}

// Register adds a target to the global registry. Panics on duplicate name —
// targets are package-level singletons.
func Register(t Target) {
	if _, dup := registry[t.Name()]; dup {
		panic("targets: duplicate target " + t.Name())
	}
	registry[t.Name()] = t
}

// Get returns the named target or an error listing available ones.
func Get(name string) (Target, error) {
	t, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown target %q; available: %v", name, Names())
	}
	return t, nil
}

// All returns every registered target, sorted by name.
func All() []Target {
	out := make([]Target, 0, len(registry))
	for _, t := range registry {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// Names returns the sorted list of registered target names.
func Names() []string {
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func init() {
	Register(&claudeTarget{})
	Register(&mikaTarget{})
	Register(&cursorTarget{})
	Register(&codexTarget{})
	Register(&genericTarget{})
}
