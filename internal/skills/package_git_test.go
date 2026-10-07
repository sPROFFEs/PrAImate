package skills

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNormalizeGitHubSkillFolderURLs(t *testing.T) {
	const root = "https://github.com/owner/repo"
	for _, tc := range []struct {
		name     string
		in, want GitPackageSource
	}{
		{"repository", GitPackageSource{Repository: root + ".git/", Ref: "feature/skills", Subpath: "skills/example"}, GitPackageSource{Repository: root, Ref: "feature/skills", Subpath: "skills/example"}},
		{"folder", GitPackageSource{Repository: root + "/tree/main/skills/example"}, GitPackageSource{Repository: root, Ref: "main", Subpath: "skills/example"}},
		{"tag", GitPackageSource{Repository: root + "/tree/v2.0/skills/example/"}, GitPackageSource{Repository: root, Ref: "v2.0", Subpath: "skills/example"}},
		{"branch root", GitPackageSource{Repository: root + "/tree/main", Subpath: "skills/example"}, GitPackageSource{Repository: root, Ref: "main", Subpath: "skills/example"}},
		{"explicit slash ref", GitPackageSource{Repository: root + "/tree/feature/skills/skills/example", Ref: "feature/skills"}, GitPackageSource{Repository: root, Ref: "feature/skills", Subpath: "skills/example"}},
		{"matching fields", GitPackageSource{Repository: root + "/tree/main/skills/example", Ref: "main", Subpath: "skills/example"}, GitPackageSource{Repository: root, Ref: "main", Subpath: "skills/example"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeGitHubPackageSource(tc.in)
			if err != nil || got != tc.want {
				t.Fatalf("got %+v, want %+v: %v", got, tc.want, err)
			}
		})
	}
	for _, in := range []GitPackageSource{
		{Repository: root + "/tree/main/skills/example", Ref: "other"},
		{Repository: root + "/tree/main/skills/example", Subpath: "skills/other"},
		{Repository: root + "/tree/main/../outside"},
		{Repository: root + "/tree/main/skills//example"},
		{Repository: root + "/blob/main/skills/example/SKILL.md"},
		{Repository: root + "/tree"},
		{Repository: root + "?token=fixture"},
		{Repository: "https://fixture@github.com/owner/repo"},
		{Repository: "https://github.com.example/owner/repo"},
	} {
		if _, err := normalizeGitHubPackageSource(in); err == nil {
			t.Fatalf("accepted invalid or conflicting input: %+v", in)
		}
	}
}

func TestGitSubpathScopesBeforeParsingUnrelatedManifests(t *testing.T) {
	var archive bytes.Buffer
	w := zip.NewWriter(&archive)
	for name, body := range map[string]string{
		"repo/skills/example/SKILL.md": "---\nname: example\ndescription: Test\n---\nInstructions",
		"repo/skills/other/SKILL.md":   "invalid unrelated manifest",
		"repo/README.md":               "repository overview",
	} {
		entry, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	candidates, shared, err := inspectGitHubPackageArchive(context.Background(), archive.Bytes(), "skills/example", PackageLimits{})
	if err != nil || len(candidates) != 1 || len(shared) != 0 {
		t.Fatalf("scoped import: candidates=%d shared=%v err=%v", len(candidates), shared, err)
	}
	if _, _, err := inspectGitHubPackageArchive(context.Background(), archive.Bytes(), "", PackageLimits{}); err == nil {
		t.Fatal("full inspection must still reject invalid manifests")
	}
	if _, _, err := inspectGitHubPackageArchive(context.Background(), archive.Bytes(), "skills/exam", PackageLimits{}); err == nil {
		t.Fatal("subpath matched a sibling prefix")
	}
}

func TestGitSourceSubpathConfinesResourcesAndSelection(t *testing.T) {
	const revision = "0123456789abcdef0123456789abcdef01234567"
	var archive bytes.Buffer
	w := zip.NewWriter(&archive)
	for name, body := range map[string]string{
		".github/workflows/check.yml":      "repository automation",
		"tools/validate.py":                "repository tooling",
		"skills/README.md":                 "shared catalogue instructions",
		"skills/example/SKILL.md":          "---\nname: example\ndescription: Test\n---\nInstructions",
		"skills/example/LICENSE":           "skill license",
		"skills/example/references/api.md": "skill reference",
		"skills/example/scripts/agent.py":  "skill helper",
		"skills/example-other/SKILL.md":    "---\nname: other\ndescription: Other\n---\nOther instructions",
	} {
		entry, err := w.Create("owner-repo-0123456/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/commits/main":
			rw.Write([]byte(`{"sha":"` + revision + `"}`))
		case "/repos/owner/repo/zipball/" + revision:
			rw.Write(archive.Bytes())
		default:
			http.Error(rw, "unexpected route", 404)
		}
	}))
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	policy := PackageNetworkPolicy{PrivateOrigins: []string{server.URL}, RootCAs: roots}
	for _, tc := range []struct {
		subpath    string
		candidates int
		shared     []string
	}{
		{"skills/example", 1, nil},
		{"skills", 2, []string{"skills/README.md"}},
		{"", 2, []string{".github/workflows/check.yml", "skills/README.md", "tools/validate.py"}},
	} {
		t.Run(tc.subpath, func(t *testing.T) {
			inspection, err := fetchGitHubPackages(context.Background(), GitPackageSource{Repository: "https://github.com/owner/repo", Ref: "main", Subpath: tc.subpath}, server.URL+"/repos/owner/repo", policy, PackageLimits{})
			if err != nil {
				t.Fatal(err)
			}
			if len(inspection.Candidates) != tc.candidates || !reflect.DeepEqual(inspection.Shared, tc.shared) {
				t.Fatalf("subpath=%q: candidates=%d shared=%v, want %v", tc.subpath, len(inspection.Candidates), inspection.Shared, tc.shared)
			}
			if tc.subpath != "skills/example" {
				return
			}
			candidate := inspection.Candidates[0]
			plan, err := SelectPackages(context.Background(), inspection.Candidates, []PackageSelection{{Candidate: 0, ExpectedDigest: candidate.Digest}}, PackageLimits{})
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, file := range plan[0].Files() {
				names = append(names, file.Path)
			}
			if want := []string{"LICENSE", "SKILL.md", "references/api.md", "scripts/agent.py"}; !reflect.DeepEqual(names, want) {
				t.Fatalf("wrong skill files: %v, want %v", names, want)
			}
			_, err = SelectPackages(context.Background(), inspection.Candidates, []PackageSelection{{Candidate: 0, ExpectedDigest: candidate.Digest, Shared: []SharedAssociation{{Source: "tools/validate.py", Destination: "tools/validate.py"}}}}, PackageLimits{})
			if err == nil {
				t.Fatal("scoped import still accepts an outside resource")
			}
		})
	}
}

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
