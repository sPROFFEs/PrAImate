package artifacts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
)

const maxManifestBytes = 2 << 20
const maxArtifactBytes int64 = (2 << 30) - 1

type Artifact struct {
	Filename        string `json:"filename"`
	Size            int64  `json:"size"`
	SHA256          string `json:"sha256"`
	Format          string `json:"format"` // file, zip, tar.gz
	EntryPoint      string `json:"entry_point,omitempty"`
	LicenseName     string `json:"license_name"`
	LicenseURL      string `json:"license_url"`
	LicenseText     string `json:"license_text,omitempty"`
	UpstreamProject string `json:"upstream_project"`
	UpstreamVersion string `json:"upstream_version"`
	UpstreamSHA256  string `json:"upstream_sha256"`
}

type Manifest struct {
	Schema         int                 `json:"schema"`
	CatalogVersion string              `json:"catalog_version"`
	GeneratedAt    string              `json:"generated_at"`
	Artifacts      map[string]Artifact `json:"artifacts"`
}

func safeRelative(name string) bool {
	if name == "" || strings.ContainsAny(name, "\\\x00:") || strings.HasPrefix(name, "/") || path.Clean(name) != name || name == "." || name == ".." || strings.HasPrefix(name, "../") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		// Windows normalizes trailing dots/spaces and interprets device names
		// specially. Reject these paths consistently on every publishing host.
		if part != strings.TrimSpace(part) || strings.HasSuffix(part, ".") {
			return false
		}
		stem := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" || (len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '1' && stem[3] <= '9') {
			return false
		}
	}
	return true
}

// VerifyManifest checks release checksums, not publisher signatures. Trust is
// provided by the configured GitHub release and its HTTPS transport.
func VerifyManifest(raw, checksums []byte) (*Manifest, error) {
	if len(raw) > maxManifestBytes {
		return nil, errors.New("model manifest is too large")
	}
	sums, err := ParseChecksums(checksums)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(raw)
	if sums["manifest.json"] != hex.EncodeToString(hash[:]) {
		return nil, errors.New("model manifest SHA-256 verification failed")
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse model manifest: %w", err)
	}
	if m.Schema != 1 || m.CatalogVersion == "" || len(m.Artifacts) == 0 || len(m.Artifacts) > 1000 {
		return nil, errors.New("unsupported or empty model manifest")
	}
	for id, a := range m.Artifacts {
		if sums[a.Filename] != a.SHA256 {
			return nil, fmt.Errorf("release checksum does not match artifact %q", id)
		}
		hash, err := hex.DecodeString(a.SHA256)
		if !safeRelative(id) || !safeRelative(a.Filename) || strings.Contains(a.Filename, "/") || len(hash) != 32 || err != nil || strings.ToLower(a.SHA256) != a.SHA256 || a.Size < 1 || a.Size > maxArtifactBytes {
			return nil, fmt.Errorf("invalid artifact %q", id)
		}
		if a.Format != "file" && a.Format != "zip" && a.Format != "tar.gz" {
			return nil, fmt.Errorf("unsupported format for %q", id)
		}
		if a.Format != "file" && !safeRelative(a.EntryPoint) {
			return nil, fmt.Errorf("invalid runtime entry point for %q", id)
		}
		if a.LicenseName == "" || a.LicenseURL == "" || a.UpstreamProject == "" || a.UpstreamVersion == "" || a.UpstreamSHA256 == "" {
			return nil, fmt.Errorf("missing artifact provenance for %q", id)
		}
		upstreamHash, hashErr := hex.DecodeString(a.UpstreamSHA256)
		license, licenseErr := url.Parse(a.LicenseURL)
		if hashErr != nil || len(upstreamHash) != 32 || licenseErr != nil || license.Host == "" || license.Scheme != "https" {
			return nil, fmt.Errorf("invalid artifact provenance for %q", id)
		}
	}
	return &m, nil
}

func ParseChecksums(raw []byte) (map[string]string, error) {
	if len(raw) > maxManifestBytes {
		return nil, errors.New("release checksums are too large")
	}
	sums := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		if len(line) < 67 || line[64] != ' ' || (line[65] != ' ' && line[65] != '*') {
			return nil, errors.New("invalid SHA256SUMS entry")
		}
		name, hash := line[66:], line[:64]
		decoded, err := hex.DecodeString(hash)
		if err != nil || len(decoded) != 32 || !safeRelative(name) || strings.Contains(name, "/") || sums[name] != "" {
			return nil, errors.New("invalid or duplicate SHA256SUMS entry")
		}
		sums[name] = strings.ToLower(hash)
	}
	if len(sums) == 0 {
		return nil, errors.New("empty release checksums")
	}
	return sums, nil
}
