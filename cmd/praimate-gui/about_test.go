package main

import (
	"runtime"
	"testing"

	"github.com/sPROFFEs/PrAImate/internal/version"
)

func TestAboutReportsBuildAndEncryptionDetails(t *testing.T) {
	info := NewApp().About()
	if info.Name != version.Name || info.Version != version.Current {
		t.Fatalf("build identity = %s %s, want %s %s", info.Name, info.Version, version.Name, version.Current)
	}
	if info.OS != runtime.GOOS || info.Arch != runtime.GOARCH {
		t.Fatalf("platform = %s/%s, want %s/%s", info.OS, info.Arch, runtime.GOOS, runtime.GOARCH)
	}
	if info.DatabaseCipher != "AES-256-XTS" {
		t.Fatalf("database cipher = %q", info.DatabaseCipher)
	}
	if info.DatabaseEncrypted {
		t.Fatal("app without an open store must not claim an active encrypted database")
	}
}
