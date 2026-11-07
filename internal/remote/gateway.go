// Package remote is a transport adapter. It owns no application state and is
// linked into the GUI only by the server build tag.
package remote

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/version"
)

type Config struct {
	Listen          string
	Token           string
	TLSCert, TLSKey string
	AllowedOrigins  []string
	State           State
	Registry        *Registry
	Assets          http.Handler
	Files           *Files
	Upload          func(context.Context, string, io.Reader) (any, error)
}

type Call func(context.Context, string, json.RawMessage) (any, error)

type Gateway struct {
	config       Config
	tokenHash    [32]byte
	call         Call
	mu           sync.Mutex
	clients      map[chan []byte]struct{}
	server       *http.Server
	closed       bool
	calls        chan struct{}
	done         chan struct{}
	adminEnabled bool
	devices      *Devices
	operations   *Operations
	cancel       context.CancelFunc
	epoch        string
	sequence     uint64
	replay       []recordedEvent
	replayBytes  int
	publicOrigin string
}

// SetPublicOrigin permits exactly the configured reverse-proxy origin without
// treating forwarded headers as trusted or restarting active streams.
func (g *Gateway) SetPublicOrigin(origin string) error {
	if origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("invalid public origin")
		}
	}
	g.mu.Lock()
	g.publicOrigin = origin
	g.mu.Unlock()
	return nil
}

func New(config Config, call Call) (*Gateway, error) {
	if (config.Token != "" && len(config.Token) < 32) || (config.Token == "" && config.State == nil) {
		return nil, errors.New("remote access requires a token of at least 32 characters")
	}
	if config.Listen == "" {
		config.Listen = "127.0.0.1:8844"
	}
	hash := sha256.Sum256([]byte(config.Token))
	adminEnabled := config.Token != ""
	config.Token = ""
	epoch, err := randomID(9)
	if err != nil {
		return nil, err
	}
	devices, err := NewDevices(config.State)
	if err != nil {
		return nil, err
	}
	life, cancel := context.WithCancel(context.Background())
	g := &Gateway{config: config, tokenHash: hash, adminEnabled: adminEnabled, devices: devices, cancel: cancel, epoch: epoch, call: call, clients: make(map[chan []byte]struct{}), calls: make(chan struct{}, 16), done: make(chan struct{})}
	g.operations, err = NewOperations(life, config.State, g.dispatch)
	if err != nil {
		cancel()
		return nil, err
	}
	return g, nil
}

// Start validates TLS before opening a socket. Loopback HTTP supports SSH/VPN
// forwarding; an externally bound endpoint always requires explicit TLS.
func (g *Gateway) Start(ctx context.Context) (string, error) {
	host, _, err := net.SplitHostPort(g.config.Listen)
	if err != nil {
		return "", fmt.Errorf("remote listen address: %w", err)
	}
	var tlsConfig *tls.Config
	if g.config.TLSCert != "" || g.config.TLSKey != "" {
		cert, err := tls.LoadX509KeyPair(g.config.TLSCert, g.config.TLSKey)
		if err != nil {
			return "", fmt.Errorf("remote TLS certificate: %w", err)
		}
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
	} else if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return "", errors.New("remote listeners outside loopback require a TLS certificate and key")
	}
	g.mu.Lock()
	if g.server != nil || g.closed {
		g.mu.Unlock()
		return "", errors.New("remote gateway already started or closed")
	}
	listener, err := net.Listen("tcp", g.config.Listen)
	if err != nil {
		g.mu.Unlock()
		return "", err
	}
	if tlsConfig != nil {
		listener = tls.NewListener(listener, tlsConfig)
	}
	srv := &http.Server{Handler: g, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	g.server = srv
	g.mu.Unlock()
	go func() { _ = srv.Serve(listener) }()
	go func() {
		select {
		case <-ctx.Done():
			_ = g.Close()
		case <-g.done:
		}
	}()
	return listener.Addr().String(), nil
}

