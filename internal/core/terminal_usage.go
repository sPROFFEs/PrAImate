package core

// A per-launch, authenticated loopback receiver for native terminal usage.
// Raw telemetry is never logged or persisted. Only provider counters and model
// labels reach the encrypted database. No transcript/profile scanning is used.

import (
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type TerminalUsage struct {
	Args                                   []string
	Env                                    map[string]string
	endpoint, token, cli, model, pluginDir string
	core                                   *Core
	server                                 *http.Server
	once                                   sync.Once
	done                                   chan struct{}
	captureMu                              sync.Mutex
	captured                               []StreamEvent
}

func (c *Core) BeginTerminalUsage(ctx context.Context, cli, model string, base map[string]string) (*TerminalUsage, error) {
	return beginUsageReceiver(ctx, c, cli, model, base)
}

// A nil core captures reports in memory for a headless adapter to emit through
// its existing metering path. It must not also insert terminal usage rows.
func beginUsageReceiver(ctx context.Context, c *Core, cli, model string, base map[string]string) (*TerminalUsage, error) {
	// praimate-cli already records through Chat; an extra receiver would
	// double count. Antigravity has no verified interactive usage export.
	switch cli {
	case "codex", "claude", "openclaude", "copilot", "opencode", "praimate-code":
	default:
		return nil, nil
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return nil, err
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	u := &TerminalUsage{core: c, cli: cli, model: model, token: hex.EncodeToString(secret[:]), endpoint: "http://" + l.Addr().String(), Env: map[string]string{}, done: make(chan struct{})}
	u.server = &http.Server{Handler: http.HandlerFunc(u.receive), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, MaxHeaderBytes: 16 << 10}
	u.Env["PRAIMATE_USAGE_TOKEN"] = u.token
	if cli == "codex" {
		// Scalar overrides also survive Windows batch launch quoting.
		u.Args = []string{"-c", "otel.exporter.otlp-http.endpoint=" + u.endpoint + "/v1/logs", "-c", "otel.exporter.otlp-http.protocol=json", "-c", "otel.exporter.otlp-http.headers.Authorization=Bearer ${PRAIMATE_USAGE_TOKEN}", "-c", "otel.log_user_prompt=false"}
	} else if cli == "opencode" || cli == "praimate-code" {
		dir, err := os.MkdirTemp("", "praimate-usage-")
		if err != nil {
			l.Close()
			return nil, err
		}
		u.pluginDir = dir
		path := filepath.Join(dir, "usage.mjs")
		if err := os.WriteFile(path, []byte(openCodeUsagePlugin), 0600); err != nil {
			l.Close()
			os.RemoveAll(dir)
			return nil, err
		}
		config := map[string]any{}
		raw := base["OPENCODE_CONFIG_CONTENT"]
		if raw == "" {
			raw = os.Getenv("OPENCODE_CONFIG_CONTENT")
		}
		if raw != "" && (json.Unmarshal([]byte(raw), &config) != nil || config == nil) {
			l.Close()
			os.RemoveAll(dir)
			return nil, errors.New("invalid OpenCode launch configuration for usage tracking")
		}
		plugins, _ := config["plugin"].([]any)
		if value, exists := config["plugin"]; exists && value != nil && plugins == nil {
			l.Close()
			os.RemoveAll(dir)
			return nil, errors.New("OpenCode launch plugins must be an array")
		}
		p := filepath.ToSlash(path)
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		config["plugin"] = append(plugins, (&url.URL{Scheme: "file", Path: p}).String())
		encoded, _ := json.Marshal(config)
		u.Env["OPENCODE_CONFIG_CONTENT"] = string(encoded)
		u.Env["PRAIMATE_USAGE_ENDPOINT"] = u.endpoint + "/v1/usage"
	} else {
		u.Env["OTEL_EXPORTER_OTLP_ENDPOINT"] = u.endpoint
		u.Env["OTEL_EXPORTER_OTLP_PROTOCOL"] = "http/json"
		u.Env["OTEL_EXPORTER_OTLP_HEADERS"] = "Authorization=Bearer " + u.token
		for _, signal := range []string{"LOGS", "TRACES", "METRICS"} {
			u.Env["OTEL_EXPORTER_OTLP_"+signal+"_ENDPOINT"] = u.endpoint + "/v1/" + strings.ToLower(signal)
			u.Env["OTEL_EXPORTER_OTLP_"+signal+"_PROTOCOL"] = "http/json"
			u.Env["OTEL_EXPORTER_OTLP_"+signal+"_HEADERS"] = u.Env["OTEL_EXPORTER_OTLP_HEADERS"]
		}
		u.Env["OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT"] = "false"
		if cli == "copilot" {
			u.Env["COPILOT_OTEL_ENABLED"] = "true"
			u.Env["COPILOT_OTEL_EXPORTER_TYPE"] = "otlp-http"
			u.Env["COPILOT_OTEL_FILE_EXPORTER_PATH"] = ""
		} else {
			u.Env["CLAUDE_CODE_ENABLE_TELEMETRY"] = "1"
			u.Env["OTEL_LOGS_EXPORTER"] = "otlp"
			u.Env["OTEL_METRICS_EXPORTER"] = "none"
			u.Env["OTEL_TRACES_EXPORTER"] = "none"
			u.Env["OTEL_LOGS_EXPORT_INTERVAL"] = "1000"
			for _, key := range []string{"OTEL_LOG_USER_PROMPTS", "OTEL_LOG_ASSISTANT_RESPONSES", "OTEL_LOG_TOOL_DETAILS", "OTEL_LOG_TOOL_CONTENT", "OTEL_LOG_RAW_API_BODIES"} {
				u.Env[key] = "0"
			}
		}
	}
	go func() { _ = u.server.Serve(l) }()
	go func() {
		select {
		case <-ctx.Done():
			u.Close()
		case <-u.done:
		}
	}()
	return u, nil
}

func (u *TerminalUsage) Close() {
	if u == nil {
		return
	}
	u.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = u.server.Shutdown(ctx)
		_ = u.server.Close()
		if u.pluginDir != "" {
			_ = os.RemoveAll(u.pluginDir)
		}
		close(u.done)
	})
}

