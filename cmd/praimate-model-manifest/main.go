// praimate-model-manifest is an offline publisher utility. It never downloads
// upstream models or uploads assets. It produces SHA256SUMS without signing keys.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/artifacts"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	input := flag.String("input", "", "Manifest template with license/provenance metadata")
	assets := flag.String("assets", "", "Directory containing release payloads")
	out := flag.String("out", "", "Output directory for manifest.json and SHA256SUMS")
	flag.Parse()
	if *input == "" || *assets == "" || *out == "" {
		return errors.New("provide --input, --assets and --out")
	}
	return publish(*input, *assets, *out, os.Stdout)
}

func publish(input, assets, out string, log io.Writer) error {
	raw, err := os.ReadFile(input)
	if err != nil {
		return err
	}
	var m artifacts.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	m.Schema = 1
	m.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	checksums := map[string]string{}
	for id, a := range m.Artifacts {
		if a.Filename == "" || filepath.Base(a.Filename) != a.Filename || strings.ContainsAny(a.Filename, "/\\") || a.Filename == "manifest.json" || a.Filename == "SHA256SUMS" {
			return fmt.Errorf("invalid filename for %s", id)
		}
		f, err := os.Open(filepath.Join(assets, a.Filename))
		if err != nil {
			return err
		}
		hash := sha256.New()
		a.Size, err = io.Copy(hash, f)
		f.Close()
		if err != nil {
			return err
		}
		a.SHA256 = hex.EncodeToString(hash.Sum(nil))
		checksums[a.Filename] = a.SHA256
		m.Artifacts[id] = a
	}
	raw, err = json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	hash := sha256.Sum256(raw)
	checksums["manifest.json"] = hex.EncodeToString(hash[:])
	names := make([]string, 0, len(checksums))
	for name := range checksums {
		names = append(names, name)
	}
	sort.Strings(names)
	var sums strings.Builder
	for _, name := range names {
		fmt.Fprintf(&sums, "%s  %s\n", checksums[name], name)
	}
	if _, err := artifacts.VerifyManifest(raw, []byte(sums.String())); err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "manifest.json"), raw, 0644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "SHA256SUMS"), []byte(sums.String()), 0644); err != nil {
		return err
	}
	fmt.Fprintln(log, "Prepared catalog:", m.CatalogVersion)
	return nil
}
