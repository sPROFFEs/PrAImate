package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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
	var exportedPath string
	for _, cli := range []string{"praimate-code", "opencode"} {
		for _, surface := range []ExecutionSurface{SurfaceChat, SurfaceStudio, SurfaceWorkflow, SurfaceTerminal} {
			cfg, err := c.ResolveExecutionConfig(ctx, ExecutionRequest{Surface: surface, CLI: cli, Cwd: t.TempDir(), Local: &ChatLocalEndpoint{Endpoint: s.URL, Model: "fixture"}})
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
			exportedPath = cfg.Env["NODE_EXTRA_CA_CERTS"]
			bundle, err := os.ReadFile(exportedPath)
			if err != nil || string(bundle) != cert.PEM {
				t.Fatalf("%s / %s lost public CA export: %v", cli, surface, err)
			}
		}
	}
	if err := c.RemoveLocalHostCertificate(ctx, s.URL); err != nil {
		t.Fatal(err)
	}
	cfg := &EffectiveExecutionConfig{CLI: "praimate-code", Local: &ChatLocalEndpoint{Endpoint: s.URL}, Env: map[string]string{"PRAIMATE_HOST_TLS": "stale", "NODE_EXTRA_CA_CERTS": exportedPath}}
	if err := c.prepareCLIHostTLS(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Env["PRAIMATE_HOST_TLS"] != "{}" {
		t.Fatal("revoked consent retained")
	}
	if cfg.Env["NODE_EXTRA_CA_CERTS"] != "" {
		t.Fatal("revoked CA export retained")
	}
}
