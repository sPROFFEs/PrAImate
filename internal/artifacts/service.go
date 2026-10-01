package artifacts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Options struct {
	Root        string
	ManifestURL string
	Client      *http.Client
}

type Service struct {
	mu          sync.Mutex
	root        string
	manifestURL string
	client      *http.Client
}

type Progress struct {
	ArtifactID string `json:"artifact_id"`
	Phase      string `json:"phase"`
	Downloaded int64  `json:"downloaded"`
	Total      int64  `json:"total"`
}

type Installation struct {
	ArtifactID     string   `json:"artifact_id"`
	CatalogVersion string   `json:"catalog_version"`
	Artifact       Artifact `json:"artifact"`
	Path           string   `json:"path"`
}

func New(o Options) (*Service, error) {
	if o.Root == "" {
		return nil, errors.New("artifact storage root is required")
	}
	if o.ManifestURL == "" {
		o.ManifestURL = ManifestURL
	}
	u, err := url.Parse(o.ManifestURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"))) {
		return nil, errors.New("invalid model manifest endpoint")
	}
	if o.Client == nil {
		o.Client = &http.Client{Timeout: 30 * time.Minute}
	}
	root, err := filepath.Abs(o.Root)
	if err != nil {
		return nil, err
	}
	return &Service{root: root, manifestURL: o.ManifestURL, client: o.Client}, nil
}

func (s *Service) fetch(ctx context.Context, endpoint string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "PrAImate-Artifact-Manager")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("model distribution unavailable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("model distribution returned HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("distribution response exceeds size limit")
	}
	return raw, nil
}

func (s *Service) manifest(ctx context.Context) (*Manifest, []byte, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	raw, err := s.fetch(ctx, s.manifestURL, maxManifestBytes)
	if err != nil {
		return nil, nil, nil, err
	}
	sumsURL := strings.TrimSuffix(s.manifestURL, "manifest.json") + "SHA256SUMS"
	sums, err := s.fetch(ctx, sumsURL, maxManifestBytes)
	if err != nil {
		return nil, nil, nil, err
	}
	m, err := VerifyManifest(raw, sums)
	return m, raw, sums, err
}

// Refresh is explicit: catalog rendering and already-installed runtimes do not
// require network access or silently fetch remote manifests at startup.
func (s *Service) Refresh(ctx context.Context) (*Manifest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, _, _, err := s.manifest(ctx)
	return m, err
}

func artifactSlot(id string) string {
	hash := sha256.Sum256([]byte(id))
	return hex.EncodeToString(hash[:])
}
func (s *Service) slot(id string) string { return filepath.Join(s.root, "installed", artifactSlot(id)) }

func (s *Service) installedBase(id string) string {
	base := s.slot(id)
	// A crash between replacement renames must not hide the old installation.
	if _, err := os.Stat(base); errors.Is(err, os.ErrNotExist) {
		if _, err := os.Stat(base + ".previous"); err == nil {
			return base + ".previous"
		}
	}
	return base
}

func readBoundedFile(name string, limit int64) ([]byte, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("installed receipt exceeds size limit")
	}
	return raw, nil
}

func (s *Service) receipt(id string) (*Installation, error) {
	base := s.installedBase(id)
	raw, err := readBoundedFile(filepath.Join(base, "manifest.json"), maxManifestBytes)
	if err != nil {
		return nil, err
	}
	sums, err := readBoundedFile(filepath.Join(base, "SHA256SUMS"), maxManifestBytes)
	if err != nil {
		return nil, err
	}
	m, err := VerifyManifest(raw, sums)
	if err != nil {
		return nil, err
	}
	a, ok := m.Artifacts[id]
	if !ok {
		return nil, errors.New("installed receipt does not contain requested artifact")
	}
	path := filepath.Join(base, "payload")
	if a.Format != "file" {
		path = filepath.Join(base, "files", filepath.FromSlash(a.EntryPoint))
	}
	return &Installation{ArtifactID: id, CatalogVersion: m.CatalogVersion, Artifact: a, Path: path}, nil
}

func verifyPayload(ctx context.Context, name string, a Artifact) error {
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != a.Size {
		return errors.New("artifact size verification failed")
	}
	h := sha256.New()
	if _, err := io.Copy(h, &contextReader{ctx: ctx, r: f}); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != a.SHA256 {
		return errors.New("artifact SHA-256 verification failed")
	}
	return nil
}

func (s *Service) Verify(ctx context.Context, id string) (*Installation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.verify(ctx, id)
}

func (s *Service) verify(ctx context.Context, id string) (*Installation, error) {
	i, err := s.receipt(id)
	if err != nil {
		return nil, err
	}
	base := s.installedBase(id)
	payload := filepath.Join(base, "payload")
	if err := verifyPayload(ctx, payload, i.Artifact); err != nil {
		return nil, err
	}
	if i.Artifact.Format != "file" {
		if err := walkArchive(ctx, payload, i.Artifact.Format, filepath.Join(base, "files"), false); err != nil {
			return nil, err
		}
		info, err := os.Stat(i.Path)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, errors.New("runtime entry point is not a regular file")
		}
	}
	return i, nil
}

func (s *Service) Installed(id string) bool {
	_, err := s.receipt(id)
	return err == nil
}

// Remove never touches another artifact or the shared application database.
func (s *Service) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !safeRelative(id) {
		return errors.New("invalid artifact ID")
	}
	if err := os.RemoveAll(s.slot(id)); err != nil {
		return err
	}
	return os.RemoveAll(s.slot(id) + ".previous")
}

