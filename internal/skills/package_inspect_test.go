package skills

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func TestInspectPackageZIPHostLimits(t *testing.T) {
	body := "---\nname: test\ndescription: Test\n---\n"
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, name := range []string{"SKILL.md", "reference.txt"} {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(f, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	n := int64(len(body))
	for _, tc := range []struct {
		name      string
		limits    PackageLimits
		wantError bool
	}{
		{"exact", PackageLimits{CompressedBytes: int64(buf.Len()), ExpandedBytes: 2 * n, FileBytes: n, Entries: 2}, false},
		{"compressed-plus-one", PackageLimits{CompressedBytes: int64(buf.Len()) - 1}, true},
		{"expanded-plus-one", PackageLimits{ExpandedBytes: 2*n - 1}, true},
		{"file-plus-one", PackageLimits{FileBytes: n - 1}, true},
		{"entry-plus-one", PackageLimits{Entries: 1}, true},
		{"negative", PackageLimits{Entries: -1}, true},
		{"overflow", PackageLimits{FileBytes: 1<<63 - 1}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := InspectPackageZIPWithLimits(context.Background(), bytes.NewReader(buf.Bytes()), int64(buf.Len()), tc.limits)
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v, wantError %v", err, tc.wantError)
			}
		})
	}
}

type cancelPackageReader struct {
	cancel context.CancelFunc
	reads  int
}

func (r *cancelPackageReader) Read(p []byte) (int, error) {
	r.reads++
	p[0] = 'x'
	r.cancel()
	return 1, nil
}

func TestPackageReadCancellationDuringRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &cancelPackageReader{cancel: cancel}
	_, err := io.ReadAll(packageContextReader{ctx, source})
	if !errors.Is(err, context.Canceled) || source.reads != 1 {
		t.Fatalf("error=%v reads=%d", err, source.reads)
	}
}

func TestInspectPackageZIPRejectsAmbiguousInventory(t *testing.T) {
	for _, names := range [][]string{
		{"Assets/", "assets/file.txt"},
		{"assets/", "assets"},
		{"assets", "assets/"},
		{"A/one", "a/two"},
		{"same/", "same/"},
		{"Σ/one", "ς/two"},
	} {
		t.Run(strings.Join(names, ","), func(t *testing.T) {
			var buf bytes.Buffer
			w := zip.NewWriter(&buf)
			for _, name := range append(names, "SKILL.md") {
				f, err := w.Create(name)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.HasSuffix(name, "/") {
					if _, err := f.Write([]byte("---\nname: test\ndescription: Test\n---\n")); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			if _, _, err := InspectPackageZIP(context.Background(), bytes.NewReader(buf.Bytes()), int64(buf.Len())); err == nil {
				t.Fatal("ambiguous inventory accepted")
			}
		})
	}
}

func TestInspectPackageZIPRejectsSpecialFilesAndOversize(t *testing.T) {
	for _, mode := range []os.FileMode{os.ModeSymlink | 0777, os.ModeNamedPipe | 0600, os.ModeDevice | 0600} {
		var buf bytes.Buffer
		w := zip.NewWriter(&buf)
		h := &zip.FileHeader{Name: "SKILL.md"}
		h.SetMode(mode)
		f, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte("target")); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if _, _, err := InspectPackageZIP(context.Background(), bytes.NewReader(buf.Bytes()), int64(buf.Len())); err == nil {
			t.Fatalf("mode %v accepted", mode)
		}
	}
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, err := w.Create("SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(bytes.Repeat([]byte("x"), maxSkillMarkdown+1)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := InspectPackageZIP(context.Background(), bytes.NewReader(buf.Bytes()), int64(buf.Len())); err == nil {
		t.Fatal("oversized entry accepted")
	}
}

func TestInspectPackageZIPSeparateCandidates(t *testing.T) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		"repo/a/SKILL.md":          "---\nname: a\ndescription: Alpha\n---\nALPHA",
		"repo/a/b/SKILL.md":        "---\nname: b\ndescription: Beta\n---\nBETA",
		"repo/a/references/doc.md": "REFERENCE",
		"repo/LICENSE":             "Synthetic license fixture",
	} {
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
	candidates, shared, err := InspectPackageZIP(context.Background(), bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 || len(shared) != 1 || shared[0] != "repo/LICENSE" {
		t.Fatalf("inspection = %+v / %v", candidates, shared)
	}
	if candidates[0].Manifest.Body != "ALPHA" || candidates[1].Manifest.Body != "BETA" {
		t.Fatal("candidate bodies were concatenated")
	}
	if len(candidates[0].files) != 2 || len(candidates[1].files) != 1 {
		t.Fatal("nested candidate duplicated in parent")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := InspectPackageZIP(ctx, bytes.NewReader(buf.Bytes()), int64(buf.Len())); err == nil {
		t.Fatal("cancel ignored")
	}
}

func TestInspectPackageZIPRejectsEscapeBeforeWriting(t *testing.T) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, err := w.Create("../escape.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("synthetic")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := InspectPackageZIP(context.Background(), bytes.NewReader(buf.Bytes()), int64(buf.Len())); err == nil {
		t.Fatal("traversal accepted")
	}
}

func TestInspectPackageZIPRejectsCorruptCRC(t *testing.T) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, err := w.CreateHeader(&zip.FileHeader{Name: "SKILL.md", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("---\nname: test\ndescription: Test\n---\nUNIQUE-CONTENT")
	if _, err := f.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	i := bytes.Index(raw, []byte("UNIQUE-CONTENT"))
	if i < 0 {
		t.Fatal("fixture payload missing")
	}
	raw[i] ^= 1
	if _, _, err := InspectPackageZIP(context.Background(), bytes.NewReader(raw), int64(len(raw))); err == nil {
		t.Fatal("corrupt CRC accepted")
	}
}

func TestInspectPackageZIPAllowsExplicitParents(t *testing.T) {
	for _, names := range [][]string{{"skill/", "skill/SKILL.md"}, {"skill/SKILL.md", "skill/"}} {
		var buf bytes.Buffer
		w := zip.NewWriter(&buf)
		for _, name := range names {
			f, err := w.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(name, "/") {
				if _, err := f.Write([]byte("---\nname: test\ndescription: Test\n---\n")); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if _, _, err := InspectPackageZIP(context.Background(), bytes.NewReader(buf.Bytes()), int64(buf.Len())); err != nil {
			t.Fatal(err)
		}
	}
}
