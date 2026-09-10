package skills

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

func TestGitSourcePinsDefaultAndSlashRefsThroughInstallation(t *testing.T) {
	const revision = "0123456789abcdef0123456789abcdef01234567"
	var archive bytes.Buffer
	w := zip.NewWriter(&archive)
	f, err := w.Create("owner-repo-0123456/skills/example/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte("---\nname: example\ndescription: Test\n---\noriginal snapshot")); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	var archiveRequests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/repos/owner/repo":
			rw.Write([]byte(`{"default_branch":"release/stable"}`))
		case "/repos/owner/repo/commits/release%2Fstable", "/repos/owner/repo/commits/feature%2Fskills":
			rw.Write([]byte(`{"sha":"` + revision + `"}`))
		case "/repos/owner/repo/zipball/" + revision:
			archiveRequests.Add(1)
			rw.Write(archive.Bytes())
		default:
			http.Error(rw, "unexpected route", 404)
		}
	}))
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	policy := PackageNetworkPolicy{PrivateOrigins: []string{server.URL}, RootCAs: roots}
	for _, ref := range []string{"", "feature/skills"} {
		inspection, err := fetchGitHubPackages(context.Background(), GitPackageSource{Repository: "https://github.com/owner/repo", Ref: ref, Subpath: "skills"}, server.URL+"/repos/owner/repo", policy, PackageLimits{})
		if err != nil {
			t.Fatal(err)
		}
		if inspection.ResolvedRevision != revision || len(inspection.Candidates) != 1 || inspection.Candidates[0].Subpath != "skills/example" {
			t.Fatalf("incorrect resolution: %+v", inspection)
		}
		plan, err := SelectPackages(context.Background(), inspection.Candidates, []PackageSelection{{Candidate: 0, ExpectedDigest: inspection.Candidates[0].Digest}}, PackageLimits{})
		if err != nil {
			t.Fatal(err)
		}
		before := archiveRequests.Load()
		root, err := os.OpenRoot(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		generation, err := InstallPackages(context.Background(), root, plan)
		if err != nil {
			root.Close()
			t.Fatal(err)
		}
		body, err := root.ReadFile(generation + "/objects/" + plan[0].Digest()[7:] + "/SKILL.md")
		root.Close()
		if err != nil || !strings.Contains(string(body), "original snapshot") || archiveRequests.Load() != before {
			t.Fatal("installation did not use inspected snapshot")
		}
	}
}

func TestPackageRedirectDoesNotReadUnapprovedDestination(t *testing.T) {
	var hits atomic.Int32
	destination := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.Write([]byte("must not be read")) }))
	defer destination.Close()
	for _, target := range []string{destination.URL, "https://169.254.169.254/latest/meta-data", "https://127.0.0.1/secret"} {
		t.Run(target, func(t *testing.T) {
			source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target, http.StatusFound) }))
			defer source.Close()
			roots := x509.NewCertPool()
			roots.AddCert(source.Certificate())
			roots.AddCert(destination.Certificate())
			_, _, err := FetchPackageZIP(context.Background(), source.URL, PackageNetworkPolicy{PrivateOrigins: []string{source.URL}, RootCAs: roots}, PackageLimits{})
			if err == nil || hits.Load() != 0 {
				t.Fatal("redirect reached unapproved private destination")
			}
		})
	}
}