func (s *Service) Install(ctx context.Context, id string, emit func(Progress)) (*Installation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, raw, sums, err := s.manifest(ctx)
	if err != nil {
		return nil, err
	}
	a, ok := m.Artifacts[id]
	if !ok {
		return nil, fmt.Errorf("artifact %q is not published in the release catalog", id)
	}
	if current, err := s.receipt(id); err == nil && current.Artifact.SHA256 != a.SHA256 {
		return nil, errors.New("immutable artifact changed; publish the replacement under a new artifact ID")
	}
	if current, err := s.verify(ctx, id); err == nil {
		return current, nil
	}
	if err := os.MkdirAll(filepath.Join(s.root, "downloads"), 0700); err != nil {
		return nil, err
	}
	partial := filepath.Join(s.root, "downloads", a.SHA256+".part")
	progress := func(phase string, n int64) {
		if emit != nil {
			emit(Progress{ArtifactID: id, Phase: phase, Downloaded: n, Total: a.Size})
		}
	}
	assetURL := strings.TrimSuffix(s.manifestURL, "manifest.json") + url.PathEscape(a.Filename)
	if err := s.download(ctx, assetURL, partial, a, func(n int64) { progress("downloading", n) }); err != nil {
		return nil, err
	}
	progress("verifying", a.Size)
	if err := verifyPayload(ctx, partial, a); err != nil {
		_ = os.Remove(partial)
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(s.root, "installed"), 0700); err != nil {
		return nil, err
	}
	staging, err := os.MkdirTemp(filepath.Join(s.root, "installed"), ".install-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(staging)
	// Keep the verified package for offline validation, including runtime files.
	if err := copyFile(ctx, partial, filepath.Join(staging, "payload")); err != nil {
		return nil, err
	}
	if a.Format != "file" {
		progress("extracting", a.Size)
		if err := walkArchive(ctx, filepath.Join(staging, "payload"), a.Format, filepath.Join(staging, "files"), true); err != nil {
			return nil, err
		}
		entry := filepath.Join(staging, "files", filepath.FromSlash(a.EntryPoint))
		info, err := os.Stat(entry)
		if err != nil || !info.Mode().IsRegular() {
			return nil, errors.New("runtime package is missing its entry point")
		}
		if err := os.Chmod(entry, 0700); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(filepath.Join(staging, "manifest.json"), raw, 0600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(staging, "SHA256SUMS"), sums, 0600); err != nil {
		return nil, err
	}
	// The old install remains usable until the complete replacement is ready.
	final := s.slot(id)
	backup := final + ".previous"
	_ = os.RemoveAll(backup)
	hadPrevious := false
	if _, err := os.Stat(final); err == nil {
		if err := os.Rename(final, backup); err != nil {
			return nil, err
		}
		hadPrevious = true
	}
	if err := os.Rename(staging, final); err != nil {
		if hadPrevious {
			_ = os.Rename(backup, final)
		}
		return nil, err
	}
	_ = os.RemoveAll(backup)
	_ = os.Remove(partial)
	progress("installed", a.Size)
	return s.receipt(id)
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func copyFile(ctx context.Context, source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, &contextReader{ctx: ctx, r: in})
	if err == nil {
		err = out.Sync()
	}
	closeErr := out.Close()
	if err == nil {
		err = closeErr
	}
	return err
}

func (s *Service) download(ctx context.Context, endpoint, target string, a Artifact, progress func(int64)) error {
	var offset int64
	if info, err := os.Stat(target); err == nil {
		offset = info.Size()
	}
	if offset > a.Size {
		if err := os.Remove(target); err != nil {
			return err
		}
		offset = 0
	}
	if offset == a.Size {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept-Encoding", "identity")
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	flags := os.O_CREATE | os.O_WRONLY
	switch resp.StatusCode {
	case http.StatusOK:
		offset = 0
		flags |= os.O_TRUNC
	case http.StatusPartialContent:
		var start, end, total int64
		_, err := fmt.Sscanf(resp.Header.Get("Content-Range"), "bytes %d-%d/%d", &start, &end, &total)
		if err != nil || start != offset || total != a.Size || end != a.Size-1 {
			return errors.New("invalid resumed artifact range")
		}
		flags |= os.O_APPEND
	default:
		return fmt.Errorf("artifact download returned HTTP %d", resp.StatusCode)
	}
	if length := resp.Header.Get("Content-Length"); length != "" {
		n, err := strconv.ParseInt(length, 10, 64)
		if err != nil || n != a.Size-offset {
			return errors.New("artifact download length does not match manifest")
		}
	}
	f, err := os.OpenFile(target, flags, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	progress(offset)
	buffer := make([]byte, 128<<10)
	reader := io.LimitReader(&contextReader{ctx: ctx, r: resp.Body}, a.Size-offset+1)
	last := time.Now()
	for {
		n, readErr := reader.Read(buffer)
		if n > 0 {
			if offset+int64(n) > a.Size {
				return errors.New("artifact download exceeds expected size")
			}
			if _, err := f.Write(buffer[:n]); err != nil {
				return err
			}
			offset += int64(n)
			if time.Since(last) > 200*time.Millisecond {
				progress(offset)
				last = time.Now()
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				return readErr
			}
			break
		}
	}
	if offset != a.Size {
		return io.ErrUnexpectedEOF
	}
	if err := f.Sync(); err != nil {
		return err
	}
	progress(offset)
	return nil
}
