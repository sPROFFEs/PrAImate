package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTerminalSnapshotRetainsOutputAndOffsets(t *testing.T) {
	tm := newTermManager()
	tm.sessions["term-1"] = &termSession{id: "term-1"}

	first, ok := tm.recordOutput("term-1", []byte("hello "))
	if !ok || first.StartOffset != 0 || first.EndOffset != 6 {
		t.Fatalf("first chunk = %+v, ok=%v", first, ok)
	}
	second, ok := tm.recordOutput("term-1", []byte("world"))
	if !ok || second.StartOffset != 6 || second.EndOffset != 11 {
		t.Fatalf("second chunk = %+v, ok=%v", second, ok)
	}

	snapshot, err := tm.snapshot("term-1")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(snapshot.Data)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "hello world" || snapshot.StartOffset != 0 || snapshot.EndOffset != 11 {
		t.Fatalf("snapshot = %+v, raw=%q", snapshot, raw)
	}
}

func TestTerminalCloseRunsPerSessionCleanupOnce(t *testing.T) {
	tm := newTermManager()
	calls := 0
	tm.sessions["term-cleanup"] = &termSession{id: "term-cleanup", cleanup: func() { calls++ }}
	tm.close("term-cleanup")
	tm.close("term-cleanup")
	if calls != 1 {
		t.Fatalf("cleanup calls = %d, want 1", calls)
	}
}

func TestTerminalSnapshotKeepsBoundedTail(t *testing.T) {
	tm := newTermManager()
	tm.sessions["term-1"] = &termSession{id: "term-1"}
	payload := []byte(strings.Repeat("x", terminalHistoryLimit+32))
	chunk, ok := tm.recordOutput("term-1", payload)
	if !ok {
		t.Fatal("recordOutput returned false")
	}
	snapshot, err := tm.snapshot("term-1")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(snapshot.Data)
	if len(raw) == 0 || len(raw) > terminalHistoryLimit {
		t.Fatalf("snapshot bytes = %d, want 1..%d", len(raw), terminalHistoryLimit)
	}
	wantStart := int64(len(payload) - len(raw))
	if snapshot.StartOffset != wantStart || snapshot.EndOffset != int64(len(payload)) {
		t.Fatalf("bounded offsets = start %d end %d, want start %d end %d (chunk start %d end %d)",
			snapshot.StartOffset, snapshot.EndOffset, wantStart, len(payload), chunk.StartOffset, chunk.EndOffset)
	}
	if string(raw) != string(payload[len(payload)-len(raw):]) {
		t.Fatal("snapshot does not contain the retained payload tail")
	}
}

func TestCodeSessionHistoryStaysInMemory(t *testing.T) {
	tm := newTermManager()
	tm.sessions["old"] = &termSession{id: "old"}

	_, _ = tm.recordOutput("old", []byte("first process\r\n"))
	if err := tm.bindChat("old", "chat-1"); err != nil {
		t.Fatal(err)
	}
	_, _ = tm.recordOutput("old", []byte("after binding\r\n"))
	// Reattaching the same live terminal must not flush its retained prefix a
	// second time.
	if err := tm.bindChat("old", "chat-1"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := tm.codeSnapshot("chat-1", "old")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(snapshot.Data)
	if err != nil {
		t.Fatal(err)
	}
	want := "first process\r\nafter binding\r\n"
	if string(raw) != want {
		t.Fatalf("live transcript = %q, want %q", raw, want)
	}
	if snapshot.EndOffset != int64(len(want)) {
		t.Fatalf("live offset = %d", snapshot.EndOffset)
	}

	// Once the process is gone there is deliberately no archived output.
	tm.close("old")
	archived, err := tm.codeSnapshot("chat-1", "")
	if err != nil {
		t.Fatal(err)
	}
	archivedRaw, _ := base64.StdEncoding.DecodeString(archived.Data)
	if len(archivedRaw) != 0 || archived.EndOffset != 0 {
		t.Fatalf("closed snapshot = %+v, raw=%q", archived, archivedRaw)
	}
}

func TestTerminalOutputCreatesNoLogFilesAndRemovesLegacyHistory(t *testing.T) {
	cache := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("LOCALAPPDATA", cache)
	} else {
		t.Setenv("XDG_CACHE_HOME", cache)
	}
	legacyDir := filepath.Join(cache, "praimate", "code-history")
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "old.log"), []byte("sensitive output"), 0o600); err != nil {
		t.Fatal(err)
	}

	tm := newTermManager()
	if _, err := os.Stat(legacyDir); !os.IsNotExist(err) {
		t.Fatalf("legacy terminal log directory still exists: %v", err)
	}
	tm.sessions["term-1"] = &termSession{id: "term-1"}
	_, _ = tm.recordOutput("term-1", []byte("memory only"))
	if err := tm.bindChat("term-1", "chat-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacyDir); !os.IsNotExist(err) {
		t.Fatalf("terminal output recreated a log directory: %v", err)
	}
}

