package installer

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func nodePathFixture(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable fixtures")
	}
	home := t.TempDir()
	for _, key := range []string{"NVM_DIR", "NVM_BIN", "NVM_HOME", "NVM_SYMLINK", "FNM_DIR", "FNM_MULTISHELL_PATH", "PRAIMATE_NODE_BIN"} {
		t.Setenv(key, "")
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("PATH", "/usr/bin:/bin")
	return home
}

func fakeNodeBin(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"node", "npm", "codex"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func assertNodePath(t *testing.T, want string) {
	t.Helper()
	for _, name := range []string{"node", "npm", "codex"} {
		got, err := exec.LookPath(name)
		if err != nil || got != filepath.Join(want, name) {
			t.Fatalf("%s resolved to %q (%v), want runtime %q", name, got, err, want)
		}
	}
	before := os.Getenv("PATH")
	ImportUserBinDirs()
	if os.Getenv("PATH") != before {
		t.Fatal("repeated detection changed PATH precedence")
	}
}

func TestServiceFindsXDGNodeAndHonorsNVMDefault(t *testing.T) {
	home := nodePathFixture(t)
	root := filepath.Join(home, ".config", "nvm")
	fakeNodeBin(t, filepath.Join(root, "versions", "node", "v9.9.0", "bin"))
	fakeNodeBin(t, filepath.Join(root, "versions", "node", "v24.1.0", "bin"))
	want := fakeNodeBin(t, filepath.Join(root, "versions", "node", "v22.12.0", "bin"))
	if err := os.MkdirAll(filepath.Join(root, "alias"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "alias", "default"), []byte("22\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ImportUserBinDirs()
	assertNodePath(t, want)
}

func TestNodeFallbackUsesNumericVersionOrder(t *testing.T) {
	home := nodePathFixture(t)
	root := filepath.Join(home, ".nvm", "versions", "node")
	fakeNodeBin(t, filepath.Join(root, "v9.9.0", "bin"))
	want := fakeNodeBin(t, filepath.Join(root, "v24.12.0", "bin"))
	fakeNodeBin(t, filepath.Join(root, "v24.9.0", "bin"))
	ImportUserBinDirs()
	assertNodePath(t, want)
}

func TestServiceFindsFNMDefaultWithoutMultishell(t *testing.T) {
	home := nodePathFixture(t)
	root := filepath.Join(home, ".local", "share", "fnm")
	want := fakeNodeBin(t, filepath.Join(root, "node-versions", "v22.12.0", "installation", "bin"))
	fakeNodeBin(t, filepath.Join(root, "node-versions", "v24.1.0", "installation", "bin"))
	if err := os.MkdirAll(filepath.Join(root, "aliases"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(want), filepath.Join(root, "aliases", "default")); err != nil {
		t.Fatal(err)
	}
	ImportUserBinDirs()
	assertNodePath(t, want)
}

func TestNodeDiscoveryPreservesActiveVersionAndCustomRoot(t *testing.T) {
	home := nodePathFixture(t)
	root := filepath.Join(home, "custom nvm")
	t.Setenv("NVM_DIR", root)
	want := fakeNodeBin(t, filepath.Join(root, "versions", "node", "v22.12.0", "bin"))
	fakeNodeBin(t, filepath.Join(root, "versions", "node", "v24.1.0", "bin"))
	t.Setenv("PATH", want+string(os.PathListSeparator)+os.Getenv("PATH"))
	ImportUserBinDirs()
	assertNodePath(t, want)
	if strings.Contains(os.Getenv("PATH"), "fnm_multishells") {
		t.Fatal("created a transient shell dependency")
	}
}

func TestManagedNodePinWinsOverAnotherActiveOrDefaultRuntime(t *testing.T) {
	home := nodePathFixture(t)
	want := fakeNodeBin(t, filepath.Join(home, "pinned runtime", "bin"))
	active := fakeNodeBin(t, filepath.Join(home, ".nvm", "versions", "node", "v24.1.0", "bin"))
	t.Setenv("PRAIMATE_NODE_BIN", want)
	t.Setenv("PATH", active+string(os.PathListSeparator)+os.Getenv("PATH"))
	ImportUserBinDirs()
	assertNodePath(t, want)
}
