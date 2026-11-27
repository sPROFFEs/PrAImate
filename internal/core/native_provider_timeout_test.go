package core

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func nativeIdleServer(t *testing.T, step func(w http.ResponseWriter, f http.Flusher)) nativeProvider {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		f := w.(http.Flusher)
		f.Flush()
		step(w, f)
	}))
	t.Cleanup(srv.Close)
	return nativeProvider{route: ChatLocalEndpoint{Endpoint: srv.URL + "/v1"}}
}

// A stream that keeps producing data must outlive the idle window.
func TestNativeIdleTimeoutResetsOnActivity(t *testing.T) {
	p := nativeIdleServer(t, func(w http.ResponseWriter, f http.Flusher) {
		for i := 0; i < 6; i++ { // ~300ms total, > idle window
			time.Sleep(50 * time.Millisecond)
			io.WriteString(w, ": thinking\n\n")
			f.Flush()
		}
	})
	p.idleTimeout = 200 * time.Millisecond
	p.http = &http.Client{Timeout: 125 * time.Millisecond}
	res, err := p.request(context.Background(), http.MethodPost, "/chat/completions", []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if _, err := io.ReadAll(res.Body); err != nil {
		t.Fatalf("active stream was cut: %v", err)
	}
}

// A backend that goes silent must fail with a clear idle error.
func TestNativeIdleTimeoutCutsSilentStream(t *testing.T) {
	p := nativeIdleServer(t, func(w http.ResponseWriter, f http.Flusher) {
		io.WriteString(w, ": hello\n\n")
		f.Flush()
		time.Sleep(300 * time.Millisecond)
	})
	p.idleTimeout = 80 * time.Millisecond
	res, err := p.request(context.Background(), http.MethodPost, "/chat/completions", []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	_, err = io.ReadAll(res.Body)
	if err == nil || !strings.Contains(err.Error(), "sent no data") {
		t.Fatalf("err = %v, want idle error", err)
	}
}

func TestNativeCompletionPreservesCallerDeadline(t *testing.T) {
	p := nativeIdleServer(t, func(w http.ResponseWriter, f http.Flusher) {
		time.Sleep(150 * time.Millisecond)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	res, err := p.request(ctx, http.MethodPost, "/chat/completions", []byte("{}"))
	if err == nil {
		defer res.Body.Close()
		_, err = io.ReadAll(res.Body)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("caller deadline lost: %v", err)
	}
}
