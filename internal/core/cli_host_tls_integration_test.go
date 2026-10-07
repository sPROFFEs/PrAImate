package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/hosttls"
)

// Exercises the actual compiled fork and core consent, without real providers.
// Unit tests of the transport helper cannot catch bundle initialization defects.
func TestPraimateCodeInstalledHostTLS(t *testing.T) {
	bin := os.Getenv("PRAIMATE_TEST_CODE_BINARY")
	if bin == "" {
		t.Skip("set PRAIMATE_TEST_CODE_BINARY to test the compiled fork with HTTPS")
	}
	if !filepath.IsAbs(bin) {
		t.Fatal("PRAIMATE_TEST_CODE_BINARY must be absolute")
	}
	root := t.TempDir()
	isolateOpenCodeFixture(t, root)
	c := nativeTestCore(t)
	var calls atomic.Int32
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(nativeSSE("trusted-model-ok", "stop")))
	}))
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cert, err := hosttls.Inspect(ctx, s.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.TrustLocalHostCertificate(ctx, s.URL, cert.PEM); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	cfg, err := c.ResolveExecutionConfig(ctx, ExecutionRequest{Surface: SurfaceChat, CLI: "praimate-code", Cwd: project, Local: &ChatLocalEndpoint{Endpoint: s.URL, Model: "fixture", APIKey: "fixture-key"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.PrepareExecution(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"enabled_providers": []string{"praimate-local"}, "model": cfg.Model, "small_model": cfg.Model, "snapshot": false})
	cfg.Env["OPENCODE_CONFIG_CONTENT"] = string(raw)
	adapter := NewPraimateCodeAdapter()
	adapter.bin = bin
	reply, err := adapter.SingleShotStream(ctx, SingleShotOpts{Cwd: project, Message: "Reply with the fixture response", Model: cfg.Model, Env: cfg.Env}, nil)
	if err != nil || reply == nil || reply.ExitCode != 0 || reply.SessionID == "" || !strings.Contains(reply.Text, "trusted-model-ok") || calls.Load() == 0 {
		t.Fatalf("accepted HTTPS completion: reply=%+v requests=%d err=%v", reply, calls.Load(), err)
	}
	resumed, err := adapter.ResumeStream(ctx, reply.SessionID, ResumeOpts{Cwd: project, Message: "Reply again", Model: cfg.Model, Env: cfg.Env}, nil)
	if err != nil || resumed == nil || resumed.ExitCode != 0 || resumed.SessionID != reply.SessionID || !strings.Contains(resumed.Text, "trusted-model-ok") {
		t.Fatalf("resumed HTTPS completion: reply=%+v err=%v", resumed, err)
	}
}
