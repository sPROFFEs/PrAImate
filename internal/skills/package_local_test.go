package skills

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestInspectPackageDirectoryMatchesZIPSnapshot(t *testing.T) {
	dir := t.TempDir()
	contents := map[string]string{
		"a/SKILL.md":           "---\nname: alpha\ndescription: Alpha\n---\nBODY\r\n",
		"a/references/doc.txt": "exact\x00bytes\r\n",
		"a/b/SKILL.md":         "---\nname: beta\ndescription: Beta\n---\nNESTED",
		"LICENSE":              "synthetic license",
	}
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range contents {
		filename := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	local, shared, err := InspectPackageDirectory(context.Background(), dir, PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	zipped, zipShared, err := InspectPackageZIP(context.Background(), bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(local) != len(zipped) || !reflect.DeepEqual(shared, zipShared) {
		t.Fatal("candidate/shared mismatch")
	}
	for i := range local {
		if local[i].Digest != zipped[i].Digest || local[i].Manifest.Body != zipped[i].Manifest.Body {
			t.Fatal("local/ZIP snapshot differs")
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "a", "SKILL.md"), []byte("changed after preview"), 0600); err != nil {
		t.Fatal(err)
	}
	if local[0].Manifest.Body != "BODY\r\n" {
		t.Fatal("preview changed with source")
	}
	if _, _, err := InspectPackageDirectory(context.Background(), dir, PackageLimits{Entries: 1}); err == nil {
		t.Fatal("entry budget ignored")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := InspectPackageDirectory(ctx, dir, PackageLimits{}); err == nil {
		t.Fatal("cancel ignored")
	}
}

func TestInspectPackageDirectoryRejectsLinks(t *testing.T) {
	for _, hard := range []bool{false, true} {
		t.Run(map[bool]string{false: "symlink", true: "hardlink"}[hard], func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(t.TempDir(), "source")
			if err := os.WriteFile(target, []byte("---\nname: test\ndescription: Test\n---\n"), 0600); err != nil {
				t.Fatal(err)
			}
			link := os.Symlink
			if hard {
				link = os.Link
			}
			if err := link(target, filepath.Join(dir, "SKILL.md")); err != nil {
				t.Skipf("platform cannot create fixture: %v", err)
			}
			if _, _, err := InspectPackageDirectory(context.Background(), dir, PackageLimits{}); err == nil {
				t.Fatal("link accepted")
			}
		})
	}
}

func TestReadLocalPackageRejectsReplacement(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "file")
	if err := os.WriteFile(name, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(name, filepath.Join(dir, "original")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte("after"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, _, err := readLocalPackageFile(context.Background(), root, "file", before, 1024); err == nil {
		t.Fatal("replacement accepted")
	}
}

func TestInspectPackageSubdirectoryConfinesAncestorLinks(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(outside, "bundle"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "bundle", "SKILL.md"), []byte("---\nname: outside\ndescription: Must not be read\n---\nbody"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, _, err := InspectPackageSubdirectory(context.Background(), root, "link/bundle", PackageLimits{}); err == nil {
		t.Fatal("escaped source root through ancestor symlink")
	}
}
