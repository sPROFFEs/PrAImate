package core

import (
	"bytes"
	"context"
	"encoding/base64"
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

func nativeTestImage(t *testing.T, path string) []byte {
	t.Helper()
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestNativeImagesValidateContentAndBounds(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "image.without-extension")
	data := nativeTestImage(t, path)
	images, err := nativeAttachmentImages([]string{path})
	if err != nil || len(images) != 1 {
		t.Fatalf("%+v %v", images, err)
	}
	if images[0].Width != 2 || images[0].Height != 3 || images[0].DataURL != "data:image/png;base64,"+base64.StdEncoding.EncodeToString(data) {
		t.Fatal("image bytes/dimensions changed")
	}
	if _, err := nativeAttachmentImages([]string{path, path, path, path, path}); err == nil {
		t.Fatal("accepted >4 images")
	}
	if _, err := nativeAttachmentImages([]string{root}); err == nil {
		t.Fatal("accepted directory")
	}
	if err := os.Symlink(path, filepath.Join(root, "link")); err == nil {
		if _, err := nativeAttachmentImages([]string{filepath.Join(root, "link")}); err == nil {
			t.Fatal("accepted symlink")
		}
	}
	for _, name := range []string{"invalid.png", "vector.svg", "unsupported.webp"} {
		file := filepath.Join(root, name)
		if err := os.WriteFile(file, []byte("not a supported image"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := nativeAttachmentImages([]string{file}); err == nil {
			t.Fatalf("accepted %s", name)
		}
	}
	textPath := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(textPath, []byte("notes"), 0600); err != nil {
		t.Fatal(err)
	}
	if images, err := nativeAttachmentImages([]string{textPath}); err != nil || len(images) != 0 {
		t.Fatal("text was treated as an image")
	}
	large := filepath.Join(root, "large.png")
	f, err := os.Create(large)
	if err != nil {
		t.Fatal(err)
	}
	err = f.Truncate(nativeMaxImageBytes + 1)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nativeAttachmentImages([]string{large}); err == nil || !strings.Contains(err.Error(), "8 MiB") {
		t.Fatalf("accepted oversized image: %v", err)
	}
}

type nativeVisionRequest struct {
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
		Images  json.RawMessage `json:"images"`
	} `json:"messages"`
}

func nativeWireImageURLs(t *testing.T, r *http.Request) []string {
	t.Helper()
	var request nativeVisionRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		t.Fatal(err)
	}
	var urls []string
	for _, message := range request.Messages {
		if message.Images != nil {
			t.Fatal("checkpoint image metadata leaked into provider protocol")
		}
		if len(message.Content) == 0 || message.Content[0] != '[' {
			continue
		}
		if message.Role != "user" {
			t.Fatal("images outside user message")
		}
		var parts []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			ImageURL struct {
				URL string `json:"url"`
			} `json:"image_url"`
		}
		if err := json.Unmarshal(message.Content, &parts); err != nil {
			t.Fatal(err)
		}
		if len(parts) == 0 || parts[0].Type != "text" {
			t.Fatal("missing user text part")
		}
		for _, part := range parts {
			if part.Type == "image_url" {
				urls = append(urls, part.ImageURL.URL)
			}
		}
	}
	return urls
}

func TestNativeChatVisionUsesSnapshotsOnResume(t *testing.T) {
	c := nativeTestCore(t)
	path := filepath.Join(t.TempDir(), "selected.png")
	data := nativeTestImage(t, path)
	requests := 0
	a := &nativeCLIAdapter{http: &http.Client{Transport: nativeTransport(func(r *http.Request) (*http.Response, error) {
		requests++
		urls := nativeWireImageURLs(t, r)
		if len(urls) != 1 || urls[0] != "data:image/png;base64,"+base64.StdEncoding.EncodeToString(data) {
			t.Fatal("image omitted, duplicated or reread on resume")
		}
		return nativeHTTP(nativeSSE("picture described", "stop")), nil
	})}}
	installNativeTestAdapter(t, a)
	chat, err := c.CreateChat(context.Background(), CreateChatRequest{CLIAgent: "praimate-cli", WorkspacePath: t.TempDir(), Settings: ChatSettings{Local: &ChatLocalEndpoint{Endpoint: "http://local.test/v1", Model: "vision"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ContinueChatStream(context.Background(), chat.ID, "Describe", chat.WorkspacePath, "rules", []string{path}, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed after send"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ContinueChatStream(context.Background(), chat.ID, "Anything else?", chat.WorkspacePath, "rules", nil, nil); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("requests=%d", requests)
	}
}

func TestNativeManagedVisionHasNoNativeTools(t *testing.T) {
	c := nativeTestCore(t)
	agent := autonomousTestAgent("vision-managed", "praimate-cli")
	saveAutonomousRuntime(t, agent.ID)
	if _, err := c.upsertAgent(context.Background(), agent); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "selected.png")
	nativeTestImage(t, path)
	requests := 0
	a := &nativeCLIAdapter{http: &http.Client{Transport: nativeTransport(func(r *http.Request) (*http.Response, error) {
		requests++
		var raw bytes.Buffer
		if _, err := raw.ReadFrom(r.Body); err != nil {
			t.Fatal(err)
		}
		var req struct {
			Tools []nativeTool `json:"tools"`
		}
		if err := json.Unmarshal(raw.Bytes(), &req); err != nil || len(req.Tools) != 0 {
			t.Fatal("managed model received native tools")
		}
		r.Body = io.NopCloser(bytes.NewReader(raw.Bytes()))
		if urls := nativeWireImageURLs(t, r); len(urls) != 1 {
			t.Fatal("managed model did not receive selected image")
		}
		if requests == 1 {
			return nativeHTTP(nativeUsageSSE(`{"action":"tool","tool":"memory.note","arguments":{"content":"image loaded"}}`, 2400, 20)), nil
		}
		return nativeHTTP(nativeUsageSSE(`{"action":"finish","message":"Picture reviewed."}`, 2400, 20)), nil
	})}}
	installNativeTestAdapter(t, a)
	chat, err := c.StartInteractiveChat(context.Background(), agent.ID, "praimate-cli", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.UpdateChatSettings(context.Background(), chat.ID, func(s *ChatSettings) {
		s.Local = &ChatLocalEndpoint{Endpoint: "http://local.test/v1", Model: "vision", ContextTokens: 16384, OutputTokens: 1024}
	}); err != nil {
		t.Fatal(err)
	}
	usageSeen := false
	turn, err := c.ContinueChatStream(context.Background(), chat.ID, "Describe image", chat.WorkspacePath, agent.Instructions, []string{path}, func(e StreamEvent) {
		if e.Type == "usage" && e.Raw["last_usage"] != nil {
			usageSeen = true
		}
	})
	if err != nil || turn.Reply != "Picture reviewed." || !usageSeen || requests != 2 {
		t.Fatalf("turn=%+v usage=%v err=%v", turn, usageSeen, err)
	}
	status, err := c.NativeContext(context.Background(), chat.ID)
	if err != nil || status.LastUsage == nil || status.LastUsage.PromptTokens != 2400 {
		t.Fatalf("managed context not associated with chat: %+v %v", status, err)
	}
}
