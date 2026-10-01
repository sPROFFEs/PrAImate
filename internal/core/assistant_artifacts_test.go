package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/artifacts"
)

func TestManagedArtifactCatalogOfflineAndRestricted(t *testing.T) {
	c := nativeTestCore(t)
	s, err := artifacts.New(artifacts.Options{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	c.artifacts = s
	catalog, err := c.ManagedArtifactCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Items) != 7 || !catalog.DistributionReady || catalog.Active {
		t.Fatalf("unexpected catalog: %+v", catalog)
	}
	if _, err := c.InstallManagedArtifact(context.Background(), "../database", nil); err == nil {
		t.Fatal("installed an unknown artifact")
	}
	if err := c.RemoveManagedArtifact("../database"); err == nil {
		t.Fatal("removed an unknown artifact")
	}
	if _, err := c.VerifyManagedArtifact(context.Background(), catalog.Items[0].ArtifactID); err == nil {
		t.Fatal("verified a missing artifact")
	}

}

func TestManagedArtifactStopBlocksNewInstallations(t *testing.T) {
	c := nativeTestCore(t)
	s, err := artifacts.New(artifacts.Options{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	c.artifacts = s
	if err := c.StopManagedArtifactInstalls(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.InstallManagedArtifact(context.Background(), artifacts.Catalog()[0].ArtifactID, nil); err == nil {
		t.Fatal("restarted installer after shutdown/data reset")
	}
}

func TestManagedArtifactStopDrainsActiveDownload(t *testing.T) {
	payload := []byte(strings.Repeat("download fixture", 1024))
	hash := sha256.Sum256(payload)
	id := artifacts.Catalog()[5].ArtifactID
	m := artifacts.Manifest{Schema: 1, CatalogVersion: "test", Artifacts: map[string]artifacts.Artifact{id: {Filename: "fixture.bin", Format: "file", Size: int64(len(payload)), SHA256: hex.EncodeToString(hash[:]), LicenseName: "Test", LicenseURL: "https://example.com/license", UpstreamProject: "fixture", UpstreamVersion: "v1", UpstreamSHA256: hex.EncodeToString(hash[:])}}}
	raw, _ := json.Marshal(m)
	manifestHash := sha256.Sum256(raw)
	sums := []byte(fmt.Sprintf("%x  manifest.json\n%x  fixture.bin\n", manifestHash, hash))
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.json":
			w.Write(raw)
		case "/SHA256SUMS":
			w.Write(sums)
		case "/fixture.bin":
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.Write(payload[:512])
			w.(http.Flusher).Flush()
			close(started)
			<-r.Context().Done()
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c := nativeTestCore(t)
	var err error
	c.artifacts, err = artifacts.New(artifacts.Options{Root: t.TempDir(), ManifestURL: server.URL + "/manifest.json", Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { _, err := c.InstallManagedArtifact(context.Background(), id, nil); finished <- err }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("download did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.StopManagedArtifactInstalls(ctx); err != nil {
		t.Fatal("download did not drain", err)
	}
	if err := <-finished; err == nil {
		t.Fatal("incomplete download succeeded")
	}
	catalog, err := c.ManagedArtifactCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Active || c.artifacts.Installed(id) {
		t.Fatal("download still active or registered after stop")
	}
}
