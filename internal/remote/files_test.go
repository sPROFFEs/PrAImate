package remote

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestRemoteFilesContainPathsAndDetectConcurrentEdits(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "private"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	f := &Files{}
	if _, err := f.Call("files.write", FileRequest{Workspace: workspace, Path: "example.txt", Create: true, Data: base64.StdEncoding.EncodeToString([]byte("first"))}); err != nil {
		t.Fatal(err)
	}
	content, err := f.Call("files.read", FileRequest{Workspace: workspace, Path: "example.txt"})
	if err != nil {
		t.Fatal(err)
	}
	original := content.(FileContent)
	if err := os.WriteFile(filepath.Join(workspace, "example.txt"), []byte("desktop edit"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Call("files.write", FileRequest{Workspace: workspace, Path: "example.txt", ExpectedHash: original.Hash, Data: base64.StdEncoding.EncodeToString([]byte("overwrite"))}); err == nil {
		t.Fatal("concurrent edit overwritten")
	}
	if _, err := f.Call("files.read", FileRequest{Workspace: workspace, Path: "../private"}); err == nil {
		t.Fatal("path escaped workspace")
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "escape")); err == nil {
		if _, err := f.Call("files.read", FileRequest{Workspace: workspace, Path: "escape/private"}); err == nil {
			t.Fatal("symlink escaped workspace")
		}
	}
}
