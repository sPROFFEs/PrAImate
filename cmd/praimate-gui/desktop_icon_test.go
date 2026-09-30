package main

import (
	"bytes"
	"debug/pe"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestWindowIconFitsX11Property(t *testing.T) {
	icon, err := png.Decode(bytes.NewReader(desktopWindowIcon(appIcon)))
	if err != nil {
		t.Fatal(err)
	}
	bounds := icon.Bounds()
	if bounds.Dx() > 128 || bounds.Dy() > 128 || bounds.Dx() <= 0 || bounds.Dy() <= 0 {
		t.Fatalf("oversized window icon: %v", bounds)
	}
	// The shipped portrait icon must retain its aspect ratio, not stretch.
	if bounds.Dx() >= bounds.Dy() {
		t.Fatalf("aspect ratio lost: %v", bounds)
	}
	if _, _, _, alpha := icon.At(0, 0).RGBA(); alpha != 0 {
		t.Fatal("transparent corner lost")
	}
}

func TestLinuxIconRespectsXDGAndUpdates(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", root)
	ensureLinuxDesktopIcon()
	path := filepath.Join(root, "icons", "hicolor", "512x512", "apps", "praimate.png")
	if err := os.WriteFile(path, []byte("old icon"), 0644); err != nil {
		t.Fatal(err)
	}
	ensureLinuxDesktopIcon()
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, appIcon) {
		t.Fatalf("icon not refreshed: %v", err)
	}
}

func TestWindowsIconResources(t *testing.T) {
	for arch, machine := range map[string]uint16{"amd64": pe.IMAGE_FILE_MACHINE_AMD64, "arm64": pe.IMAGE_FILE_MACHINE_ARM64} {
		t.Run(arch, func(t *testing.T) {
			f, err := pe.Open("rsrc_windows_" + arch + ".syso")
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if f.Machine != machine {
				t.Fatalf("wrong machine: %x", f.Machine)
			}
			found := false
			for _, section := range f.Sections {
				data, err := section.Data()
				if err == nil && bytes.Contains(data, []byte("\x89PNG\r\n\x1a\n")) {
					found = true
				}
			}
			if !found {
				t.Fatal("embedded PNG icon missing")
			}
		})
	}
}
