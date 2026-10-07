package core

// Managed MCP clients keep third-party tools behind the same approval and
// output-bounding boundary as project and command tools. The model-facing
// runtime never receives MCP credentials or direct process/network access.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const managedMCPProtocolVersion = "2025-03-26"

type managedMCPTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

type managedMCPClient interface {
	ListTools(context.Context) ([]managedMCPTool, error)
	CallTool(context.Context, string, json.RawMessage) (string, error)
	Close() error
}

type managedMCPServer struct {
	server MCPServer
	client managedMCPClient
	tools  []managedMCPTool
}

type managedMCPSet struct {
	servers map[string]*managedMCPServer
}

func newManagedMCPSet(ctx context.Context, root string, servers []MCPServer) (*managedMCPSet, error) {
	set := &managedMCPSet{servers: make(map[string]*managedMCPServer, len(servers))}
	for _, server := range servers {
		connectCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		client, err := newManagedMCPClient(connectCtx, root, server)
		if err != nil {
			cancel()
			set.Close()
			return nil, fmt.Errorf("managed MCP %s: %w", server.Name, err)
		}
		tools, err := client.ListTools(connectCtx)
		cancel()
		if err != nil {
			_ = client.Close()
			set.Close()
			return nil, fmt.Errorf("managed MCP %s list tools: %w", server.Name, err)
		}
		set.servers[server.ID] = &managedMCPServer{server: server, client: client, tools: tools}
	}
	return set, nil
}

func (s *managedMCPSet) Instructions() []string {
	if s == nil {
		return nil
	}
	ids := make([]string, 0, len(s.servers))
	for id := range s.servers {
		ids = append(ids, id)
	}
	ids = sortedStrings(ids)
	var out []string
	for _, id := range ids {
		server := s.servers[id]
		for _, tool := range server.tools {
			schema := safeMCPToolSchema(tool.InputSchema)
			if schema == "" {
				schema = "{}"
			}
			if len(schema) > 1200 {
				schema = schema[:1200] + "…"
			}
			out = append(out, fmt.Sprintf(`MCP server %q exposes tool %q with input schema %s; call with {"action":"tool","tool":"mcp.call","arguments":{"server":%q,"tool":%q,"arguments":{}}}`,
				id, tool.Name, schema, id, tool.Name))
		}
	}
	return out
}

func safeMCPToolSchema(raw json.RawMessage) string {
	var schema map[string]any
	if json.Unmarshal(raw, &schema) != nil {
		return "{}"
	}
	filtered := filterMCPSchema(schema, 0)
	out, _ := json.Marshal(filtered)
	return string(out)
}

func filterMCPSchema(value any, depth int) any {
	if depth > 5 {
		return nil
	}
	switch typed := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for _, key := range []string{"type", "required", "enum", "items", "additionalProperties"} {
			if child, ok := typed[key]; ok {
				out[key] = filterMCPSchema(child, depth+1)
			}
		}
		if properties, ok := typed["properties"].(map[string]any); ok {
			filteredProperties := map[string]any{}
			names := make([]string, 0, len(properties))
			for name := range properties {
				names = append(names, name)
			}
			for _, name := range sortedStrings(names) {
				if len(filteredProperties) >= 128 {
					break
				}
				filteredProperties[name] = filterMCPSchema(properties[name], depth+1)
			}
			out["properties"] = filteredProperties
		}
		return out
	case []any:
		if len(typed) > 64 {
			typed = typed[:64]
		}
		out := make([]any, len(typed))
		for i := range typed {
			out[i] = filterMCPSchema(typed[i], depth+1)
		}
		return out
	case string, bool, float64, nil:
		return typed
	default:
		return nil
	}
}

func (s *managedMCPSet) Call(ctx context.Context, serverID, tool string, arguments json.RawMessage) (string, error) {
	if s == nil {
		return "", errors.New("managed MCP is not configured")
	}
	server := s.servers[serverID]
	if server == nil {
		return "", fmt.Errorf("managed MCP server %q is unavailable", serverID)
	}
	found := false
	for _, known := range server.tools {
		if known.Name == tool {
			found = true
			break
		}
	}
	if !found {
		return "", fmt.Errorf("managed MCP tool %q is not advertised by server %q", tool, serverID)
	}
	callCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	return server.client.CallTool(callCtx, tool, arguments)
}

func (s *managedMCPSet) Close() error {
	if s == nil {
		return nil
	}
	var errs []error
	for _, server := range s.servers {
		if err := server.client.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

type mcpRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    any    `json:"data,omitempty"`
	} `json:"error,omitempty"`
}

type mcpRPC interface {
	Request(context.Context, string, any) (json.RawMessage, error)
	Notify(context.Context, string, any) error
	Close() error
}

