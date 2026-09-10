package skills

import "testing"

func TestPackageDigestContractVectors(t *testing.T) {
	// contracts/digest-vectors.json from the supplied kit. No runtime or
	// network dependency on the kit is needed to run these vectors.
	cases := []struct {
		files []PackageFile
		want  string
	}{
		{nil, "sha256:e7b6c60532d83f88e313962a6355eaf49c6b29a375b00d1b3422001ee877396e"},
		{[]PackageFile{{Path: "SKILL.md", Content: []byte("hello\n")}}, "sha256:200c4b8077d8590d82d7a101f82c9fde7201a43f6ffe78b8e2c0599514f756f5"},
		{[]PackageFile{{Path: "scripts/check.sh", Content: []byte("exit 0\n"), Executable: true}, {Path: "SKILL.md", Content: []byte("hola ñ\n")}}, "sha256:aa7e7993491a318013e52144de52c62fefd2019a79ac1c5b6f29eb2eb5640e65"},
	}
	for _, tc := range cases {
		got, err := PackageDigest(tc.files)
		if err != nil || got != tc.want {
			t.Fatalf("digest = %s, %v; want %s", got, err, tc.want)
		}
	}
}

func TestPackageDigestRejectsAmbiguousPaths(t *testing.T) {
	for _, path := range []string{"../outside", "/absolute", "a/../b", "a//b", `a\b`, "C:/data", "file:stream", "CON", "aux.txt", "dir/NUL", "file.", "file ", "a\x00b"} {
		if _, err := PackageDigest([]PackageFile{{Path: path}}); err == nil {
			t.Errorf("accepted %q", path)
		}
	}
	if _, err := PackageDigest([]PackageFile{{Path: "SKILL.md"}, {Path: "skill.md"}}); err == nil {
		t.Fatal("accepted case collision")
	}
	if _, err := PackageDigest([]PackageFile{{Path: "scripts"}, {Path: "scripts/run.sh"}}); err == nil {
		t.Fatal("accepted file/directory collision")
	}
}

func TestPackageDigestCoversResourcesAndExecutableIntent(t *testing.T) {
	files := []PackageFile{{Path: "SKILL.md", Content: []byte("body")}, {Path: "references/a.txt", Content: []byte("one")}}
	initial, err := PackageDigest(files)
	if err != nil {
		t.Fatal(err)
	}
	files[1].Content = []byte("two")
	changed, _ := PackageDigest(files)
	if changed == initial {
		t.Fatal("resource was not hashed")
	}
	files[1].Executable = true
	executable, _ := PackageDigest(files)
	if executable == changed {
		t.Fatal("executable intent was not hashed")
	}
}
