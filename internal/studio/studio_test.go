package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"git.jtsec.local/lab/PrAImate/internal/core"
	"git.jtsec.local/lab/PrAImate/internal/store"
)

func newTestCore(t *testing.T) (*core.Core, func()) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.InitializeWithPassword(filepath.Join(dir, "test.db"), "correct horse battery staple")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	c, err := core.New(core.Options{Store: st})
	if err != nil {
		st.Close()
		t.Fatalf("new core: %v", err)
	}
	if _, err := c.SeedBuiltins(context.Background()); err != nil {
		st.Close()
		t.Fatalf("seed builtins: %v", err)
	}
	return c, func() { _ = st.Close() }
}

func TestStudioManagerInstallAndStatus(t *testing.T) {
	c, cleanup := newTestCore(t)
	defer cleanup()

	tmpHome := t.TempDir()
	t.Setenv("PRAIMATE_HOME", tmpHome)
	// Provisioning tests must never download or launch a real editor.
	app, err := AppDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(app, 0755); err != nil {
		t.Fatal(err)
	}
	name := "codium"
	if runtime.GOOS == "windows" {
		name = "VSCodium.exe"
	}
	if err := os.WriteFile(filepath.Join(app, name), []byte("test fixture"), 0755); err != nil {
		t.Fatal(err)
	}

	mgr := NewManager(c)
	ctx := context.Background()

	status, err := mgr.GetStatus(ctx)
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if status == nil {
		t.Fatal("expected non-nil status")
	}

	installed, err := mgr.Install(ctx)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if installed == nil {
		t.Fatal("expected non-nil status after install")
	}

	extDir, err := ExtensionsDir()
	if err != nil {
		t.Fatalf("ExtensionsDir: %v", err)
	}
	pkgJSON := filepath.Join(extDir, "praimate", "package.json")
	if _, err := os.Stat(pkgJSON); err != nil {
		t.Fatalf("expected package.json to be created: %v", err)
	}
}

func TestStudioRPCServerDispatch(t *testing.T) {
	c, cleanup := newTestCore(t)
	defer cleanup()

	srv := NewServer(c)
	defer srv.Close()

	tests := []struct {
		method string
		params any
		check  func(t *testing.T, res any)
	}{
		{
			method: "system.initialize",
			params: map[string]string{"client": "test"},
			check: func(t *testing.T, res any) {
				m, ok := res.(map[string]any)
				if !ok || m["protocolVersion"] != "1" {
					t.Fatalf("unexpected initialize res: %+v", res)
				}
			},
		},
		{
			method: "system.version",
			params: nil,
			check: func(t *testing.T, res any) {
				m, ok := res.(map[string]string)
				if !ok || m["name"] != "PrAImate" {
					t.Fatalf("unexpected version res: %+v", res)
				}
			},
		},
		{
			method: "agents.list",
			params: nil,
			check: func(t *testing.T, res any) {
				arr, ok := res.([]map[string]any)
				if !ok || len(arr) == 0 {
					t.Fatalf("expected agents list, got %+v", res)
				}
			},
		},
		{
			method: "tools.list",
			params: nil,
			check: func(t *testing.T, res any) {
				if res == nil {
					t.Fatal("expected non-nil tools list")
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.method, func(t *testing.T) {
			resp := srv.dispatch(RPCRequest{
				JSONRPC: "2.0",
				ID:      1,
				Method:  tc.method,
				Params:  tc.params,
			})
			if resp.Error != nil {
				t.Fatalf("rpc error for %s: %+v", tc.method, resp.Error)
			}
			tc.check(t, resp.Result)
		})
	}
}

func TestStudioRPCServerStdio(t *testing.T) {
	c, cleanup := newTestCore(t)
	defer cleanup()

	srv := NewServer(c)
	defer srv.Close()

	req := RPCRequest{
		JSONRPC: "2.0",
		ID:      42,
		Method:  "system.version",
	}
	reqData, _ := json.Marshal(req)
	in := bytes.NewReader(append(reqData, '\n'))
	var out bytes.Buffer

	_ = srv.ServeStdio(in, &out)

	var resp RPCResponse
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v, raw: %s", err, out.String())
	}
	if resp.Error != nil {
		t.Fatalf("unexpected rpc error: %+v", resp.Error)
	}
}
