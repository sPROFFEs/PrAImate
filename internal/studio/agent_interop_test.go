package studio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sPROFFEs/PrAImate/internal/core"
)

func TestStudioMarkdownImportExportAndOfflineIndex(t *testing.T) {
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	s, _ := fixture(t)
	source := filepath.Join(t.TempDir(), "review.md")
	if err := os.WriteFile(source, []byte("---\nmodel: local/test\npermission:\n  bash: deny\n---\nReview code."), 0o600); err != nil {
		t.Fatal(err)
	}
	imported := rpcOK(t, s, "agents.import", map[string]string{"path": source}).(*core.Agent)
	if imported.Foreign["model"] != "local/test" {
		t.Fatal("external model lost")
	}
	dest := filepath.Join(t.TempDir(), "review.md")
	rpcOK(t, s, "agents.export", map[string]string{"id": imported.ID, "path": dest})
	raw, _ := os.ReadFile(dest)
	if !strings.Contains(string(raw), "permission:") {
		t.Fatalf("export: %s", raw)
	}
	dir, err := core.AgentKnowledgeDir(imported.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "guide.md"), []byte("# Review\nAlways check source citations."), 0o600); err != nil {
		t.Fatal(err)
	}
	result := rpcOK(t, s, "agents.knowledge.index", map[string]string{"id": imported.ID}).(map[string]any)
	if result["documents"] != 1 || result["chunks"] != 1 {
		t.Fatalf("index: %#v", result)
	}
	if response := s.dispatch(RPCRequest{Method: "agents.import", Params: map[string]string{"path": "unreviewed.zip"}}); response.Error == nil {
		t.Fatal("pack review bypassed")
	}
}