type protocolMCPClient struct{ rpc mcpRPC }

func newManagedMCPClient(ctx context.Context, root string, server MCPServer) (managedMCPClient, error) {
	var rpc mcpRPC
	var err error
	switch server.Transport {
	case MCPTransportStdio:
		rpc, err = newStdioMCPRPC(root, server)
	case MCPTransportHTTP:
		rpc, err = newHTTPMCPRPC(server)
	case MCPTransportSSE:
		rpc, err = newSSEMCPRPC(ctx, server)
	default:
		err = fmt.Errorf("unsupported transport %q", server.Transport)
	}
	if err != nil {
		return nil, err
	}
	client := &protocolMCPClient{rpc: rpc}
	if err := client.initialize(ctx); err != nil {
		_ = rpc.Close()
		var responseError *mcpHTTPStatusError
		// Legacy HTTP+SSE servers (including Burp) advertise their POST URL
		// with a sessionId query in a GET endpoint event. Retry only a rejected
		// initialize request; authentication and established-session errors
		// must retain their original meaning.
		if server.Transport == MCPTransportHTTP && errors.As(err, &responseError) && responseError.method == "initialize" &&
			(responseError.status == http.StatusBadRequest || responseError.status == http.StatusNotFound || responseError.status == http.StatusMethodNotAllowed) {
			legacy, legacyErr := newSSEMCPRPC(ctx, server)
			if legacyErr == nil {
				client.rpc = legacy
				legacyErr = client.initialize(ctx)
				if legacyErr == nil {
					return client, nil
				}
				_ = legacy.Close()
			}
			return nil, fmt.Errorf("Streamable HTTP initialization: %w; legacy SSE initialization: %w", err, legacyErr)
		}
		return nil, err
	}
	return client, nil
}

func (c *protocolMCPClient) initialize(ctx context.Context) error {
	_, err := c.rpc.Request(ctx, "initialize", map[string]any{
		"protocolVersion": managedMCPProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "PrAImate", "version": "managed-runtime"},
	})
	if err != nil {
		return err
	}
	return c.rpc.Notify(ctx, "notifications/initialized", nil)
}

func (c *protocolMCPClient) ListTools(ctx context.Context) ([]managedMCPTool, error) {
	result, err := c.rpc.Request(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var payload struct {
		Tools []managedMCPTool `json:"tools"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return nil, fmt.Errorf("decode tools/list: %w", err)
	}
	return payload.Tools, nil
}

func (c *protocolMCPClient) CallTool(ctx context.Context, name string, arguments json.RawMessage) (string, error) {
	if len(bytes.TrimSpace(arguments)) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	var decoded any
	if err := json.Unmarshal(arguments, &decoded); err != nil {
		return "", fmt.Errorf("invalid MCP arguments: %w", err)
	}
	result, err := c.rpc.Request(ctx, "tools/call", map[string]any{"name": name, "arguments": decoded})
	if err != nil {
		return "", err
	}
	var payload struct {
		Content           []map[string]any `json:"content"`
		StructuredContent any              `json:"structuredContent,omitempty"`
		IsError           bool             `json:"isError,omitempty"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return "", fmt.Errorf("decode tools/call: %w", err)
	}
	parts := make([]string, 0, len(payload.Content)+1)
	for _, content := range payload.Content {
		if text, ok := content["text"].(string); ok && strings.TrimSpace(text) != "" {
			parts = append(parts, text)
			continue
		}
		raw, _ := json.Marshal(content)
		parts = append(parts, string(raw))
	}
	if payload.StructuredContent != nil {
		raw, _ := json.Marshal(payload.StructuredContent)
		parts = append(parts, string(raw))
	}
	out := boundManagedOutput(strings.Join(parts, "\n"))
	if payload.IsError {
		return "", fmt.Errorf("MCP tool reported an error: %s", out)
	}
	return out, nil
}

func (c *protocolMCPClient) Close() error { return c.rpc.Close() }

type stdioMCPRPC struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	nextID int64
}

func newStdioMCPRPC(root string, server MCPServer) (*stdioMCPRPC, error) {
	command, args := resolvedStdioMCPCommand(server)
	cmd := exec.Command(command, args...)
	cmd.Dir = root
	cmd.Env = append(managedMCPEnv(), stringMapEnv(server.Env)...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr limitedBuffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &stdioMCPRPC{cmd: cmd, stdin: stdin, stdout: bufio.NewReaderSize(stdout, managedOutputLimit), nextID: 1}, nil
}

func (r *stdioMCPRPC) Request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := r.nextID
	r.nextID++
	if err := writeMCPLine(r.stdin, mcpRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}); err != nil {
		return nil, err
	}
	for skipped := 0; skipped < 100; skipped++ {
		line, err := readMCPLine(ctx, r.stdout)
		if err != nil {
			return nil, err
		}
		var response mcpResponse
		if err := json.Unmarshal(line, &response); err != nil || len(response.ID) == 0 {
			continue
		}
		responseID, idErr := mcpID(response.ID)
		if idErr != nil || responseID != id {
			continue
		}
		return mcpResult(response)
	}
	return nil, errors.New("MCP stdio server sent too many unrelated messages")
}

