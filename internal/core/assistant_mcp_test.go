package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAssistantMCPInspectionUsesRealHandshake(t *testing.T) {
	methods := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("saved auth missing")
		}
		var request struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		methods = append(methods, request.Method)
		if request.ID == 0 {
			w.WriteHeader(202)
			return
		}
		var result any = map[string]any{"protocolVersion": managedMCPProtocolVersion, "capabilities": map[string]any{}, "serverInfo": map[string]string{"name": "fixture", "version": "1"}}
		if request.Method == "tools/list" {
			result = map[string]any{"tools": []map[string]string{{"name": "echo", "description": "Echo a scoped value"}}}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
	defer server.Close()
	c := newMemCore(t)
	_, err := c.ConnectMCP(context.Background(), ConnectMCPRequest{ID: "test", Name: "Test", Transport: MCPTransportHTTP, URL: server.URL, Auth: map[string]string{"header": "Authorization", "token": "fixture-token"}})
	if err != nil {
		t.Fatal(err)
	}
	probe, err := c.InspectApplicationMCP(context.Background(), "test")
	if err != nil || !probe.OK || len(probe.Tools) != 1 || probe.Tools[0].Name != "echo" {
		t.Fatalf("probe %+v err %v methods %v", probe, err, methods)
	}
}
