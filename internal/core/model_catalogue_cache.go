package core

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/ollama"
)

const modelCatalogueTTL = 5 * time.Minute

type modelCatalogueEntry struct {
	done    chan struct{}
	expires time.Time
	models  []string
	err     error
}

type modelCatalogueCache struct {
	mu      sync.Mutex
	entries map[string]*modelCatalogueEntry
}

func (c *modelCatalogueCache) list(ctx context.Context, key string, force bool, load func(context.Context) ([]string, error)) ([]string, error) {
	c.mu.Lock()
	if c.entries == nil {
		c.entries = make(map[string]*modelCatalogueEntry)
	}
	e := c.entries[key]
	if e != nil {
		select {
		case <-e.done:
			if force || time.Now().After(e.expires) {
				e = nil
			}
		default: // Share an existing probe, including simultaneous refreshes.
		}
	}
	if e == nil {
		// Settings changes create distinct scopes. Drop completed old entries
		// so catalogue caching stays bounded for a long-running desktop.
		for oldKey, old := range c.entries {
			select {
			case <-old.done:
				if time.Now().After(old.expires) || len(c.entries) >= 128 {
					delete(c.entries, oldKey)
				}
			default:
			}
		}
		e = &modelCatalogueEntry{done: make(chan struct{})}
		c.entries[key] = e
		go func(e *modelCatalogueEntry) {
			// One picker closing must not cancel a probe shared by other pickers.
			probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
			defer cancel()
			models, err := load(probeCtx)
			c.mu.Lock()
			e.models, e.err, e.expires = slices.Clone(models), err, time.Now().Add(modelCatalogueTTL)
			if err != nil && c.entries[key] == e {
				delete(c.entries, key) // A failed discovery must not poison the cache.
			}
			close(e.done)
			c.mu.Unlock()
		}(e)
	}
	c.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-e.done:
		return slices.Clone(e.models), e.err
	}
}

var cliModelCatalogue modelCatalogueCache

// Include configuration locations and file revisions without reading auth or
// credential files. A CLI/configuration update naturally gets a fresh entry.
func cliCatalogueKey(cli string) string {
	var b strings.Builder
	fmt.Fprintln(&b, cli)
	for _, name := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "PRAIMATE_HOME", "PATH", "OPENCODE_CONFIG", "OPENCODE_CONFIG_CONTENT"} {
		fmt.Fprintln(&b, os.Getenv(name))
	}
	cwd, _ := os.Getwd()
	fmt.Fprintln(&b, cwd)
	paths := []string{os.Getenv("OPENCODE_CONFIG")}
	if bin, err := cliCatalogueBinary(cli); err == nil {
		paths = append(paths, bin)
	}
	if path, err := ollama.OpenCodeConfigPath(); err == nil {
		paths = append(paths, path, path+"c")
	}
	if path, err := ollama.CodexConfigPath(); err == nil {
		paths = append(paths, path)
	}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".openclaude", ".openclaude-profile.json"))
	}
	paths = append(paths, filepath.Join(cwd, "opencode.json"), filepath.Join(cwd, "opencode.jsonc"))
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil {
			fmt.Fprintf(&b, "%s:%d:%d\n", path, info.Size(), info.ModTime().UnixNano())
		}
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(b.String())))
}

func cachedCLIModels(ctx context.Context, cli string, force bool) []string {
	models, _ := cliModelCatalogue.list(ctx, cliCatalogueKey(cli), force, func(ctx context.Context) ([]string, error) {
		return listCLIModelsUncached(ctx, cli)
	})
	return models
}

// RefreshCLIModels bypasses a completed catalogue while coalescing live probes.
func RefreshCLIModels(ctx context.Context, cli string) []string {
	return cachedCLIModels(ctx, cli, true)
}

func (c *Core) RefreshNativeModels(ctx context.Context, endpoint string) ([]string, error) {
	return c.nativeModels(ctx, endpoint, true)
}
