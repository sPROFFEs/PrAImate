package core

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/hosttls"
)

type nativeLimitEntry struct {
	window int
	source string
	until  time.Time
}

// Metadata is optional and cached in memory. Discovery never loads a model,
// sends a prompt, follows redirects, or changes a server's configuration.
type nativeLimitCache struct {
	mu      sync.Mutex
	entries map[[32]byte]nativeLimitEntry
	http    *http.Client
}

func (c *nativeLimitCache) remember(route ChatLocalEndpoint, window int, source string) {
	key := sha256.Sum256([]byte(route.Endpoint + "\x00" + route.Model + "\x00" + route.APIKey + "\x00" + route.TLSCertificate))
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil || len(c.entries) >= 128 {
		c.entries = make(map[[32]byte]nativeLimitEntry)
	}
	c.entries[key] = nativeLimitEntry{window, source, time.Now().Add(time.Minute)}
}

func (c *nativeLimitCache) lookup(ctx context.Context, route ChatLocalEndpoint) (int, string) {
	key := sha256.Sum256([]byte(route.Endpoint + "\x00" + route.Model + "\x00" + route.APIKey + "\x00" + route.TLSCertificate))
	c.mu.Lock()
	entry, ok := c.entries[key]
	c.mu.Unlock()
	if ok && time.Now().Before(entry.until) {
		return entry.window, entry.source
	}
	window, source := (nativeProvider{route: route, http: c.http}).discoverContext(ctx)
	ttl := time.Minute
	if window == 0 {
		window, source = autoContextWindow(route.Model, 0), "fallback; server window unknown"
		ttl = 10 * time.Second // cold models may become discoverable after one turn
	}
	if ctx.Err() == nil {
		c.mu.Lock()
		if c.entries == nil || len(c.entries) >= 128 {
			c.entries = make(map[[32]byte]nativeLimitEntry)
		}
		c.entries[key] = nativeLimitEntry{window, source, time.Now().Add(ttl)}
		c.mu.Unlock()
	}
	return window, source
}

func validNativeWindow(n int) bool { return n >= 2048 && n <= 2_000_000 }

func (p nativeProvider) discoverContext(ctx context.Context) (int, string) {
	base, err := nativeBaseURL(p.route.Endpoint)
	if err != nil || !strings.HasSuffix(base, "/v1") {
		return 0, ""
	}
	// Keep reverse-proxy prefixes and credentials on the same configured origin.
	root := strings.TrimSuffix(base, "/v1")
	ctx, cancel := context.WithTimeout(ctx, 900*time.Millisecond)
	defer cancel()
	get := func(path string, target any) bool {
		probeCtx, stop := context.WithTimeout(ctx, 300*time.Millisecond)
		defer stop()
		req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, root+path, nil)
		if err != nil {
			return false
		}
		req.Header.Set("Accept", "application/json")
		if p.route.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+p.route.APIKey)
		}
		client := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		if p.http != nil {
			client.Transport = p.http.Transport
		} else if p.route.TLSCertificate != "" {
			trusted, err := hosttls.Client(p.route.Endpoint, p.route.TLSCertificate, 0)
			if err != nil {
				return false
			}
			client.Transport = trusted.Transport
			defer trusted.CloseIdleConnections()
		}
		res, err := client.Do(req)
		if err != nil {
			return false
		}
		defer res.Body.Close()
		return res.StatusCode == http.StatusOK && json.NewDecoder(io.LimitReader(res.Body, 512<<10)).Decode(target) == nil
	}
	// Ollama /api/ps reports the RUNNING window; /api/show only reports
	// the training maximum, which can be much larger than available KV cache.
	var ollama struct {
		Models []struct {
			Name    string `json:"name"`
			Model   string `json:"model"`
			Context int    `json:"context_length"`
		} `json:"models"`
	}
	if get("/api/ps", &ollama) {
		for _, m := range ollama.Models {
			match := func(name string) bool {
				return name == p.route.Model || strings.TrimSuffix(name, ":latest") == strings.TrimSuffix(p.route.Model, ":latest")
			}
			if (match(m.Name) || match(m.Model)) && validNativeWindow(m.Context) {
				return m.Context, "Ollama running model"
			}
		}
	}
	var studio struct {
		Models []struct {
			Key       string `json:"key"`
			Instances []struct {
				ID     string `json:"id"`
				Config struct {
					Context int `json:"context_length"`
				} `json:"config"`
			} `json:"loaded_instances"`
		} `json:"models"`
	}
	if get("/api/v1/models", &studio) {
		window := 0
		for _, model := range studio.Models {
			for _, instance := range model.Instances {
				if (instance.ID == p.route.Model || model.Key == p.route.Model) && validNativeWindow(instance.Config.Context) {
					if window == 0 || instance.Config.Context < window {
						window = instance.Config.Context
					}
				}
			}
		}
		if window > 0 {
			return window, "LM Studio loaded model"
		}
	}
	var props struct {
		Settings struct {
			Context int `json:"n_ctx"`
		} `json:"default_generation_settings"`
	}
	if get("/props", &props) && validNativeWindow(props.Settings.Context) {
		return props.Settings.Context, "llama.cpp server"
	}
	// vLLM exposes the serving max_model_len on its OpenAI model cards.
	// Do not infer a trained maximum from model names or config files.
	var cards struct {
		Data []struct {
			ID     string `json:"id"`
			Window int    `json:"max_model_len"`
		} `json:"data"`
	}
	if get("/v1/models", &cards) {
		for _, model := range cards.Data {
			if model.ID == p.route.Model && validNativeWindow(model.Window) {
				return model.Window, "OpenAI model serving metadata"
			}
		}
	}

	return 0, ""
}
