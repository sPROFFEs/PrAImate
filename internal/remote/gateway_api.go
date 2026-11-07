package remote

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Devices returns the shared registry so local administration and HTTP access
// use the same revocation state, including while requests are in flight.
func (g *Gateway) Devices() *Devices { return g.devices }

func (g *Gateway) authenticate(r *http.Request) (Principal, bool) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return Principal{}, false
	}
	token := strings.TrimPrefix(auth, "Bearer ")
	hash := sha256.Sum256([]byte(token))
	if g.adminEnabled && subtle.ConstantTimeCompare(hash[:], g.tokenHash[:]) == 1 {
		return Principal{ID: "administrator", Role: "admin"}, true
	}
	return g.devices.Authenticate(token)
}
func (g *Gateway) dispatch(ctx context.Context, name string, params json.RawMessage) (any, error) {
	if g.config.Registry != nil {
		if _, ok := g.config.Registry.Lookup(name); ok {
			return g.config.Registry.Call(ctx, name, params)
		}
	}
	if g.call == nil {
		return nil, Fail(404, "unknown_method", "Method unavailable")
	}
	return g.call(ctx, name, params)
}
func (g *Gateway) methodPermission(name string) (string, bool, error) {
	if g.config.Registry != nil {
		if method, ok := g.config.Registry.Lookup(name); ok {
			return method.Scope, method.Mutation, nil
		}
	}
	switch name {
	case "chats.list", "chats.messages", "agents.list", "terminals.list", "terminals.snapshot", "workers.list", "workers.get":
		return "read", false, nil
	case "terminals.write", "terminals.resize", "terminals.close", "chats.cancel", "workers.cancel":
		return "control", false, nil
	case "chats.create", "chats.send", "terminals.start", "terminals.resume", "workers.start", "workers.continue", "workers.task.retry":
		return "control", true, nil
	}
	return "", false, Fail(404, "unknown_method", "Method unavailable")
}
func decodeRequest(w http.ResponseWriter, r *http.Request, value any) error {
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(15 * time.Second))
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return Fail(400, "invalid_request", "Invalid JSON request")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return Fail(400, "invalid_request", "Trailing request data")
	}
	return nil
}
func writeError(w http.ResponseWriter, err error) {
	var api *APIError
	if !errors.As(err, &api) {
		api = &APIError{"operation_failed", "Operation failed; inspect its session, artifact or run", 422}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(api.Status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": api})
}
func (g *Gateway) publicRoute(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == "GET" && r.URL.Path == "/v1/discovery" {
		writeJSON(w, map[string]any{"product": "PrAImate Server", "protocol": 2})
		return true
	}
	if r.Method == http.MethodPost && r.URL.Path == "/v1/pair" {
		var p struct {
			Code string `json:"code"`
			Name string `json:"name"`
		}
		if err := decodeRequest(w, r, &p); err != nil {
			writeError(w, err)
			return true
		}
		device, token, err := g.devices.Pair(p.Code, p.Name)
		if err != nil {
			writeError(w, err)
		} else {
			writeJSON(w, map[string]any{"device": device, "token": token})
		}
		return true
	}
	if !strings.HasPrefix(r.URL.Path, "/v1/") && g.config.Assets != nil && (r.Method == "GET" || r.Method == "HEAD") {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; connect-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'")
		g.config.Assets.ServeHTTP(w, r)
		return true
	}
	return false
}
func (g *Gateway) deviceRoute(w http.ResponseWriter, r *http.Request) bool {
	if g.transferRoute(w, r) {
		return true
	}
	p := Identity(r.Context())
	switch {
	case r.Method == "GET" && r.URL.Path == "/v1/methods":
		if g.config.Registry == nil {
			writeJSON(w, []Method{})
		} else {
			writeJSON(w, g.config.Registry.Catalog(p))
		}
		return true
	case r.Method == "GET" && r.URL.Path == "/v1/devices":
		if !p.Allows("admin") {
			writeError(w, Fail(403, "forbidden", "Administrator permission required"))
		} else {
			writeJSON(w, g.devices.List())
		}
		return true
	case r.Method == "POST" && r.URL.Path == "/v1/pairings":
		if !p.Allows("admin") {
			writeError(w, Fail(403, "forbidden", "Administrator permission required"))
			return true
		}
		var input struct {
			Name string `json:"name"`
			Role string `json:"role"`
		}
		if err := decodeRequest(w, r, &input); err != nil {
			writeError(w, err)
			return true
		}
		pairing, err := g.devices.CreatePairing(input.Name, input.Role)
		if err != nil {
			writeError(w, err)
		} else {
			writeJSON(w, pairing)
		}
		return true
	case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/v1/devices/") && strings.HasSuffix(r.URL.Path, "/revoke"):
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/devices/"), "/revoke")
		if id != p.ID && !p.Allows("admin") {
			writeError(w, Fail(403, "forbidden", "Administrator permission required"))
			return true
		}
		if err := g.devices.Revoke(id); err != nil {
			writeError(w, err)
		} else {
			writeJSON(w, map[string]bool{"revoked": true})
		}
		return true
	case r.Method == "GET" && r.URL.Path == "/v1/operations":
		writeJSON(w, g.operations.List(p))
		return true
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/operations/"):
		op, err := g.operations.Get(p, strings.TrimPrefix(r.URL.Path, "/v1/operations/"))
		if err != nil {
			writeError(w, err)
		} else {
			writeJSON(w, op)
		}
		return true
	}
	return false
}
func (g *Gateway) CreatePairing(name, role string) (Pairing, error) {
	return g.devices.CreatePairing(name, role)
}
func (g *Gateway) Done() <-chan struct{} { return g.done }

type recordedEvent struct {
	sequence uint64
	raw      []byte
}

func safeEvent(name string) bool {
	if strings.HasPrefix(name, "term:data:") || strings.HasPrefix(name, "term:exit:") {
		return true
	}
	switch name {
	case "praimate:chat-stream", "praimate:chat-finished", "praimate:approval", "praimate:approval-resolved", "praimate:managed-run", "praimate:turn", "praimate:workflow-stream", "praimate:requirements", "praimate:install", "praimate:worker-update", "praimate:editor-fs", "assistant:event", "assistant:navigate", "assistant:appearance", "assistant:artifact-progress", "voice:state", "praimate:terminal-control":
		return true
	}
	return false
}
func (g *Gateway) subscribeFrom(cursor string) (chan []byte, func(), [][]byte, bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed || len(g.clients) >= 8 {
		return nil, nil, nil, false, errors.New("event connections unavailable")
	}
	replay := [][]byte{}
	resync := true
	epoch, position, ok := strings.Cut(cursor, ":")
	sequence, err := strconv.ParseUint(position, 10, 64)
	if ok && err == nil && epoch == g.epoch && sequence <= g.sequence && (len(g.replay) == 0 || sequence+1 >= g.replay[0].sequence) {
		resync = false
		for _, event := range g.replay {
			if event.sequence > sequence {
				replay = append(replay, event.raw)
			}
		}
	}
	ch := make(chan []byte, 64)
	g.clients[ch] = struct{}{}
	unsubscribe := func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		if _, ok := g.clients[ch]; ok {
			delete(g.clients, ch)
			close(ch)
		}
	}
	return ch, unsubscribe, replay, resync, nil
}
