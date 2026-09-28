package updater

import (
	"runtime"
	"strings"
	"testing"
)

func TestIsNewer(t *testing.T) {
	cases := []struct {
		remote, local string
		want          bool
	}{
		{"0.1.0", "0.1.0", false},
		{"v0.1.1", "0.1.0", true},
		{"v0.1.0", "0.1.1", false},
		{"v1.0.0", "0.9.9", true},
		{"v0.2.0-rc1", "0.1.5", true},
		{"v0.1.0", "v0.1.0", false},
		{"v0.1", "0.1.0", false}, // missing patch parses as 0
		{"v0.1.0+build5", "v0.1.0", false},
	}
	for _, c := range cases {
		got := IsNewer(c.remote, c.local)
		if got != c.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", c.remote, c.local, got, c.want)
		}
	}
}

func TestAssetForHost(t *testing.T) {
	rel := &Release{
		TagName: "v0.2.0",
		Assets: []Asset{
			{Name: "praimate-0.2.0-linux-amd64.tar.gz", BrowserDownloadURL: "u1"},
			{Name: "praimate-0.2.0-linux-arm64.tar.gz", BrowserDownloadURL: "u2"},
			{Name: "praimate-0.2.0-windows-amd64.zip", BrowserDownloadURL: "u5"},
		},
	}
	a, err := AssetForHost(rel)
	if err != nil {
		t.Fatalf("AssetForHost: %v", err)
	}
	triplet := runtime.GOOS + "-" + runtime.GOARCH
	if !strings.Contains(a.Name, triplet) {
		t.Errorf("asset %q does not mention host triplet %q", a.Name, triplet)
	}
}

func TestAssetForPlatform_SupportsMacOSARM64(t *testing.T) {
	rel := &Release{
		TagName: "v0.2.0",
		Assets:  []Asset{{Name: "praimate-darwin-arm64.tar.gz"}},
	}
	a, err := assetForPlatform(rel, "darwin", "arm64")
	if err != nil {
		t.Fatalf("expected darwin-arm64 asset to resolve, got %v", err)
	}
	if a.Name != "praimate-darwin-arm64.tar.gz" {
		t.Errorf("unexpected asset: %s", a.Name)
	}
}

func TestAssetForPlatform_RejectsUnsupportedOS(t *testing.T) {
	rel := &Release{
		TagName: "v0.2.0",
		Assets:  []Asset{{Name: "praimate-0.2.0-plan9-mips.tar.gz"}},
	}
	if _, err := assetForPlatform(rel, "plan9", "mips"); err == nil {
		t.Fatal("plan9 must not resolve a release asset")
	}
}

func TestAssetForHost_NoMatch(t *testing.T) {
	rel := &Release{
		TagName: "v0.2.0",
		Assets:  []Asset{{Name: "praimate-0.2.0-plan9-mips.tar.gz"}},
	}
	if _, err := AssetForHost(rel); err == nil {
		t.Fatalf("expected error when no asset matches host")
	}
}
