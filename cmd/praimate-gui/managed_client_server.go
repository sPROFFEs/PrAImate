//go:build server

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/remote"
	"github.com/sPROFFEs/PrAImate/internal/serverboot"
)

type ManagedConnectionInfo struct {
	Active bool   `json:"active"`
	Origin string `json:"origin,omitempty"`
}
type managedRequest struct {
	cancel context.CancelFunc
	writer *io.PipeWriter
	ack    chan struct{}
	mu     sync.Mutex
	size   int64
}

var managedID = regexp.MustCompile(`^[a-f0-9]{32}$`)

func (e *editionServices) connectManaged(a *App) bool {
	if a.headless || a.detachedClient != nil {
		return false
	}
	_, err := serverboot.LoadSession(a.dbPath)
	if err == nil {
		e.mu.Lock()
		e.clientMode = true
		e.mu.Unlock()
		return true
	}
	return false
}
func (e *editionServices) isClient() bool { e.mu.Lock(); defer e.mu.Unlock(); return e.clientMode }
func (a *App) writeManagedSession() (func(), error) {
	a.edition.mu.Lock()
	session := serverboot.Session{Origin: serverOriginURL(a.edition.address, a.edition.config.TLSCert != ""), Token: a.edition.localToken, TLSCert: a.edition.config.TLSCert, PID: os.Getpid()}
	a.edition.mu.Unlock()
	if err := serverboot.SaveSession(a.dbPath, session); err != nil {
		return nil, err
	}
	return func() { serverboot.RemoveSession(a.dbPath, session.Token) }, nil
}
func (a *App) managedHTTPClient(s serverboot.Session) (*http.Client, error) {
	a.edition.mu.Lock()
	defer a.edition.mu.Unlock()
	key := s.Origin + "\x00" + s.Token + "\x00" + s.TLSCert
	if a.edition.clientHTTP != nil && a.edition.clientHTTPKey == key {
		return a.edition.clientHTTP, nil
	}
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext, ResponseHeaderTimeout: 30 * time.Second}
	if strings.HasPrefix(s.Origin, "https:") {
		pem, err := os.ReadFile(s.TLSCert)
		if err != nil {
			return nil, errors.New("Cannot load the background Server's TLS certificate")
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("Invalid local Server TLS certificate")
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
		// The dial address stays on this host. Use the configured certificate's
		// SAN for identity verification when it names a public tunnel hostname.
		block, _ := decodeCertificate(pem)
		if block != nil {
			if len(block.DNSNames) > 0 {
				transport.TLSClientConfig.ServerName = block.DNSNames[0]
			} else if len(block.IPAddresses) > 0 {
				transport.TLSClientConfig.ServerName = block.IPAddresses[0].String()
			}
		}
	}
	if previous := a.edition.clientHTTP; previous != nil {
		previous.CloseIdleConnections()
	}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	a.edition.clientHTTP = client
	a.edition.clientHTTPKey = key
	return client, nil
}
func (a *App) ManagedServerConnection() (ManagedConnectionInfo, error) {
	if a.headless || a.core != nil || a.detachedClient != nil {
		return ManagedConnectionInfo{}, nil
	}
	s, err := serverboot.LoadSession(a.dbPath)
	if errors.Is(err, os.ErrNotExist) {
		if strings.Contains(a.initErr, remote.ErrProfileOwned.Error()) {
			a.edition.mu.Lock()
			a.edition.clientMode = true
			a.edition.mu.Unlock()
			return ManagedConnectionInfo{}, errors.New("Another Server owns this profile and its local session is not ready. Retry after startup, or inspect the service below. Stop a manually managed older server before configuring system startup")
		}
		return ManagedConnectionInfo{}, nil
	}
	if err != nil {
		return ManagedConnectionInfo{}, err
	}
	client, err := a.managedHTTPClient(s)
	if err != nil {
		return ManagedConnectionInfo{}, err
	}
	ctx, cancel := context.WithTimeout(a.ctx, 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", s.Origin+"/v1/status", nil)
	req.Header.Set("Authorization", "Bearer "+s.Token)
	response, err := client.Do(req)
	if err != nil {
		return ManagedConnectionInfo{}, errors.New("The background Server is not ready. Retry after it starts, or stop its service from the OS service manager")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return ManagedConnectionInfo{}, errors.New("The local Server session is no longer valid; restart the background service")
	}
	var status struct {
		Protocol int `json:"protocol"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 65536)).Decode(&status) != nil || status.Protocol != 2 {
		return ManagedConnectionInfo{}, errors.New("The background Server uses an incompatible protocol")
	}
	a.edition.mu.Lock()
	a.edition.clientMode = true
	a.edition.mu.Unlock()
	return ManagedConnectionInfo{Active: true, Origin: s.Origin}, nil
}

// Explicit recovery after a failed or stopped supervisor. Taking the profile
// lease first prevents deleting a session that still belongs to a live server.
func (a *App) UseForegroundServer() error {
	if a.headless || a.detachedClient != nil {
		return errors.New("Foreground recovery is available on the Server host")
	}
	owner, err := remote.LockProfile(a.dbPath)
	if err != nil {
		return errors.New("Stop the background service before opening this profile in foreground mode")
	}
	defer owner.Close()
	if s, err := serverboot.LoadSession(a.dbPath); err == nil {
		serverboot.RemoveSession(a.dbPath, s.Token)
	}
	a.edition.mu.Lock()
	a.edition.clientMode = false
	a.edition.mu.Unlock()
	a.initErr = ""
	return nil
}

// Native Wails transport uses bounded, acknowledged chunks for JSON, uploads,
// downloads and SSE. Only authenticated requests to this profile's own address
// are accepted; the local owner capability never reaches JavaScript.
func (a *App) BeginManagedRequest(id, path, method, contentType, clientID string, hasBody bool) error {
	if !managedID.MatchString(id) || len(path) > 8192 || len(contentType) > 512 || len(clientID) > 80 || !strings.HasPrefix(path, "/v1/") || strings.ContainsAny(path+contentType+clientID, "\r\n\x00") {
		return errors.New("Invalid local Server request")
	}
	if method != "GET" && method != "POST" && method != "PUT" && method != "DELETE" {
		return errors.New("Unsupported local request method")
	}
	parsed, err := url.Parse(path)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.Fragment != "" {
		return errors.New("Invalid local request path")
	}
	s, err := serverboot.LoadSession(a.dbPath)
	if err != nil {
		return err
	}
	client, err := a.managedHTTPClient(s)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(a.ctx)
	stream := &managedRequest{cancel: cancel, ack: make(chan struct{}, 1)}
	var reader io.Reader
	var pipe *io.PipeReader
	if hasBody {
		pipe, stream.writer = io.Pipe()
		reader = io.LimitReader(pipe, 512<<20+1)
	}
	a.edition.mu.Lock()
	if a.edition.clientRequests == nil {
		a.edition.clientRequests = map[string]*managedRequest{}
	}
	if len(a.edition.clientRequests) >= 64 || a.edition.clientRequests[id] != nil {
		a.edition.mu.Unlock()
		cancel()
		if pipe != nil {
			pipe.Close()
			stream.writer.Close()
		}
		return errors.New("Too many local Server requests")
	}
	a.edition.clientRequests[id] = stream
	a.edition.mu.Unlock()
	go func() {
		defer func() {
			cancel()
			if pipe != nil {
				pipe.Close()
				stream.writer.Close()
			}
			a.edition.mu.Lock()
			delete(a.edition.clientRequests, id)
			a.edition.mu.Unlock()
		}()
		emit := func(payload map[string]any) {
			if ctx.Err() == nil && a.emit != nil {
				a.emit(a.ctx, "praimate:managed:"+id, payload)
			}
		}
		req, err := http.NewRequestWithContext(ctx, method, s.Origin+path, reader)
		if err != nil {
			emit(map[string]any{"error": "Invalid local request"})
			return
		}
		req.Header.Set("Authorization", "Bearer "+s.Token)
		req.Header.Set("X-Praimate-Client", clientID)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		response, err := client.Do(req)
		if err != nil {
			emit(map[string]any{"error": "The background Server connection was interrupted"})
			return
		}
		defer response.Body.Close()
		emit(map[string]any{"status": response.StatusCode, "headers": map[string]string{"Content-Type": response.Header.Get("Content-Type"), "Content-Disposition": response.Header.Get("Content-Disposition")}})
		buffer := make([]byte, 48*1024)
		for {
			n, err := response.Body.Read(buffer)
			if n > 0 {
				emit(map[string]any{"data": base64.StdEncoding.EncodeToString(buffer[:n])})
				select {
				case <-ctx.Done():
					return
				case <-stream.ack:
				}
			}
			if err == io.EOF {
				emit(map[string]any{"done": true})
				return
			}
			if err != nil {
				emit(map[string]any{"error": "The background Server response was interrupted"})
				return
			}
		}
	}()
	return nil
}
func (a *App) WriteManagedRequest(id, data string, done bool) error {
	a.edition.mu.Lock()
	stream := a.edition.clientRequests[id]
	a.edition.mu.Unlock()
	if stream == nil || stream.writer == nil {
		return errors.New("The local upload is no longer available")
	}
	if len(data) > 96*1024 {
		return errors.New("Upload chunk exceeds the local transport limit")
	}
	body, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return errors.New("Invalid upload encoding")
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	stream.size += int64(len(body))
	if stream.size > 512<<20 {
		stream.cancel()
		return errors.New("File exceeds the 512 MiB upload limit")
	}
	if len(body) > 0 {
		if _, err = stream.writer.Write(body); err != nil {
			return errors.New("The local upload was interrupted")
		}
	}
	if done {
		return stream.writer.Close()
	}
	return nil
}
func (a *App) AckManagedRequest(id string) {
	a.edition.mu.Lock()
	stream := a.edition.clientRequests[id]
	a.edition.mu.Unlock()
	if stream != nil {
		select {
		case stream.ack <- struct{}{}:
		default:
		}
	}
}
func (a *App) CancelManagedRequest(id string) {
	a.edition.mu.Lock()
	stream := a.edition.clientRequests[id]
	a.edition.mu.Unlock()
	if stream != nil {
		stream.cancel()
		if stream.writer != nil {
			stream.writer.CloseWithError(context.Canceled)
		}
	}
}

func decodeCertificate(body []byte) (*x509.Certificate, error) {
	for len(body) > 0 {
		block, rest := pem.Decode(body)
		if block == nil {
			break
		}
		body = rest
		if block.Type == "CERTIFICATE" {
			return x509.ParseCertificate(block.Bytes)
		}
	}
	return nil, errors.New("No local Server certificate found")
}
