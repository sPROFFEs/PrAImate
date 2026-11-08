package installer

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNpmUpdateExecutesAgainstDetectedNodePrefix(t *testing.T) {
	home := nodePathFixture(t)
	target := fakeNodeBin(t, filepath.Join(home, "nvm space", "versions", "node", "v22.12.0", "bin"))
	active := fakeNodeBin(t, filepath.Join(home, "fnm", "node-versions", "v24.1.0", "installation", "bin"))
	prefix := filepath.Dir(target)
	script := filepath.Join(prefix, "lib", "node_modules", "npm", "bin", "npm-cli.js")
	if err := os.MkdirAll(filepath.Dir(script), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("// synthetic npm script; never executed as JavaScript"), 0644); err != nil {
		t.Fatal(err)
	}
	// A fake Node records its argv and PATH. No npm install or network request
	// happens, but this exercises the same execution entrypoint as the GUI.
	if err := os.WriteFile(filepath.Join(target, "node"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\nprintf 'PATH=%s\\n' \"$PATH\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", active+string(os.PathListSeparator)+os.Getenv("PATH"))
	before := os.Getenv("PATH")
	method := Method{ID: "npm", Command: "npm install -g --no-fund --no-audit @openai/codex"}
	var output bytes.Buffer
	err := RunWithOptions(context.Background(), method, RunOptions{ExistingBinary: filepath.Join(target, "codex")}, &output, &output)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{script + "\ninstall\n-g\n", "@openai/codex\n--prefix\n" + prefix + "\n", "PATH=" + target + string(os.PathListSeparator)} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("update did not stay with the detected installation; missing %q:\n%s", want, output.String())
		}
	}
	if os.Getenv("PATH") != before {
		t.Fatal("updating one CLI changed the runtime used by unrelated CLIs")
	}
}

func TestNpmTargetDoesNotRewriteLocalOrNonNpmCommands(t *testing.T) {
	nodePathFixture(t)
	for _, method := range []Method{{ID: "npm", Command: "npm install package"}, {ID: "brew", Command: "brew upgrade codex"}} {
		cmd := buildCmd(context.Background(), method)
		before := append([]string(nil), cmd.Args...)
		alignNpmTarget(cmd, method, "codex", nil)
		if strings.Join(before, "\x00") != strings.Join(cmd.Args, "\x00") || cmd.Env != nil {
			t.Fatalf("unrelated command changed: %v", cmd.Args)
		}
	}
}
