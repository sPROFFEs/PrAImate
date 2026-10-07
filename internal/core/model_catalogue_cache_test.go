package core

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestModelCatalogueSharesProbeAndReturnsIndependentResults(t *testing.T) {
	var cache modelCatalogueCache
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	load := func(context.Context) ([]string, error) {
		calls.Add(1)
		close(started)
		<-release
		return []string{"model-a"}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, err := cache.list(ctx, "cli", false, load); first <- err }()
	<-started
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter: %v", err)
	}
	const waiters = 12
	results := make([][]string, waiters)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var err error
			results[i], err = cache.list(context.Background(), "cli", false, load)
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("probes = %d", calls.Load())
	}
	results[0][0] = "edited"
	for _, result := range results[1:] {
		if !slices.Equal(result, []string{"model-a"}) {
			t.Fatalf("shared mutable catalogue: %v", result)
		}
	}
}

func TestCLIModelCatalogueCachesRefreshesAndNoticesConfigChanges(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX CLI fixture")
	}
	countFile := filepath.Join(t.TempDir(), "calls")
	t.Setenv("PRAIMATE_TEST_MODEL_COUNTER", countFile)
	fakeBinOnPath(t, "opencode", `printf '1\n' >> "$PRAIMATE_TEST_MODEL_COUNTER"
printf 'fixture/discovered\n'
`)
	configPath := filepath.Join(t.TempDir(), "opencode.json")
	t.Setenv("OPENCODE_CONFIG", configPath)
	ctx := context.Background()
	first := ListCLIModels(ctx, "opencode")
	if !slices.Contains(first, "fixture/discovered") {
		t.Fatal(first)
	}
	first[0] = "modified"
	if slices.Contains(ListCLIModels(ctx, "opencode"), "modified") {
		t.Fatal("caller modified shared cache")
	}
	count := func() int { data, _ := os.ReadFile(countFile); return len(strings.Fields(string(data))) }
	if count() != 1 {
		t.Fatalf("ordinary requests probed %d times", count())
	}
	RefreshCLIModels(ctx, "opencode")
	if count() != 2 {
		t.Fatalf("refresh probes = %d", count())
	}
	if err := os.WriteFile(configPath, []byte(`{"model":"fixture/new"}`), 0600); err != nil {
		t.Fatal(err)
	}
	ListCLIModels(ctx, "opencode")
	if count() != 3 {
		t.Fatalf("configuration change retained old catalogue: %d", count())
	}
}

func TestModelCatalogueRefreshExpiryAndFailures(t *testing.T) {
	var cache modelCatalogueCache
	calls := 0
	load := func(context.Context) ([]string, error) { calls++; return []string{"model"}, nil }
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		_, _ = cache.list(ctx, "a", false, load)
	}
	_, _ = cache.list(ctx, "a", true, load)
	if calls != 2 {
		t.Fatalf("refresh probes = %d", calls)
	}
	cache.mu.Lock()
	cache.entries["a"].expires = time.Now().Add(-time.Second)
	cache.mu.Unlock()
	_, _ = cache.list(ctx, "a", false, load)
	_, _ = cache.list(ctx, "b", false, load)
	if calls != 4 {
		t.Fatalf("expiry/other scope probes = %d", calls)
	}
	_, err := cache.list(ctx, "failed", false, func(context.Context) ([]string, error) { return nil, errors.New("offline") })
	if err == nil {
		t.Fatal("discovery failure lost")
	}
	_, err = cache.list(ctx, "failed", false, load)
	if err != nil || calls != 5 {
		t.Fatalf("failed probe was cached: %v, %d", err, calls)
	}
}

func TestNativeModelCatalogueRefreshIsScopedAndDoesNotProbeLimits(t *testing.T) {
	c := nativeTestCore(t)
	ctx := context.Background()
	var first, second atomic.Int32
	server := func(calls *atomic.Int32) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/models" {
				t.Errorf("catalogue probed unrelated route: %s", r.URL.Path)
				http.NotFound(w, r)
				return
			}
			calls.Add(1)
			_, _ = w.Write([]byte(`{"data":[{"id":"z"},{"id":"a"},{"id":"a"},{"id":" "}]}`))
		}))
	}
	a, b := server(&first), server(&second)
	defer a.Close()
	defer b.Close()
	// The catalogue belongs to the requested endpoint, regardless of a
	// model selected by a CLI environment or invalid generation limits.
	for _, host := range []LocalHost{{ID: "a", Endpoint: a.URL, NativeModels: []string{"model-a"}, ContextTokens: 2048, OutputTokens: 2048}, {ID: "b", Endpoint: b.URL, NativeModels: []string{"model-b"}}} {
		if _, err := c.SaveLocalHost(ctx, host); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PRAIMATE_MODEL", "b::model-b")
	for _, endpoint := range []string{a.URL, b.URL, a.URL + "/v1"} {
		models, err := c.NativeModels(ctx, endpoint)
		if err != nil || !slices.Equal(models, []string{"a", "z"}) {
			t.Fatalf("catalogue %s: %v %v", endpoint, models, err)
		}
	}
	if first.Load() != 1 || second.Load() != 1 {
		t.Fatalf("repeated probes: %d %d", first.Load(), second.Load())
	}
	if _, err := c.RefreshNativeModels(ctx, a.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := c.NativeModels(ctx, b.URL); err != nil {
		t.Fatal(err)
	}
	if first.Load() != 2 || second.Load() != 1 {
		t.Fatalf("refresh invalidated another host: %d %d", first.Load(), second.Load())
	}
}

func TestCLIModelCatalogueFailureDoesNotCacheFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX CLI fixture")
	}
	countFile := filepath.Join(t.TempDir(), "first-probe")
	t.Setenv("PRAIMATE_TEST_MODEL_COUNTER", countFile)
	fakeBinOnPath(t, "codex", `if [ ! -f "$PRAIMATE_TEST_MODEL_COUNTER" ]; then
  printf '1' > "$PRAIMATE_TEST_MODEL_COUNTER"
  exit 1
fi
printf '%s\n' '{"models":[{"slug":"recovered-model","visibility":"list"}]}'
`)
	ctx := context.Background()
	if first := ListCLIModels(ctx, "codex"); len(first) == 0 {
		t.Fatal("fallback suggestions disappeared")
	}
	if second := ListCLIModels(ctx, "codex"); !slices.Equal(second, []string{"recovered-model"}) {
		t.Fatalf("failed probe poisoned catalogue: %v", second)
	}
}
