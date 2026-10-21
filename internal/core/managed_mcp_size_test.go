package core

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestManagedStdioMCPLargeCatalogAndResult(t *testing.T) {
	if os.Getenv("PRAIMATE_MCP_LARGE_HELPER") == "1" {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			var req mcpRequest
			if json.Unmarshal(scanner.Bytes(), &req) != nil || req.ID == 0 {
				continue
			}
			var result any = map[string]any{"protocolVersion": managedMCPProtocolVersion, "capabilities": map[string]any{}, "serverInfo": map[string]string{"name": "large-catalog-fixture", "version": "1"}}
			if req.Method == "tools/list" {
				if os.Getenv("PRAIMATE_MCP_LARGE_MODE") == "wait" {
					continue
				}
				tools := make([]managedMCPTool, 200)
				for i := range tools {
					tools[i] = managedMCPTool{Name: fmt.Sprintf("fixture_%d", i), Description: strings.Repeat("catalogue description ", 150), InputSchema: json.RawMessage(`{"type":"object"}`)}
				}
				result = map[string]any{"tools": tools}
			} else if req.Method == "tools/call" {
				result = map[string]any{"content": []map[string]string{{"type": "text", "text": strings.Repeat("result ", 50000)}}}
			}
			_ = writeMCPLine(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		}
		os.Exit(0)
	}
	server := MCPServer{ID: "large-catalog", Transport: MCPTransportStdio, Command: os.Args[0], Args: []string{"-test.run=^TestManagedStdioMCPLargeCatalogAndResult$"}, Env: map[string]string{"PRAIMATE_MCP_LARGE_HELPER": "1"}}
	client, err := newManagedMCPClient(context.Background(), t.TempDir(), server)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatalf("large tool catalogue failed: %v", err)
	}
	if len(tools) != 200 || tools[199].Name != "fixture_199" {
		t.Fatal("tool catalogue was truncated")
	}
	result, err := client.CallTool(context.Background(), tools[199].Name, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "output truncated by PrAImate") || len(result) > managedOutputLimit+100 {
		t.Fatal("model-facing output bound was lost")
	}
}

func TestMCPLineEnforcesProtocolLimit(t *testing.T) {
	for _, size := range []int{managedMCPMessageLimit, managedMCPMessageLimit + 1} {
		reader := bufio.NewReader(strings.NewReader(strings.Repeat("x", size) + "\n"))
		line, err := readMCPLine(context.Background(), reader)
		if size == managedMCPMessageLimit {
			if err != nil || len(line) != size {
				t.Fatalf("valid boundary failed: %v", err)
			}
		} else if !errors.Is(err, errMCPMessageTooLarge) {
			t.Fatalf("oversize message did not give bounded error: %v", err)
		}
	}
}

func TestManagedNetworkMCPLargeCatalog(t *testing.T) {
	for _, mode := range []string{"json", "http-events", "legacy-sse", "legacy-oversize"} {
		t.Run(mode, func(t *testing.T) {
			events := make(chan []byte, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprint(w, "event: endpoint\ndata: /messages\n\n")
					w.(http.Flusher).Flush()
					for {
						select {
						case body := <-events:
							_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", body)
							w.(http.Flusher).Flush()
						case <-r.Context().Done():
							return
						}
					}
				}
				var req mcpRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				if req.ID == 0 {
					w.WriteHeader(http.StatusAccepted)
					return
				}
				result := map[string]any{"protocolVersion": managedMCPProtocolVersion, "capabilities": map[string]any{}, "serverInfo": map[string]string{"name": "fixture", "version": "1"}}
				if req.Method == "tools/list" {
					result = map[string]any{"tools": []managedMCPTool{{Name: "large", Description: strings.Repeat("metadata ", 60000), InputSchema: json.RawMessage(`{"type":"object"}`)}}}
					if mode == "legacy-oversize" {
						result = map[string]any{"tools": []managedMCPTool{{Name: "oversize", Description: strings.Repeat("x", managedMCPMessageLimit+1)}}}
					}
				}
				body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
				switch mode {
				case "json":
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(body)
				case "http-events":
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "data: %s\n\n", body)
				case "legacy-sse", "legacy-oversize":
					w.WriteHeader(http.StatusAccepted)
					events <- body
				}
			}))
			defer server.Close()
			transport := MCPTransportHTTP
			if strings.HasPrefix(mode, "legacy-") {
				transport = MCPTransportSSE
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			client, err := newManagedMCPClient(ctx, "", MCPServer{ID: "large", Transport: transport, URL: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			tools, err := client.ListTools(ctx)
			if mode == "legacy-oversize" {
				if !errors.Is(err, errMCPMessageTooLarge) || ctx.Err() != nil {
					t.Fatalf("oversize SSE response hid its error behind a timeout: %v", err)
				}
				return
			}
			if err != nil || len(tools) != 1 {
				t.Fatalf("large %s catalogue: count=%d error=%v", mode, len(tools), err)
			}
		})
	}
}

func TestMCPHTTPResponsesRejectOversizeMessages(t *testing.T) {
	for _, kind := range []string{"application/json", "text/event-stream"} {
		body := strings.Repeat("x", managedMCPMessageLimit+1)
		if kind == "text/event-stream" {
			body = "data: " + body + "\n\n"
		}
		response := &http.Response{Header: http.Header{"Content-Type": []string{kind}}, Body: io.NopCloser(strings.NewReader(body))}
		if _, err := decodeMCPHTTPResponse(response); !errors.Is(err, errMCPMessageTooLarge) {
			t.Fatalf("%s oversize result: %v", kind, err)
		}
	}
}

func TestStdioMCPReadCancellationStopsProcess(t *testing.T) {
	server := MCPServer{ID: "cancel-read", Transport: MCPTransportStdio, Command: os.Args[0], Args: []string{"-test.run=^TestManagedStdioMCPLargeCatalogAndResult$"}, Env: map[string]string{"PRAIMATE_MCP_LARGE_HELPER": "1", "PRAIMATE_MCP_LARGE_MODE": "wait"}}
	client, err := newManagedMCPClient(context.Background(), t.TempDir(), server)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := client.ListTools(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled read returned %v", err)
	}
	rpc := client.(*protocolMCPClient).rpc.(*stdioMCPRPC)
	if rpc.cmd.ProcessState == nil {
		t.Fatal("cancelled read retained a live MCP process")
	}
	if _, err := client.ListTools(context.Background()); err == nil {
		t.Fatal("cancelled transport was reused")
	}
}

func TestMCPLineAcceptsMessagesBeyondReaderBuffer(t *testing.T) {
	for _, size := range []int{4096, 128 << 10, 1 << 20} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			want := strings.Repeat("x", size)
			reader := bufio.NewReaderSize(strings.NewReader(want+"\nnext\n"), 32)
			got, err := readMCPLine(context.Background(), reader)
			if err != nil || string(got) != want {
				t.Fatalf("failed reading %d-byte message: %v", size, err)
			}
			got, err = readMCPLine(context.Background(), reader)
			if err != nil || string(got) != "next" {
				t.Fatalf("message framing was lost: %q %v", got, err)
			}
		})
	}
}