func (u *TerminalUsage) receive(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" || r.Header.Get("Origin") != "" {
		http.Error(w, "method not allowed", 405)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+u.token)) != 1 {
		http.Error(w, "unauthorized", 401)
		return
	}
	kind := strings.TrimPrefix(r.URL.Path, "/v1/")
	if kind != "logs" && kind != "traces" && kind != "metrics" && kind != "usage" {
		http.NotFound(w, r)
		return
	}
	var reader io.Reader = http.MaxBytesReader(w, r.Body, 2<<20)
	if r.Header.Get("Content-Encoding") == "gzip" {
		z, err := gzip.NewReader(reader)
		if err != nil {
			http.Error(w, "invalid payload", 400)
			return
		}
		defer z.Close()
		reader = z
	}
	body, err := io.ReadAll(io.LimitReader(reader, (2<<20)+1))
	if err != nil || len(body) > 2<<20 {
		http.Error(w, "payload too large", 413)
		return
	}
	events, err := parseTerminalUsage(body, u.cli, kind)
	if err != nil {
		http.Error(w, "invalid telemetry", 400)
		return
	}
	if u.core == nil {
		u.captureMu.Lock()
		u.captured = append(u.captured, events...)
		u.captureMu.Unlock()
	} else if u.core.store != nil && len(events) > 0 {
		tx, err := u.core.store.DB().BeginTx(r.Context(), nil)
		if err != nil {
			http.Error(w, "usage store unavailable", 503)
			return
		}
		defer tx.Rollback()
		for _, e := range events {
			if e.ID == "" || e.Usage == nil {
				continue
			}
			model := e.Model
			if model == "" {
				model = u.model
			}
			if len(model) > 200 {
				continue
			}
			key := sha256.Sum256([]byte(u.token + "\x00" + e.ID))
			_, err = tx.ExecContext(r.Context(), `INSERT OR IGNORE INTO usage_runs(id,started_at,cli,model,surface,outcome,input_tokens,output_tokens,reported_calls) VALUES(?,?,?,?,?,'completed',?,?,1)`, hex.EncodeToString(key[:]), time.Now().UTC().Format(time.RFC3339Nano), u.cli, model, "terminal", e.Usage.PromptTokens, e.Usage.CompletionTokens)
			if err != nil {
				http.Error(w, "usage store unavailable", 503)
				return
			}
		}
		if err = tx.Commit(); err != nil {
			http.Error(w, "usage store unavailable", 503)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, "{}")
}

type otelAttribute struct {
	Key   string `json:"key"`
	Value struct {
		String string          `json:"stringValue"`
		Int    json.RawMessage `json:"intValue"`
		Double *float64        `json:"doubleValue"`
	} `json:"value"`
}

