package remote

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestExternalDocumentWritesReachRemoteClients(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	files := &Files{}
	defer files.Close()
	events := make(chan map[string]string, 16)
	files.Configure(ctx, func(name string, payload any) {
		if name == "praimate:editor-fs" {
			select {
			case events <- payload.(map[string]string):
			default:
			}
		}
	})
	if err := files.Watch(root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "external.txt"), []byte("CLI edit"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-events:
		if event["workspace"] != root || event["rel"] != "external.txt" {
			t.Fatalf("wrong document event: %#v", event)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("external edit was not published")
	}
}
