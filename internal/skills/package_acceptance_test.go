package skills

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPackageImportInstallNeverExecutesScript(t *testing.T) {
	directory := t.TempDir()
	marker := filepath.Join(directory, "must-not-exist")
	script := []byte("#!/bin/sh\nprintf unsafe > '" + marker + "'\n")
	var archive bytes.Buffer
	w := zip.NewWriter(&archive)
	for _, file := range []PackageFile{
		{Path: "skill/SKILL.md", Content: []byte("---\nname: fixture\ndescription: Script fixture\nallowed-tools: all\n---\nRun scripts/install.sh to install.\n")},
		{Path: "skill/scripts/install.sh", Content: script, Executable: true},
		{Path: "skill/assets/binary.dat", Content: []byte{0, 255, 1}},
	} {
		h := &zip.FileHeader{Name: file.Path, Method: zip.Deflate}
		if file.Executable {
			h.SetMode(0755)
		} else {
			h.SetMode(0600)
		}
		f, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(file.Content); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	candidates, _, err := InspectPackageZIP(context.Background(), bytes.NewReader(archive.Bytes()), int64(archive.Len()))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := SelectPackages(context.Background(), candidates, []PackageSelection{{Candidate: 0, ExpectedDigest: candidates[0].Digest}}, PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	generation, err := InstallPackages(context.Background(), root, plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyPackageInstallation(context.Background(), root, generation, PackageLimits{}); err != nil {
		t.Fatal(err)
	}
	info, err := root.Stat(generation + "/objects/" + plan[0].Digest()[7:] + "/scripts/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0111 != 0 {
		t.Fatal("installer granted executable permission")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("script executed during import or install")
	}
}

func TestPackageUnicodeCollisionProfile(t *testing.T) {
	for _, names := range [][]string{{"é.txt", "e\u0301.txt"}, {"Straße.txt", "STRASSE.txt"}, {"ﬀ.txt", "ff.txt"}, {"가.txt", "가.txt"}} {
		files := []PackageFile{{Path: names[0]}, {Path: names[1]}}
		if _, err := PackageDigest(files); err == nil {
			t.Fatalf("normalization collision accepted: %v", names)
		}
	}
	if _, err := PackageDigest([]PackageFile{{Path: "日本語.txt"}, {Path: "é.txt"}}); err != nil {
		t.Fatal(err)
	}
}

func TestPackageZIPRejectsUnixLinkMetadata(t *testing.T) {
	for _, kind := range []uint16{0x000d, 0x756e} {
		var b bytes.Buffer
		w := zip.NewWriter(&b)
		h := &zip.FileHeader{Name: "SKILL.md", Extra: []byte{byte(kind), byte(kind >> 8), 0, 0}}
		f, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte("---\nname: test\ndescription: Test\n---\n")); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if _, _, err := InspectPackageZIP(context.Background(), bytes.NewReader(b.Bytes()), int64(b.Len())); err == nil {
			t.Fatal("Unix link metadata accepted")
		}
	}
}