func (r *stdioMCPRPC) Notify(_ context.Context, method string, params any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return writeMCPLine(r.stdin, mcpRequest{JSONRPC: "2.0", Method: method, Params: params})
}

func (r *stdioMCPRPC) Close() error {
	_ = r.stdin.Close()
	if r.cmd.Process != nil {
		_ = r.cmd.Process.Kill()
	}
	_ = r.cmd.Wait()
	return nil
}

func writeMCPLine(w io.Writer, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	_, err = w.Write(raw)
	return err
}

func readMCPLine(ctx context.Context, reader *bufio.Reader) ([]byte, error) {
	type result struct {
		line []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := reader.ReadSlice('\n')
		ch <- result{line: line, err: err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-ch:
		if result.err != nil {
			return nil, result.err
		}
		return bytes.TrimSpace(result.line), nil
	}
}

type httpMCPRPC struct {
	mu        sync.Mutex
	client    *http.Client
	server    MCPServer
	endpoint  string
	sessionID string
	nextID    int64
}

type mcpHTTPStatusError struct {
	status int
	method string
	body   string
}

func (e *mcpHTTPStatusError) Error() string {
	return fmt.Sprintf("HTTP %d %s: %s", e.status, http.StatusText(e.status), e.body)
}

func newHTTPMCPRPC(server MCPServer) (*httpMCPRPC, error) {
	if _, err := url.ParseRequestURI(server.URL); err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	return &httpMCPRPC{client: &http.Client{Timeout: 45 * time.Second}, server: server, endpoint: server.URL, nextID: 1}, nil
}

func (r *httpMCPRPC) Request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := r.nextID
	r.nextID++
	response, err := r.post(ctx, mcpRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	return mcpResult(*response)
}

func (r *httpMCPRPC) Notify(ctx context.Context, method string, params any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.post(ctx, mcpRequest{JSONRPC: "2.0", Method: method, Params: params})
	return err
}

func (r *httpMCPRPC) post(ctx context.Context, body mcpRequest) (*mcpResponse, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	applyMCPHeaders(req, r.server, r.sessionID)
	response, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if sessionID := response.Header.Get("Mcp-Session-Id"); sessionID != "" {
		r.sessionID = sessionID
	}
	if response.StatusCode == http.StatusAccepted || response.StatusCode == http.StatusNoContent {
		return &mcpResponse{}, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return nil, &mcpHTTPStatusError{status: response.StatusCode, method: body.Method, body: strings.TrimSpace(string(message))}
	}
	if body.ID == 0 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return &mcpResponse{}, nil
	}
	return decodeMCPHTTPResponse(response)
}

func (r *httpMCPRPC) Close() error { return nil }

func decodeMCPHTTPResponse(response *http.Response) (*mcpResponse, error) {
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	if strings.Contains(contentType, "text/event-stream") {
		scanner := bufio.NewScanner(io.LimitReader(response.Body, managedOutputLimit*2))
		scanner.Buffer(make([]byte, 4096), managedOutputLimit)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			var result mcpResponse
			if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &result); err == nil && len(result.ID) > 0 {
				return &result, nil
			}
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
		return nil, errors.New("MCP HTTP stream ended without a response")
	}
	var result mcpResponse
	dec := json.NewDecoder(io.LimitReader(response.Body, managedOutputLimit*2))
	if err := dec.Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

type sseMCPRPC struct {
	server   MCPServer
	client   *http.Client
	cancel   context.CancelFunc
	endpoint chan string
	ready    chan error
	mu       sync.Mutex
	nextID   int64
	pending  map[int64]chan mcpResponse
}

func newSSEMCPRPC(ctx context.Context, server MCPServer) (*sseMCPRPC, error) {
	streamCtx, cancel := context.WithCancel(context.Background())
	rpc := &sseMCPRPC{
		server: server, client: &http.Client{}, cancel: cancel, endpoint: make(chan string, 1), ready: make(chan error, 1),
		nextID: 1, pending: map[int64]chan mcpResponse{},
	}
	go rpc.readStream(streamCtx)
	select {
	case <-ctx.Done():
		cancel()
		return nil, ctx.Err()
	case err := <-rpc.ready:
		if err != nil {
			cancel()
			return nil, err
		}
		return rpc, nil
	case <-time.After(15 * time.Second):
		cancel()
		return nil, errors.New("timed out waiting for SSE endpoint")
	}
}

