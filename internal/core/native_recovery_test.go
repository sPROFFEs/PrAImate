package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeOutputRecoveryContinuesWithoutReplayingCompletedTools(t *testing.T) {
	c := nativeTestCore(t)
	run := nativeTestRun(c)
	run.local.OutputAutomatic = true
	root := t.TempDir()
	calls, notices, toolStarts := 0, 0, 0
	a := &nativeCLIAdapter{http: &http.Client{Transport: nativeTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		var request struct {
			Messages  []nativeMessage `json:"messages"`
			MaxTokens int             `json:"max_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.MaxTokens <= 0 || request.MaxTokens >= run.local.ContextTokens {
			t.Fatalf("unsafe allowance: %d", request.MaxTokens)
		}
		switch calls {
		case 1:
			return nativeHTTP(nativeSSE("", "tool_calls", nativeCall("done", "write_file", `{"path":"once.txt","content":"first"}`))), nil
		case 2:
			if err := os.WriteFile(filepath.Join(root, "once.txt"), []byte("changed after completed tool"), 0600); err != nil {
				t.Fatal(err)
			}
			return nativeHTTP(nativeSSE("Part one.", "length", nativeCall("unfinished", "write_file", `{"path":"never.txt","content":"bad"}`))), nil
		case 3:
			raw, _ := json.Marshal(request.Messages)
			if !strings.Contains(string(raw), "previous response exhausted") || !strings.Contains(string(raw), "Keep the complete task") {
				t.Fatalf("lost continuation contract: %s", raw)
			}
			return nativeHTTP(nativeSSE("Part two.", "stop")), nil
		default:
			t.Fatal("unexpected replay")
		}
		return nil, errors.New("unexpected request")
	})}}
	reply, err := a.SingleShotStream(withNativeExecution(context.Background(), run), SingleShotOpts{Cwd: root, Message: "Keep the complete task", Tools: "edits"}, func(e StreamEvent) {
		if e.Type == "context_recovery" {
			notices++
		}
		if e.Type == "tool_start" {
			toolStarts++
		}
	})
	if err != nil || reply.Text != "Part one.\n\nPart two." || calls != 3 || notices != 1 || toolStarts != 1 {
		t.Fatalf("reply=%+v err=%v calls=%d notices=%d tools=%d", reply, err, calls, notices, toolStarts)
	}
	content, err := os.ReadFile(filepath.Join(root, "once.txt"))
	if err != nil || string(content) != "changed after completed tool" {
		t.Fatalf("replayed completed write: %s %v", content, err)
	}
	if _, err := os.Stat(filepath.Join(root, "never.txt")); !os.IsNotExist(err) {
		t.Fatal("executed a truncated call")
	}
}

func TestNativeReasoningRecoveryIsBoundedAndKeepsExplicitLimit(t *testing.T) {
	c := nativeTestCore(t)
	run := nativeTestRun(c)
	requests := 0
	a := &nativeCLIAdapter{http: &http.Client{Transport: nativeTransport(func(r *http.Request) (*http.Response, error) {
		requests++
		var request struct {
			MaxTokens int `json:"max_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.MaxTokens != 1024 {
			t.Fatalf("changed explicit limit: %d", request.MaxTokens)
		}
		return nativeHTTP(nativeSSE("", "length")), nil
	})}}
	_, err := a.SingleShot(withNativeExecution(context.Background(), run), SingleShotOpts{Cwd: t.TempDir(), Message: "task"})
	if !errors.Is(err, errNativeOutputLimit) || requests != 4 {
		t.Fatalf("err=%v requests=%d", err, requests)
	}
}

func TestNativeRecoveryCompactsHistoryToMakeOutputRoom(t *testing.T) {
	route := nativeTestRun(nil).local
	route.OutputAutomatic = true
	s := &nativeSession{Context: nativeContextBudget(route, NativeContextStatus{}), Messages: []nativeMessage{{Role: "system", Content: "Instructions"}, {Role: "user", Content: "older task"}, {Role: "assistant", Content: strings.Repeat("old answer ", 2500)}, {Role: "user", Content: "Preserve my current request"}}}
	s.Context.LastOutputLimit = 2335
	reserveNativeRecoveryOutput(s, 200)
	if s.Context.OutputReserve <= 2335 {
		t.Fatalf("no additional generation space: %+v", s.Context)
	}
	messages, changed, err := compactNativeContext(s.Messages, s.Context.InputLimit-200, nativeMessageTokens)
	if err != nil || !changed || messages[len(messages)-1].Role != "user" || !strings.Contains(messages[len(messages)-1].Content, "Preserve my current request") {
		t.Fatalf("lost essential context: %+v %v", messages, err)
	}
}
