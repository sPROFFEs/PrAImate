package installer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sPROFFEs/PrAImate/internal/updater"
	"github.com/sPROFFEs/PrAImate/internal/version"
)

func TestReleaseAssetURL_WebFallbackFindsStandaloneCLI(t *testing.T) {
	const asset = "praimate-cli-windows-amd64.exe"
	rel := &updater.Release{
		TagName: "1.2.10", AssetsIncomplete: true,
		Assets: []updater.Asset{{Name: "praimate-windows-amd64.zip", BrowserDownloadURL: "bundle-url"}},
	}
	got, size := releaseAssetURL(rel, asset)
	want := version.RepoURL + "/releases/download/1.2.10/" + asset
	if got != want || size != 0 {
		t.Fatalf("fallback asset = %q (%d), want %q", got, size, want)
	}
	rel.AssetsIncomplete = false
	if got, _ := releaseAssetURL(rel, asset); got != "" {
		t.Fatalf("complete API response must not invent missing asset: %q", got)
	}
	rel.Assets = append(rel.Assets, updater.Asset{Name: asset, BrowserDownloadURL: "published-url", Size: 123})
	if got, size := releaseAssetURL(rel, asset); got != "published-url" || size != 123 {
		t.Fatalf("published asset = %q (%d)", got, size)
	}
}

func TestDownloadTo_MissingFallbackAssetIsNotInstalled(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	dest := filepath.Join(t.TempDir(), "praimate-cli.exe")
	err := downloadTo(context.Background(), server.URL+"/missing", dest, 0)
	if !errors.Is(err, ErrNoPrebuiltAsset) || !strings.Contains(err.Error(), "404") {
		t.Fatalf("missing asset error = %v", err)
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("missing asset created destination: %v", statErr)
	}
}
