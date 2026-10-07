package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Burp's legacy transport uses the same URL for GET and POST, but POST
// requires the sessionId provided by the GET endpoint event.
func burpMCPFixture(t *testing.T, rejectedStatus int) MCPServer {
	t.Helper()
	type session struct {
		events      chan string
		initialized atomic.Bool
	}
	var sessions sync.Map
	var nextID atomic.Int64
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-MCP-Test") != "fixture-auth" {
			http.Error(w, "fixture authentication required", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodGet {
			if r.Header.Get("Accept") != "text/event-stream" {
				http.Error(w, "expected SSE", http.StatusNotAcceptable)
				return
			}
			id := strconv.FormatInt(nextID.Add(1), 10)
			current := &session{events: make(chan string, 8)}
			sessions.Store(id, current)
			defer sessions.Delete(id)
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "event: endpoint\ndata: ?sessionId=%s\n\n", id)
			w.(http.Flusher).Flush()
			for {
				select {
				case event := <-current.events:
					fmt.Fprintf(w, "event: message\ndata: %s\n\n", event)
					w.(http.Flusher).Flush()
				case <-r.Context().Done():
					return
				}
			}
		}
		id := r.URL.Query().Get("sessionId")
		if id == "" {
			http.Error(w, "sessionId query parameter is not provided", rejectedStatus)
			return
		}
		value, ok := sessions.Load(id)
		if !ok {
			http.Error(w, "unknown session", http.StatusNotFound)
			return
		}
		current := value.(*session)
		var req mcpRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if req.Method == "notifications/initialized" {
			current.initialized.Store(true)
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "Burp fixture", "version": "1"}}
		case "tools/list":
			if !current.initialized.Load() {
				http.Error(w, "not initialized", http.StatusBadRequest)
				return
			}
			result = map[string]any{"tools": []map[string]any{{"name": "get_proxy_history", "inputSchema": map[string]any{"type": "object"}}}}
		case "tools/call":
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": "burp-tool-ok"}}}
		}
		w.WriteHeader(http.StatusAccepted)
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		current.events <- string(raw)
	}))
	t.Cleanup(s.Close)
	return MCPServer{ID: "burp-fixture", Name: "Burp", Transport: MCPTransportHTTP, URL: s.URL, Auth: map[string]string{"header": "X-MCP-Test", "token": "fixture-auth"}}
}

func TestManagedHTTPNegotiatesLegacySSESession(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusNotFound, http.StatusMethodNotAllowed} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			server := burpMCPFixture(t, status)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			client, err := newManagedMCPClient(ctx, t.TempDir(), server)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			tools, err := client.ListTools(ctx)
			if err != nil || len(tools) != 1 || tools[0].Name != "get_proxy_history" {
				t.Fatalf("tools=%+v err=%v", tools, err)
			}
			result, err := client.CallTool(ctx, tools[0].Name, json.RawMessage(`{}`))
			if err != nil || result != "burp-tool-ok" {
				t.Fatalf("tool result=%q err=%v", result, err)
			}
		})
	}
}

func TestManagedHTTPPreservesModernAndAuthenticationErrors(t *testing.T) {
	for _, status := range []int{200, 401, 403, 500} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var gets atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					gets.Add(1)
					http.Error(w, "must not fall back", 500)
					return
				}
				if status != 200 {
					http.Error(w, "fixture rejection", status)
					return
				}
				var req mcpRequest
				json.NewDecoder(r.Body).Decode(&req)
				if req.Method == "notifications/initialized" {
					w.WriteHeader(http.StatusAccepted)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"protocolVersion": managedMCPProtocolVersion}})
			}))
			defer s.Close()
			client, err := newManagedMCPClient(context.Background(), t.TempDir(), MCPServer{Transport: MCPTransportHTTP, URL: s.URL})
			if client != nil {
				defer client.Close()
			}
			if (err != nil) != (status != 200) || gets.Load() != 0 {
				t.Fatalf("status=%d err=%v GET requests=%d", status, err, gets.Load())
			}
		})
	}
}

func TestMCPProbeUsesRealHandshakeAndLegacyNegotiation(t *testing.T) {
	c := nativeTestCore(t)
	ctx := context.Background()
	server := burpMCPFixture(t, http.StatusBadRequest)
	if _, err := c.ConnectMCP(ctx, ConnectMCPRequest{ID: server.ID, Name: server.Name, Transport: server.Transport, URL: server.URL, Auth: server.Auth}); err != nil {
		t.Fatal(err)
	}
	result, err := c.ProbeMCPServer(ctx, server.ID)
	if err != nil || !result.OK || len(result.Tools) != 1 || result.Tools[0].Name != "get_proxy_history" {
		t.Fatalf("probe=%+v err=%v", result, err)
	}
	// A reachable web page is not a functioning MCP server.
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "<html>reachable</html>") }))
	defer web.Close()
	if _, err := c.ConnectMCP(ctx, ConnectMCPRequest{ID: "not-mcp", Name: "Web page", Transport: MCPTransportHTTP, URL: web.URL}); err != nil {
		t.Fatal(err)
	}
	result, err = c.ProbeMCPServer(ctx, "not-mcp")
	if err != nil || result.OK || result.Error == "" {
		t.Fatalf("non-MCP probe=%+v err=%v", result, err)
	}
}
