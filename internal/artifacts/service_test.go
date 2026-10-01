package artifacts

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type distributionFixture struct {
	s              *Service
	server         *httptest.Server
	payload        []byte
	raw, checksums []byte
	fail           bool
	rangeSeen      string
}

func newDistribution(t *testing.T, payload []byte, format, entry string) *distributionFixture {
	t.Helper()
	f := &distributionFixture{payload: payload}
	f.metadata(t, format, entry)
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.fail {
			http.Error(w, "offline", 503)
			return
		}
		switch r.URL.Path {
		case "/manifest.json":
			w.Write(f.raw)
		case "/SHA256SUMS":
			w.Write(f.checksums)
		case "/model.bin":
			start := 0
			if v := r.Header.Get("Range"); v != "" {
				f.rangeSeen = v
				fmt.Sscanf(v, "bytes=%d-", &start)
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(f.payload)-1, len(f.payload)))
				w.WriteHeader(http.StatusPartialContent)
			}
			w.Write(f.payload[start:])
		default:
			t.Errorf("unexpected upstream fallback %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.server.Close)
	var err error
	f.s, err = New(Options{Root: t.TempDir(), ManifestURL: f.server.URL + "/manifest.json", Client: f.server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *distributionFixture) metadata(t *testing.T, format, entry string) {
	hash := sha256.Sum256(f.payload)
	m := Manifest{Schema: 1, CatalogVersion: "test-v1", Artifacts: map[string]Artifact{"test/model/v1": {Filename: "model.bin", Size: int64(len(f.payload)), SHA256: hex.EncodeToString(hash[:]), Format: format, EntryPoint: entry, LicenseName: "Test", LicenseURL: "https://example.com/license", UpstreamProject: "fixture", UpstreamVersion: "v1", UpstreamSHA256: hex.EncodeToString(hash[:])}}}
	var err error
	f.raw, err = json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	manifestHash := sha256.Sum256(f.raw)
	f.checksums = []byte(fmt.Sprintf("%x  manifest.json\n%x  model.bin\n", manifestHash, hash))
}

func TestChecksumInstallResumeOfflineAndTampering(t *testing.T) {
	f := newDistribution(t, bytes.Repeat([]byte("verified-model"), 200), "file", "")
	a := sha256.Sum256(f.payload)
	partial := filepath.Join(f.s.root, "downloads", hex.EncodeToString(a[:])+".part")
	if err := os.MkdirAll(filepath.Dir(partial), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(partial, f.payload[:75], 0600); err != nil {
		t.Fatal(err)
	}
	i, err := f.s.Install(context.Background(), "test/model/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.rangeSeen != "bytes=75-" {
		t.Fatal("download was not resumed", f.rangeSeen)
	}
	f.fail = true
	if _, err := f.s.Verify(context.Background(), i.ArtifactID); err != nil {
		t.Fatal("installed model requires network", err)
	}
	if _, err := f.s.Install(context.Background(), i.ArtifactID, nil); err == nil {
		t.Fatal("offline update succeeded")
	}
	if _, err := f.s.Verify(context.Background(), i.ArtifactID); err != nil {
		t.Fatal("offline failure removed install", err)
	}
	corrupt := append([]byte(nil), f.payload...)
	corrupt[0] ^= 0xff
	if err := os.WriteFile(i.Path, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Verify(context.Background(), i.ArtifactID); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatal("accepted corrupted model", err)
	}
}

func TestManifestAndArtifactHashesMustMatch(t *testing.T) {
	f := newDistribution(t, []byte("safe model"), "file", "")
	f.raw = bytes.ReplaceAll(f.raw, []byte("test-v1"), []byte("evil-v1"))
	if _, err := f.s.Install(context.Background(), "test/model/v1", nil); err == nil {
		t.Fatal("accepted manifest checksum mismatch")
	}
	f.metadata(t, "file", "")
	f.payload[0] ^= 0xff
	if _, err := f.s.Install(context.Background(), "test/model/v1", nil); err == nil {
		t.Fatal("accepted bad payload")
	}
	if f.s.Installed("test/model/v1") {
		t.Fatal("registered invalid payload")
	}
}

func runtimeZip(t *testing.T, name string, mode os.FileMode) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	h := &zip.FileHeader{Name: name, Method: zip.Deflate}
	h.SetMode(mode)
	f, err := w.CreateHeader(h)
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte("runtime payload"))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestRuntimeArchiveRejectsTraversalAndLinks(t *testing.T) {
	for _, test := range []struct {
		name string
		mode os.FileMode
	}{{"../escape", 0700}, {"bin/link", os.ModeSymlink | 0777}, {"C:/escape", 0700}, {".. /escape", 0700}, {"bin/CON.exe", 0700}} {
		t.Run(test.name, func(t *testing.T) {
			f := newDistribution(t, runtimeZip(t, test.name, test.mode), "zip", "bin/server")
			if _, err := f.s.Install(context.Background(), "test/model/v1", nil); err == nil {
				t.Fatal("accepted unsafe archive")
			}
		})
	}
}

func TestRuntimeExtractedFilesVerifiedAndRemoveIsIsolated(t *testing.T) {
	f := newDistribution(t, runtimeZip(t, "bin/server", 0700), "zip", "bin/server")
	i, err := f.s.Install(context.Background(), "test/model/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Verify(context.Background(), i.ArtifactID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(i.Path, []byte("changed payload"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Verify(context.Background(), i.ArtifactID); err == nil {
		t.Fatal("accepted replaced executable")
	}
	sentinel := filepath.Join(f.s.root, "unrelated")
	os.WriteFile(sentinel, []byte("keep"), 0600)
	if err := f.s.Remove(i.ArtifactID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatal("removed unrelated application data")
	}
}

func TestMissingChecksumsFailsClosed(t *testing.T) {
	f := newDistribution(t, []byte("safe model"), "file", "")
	f.checksums = nil
	if _, err := f.s.Install(context.Background(), "test/model/v1", nil); err == nil {
		t.Fatal("installed without release checksums")
	}
}

func TestFailedReplacementPreservesExistingModel(t *testing.T) {
	f := newDistribution(t, []byte("original model"), "file", "")
	i, err := f.s.Install(context.Background(), "test/model/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	f.payload = []byte("replacement model")
	f.metadata(t, "file", "")
	f.payload[0] ^= 0xff
	if _, err := f.s.Install(context.Background(), i.ArtifactID, nil); err == nil {
		t.Fatal("accepted invalid update")
	}
	if _, err := f.s.Verify(context.Background(), i.ArtifactID); err != nil {
		t.Fatal("destroyed previous model", err)
	}
}

func TestInterruptedPromotionKeepsPreviousInstallReadable(t *testing.T) {
	f := newDistribution(t, []byte("valid previous model"), "file", "")
	i, err := f.s.Install(context.Background(), "test/model/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(f.s.slot(i.ArtifactID), f.s.slot(i.ArtifactID)+".previous"); err != nil {
		t.Fatal(err)
	}
	f.fail = true
	if _, err := f.s.Verify(context.Background(), i.ArtifactID); err != nil {
		t.Fatal("crash hid valid previous install", err)
	}
	if err := f.s.Remove(i.ArtifactID); err != nil {
		t.Fatal(err)
	}
	if f.s.Installed(i.ArtifactID) {
		t.Fatal("previous install survived explicit removal")
	}
}

func TestPublishedArtifactIDCannotChangeBytes(t *testing.T) {
	f := newDistribution(t, []byte("original immutable model"), "file", "")
	i, err := f.s.Install(context.Background(), "test/model/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	f.payload = []byte("valid but different model")
	f.metadata(t, "file", "")
	if _, err := f.s.Install(context.Background(), i.ArtifactID, nil); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatal("accepted mutated published artifact", err)
	}
	if _, err := f.s.Verify(context.Background(), i.ArtifactID); err != nil {
		t.Fatal("lost immutable original", err)
	}
}
