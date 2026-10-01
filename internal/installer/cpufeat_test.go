package installer

import (
	"errors"
	"strings"
	"testing"
)

func TestPraimateCodeAssetNameFor(t *testing.T) {
	cases := []struct {
		goos, goarch string
		baseline     bool
		want         string
	}{
		{"windows", "amd64", false, "praimate-code-windows-amd64.exe"},
		{"windows", "amd64", true, "praimate-code-windows-amd64-baseline.exe"},
		{"linux", "amd64", true, "praimate-code-linux-amd64-baseline"},
		{"darwin", "arm64", false, "praimate-code-darwin-arm64"},
		{"darwin", "amd64", false, "praimate-code-darwin-amd64"},
		{"darwin", "amd64", true, "praimate-code-darwin-amd64-baseline"},
		// baseline variants only exist for x64 — arm64 never gets the suffix
		{"darwin", "arm64", true, "praimate-code-darwin-arm64"},
	}
	for _, c := range cases {
		if got := praimateCodeAssetNameFor(c.goos, c.goarch, c.baseline); got != c.want {
			t.Errorf("praimateCodeAssetNameFor(%s,%s,%v) = %q, want %q",
				c.goos, c.goarch, c.baseline, got, c.want)
		}
	}
}

func TestIsIllegalInstruction(t *testing.T) {
	if IsIllegalInstruction(nil) {
		t.Error("nil should not be an illegal instruction")
	}
	// Windows exit-status string form (what CombinedOutput errors carry
	// once wrapped by fmt.Errorf in callers).
	if !IsIllegalInstruction(errors.New("exit status 0xc000001d")) {
		t.Error("windows 0xc000001d string form should match")
	}
	// Unix SIGILL form.
	if !IsIllegalInstruction(errors.New("signal: illegal instruction")) {
		t.Error("SIGILL string form should match")
	}
	if IsIllegalInstruction(errors.New("exit status 1")) {
		t.Error("plain exit status 1 should not match")
	}
}

func TestPraimateCodeMethodsVerifyAndFallback(t *testing.T) {
	methods := praimateCodeMethods(OSWindows)
	if len(methods) != 1 {
		t.Fatalf("praimateCodeMethods = %+v", methods)
	}
	m := methods[0]
	if !m.VerifyRun {
		t.Error("praimate-code install must verify the binary runs (AVX2 mismatch)")
	}
	// On amd64 hosts the fallback is the baseline asset unless the
	// primary already IS the baseline (no-AVX2 host). Either way the
	// two must never be the same non-empty asset.
	if m.FallbackAsset != "" {
		if m.FallbackAsset == m.DownloadAsset {
			t.Error("fallback asset must differ from the primary asset")
		}
		if !strings.Contains(m.FallbackAsset, "-baseline") {
			t.Errorf("fallback should be the baseline variant, got %q", m.FallbackAsset)
		}
	}
}