func otelFields(attrs []otelAttribute) map[string]any {
	out := map[string]any{}
	for _, a := range attrs {
		if len(a.Key) > 100 {
			continue
		}
		value := a.Value.String
		if len(a.Value.Int) > 0 {
			value = strings.Trim(string(a.Value.Int), `"`)
		}
		if n, err := strconv.ParseFloat(value, 64); err == nil {
			out[a.Key] = n
		} else {
			out[a.Key] = value
		}
		if a.Value.Double != nil {
			out[a.Key] = *a.Value.Double
		}
	}
	return out
}

func parseTerminalUsage(raw []byte, cli, kind string) ([]StreamEvent, error) {
	var result []StreamEvent
	if kind == "usage" {
		if !isOpenCodeLikeAdapter(cli) {
			return nil, nil
		}
		var e struct {
			ID, Model string
			Tokens    map[string]any
		}
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, err
		}
		if u := openCodeUsage(e.Tokens); u != nil && e.ID != "" {
			result = append(result, StreamEvent{Type: "usage", ID: e.ID, Model: e.Model, Usage: u})
		}
		return result, nil
	}
	var payload struct {
		ResourceLogs []struct {
			ScopeLogs []struct {
				LogRecords []struct {
					Time       string          `json:"timeUnixNano"`
					Attributes []otelAttribute `json:"attributes"`
				} `json:"logRecords"`
			} `json:"scopeLogs"`
		} `json:"resourceLogs"`
		ResourceSpans []struct {
			ScopeSpans []struct {
				Spans []struct {
					TraceID    string          `json:"traceId"`
					SpanID     string          `json:"spanId"`
					Attributes []otelAttribute `json:"attributes"`
				} `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"resourceSpans"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	if kind == "logs" && (cli == "claude" || cli == "openclaude" || cli == "codex") {
		for _, resource := range payload.ResourceLogs {
			for _, scope := range resource.ScopeLogs {
				for _, record := range scope.LogRecords {
					f := otelFields(record.Attributes)
					name := stringFromMap(f, "event.name")
					var u *NativeUsage
					if cli == "codex" && (name == "codex.sse_event" || name == "codex.websocket_event") && stringFromMap(f, "event.kind") == "response.completed" {
						u = reportedUsage(f, "input_token_count", "output_token_count")
					}
					if cli != "codex" && (name == "api_request" || name == "claude_code.api_request") {
						u = reportedUsage(f, "input_tokens", "output_tokens")
						if u != nil {
							addUsageParts(u, f, []string{"cache_read_tokens", "cache_creation_tokens"}, nil)
						}
					}
					if u == nil || record.Time == "" {
						continue
					}
					id := stringFromMap(f, "request_id")
					if id == "" {
						id = fmt.Sprint(f["session.id"], f["conversation.id"], record.Time, f["event.sequence"])
					}
					result = append(result, StreamEvent{Type: "usage", ID: id, Model: stringFromMap(f, "model"), Usage: u})
				}
			}
		}
	}
	if kind == "traces" && cli == "copilot" {
		for _, resource := range payload.ResourceSpans {
			for _, scope := range resource.ScopeSpans {
				for _, span := range scope.Spans {
					f := otelFields(span.Attributes)
					if stringFromMap(f, "gen_ai.operation.name") != "chat" || span.SpanID == "" {
						continue
					}
					u := reportedUsage(f, "gen_ai.usage.input_tokens", "gen_ai.usage.output_tokens")
					if u == nil {
						continue
					}
					model := stringFromMap(f, "gen_ai.response.model")
					if model == "" {
						model = stringFromMap(f, "gen_ai.request.model")
					}
					result = append(result, StreamEvent{Type: "usage", ID: span.TraceID + ":" + span.SpanID, Model: model, Usage: u})
				}
			}
		}
	}
	return result, nil
}

const openCodeUsagePlugin = `export const PrAImateUsage = async () => {
 const models = new Map();
 return {
  event: async ({event}) => {
    if (event.type === 'message.updated') {
      const m = event.properties?.info;
      if (m?.role === 'assistant' && m.id && m.modelID) {
        models.set(m.id,m.modelID);
        if (models.size > 128) models.delete(models.keys().next().value);
      }
      return;
    }
    if (event.type !== 'message.part.updated') return;
    const p = event.properties?.part;
    if (p?.type !== 'step-finish' || !p.id || !p.tokens) return;
    try {
      await fetch(process.env.PRAIMATE_USAGE_ENDPOINT, {method:'POST',
        headers:{'Content-Type':'application/json',Authorization:'Bearer '+process.env.PRAIMATE_USAGE_TOKEN},
        body:JSON.stringify({id:p.id,model:models.get(p.messageID) || '',tokens:p.tokens}),signal:AbortSignal.timeout(2000)});
    } catch {}
  }
 };
};
`
