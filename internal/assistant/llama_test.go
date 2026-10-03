package assistant

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The test executable doubles as a runtime that exits before its HTTP server
// starts. This also runs on Windows, without a shell or downloaded models.
func init() {
	if os.Getenv("PRAIMATE_ASSISTANT_EXIT_FIXTURE") == "1" {
		fmt.Fprintln(os.Stderr, "fixture: unsupported model architecture; key="+os.Getenv("LLAMA_API_KEY"))
		os.Exit(86)
	}
}

func TestRuntimeDiagnosticsBoundedAndClassifyLoaderFailures(t *testing.T) {
	var diagnostic runtimeDiagnostic
	large := strings.Repeat("noise", 10000)
	_, _ = diagnostic.Write([]byte(large))
	_, _ = diagnostic.Write([]byte("\nmodel load failed; key=fixture-key"))
	detail := diagnostic.detail("fixture-key")
	if len(detail) > 4098 || !strings.Contains(detail, "model load failed") || strings.Contains(detail, "fixture-key") {
		t.Fatal("startup output was unbounded, discarded, or exposed authentication")
	}
	for code, want := range map[int]string{0xc0000135: "Visual C++", 0xc000007b: "architecture", 0xc000001d: "CPU instruction", 0xc0000142: "initialize", 86: "86"} {
		if !strings.Contains(runtimeExitDescription(code), want) {
			t.Fatalf("exit %x lost diagnostic %q", code, want)
		}
	}
}

func TestPublishedWindowsRuntimeCRTDependencies(t *testing.T) {
	path := os.Getenv("PRAIMATE_TEST_LLAMA_WINDOWS_RUNTIME")
	if path == "" {
		t.Skip("set PRAIMATE_TEST_LLAMA_WINDOWS_RUNTIME to inspect a Windows runtime package")
	}
	if !filepath.IsAbs(path) {
		t.Fatal("runtime fixture path must be absolute")
	}
	deps, err := runtimeCRTImports(path)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(deps, ",")
	for _, dll := range []string{"msvcp140.dll", "vcruntime140.dll", "vcruntime140_1.dll"} {
		if !strings.Contains(joined, dll) {
			t.Fatalf("missing transitive CRT dependency %s: %v", dll, deps)
		}
	}
	t.Logf("Required Windows system CRT: %v", deps)
}

func TestLlamaStartupPreservesDiagnosticsWithoutAuthentication(t *testing.T) {
	t.Setenv("PRAIMATE_ASSISTANT_EXIT_FIXTURE", "1")
	p := &LlamaProvider{Runtime: os.Args[0], ModelPath: "fixture.gguf", Config: DefaultConfig()}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := p.Start(ctx)
	if err == nil || !strings.Contains(err.Error(), "unsupported model architecture") || !strings.Contains(err.Error(), "86") {
		t.Fatalf("startup diagnostic lost: %v", err)
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("runtime authentication was not redacted: %v", err)
	}
	if p.cmd != nil || p.http != nil {
		t.Fatal("failed startup retained a ready provider")
	}
}
