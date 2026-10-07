package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sPROFFEs/PrAImate/internal/hosttls"
)

func TestPraimateCodeHostTLSConsentReachesEveryExecutionSurface(t *testing.T) {
	c := nativeTestCore(t)
	ctx := context.Background()
	s := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer s.Close()
	cert, err := hosttls.Inspect(ctx, s.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.TrustLocalHostCertificate(ctx, s.URL, cert.PEM); err != nil {
		t.Fatal(err)
	}
	for _, surface := range []ExecutionSurface{SurfaceChat, SurfaceStudio, SurfaceWorkflow, SurfaceTerminal} {
		cfg, err := c.ResolveExecutionConfig(ctx, ExecutionRequest{Surface: surface, CLI: "praimate-code", Cwd: t.TempDir(), Local: &ChatLocalEndpoint{Endpoint: s.URL, Model: "fixture"}})
		if err != nil {
			t.Fatal(err)
		}
		if err := c.PrepareExecution(ctx, cfg); err != nil {
			t.Fatal(err)
		}
		var trusted map[string]string
		if err := json.Unmarshal([]byte(cfg.Env["PRAIMATE_HOST_TLS"]), &trusted); err != nil {
			t.Fatal(err)
		}
		if len(trusted) != 1 || trusted[cert.Origin] != cert.PEM {
			t.Fatalf("%s lost scoped consent", surface)
		}
	}
	if err := c.RemoveLocalHostCertificate(ctx, s.URL); err != nil {
		t.Fatal(err)
	}
	cfg := &EffectiveExecutionConfig{CLI: "praimate-code", Local: &ChatLocalEndpoint{Endpoint: s.URL}, Env: map[string]string{"PRAIMATE_HOST_TLS": "stale"}}
	if err := c.prepareCLIHostTLS(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Env["PRAIMATE_HOST_TLS"] != "{}" {
		t.Fatal("revoked consent retained")
	}
}
