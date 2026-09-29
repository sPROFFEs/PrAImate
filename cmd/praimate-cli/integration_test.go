package main

import (
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type cliTestTransport func(*http.Request) (*http.Response, error)

func (f cliTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNativeCLIImageOnlyJSONUsesSharedCore(t *testing.T) {
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "")
	root := t.TempDir()
	path := filepath.Join(root, "screen shot.png")
	picture, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	err = png.Encode(picture, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	picture.Close()
	if err != nil {
		t.Fatal(err)
	}
	input, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if _, err := io.WriteString(input, "test-only-cli-password\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := input.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	output, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	oldIn, oldOut, oldTransport := os.Stdin, os.Stdout, http.DefaultTransport
	os.Stdin, os.Stdout = input, output
	defer func() { os.Stdin, os.Stdout, http.DefaultTransport = oldIn, oldOut, oldTransport }()
	requests, metadataRequests := 0, 0
	http.DefaultTransport = cliTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet && r.URL.Host == "cli.fixture" && r.URL.Path == "/api/ps" {
			metadataRequests++
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"models":[{"name":"vision","context_length":32768}]}`))}, nil
		}
		requests++
		if r.URL.Host != "cli.fixture" || r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected route: %s", r.URL)
		}
		var req struct {
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		last := string(req.Messages[len(req.Messages)-1].Content)
		if !strings.Contains(last, `"type":"image_url"`) || !strings.Contains(last, "data:image/png;base64,") {
			t.Fatal("--attach image never reached native core provider")
		}
		body := "data: {\"choices\":[{\"delta\":{\"content\":\"Visible image.\"},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":1900,\"completion_tokens\":8,\"total_tokens\":1908}}\n\ndata: [DONE]\n\n"
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	if err := run(context.Background(), []string{"--db-password-stdin", "--format", "json", "--endpoint", "http://cli.fixture/v1", "--model", "vision", "--cwd", root, "--attach", path}); err != nil {
		t.Fatal(err)
	}
	if requests != 1 || metadataRequests != 1 {
		t.Fatalf("requests=%d metadataRequests=%d", requests, metadataRequests)
	}
	if _, err := output.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(output)
	types := map[string]map[string]any{}
	for {
		var event map[string]any
		if err := decoder.Decode(&event); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("stdout is not pure JSONL: %v", err)
		}
		types[event["type"].(string)] = event
	}
	if types["session"]["chatID"] == nil || types["usage"]["raw"] == nil || types["context"]["raw"] == nil || types["text"]["text"] != "Visible image." {
		t.Fatalf("missing core events: %v", types)
	}
}
