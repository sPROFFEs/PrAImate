package runtime

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/core"
)

func TestNativeWorkerReportsStartupAndKeepsPartialFailures(t *testing.T) {
	for _, outcome := range []string{"provider-error", "deadline"} {
		t.Run(outcome, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if outcome == "provider-error" {
					http.Error(w, `{"error":{"message":"Fixture provider unavailable"}}`, http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial findings\"},\"finish_reason\":null}]}\n\n")
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer server.Close()
			var progress []ProgressEvent
			result, err := (Native{Route: core.ChatLocalEndpoint{Endpoint: server.URL, ContextTokens: 4096}}).Execute(context.Background(), Request{
				Model: "fixture-model", Task: "Inspect the fixture", WorkspaceRoot: t.TempDir(),
				Limits:   Limits{MaxOutputTokens: 32, Timeout: time.Second},
				Progress: func(e ProgressEvent) { progress = append(progress, e) },
			})
			if err == nil {
				t.Fatal("failed native call succeeded")
			}
			if outcome == "deadline" && (!errors.Is(err, context.DeadlineExceeded) || result == nil || result.Content != "partial findings") {
				t.Fatalf("native deadline lost partial output: %+v %v", result, err)
			}
			if outcome == "provider-error" && !strings.Contains(err.Error(), "Fixture provider unavailable") {
				t.Fatalf("native provider diagnostic lost: %v", err)
			}
			if len(progress) < 2 || progress[0].Kind != "backend_status" || !strings.Contains(progress[0].Text, "fixture-model") || progress[len(progress)-1].Kind != "error" {
				t.Fatalf("native lifecycle/error lost: %+v", progress)
			}
		})
	}
}
