//go:build server

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/launcher"
	"github.com/sPROFFEs/PrAImate/internal/store"
)

type readyOutput struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	ready  chan struct{}
	once   sync.Once
}

func (w *readyOutput) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buffer.Write(data)
	if strings.Contains(w.buffer.String(), "server ready on") {
		w.once.Do(func() { close(w.ready) })
	}
	return n, err
}
func (w *readyOutput) String() string { w.mu.Lock(); defer w.mu.Unlock(); return w.buffer.String() }
func TestHeadlessServerPairsAndUsesTheRealCoreWithoutADisplay(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PRAIMATE_HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("PRAIMATE_REMOTE_LISTEN", "")
	t.Setenv("PRAIMATE_REMOTE_TOKEN", "")
	t.Setenv("PRAIMATE_DB_PASSWORD", "")
	t.Setenv("PRAIMATE_DB_PASSWORD_FILE", "")
	if err := launcher.SaveConfig(&launcher.Config{}); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(root, "db.sqlite")
	st, err := store.InitializeWithPassword(database, "test-only-database-password")
	if err != nil {
		t.Fatal(err)
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := &readyOutput{ready: make(chan struct{})}
	stderr := &readyOutput{}
	done := make(chan int, 1)
	go func() {
		args := []string{"--listen", "127.0.0.1:0", "--db", database, "--db-password-stdin", "--pair"}
		if binary := os.Getenv("PRAIMATE_TEST_SERVER_BIN"); binary != "" {
			cmd := exec.CommandContext(ctx, binary, append([]string{"--server"}, args...)...)
			cmd.Stdin = strings.NewReader("test-only-database-password\n")
			cmd.Stdout = output
			cmd.Stderr = stderr
			cmd.WaitDelay = 10 * time.Second
			cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
			err := cmd.Run()
			if err != nil && !errors.Is(err, context.Canceled) {
				done <- 1
			} else {
				done <- 0
			}
			return
		}
		done <- runHeadlessServer(ctx, args, strings.NewReader("test-only-database-password\n"), output, stderr)
	}()
	defer func() {
		cancel()
		select {
		case code := <-done:
			if code != 0 {
				t.Errorf("server exited %d: %s", code, stderr.String())
			}
		case <-time.After(15 * time.Second):
			t.Error("headless server did not shut down")
		}
	}()
	select {
	case <-output.ready:
	case <-time.After(15 * time.Second):
		t.Fatalf("server not ready: %s", stderr.String())
	}
	text := output.String()
	address := regexp.MustCompile(`server ready on ([^\s]+)`).FindStringSubmatch(text)
	code := regexp.MustCompile(`Pairing code: ([0-9]+)`).FindStringSubmatch(text)
	if len(address) != 2 || len(code) != 2 {
		t.Fatalf("missing server startup data: %s", text)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	url := "http://" + address[1]
	response, err := client.Post(url+"/v1/pair", "application/json", strings.NewReader(fmt.Sprintf(`{"code":%q,"name":"integration phone"}`, code[1])))
	if err != nil {
		t.Fatal(err)
	}
	var paired struct {
		Token string `json:"token"`
	}
	err = json.NewDecoder(response.Body).Decode(&paired)
	response.Body.Close()
	if err != nil || paired.Token == "" {
		t.Fatalf("pairing failed: %v", err)
	}
	request, _ := http.NewRequest("POST", url+"/v1/call", strings.NewReader(`{"method":"api.ListAgents","params":[]}`))
	request.Header.Set("Authorization", "Bearer "+paired.Token)
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || !bytes.Contains(body, []byte(`"result"`)) {
		t.Fatalf("core request failed: %d %s", response.StatusCode, body)
	}
	request, _ = http.NewRequest("GET", url+"/v1/methods", nil)
	request.Header.Set("Authorization", "Bearer "+paired.Token)
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if !bytes.Contains(body, []byte("api.SetChatMCPServers")) || !bytes.Contains(body, []byte("files.write")) {
		t.Fatal("full service catalog was not available")
	}
	// A real mutation is journalled before execution and remains recoverable
	// over a fresh HTTP request; this exercises the same Core used by desktop.
	id := fmt.Sprintf("%d_request123", time.Now().UnixMilli())
	raw, _ := json.Marshal(map[string]any{"method": "api.StartCleanChat", "params": []string{"praimate-cli", "", root}, "requestId": id})
	request, _ = http.NewRequest("POST", url+"/v1/call", bytes.NewReader(raw))
	request.Header.Set("Authorization", "Bearer "+paired.Token)
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 202 {
		t.Fatalf("mutation not accepted: %d", response.StatusCode)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		request, _ = http.NewRequest("GET", url+"/v1/operations/"+id, nil)
		request.Header.Set("Authorization", "Bearer "+paired.Token)
		response, err = client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		var op struct {
			Status string         `json:"status"`
			Error  string         `json:"error"`
			Result map[string]any `json:"result"`
		}
		err = json.NewDecoder(response.Body).Decode(&op)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if op.Status == "completed" {
			if op.Result["ID"] == "" || op.Result["ID"] == nil {
				t.Fatal("Core chat result missing")
			}
			break
		}
		if op.Status != "running" || time.Now().After(deadline) {
			t.Fatalf("mutation failed: %s %s", op.Status, op.Error)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
