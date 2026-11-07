package cloudflared

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const downloadLimit = 128 << 20

func downloadBinary(ctx context.Context, target string) error {
	client := &http.Client{Timeout: 4 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		h := req.URL.Hostname()
		if len(via) > 5 || req.URL.Scheme != "https" || !(h == "github.com" || h == "api.github.com" || h == "release-assets.githubusercontent.com" || h == "objects.githubusercontent.com") {
			return errors.New("Untrusted cloudflared download redirect")
		}
		return nil
	}}
	return fetchBinary(ctx, target, client, runtime.GOOS, runtime.GOARCH)
}

func fetchBinary(ctx context.Context, target string, client *http.Client, platform, arch string) error {
	request, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/repos/cloudflare/cloudflared/releases/latest", nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "PrAImate-Server")
	response, err := client.Do(request)
	if err != nil {
		return errors.New("Cannot contact the official cloudflared release service")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("Official cloudflared release metadata returned HTTP %d", response.StatusCode)
	}
	var release struct {
		Assets []struct {
			Name   string `json:"name"`
			URL    string `json:"browser_download_url"`
			Digest string `json:"digest"`
		}
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&release); err != nil {
		return errors.New("Invalid cloudflared release metadata")
	}
	name := "cloudflared-" + platform + "-" + arch
	if platform == "windows" {
		name += ".exe"
	}
	if platform == "darwin" {
		name += ".tgz"
	}
	var source, digest string
	for _, asset := range release.Assets {
		if asset.Name == name {
			source = asset.URL
			digest = strings.TrimPrefix(asset.Digest, "sha256:")
			if !strings.HasPrefix(asset.Digest, "sha256:") {
				digest = ""
			}
			break
		}
	}
	expected, err := hex.DecodeString(digest)
	if err != nil || len(expected) != 32 || !strings.HasPrefix(source, "https://github.com/cloudflare/cloudflared/releases/download/") {
		return errors.New("No SHA-256 verified official cloudflared binary is available for this platform")
	}
	request, err = http.NewRequestWithContext(ctx, "GET", source, nil)
	if err != nil {
		return err
	}
	response, err = client.Do(request)
	if err != nil {
		return errors.New("Official cloudflared binary download failed")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("Cloudflared download returned HTTP %d", response.StatusCode)
	}
	file, err := os.CreateTemp(filepath.Dir(target), ".download-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(response.Body, downloadLimit+1))
	if err != nil || n > downloadLimit {
		return errors.New("Cloudflared download failed or exceeds the size limit")
	}
	if hex.EncodeToString(hash.Sum(nil)) != digest {
		return errors.New("Cloudflared SHA-256 verification failed")
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	var body []byte
	if platform == "darwin" {
		zipped, err := gzip.NewReader(file)
		if err != nil {
			return err
		}
		defer zipped.Close()
		archive := tar.NewReader(zipped)
		for {
			entry, err := archive.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			if entry.Name == "cloudflared" && entry.Typeflag == tar.TypeReg && entry.Size <= downloadLimit {
				body, err = io.ReadAll(io.LimitReader(archive, downloadLimit+1))
				if err != nil {
					return err
				}
				break
			}
		}
		if len(body) == 0 || len(body) > downloadLimit {
			return errors.New("Cloudflared archive does not contain a supported executable")
		}
	} else {
		body, err = io.ReadAll(file)
		if err != nil {
			return err
		}
	}
	if err = privateWrite(target, body); err != nil {
		return err
	}
	return os.Chmod(target, 0700)
}
