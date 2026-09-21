package studio

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStageAttachmentsCopiesIntoManagedStorageWithoutClobbering(t *testing.T) {
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	sourceDir := t.TempDir()
	source := filepath.Join(sourceDir, "image.png")
	if err := os.WriteFile(source, []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := stageAttachments("chat-1", []string{source})
	if err != nil {
		t.Fatal(err)
	}
	second, err := stageAttachments("chat-1", []string{source})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || len(second) != 1 || first[0].Path == second[0].Path || !first[0].Image {
		t.Fatalf("unexpected staged files: %+v %+v", first, second)
	}
	if raw, err := os.ReadFile(first[0].Path); err != nil || string(raw) != "png" {
		t.Fatalf("staged content = %q, %v", raw, err)
	}
}

func TestStageAttachmentsRejectsNonRegularSourcesAndUnsafeChatIDs(t *testing.T) {
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	if _, err := stageAttachments("../chat", nil); err == nil {
		t.Fatal("unsafe chat ID was accepted")
	}
	if _, err := stageAttachments("chat", []string{t.TempDir()}); err == nil {
		t.Fatal("directory attachment was accepted")
	}
}