func (g *Gateway) Close() error {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return nil
	}
	g.closed = true
	g.cancel()
	close(g.done)
	for ch := range g.clients {
		close(ch)
		delete(g.clients, ch)
	}
	srv := g.server
	g.mu.Unlock()
	if srv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		_ = srv.Close()
		return err
	}
	return nil
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	origin := r.Header.Get("Origin")
	if origin != "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		allowed := origin == scheme+"://"+r.Host
		g.mu.Lock()
		allowed = allowed || origin == g.publicOrigin
		g.mu.Unlock()
		for _, candidate := range g.config.AllowedOrigins {
			allowed = allowed || origin == candidate
		}
		if !allowed {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Range, If-Range, Last-Event-ID, X-Praimate-Client")
			w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, PUT, OPTIONS")
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	if g.publicRoute(w, r) {
		return
	}
	identity, ok := g.authenticate(r)
	if !ok {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	clientID := r.Header.Get("X-Praimate-Client")
	if clientID != "" && !clientIDPattern.MatchString(clientID) {
		writeError(w, Fail(400, "invalid_client", "Invalid client identifier"))
		return
	}
	identity.ClientID = clientID
	r = r.WithContext(WithPrincipal(r.Context(), identity))
	if g.deviceRoute(w, r) {
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/status":
		writeJSON(w, map[string]any{"version": version.Current, "edition": version.Edition, "protocol": 2, "serverTime": time.Now().UTC(), "identity": identity, "capabilities": map[string]bool{"devices": true, "operations": true, "eventReplay": true, "files": true}})
	case r.Method == http.MethodGet && r.URL.Path == "/v1/events":
		g.events(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/call":
		select {
		case g.calls <- struct{}{}:
			defer func() { <-g.calls }()
		default:
			http.Error(w, "too many active requests", http.StatusTooManyRequests)
			return
		}
		var input struct {
			Method    string          `json:"method"`
			Params    json.RawMessage `json:"params"`
			RequestID string          `json:"requestId,omitempty"`
		}
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(15 * time.Second))
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20))
		d.DisallowUnknownFields()
		if err := d.Decode(&input); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if err := d.Decode(new(any)); err != io.EOF || input.Method == "" {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		scope, mutation, err := g.methodPermission(input.Method)
		if err != nil {
			writeError(w, err)
			return
		}
		if !identity.Allows(scope) {
			writeError(w, Fail(403, "forbidden", "This device lacks the required permission"))
			return
		}
		if identity.ClientID == "" && (input.Method == "api.WriteTerminal" || input.Method == "api.ResizeTerminal" || input.Method == "api.CloseTerminal" || input.Method == "terminals.claim" || input.Method == "terminals.write" || input.Method == "terminals.resize" || input.Method == "terminals.close") {
			writeError(w, Fail(400, "client_id_required", "Terminal control requires X-Praimate-Client"))
			return
		}
		if mutation {
			op, err := g.operations.Submit(identity, input.RequestID, input.Method, input.Params)
			if err != nil {
				writeError(w, err)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			writeJSON(w, map[string]any{"operation": op})
			return
		}
		result, err := g.dispatch(r.Context(), input.Method, input.Params)
		if err != nil {
			// Backend errors may contain CLI output or endpoint credentials.
			// Detailed execution failures remain in the authorized run/chat.
			writeError(w, err)
			return
		}
		writeJSON(w, map[string]any{"result": result})
	default:
		http.NotFound(w, r)
	}
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

// Publish is best-effort transport. A slow connection is dropped instead of
// blocking a CLI; clients recover from application/terminal snapshots on attach.
func (g *Gateway) Publish(name string, payload any) {
	if !safeEvent(name) {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sequence++
	raw, err := json.Marshal(map[string]any{"id": fmt.Sprintf("%s:%d", g.epoch, g.sequence), "name": name, "payload": payload})
	if err != nil || len(raw) > 256<<10 {
		return
	}
	g.replay = append(g.replay, recordedEvent{g.sequence, raw})
	g.replayBytes += len(raw)
	for len(g.replay) > 512 || g.replayBytes > 8<<20 {
		g.replayBytes -= len(g.replay[0].raw)
		g.replay = g.replay[1:]
	}
	for ch := range g.clients {
		select {
		case ch <- raw:
		default:
			close(ch)
			delete(g.clients, ch)
		}
	}
}

func (g *Gateway) subscribe() (chan []byte, func(), error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed || len(g.clients) >= 8 {
		return nil, nil, errors.New("remote event connections unavailable")
	}
	ch := make(chan []byte, 64)
	g.clients[ch] = struct{}{}
	return ch, func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		if _, ok := g.clients[ch]; ok {
			delete(g.clients, ch)
			close(ch)
		}
	}, nil
}

func (g *Gateway) events(w http.ResponseWriter, r *http.Request) {
	cursor := r.Header.Get("Last-Event-ID")
	if cursor == "" {
		cursor = r.URL.Query().Get("cursor")
	}
	ch, unsubscribe, replay, resync, err := g.subscribeFrom(cursor)
	if err != nil {
		http.Error(w, "event connection limit reached", http.StatusServiceUnavailable)
		return
	}
	defer unsubscribe()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	control := http.NewResponseController(w)
	send := func(raw []byte) bool {
		if _, ok := g.authenticate(r); !ok {
			return false
		}
		_ = control.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err := fmt.Fprintf(w, "data: %s\n\n", raw); err != nil {
			return false
		}
		return control.Flush() == nil
	}
	if resync && !send([]byte(`{"name":"resync","payload":{"snapshotsRequired":true}}`)) {
		return
	}
	for _, event := range replay {
		if !send(event) {
			return
		}
	}
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case raw, ok := <-ch:
			if !ok || !send(raw) {
				return
			}
		case <-ticker.C:
			if !send([]byte(`{"name":"heartbeat"}`)) {
				return
			}
		}
	}
}
