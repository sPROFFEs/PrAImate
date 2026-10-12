package orchestrator

import (
	"testing"

	"github.com/sPROFFEs/PrAImate/internal/core"
)

func TestCLIResolverHonorsWorkspaceEdits(t *testing.T) {
	for _, adapter := range []core.CLIAdapter{core.NewOpenCodeAdapter(), core.NewPraimateCodeAdapter()} {
		t.Run(adapter.Name(), func(t *testing.T) {
			previous, _ := core.GetCLIAdapter(adapter.Name())
			core.RegisterCLIAdapter(adapter)
			defer func() {
				if previous != nil {
					core.RegisterCLIAdapter(previous)
				} else {
					core.UnregisterCLIAdapter(adapter.Name())
				}
			}()
			for _, edits := range []bool{false, true} {
				worker, err := ResolveRuntime(Profile{Runtime: "cli", CLI: adapter.Name(), AllowEdits: edits})
				if err != nil {
					t.Fatal(err)
				}
				cap := worker.Capabilities()
				if cap.CanEdit != edits || cap.ReadOnly == edits {
					t.Fatalf("edits=%t capabilities=%+v", edits, cap)
				}
			}
		})
	}
}
