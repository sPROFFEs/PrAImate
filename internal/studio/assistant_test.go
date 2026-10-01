package studio

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"
)

func TestAssistantBridgeAuthenticationAndSharedCaptureCleanup(t *testing.T) {
	s, _ := fixture(t)
	if rpcOK(t, s, "assistant.available", nil) != false {
		t.Fatal("standalone assistant reported available")
	}
	ended := make(chan string, 1)
	calls := 0
	s.assistant = &AssistantHooks{Call: func(ctx context.Context, method string, raw json.RawMessage) (any, error) {
		calls++
		if method == "voice.begin" {
			return map[string]any{"id": "owned-lease", "native": true}, nil
		}
		if method == "voice.end" {
			var p struct {
				ID string `json:"id"`
			}
			json.Unmarshal(raw, &p)
			ended <- p.ID
		}
		return true, nil
	}}
	child := s.newConnectionServer()
	child.token = "fixture"
	denied := child.dispatch(RPCRequest{JSONRPC: "2.0", ID: 1, Method: "assistant.config"})
	if denied.Error == nil || calls != 0 {
		t.Fatal("unauthenticated assistant call reached desktop")
	}
	child.authenticated = true
	if rpcOK(t, child, "assistant.available", nil) != true {
		t.Fatal("child did not share desktop operator")
	}
	rpcOK(t, child, "voice.begin", nil)
	if err := child.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-ended:
		if id != "owned-lease" {
			t.Fatal(id)
		}
	case <-time.After(time.Second):
		t.Fatal("disconnect left microphone running")
	}
}

func TestAssistantPendingModelDoesNotBlockCancel(t *testing.T) {
	s, _ := fixture(t)
	started := make(chan struct{})
	release := make(chan struct{})
	s.assistant = &AssistantHooks{Call: func(ctx context.Context, method string, _ json.RawMessage) (any, error) {
		if method == "assistant.send" {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if method == "assistant.cancel" {
			close(release)
		}
		return true, nil
	}}
	server, client := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- s.ServeStdio(server, server) }()
	t.Cleanup(func() { client.Close(); server.Close(); <-done })
	client.SetDeadline(time.Now().Add(3 * time.Second))
	encoder, decoder := json.NewEncoder(client), json.NewDecoder(client)
	encoder.Encode(RPCRequest{JSONRPC: "2.0", ID: 1, Method: "assistant.send"})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("send never started")
	}
	encoder.Encode(RPCRequest{JSONRPC: "2.0", ID: 2, Method: "assistant.cancel"})
	ids := map[float64]bool{}
	for i := 0; i < 2; i++ {
		var response RPCResponse
		if err := decoder.Decode(&response); err != nil {
			t.Fatal(err)
		}
		if response.Error != nil {
			t.Fatal(response.Error)
		}
		ids[response.ID.(float64)] = true
	}
	if !ids[1] || !ids[2] {
		t.Fatal("missing send/cancel reply")
	}
}