func TestBindChatRejectsMissingOrConflictingTerminal(t *testing.T) {
	tm := newTermManager()
	if err := tm.bindChat("missing", "chat-1"); err == nil {
		t.Fatal("expected missing terminal error")
	}
	tm.sessions["term-1"] = &termSession{id: "term-1"}
	if err := tm.bindChat("term-1", "chat-1"); err != nil {
		t.Fatal(err)
	}
	if err := tm.bindChat("term-1", "chat-2"); err == nil {
		t.Fatal("expected conflicting chat binding error")
	}
}

func TestTerminalEnvironmentPreservesHostAndAppliesOverrides(t *testing.T) {
	got := environmentByKey(terminalEnvironment(
		[]string{"PATH=/usr/bin", "HOME=/home/test", "LANG=es_ES.UTF-8", "TOKEN=old"},
		[]string{"TOKEN=nuevo-á", "OPENAI_BASE_URL=http://localhost:11434/v1"},
	))
	if got["PATH"] != "/usr/bin" || got["HOME"] != "/home/test" {
		t.Fatalf("host environment was not preserved: %#v", got)
	}
	if got["TOKEN"] != "nuevo-á" {
		t.Fatalf("override was not applied as UTF-8: %q", got["TOKEN"])
	}
	if got["LANG"] != "es_ES.UTF-8" {
		t.Fatalf("UTF-8 locale changed unexpectedly: %q", got["LANG"])
	}
	if got["TERM"] != "xterm-256color" || got["COLORTERM"] != "truecolor" {
		t.Fatalf("terminal capabilities missing: %#v", got)
	}
}

func TestTerminalEnvironmentUpgradesNonUTF8Locale(t *testing.T) {
	got := environmentByKey(terminalEnvironment([]string{"PATH=/usr/bin", "LC_ALL=C", "LANG=C"}, nil))
	if got["LC_ALL"] != "C.UTF-8" {
		t.Fatalf("LC_ALL = %q, want C.UTF-8", got["LC_ALL"])
	}

	got = environmentByKey(terminalEnvironment([]string{"PATH=/usr/bin", "LANG=C"}, nil))
	if got["LC_CTYPE"] != "C.UTF-8" {
		t.Fatalf("LC_CTYPE = %q, want C.UTF-8", got["LC_CTYPE"])
	}
}

func TestOpenCodeLogDirectoryIsPreparedBeforeFirstLaunch(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", root)
	if err := ensureOpenCodeLogDir(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "opencode", "log")); err != nil {
		t.Fatalf("OpenCode log directory was not prepared: %v", err)
	}
}

func TestOpenCodeLogDirectoryUsesPlatformDataRoots(t *testing.T) {
	if got := opencodeLogDir("linux", "/home/test", "", ""); got != "/home/test/.local/share/opencode/log" {
		t.Fatalf("linux log directory = %q", got)
	}
	if got := opencodeLogDir("linux", "/home/test", "/tmp/data", ""); got != "/tmp/data/opencode/log" {
		t.Fatalf("XDG log directory = %q", got)
	}
	if runtime.GOOS == "windows" {
		if got := opencodeLogDir("windows", `C:\\Users\\test`, "", `C:\\Users\\test\\AppData\\Local`); got != `C:\\Users\\test\\AppData\\Local\\opencode\\log` {
			t.Fatalf("Windows log directory = %q", got)
		}
	}
}

func environmentByKey(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, kv := range env {
		if key, value, ok := strings.Cut(kv, "="); ok {
			out[key] = value
		}
	}
	return out
}
