package cloudflared

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type managedOutput struct {
	mu   sync.Mutex
	body bytes.Buffer
}

func (w *managedOutput) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.Write(data)
}
func (w *managedOutput) String() string { w.mu.Lock(); defer w.mu.Unlock(); return w.body.String() }
func TestManagedConnectorStopsWithServerWithoutExposingTokenOnCommandLine(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	root := t.TempDir()
	config, _ := json.Marshal(Configuration{PublicURL: "https://fixture.example", Token: "synthetic-token-with-at-least-32-characters"})
	m := New(root, func(context.Context) ([]byte, error) { return config, nil }, func(context.Context, []byte) error { return nil })
	if err := os.WriteFile(m.binary(), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\nexec sleep 60\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := &managedOutput{}
	stop, err := m.StartManaged(ctx, output)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	deadline := time.After(3 * time.Second)
	for !strings.Contains(output.String(), "--token-file") {
		select {
		case <-deadline:
			t.Fatal("connector did not start")
		case <-time.After(time.Millisecond):
		}
	}
	if strings.Contains(output.String(), "synthetic-token-with-at-least-32-characters") {
		t.Fatal("connector credential exposed in arguments")
	}
	if err := m.Control(ctx, "install"); err == nil {
		t.Fatal("independent connector service can overlap the managed runtime")
	}
	stop()
	m.mu.Lock()
	running := m.managedCancel != nil
	m.mu.Unlock()
	if running {
		t.Fatal("connector survived server shutdown")
	}
	if _, err := os.Stat(filepath.Join(root, ".gitignore")); err != nil {
		t.Fatal("managed connector credential directory is not excluded from backup")
	}
}
