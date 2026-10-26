package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sPROFFEs/PrAImate/internal/studio"
)

func TestStudioMaintenanceDoesNotRequireDatabase(t *testing.T) {
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	db := filepath.Join(t.TempDir(), "unavailable.db")
	const contents = "not an unlocked database"
	if err := os.WriteFile(db, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PRAIMATE_DB", db)
	app, _ := studio.AppDir()
	if err := os.MkdirAll(app, 0755); err != nil {
		t.Fatal(err)
	}
	name := "codium"
	if runtime.GOOS == "windows" {
		name = "VSCodium.exe"
	}
	if err := os.WriteFile(filepath.Join(app, name), []byte("fixture"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, action := range []struct {
		name string
		run  func() int
	}{{"status", runStudioStatus}, {"install", runStudioInstall}, {"update", runStudioUpdate}, {"repair", runStudioRepair}} {
		t.Run(action.name, func(t *testing.T) {
			if code := action.run(); code != 0 {
				t.Fatalf("Studio %s requires database access: exit %d", action.name, code)
			}
			body, err := os.ReadFile(db)
			if err != nil || string(body) != contents {
				t.Fatalf("maintenance changed the database fixture: %v", err)
			}
		})
	}
}
