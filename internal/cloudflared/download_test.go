package cloudflared

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fixtureTransport func(*http.Request) (*http.Response, error)

func (f fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestOfficialDownloadRequiresMatchingDigestBeforeInstallation(t *testing.T) {
	for _, valid := range []bool{false, true} {
		t.Run(fmt.Sprint(valid), func(t *testing.T) {
			body := "synthetic executable fixture, never run"
			digest := fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
			if !valid {
				digest = strings.Repeat("0", 64)
			}
			client := &http.Client{Transport: fixtureTransport(func(r *http.Request) (*http.Response, error) {
				text := body
				if r.URL.Host == "api.github.com" {
					text = fmt.Sprintf(`{"assets":[{"name":"cloudflared-linux-amd64","browser_download_url":"https://github.com/cloudflare/cloudflared/releases/download/fixture/cloudflared-linux-amd64","digest":"sha256:%s"}]}`, digest)
				} else if r.URL.Host != "github.com" {
					t.Fatal("unexpected download origin")
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(text))}, nil
			})}
			target := filepath.Join(t.TempDir(), "cloudflared")
			err := fetchBinary(context.Background(), target, client, "linux", "amd64")
			if valid {
				if err != nil {
					t.Fatal(err)
				}
				got, err := os.ReadFile(target)
				if err != nil || string(got) != body {
					t.Fatalf("wrong executable: %v", err)
				}
			} else {
				if err == nil {
					t.Fatal("unverified executable accepted")
				}
				if _, err = os.Stat(target); !os.IsNotExist(err) {
					t.Fatal("unverified download was installed")
				}
			}
		})
	}
}
