package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sPROFFEs/PrAImate/internal/core"
)

func authenticatedMCPFixture(t *testing.T, extra bool) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-token" || (extra && r.Header.Get("X-Fixture-Key") != "fixture-key") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(400)
			return
		}
		if len(req.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any = map[string]any{}
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "fixture", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "fixture_tool", "description": "fixture", "inputSchema": map[string]any{"type": "object"}}}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	t.Cleanup(server.Close)
	return server
}

func TestMCPProbeUsesAuthorizationAndProtocolHandshake(t *testing.T) {
	parent, _, _, _ := documentFixture(t)
	server := authenticatedMCPFixture(t, false)
	connected, err := parent.core.ConnectMCP(parent.ctx, core.ConnectMCPRequest{ID: "auth-test", Name: "Auth fixture", Transport: core.MCPTransportHTTP, URL: server.URL, Auth: map[string]string{"header": "Authorization", "token": "fixture-token"}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := parent.TestMCPServer(connected.ID)
	if err != nil || !result.OK || len(result.Tools) != 1 {
		t.Fatalf("authenticated probe failed: %+v %v", result, err)
	}
}

func TestMCPJSONImportPreservesMultipleHeadersForProbeAndRuntime(t *testing.T) {
	parent, _, _, _ := documentFixture(t)
	server := authenticatedMCPFixture(t, true)
	for _, wrapped := range []bool{true, false} {
		definition := map[string]any{"fixture-auth": map[string]any{"url": server.URL, "type": "http", "headers": map[string]string{"Authorization": "Bearer fixture-token", "X-Fixture-Key": "fixture-key"}}}
		var data any = definition
		if wrapped {
			data = map[string]any{"mcpServers": definition}
		}
		body, _ := json.Marshal(data)
		count, err := parent.ImportMCPServersJSON(string(body))
		if err != nil || count != 1 {
			t.Fatalf("import: %d %v", count, err)
		}
		result, err := parent.TestMCPServer("fixture-auth")
		if err != nil || !result.OK || len(result.Tools) != 1 {
			t.Fatalf("headers lost: %+v %v", result, err)
		}
	}
}

func TestMCPImportRejectsHeaderInjectionWithoutExposingToken(t *testing.T) {
	parent, _, _, _ := documentFixture(t)
	count, err := parent.ImportMCPServersJSON(`{"mcpServers":{"bad":{"url":"http://127.0.0.1:1","headers":{"Authorization":"Bearer fixture-secret\r\nX-Injected: value"}}}}`)
	if err == nil || count != 0 {
		t.Fatalf("unsafe headers accepted: %d %v", count, err)
	}
	if strings.Contains(err.Error(), "fixture-secret") {
		t.Fatal("header value leaked in validation error")
	}
}