func (r *sseMCPRPC) readStream(ctx context.Context) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.server.URL, nil)
	if err != nil {
		r.ready <- err
		return
	}
	applyMCPHeaders(req, r.server, "")
	req.Header.Set("Accept", "text/event-stream")
	response, err := r.client.Do(req)
	if err != nil {
		r.ready <- err
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		r.ready <- fmt.Errorf("SSE HTTP %s", response.Status)
		return
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 4096), managedOutputLimit)
	event, data := "", ""
	readySent := false
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			if data != "" {
				data += "\n"
			}
			data += strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		case line == "":
			if event == "endpoint" && data != "" && !readySent {
				endpoint, resolveErr := resolveMCPEndpoint(r.server.URL, data)
				if resolveErr != nil {
					r.ready <- resolveErr
					return
				}
				r.endpoint <- endpoint
				r.ready <- nil
				readySent = true
			} else if event == "message" && data != "" {
				r.deliverSSE([]byte(data))
			}
			event, data = "", ""
		}
	}
	if !readySent {
		r.ready <- errors.New("SSE stream ended before endpoint event")
	}
}

func (r *sseMCPRPC) deliverSSE(raw []byte) {
	var response mcpResponse
	if json.Unmarshal(raw, &response) != nil {
		return
	}
	id, err := mcpID(response.ID)
	if err != nil {
		return
	}
	r.mu.Lock()
	ch := r.pending[id]
	delete(r.pending, id)
	r.mu.Unlock()
	if ch != nil {
		ch <- response
	}
}

func (r *sseMCPRPC) Request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	r.mu.Lock()
	id := r.nextID
	r.nextID++
	ch := make(chan mcpResponse, 1)
	r.pending[id] = ch
	r.mu.Unlock()
	if err := r.post(ctx, mcpRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}); err != nil {
		r.mu.Lock()
		delete(r.pending, id)
		r.mu.Unlock()
		return nil, err
	}
	select {
	case <-ctx.Done():
		r.mu.Lock()
		delete(r.pending, id)
		r.mu.Unlock()
		return nil, ctx.Err()
	case response := <-ch:
		return mcpResult(response)
	}
}

func (r *sseMCPRPC) Notify(ctx context.Context, method string, params any) error {
	return r.post(ctx, mcpRequest{JSONRPC: "2.0", Method: method, Params: params})
}

func (r *sseMCPRPC) post(ctx context.Context, body mcpRequest) error {
	var endpoint string
	select {
	case endpoint = <-r.endpoint:
		r.endpoint <- endpoint
	case <-ctx.Done():
		return ctx.Err()
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	applyMCPHeaders(req, r.server, "")
	response, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("SSE message HTTP %s", response.Status)
	}
	return nil
}

func (r *sseMCPRPC) Close() error { r.cancel(); return nil }

func resolveMCPEndpoint(base, endpoint string) (string, error) {
	baseURL, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	rel, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	return baseURL.ResolveReference(rel).String(), nil
}

func applyMCPHeaders(req *http.Request, server MCPServer, sessionID string) {
	req.Header.Set("Accept", "application/json, text/event-stream")
	if req.Method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	if header, token, ok := headerAndToken(server); ok {
		if strings.EqualFold(header, "Authorization") && !strings.HasPrefix(strings.ToLower(token), "bearer ") {
			token = "Bearer " + token
		}
		req.Header.Set(header, token)
	}
}

func mcpResult(response mcpResponse) (json.RawMessage, error) {
	if response.Error != nil {
		return nil, fmt.Errorf("MCP error %d: %s", response.Error.Code, response.Error.Message)
	}
	return response.Result, nil
}

func stringMapEnv(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	keys = sortedStrings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+values[key])
	}
	return out
}

func managedMCPEnv() []string {
	allowed := map[string]bool{
		"PATH": true, "HOME": true, "USERPROFILE": true, "APPDATA": true, "LOCALAPPDATA": true,
		"SYSTEMROOT": true, "WINDIR": true, "TEMP": true, "TMP": true, "TMPDIR": true,
		"LANG": true, "LC_ALL": true, "LC_CTYPE": true,
	}
	var out []string
	for _, item := range os.Environ() {
		key, _, ok := strings.Cut(item, "=")
		if ok && allowed[strings.ToUpper(key)] {
			out = append(out, item)
		}
	}
	return out
}

// Keep strconv referenced in this protocol file when JSON-RPC servers return
// string IDs. Supporting them makes the decoder tolerant without accepting a
// mismatched request.
func mcpID(raw json.RawMessage) (int64, error) {
	var id int64
	if err := json.Unmarshal(raw, &id); err == nil {
		return id, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return 0, err
	}
	return strconv.ParseInt(text, 10, 64)
}
