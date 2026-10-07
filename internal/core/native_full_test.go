package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeFullCanUseSystemFilesAndCommandDirectory(t *testing.T) {
	c := nativeTestCore(t)
	ctx := context.Background()
	root, outside := t.TempDir(), t.TempDir()
	b, defs, err := nativeTestRun(c).nativeTools(ctx, root, "full", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	path := filepath.Join(outside, "created.txt")
	raw, _ := json.Marshal(map[string]string{"path": path, "content": "full fixture"})
	if _, err := executeNativeTool(ctx, b, defs, nativeCall("w", "write_file", string(raw))); err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(map[string]string{"path": path})
	if out, err := executeNativeTool(ctx, b, defs, nativeCall("r", "read_file", string(raw))); err != nil || !strings.Contains(out, "full fixture") {
		t.Fatalf("read: %s %v", out, err)
	}
	t.Setenv("HOME", root)
	t.Setenv("USERPROFILE", root)
	t.Setenv("APPDATA", "praimate-full-test-child")
	t.Setenv("OPENAI_API_KEY", "fixture-must-not-be-forwarded")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(map[string]any{"command": exe, "args": []string{"-test.run=^TestNativeFullCommandEnvironmentChild$"}, "cwd": outside})
	out, err := executeNativeTool(ctx, b, defs, nativeCall("c", "run_command", string(raw)))
	if err != nil || !strings.Contains(out, root) || !strings.Contains(out, outside) {
		t.Fatalf("command did not retain home/cwd: %s %v", out, err)
	}
	for _, level := range []string{"", "ask", "edits"} {
		restricted, restrictedDefs, err := nativeTestRun(c).nativeTools(ctx, root, level, &ApprovalConfig{Request: func(context.Context, string, map[string]any) (bool, error) { return true, nil }})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := executeNativeTool(ctx, restricted, restrictedDefs, nativeCall("c", "run_command", string(raw))); err == nil {
			t.Fatalf("%q allowed command cwd outside workspace", level)
		}
		_ = restricted.Close()
	}
}

func TestNativeFullCommandEnvironmentChild(t *testing.T) {
	if os.Getenv("APPDATA") != "praimate-full-test-child" {
		t.Skip("subprocess fixture")
	}
	if os.Getenv("OPENAI_API_KEY") != "" {
		t.Fatal("provider credential forwarded")
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println(os.Getenv("HOME"), os.Getenv("USERPROFILE"), cwd)
}

func TestNativeFullFollowsNetworkRedirects(t *testing.T) {
	ctx := context.Background()
	c := nativeTestCore(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("redirect fixture"))
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()
	for _, level := range []string{"full", "ask"} {
		b, defs, err := nativeTestRun(c).nativeTools(ctx, t.TempDir(), level, &ApprovalConfig{Request: func(context.Context, string, map[string]any) (bool, error) { return true, nil }})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(map[string]string{"url": source.URL})
		out, err := executeNativeTool(ctx, b, defs, nativeCall("net", "fetch_url", string(raw)))
		_ = b.Close()
		if level == "full" && (err != nil || !strings.Contains(out, "redirect fixture")) {
			t.Fatalf("full blocked normal redirect: %s %v", out, err)
		}
		if level == "ask" && (err == nil || !strings.Contains(err.Error(), "cross-origin redirect")) {
			t.Fatalf("ask followed unapproved redirect: %s %v", out, err)
		}
	}
}
