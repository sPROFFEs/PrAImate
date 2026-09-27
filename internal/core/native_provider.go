package core

// The native provider is a transport, not an agent. Only the core runtime may
// authorize/execute returned calls. No provider credentials enter model context.
import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type nativeMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content"`
	Reasoning  string           `json:"reasoning_content,omitempty"`
	ToolCalls  []nativeToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	Images     []nativeImage    `json:"images,omitempty"`
	Usage      *NativeUsage     `json:"-"`
}
type nativeFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}
type nativeToolCall struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Function nativeFunction `json:"function"`
}
type nativeTool struct {
	Type     string               `json:"type"`
	Function nativeToolDefinition `json:"function"`
}
type nativeToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}
type nativeProvider struct {
	route        ChatLocalEndpoint
	http         *http.Client
	withoutUsage bool
}

type nativeHTTPError struct {
	status  int
	message string
}

func (e *nativeHTTPError) Error() string {
	return fmt.Sprintf("native model HTTP %d: %s", e.status, e.message)
}

func nativeBaseURL(endpoint string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("native endpoint must be an HTTP(S) base URL without credentials, query or fragment")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.Path = strings.TrimSuffix(u.Path, "/chat/completions")
	if u.Path == "" {
		u.Path = "/v1"
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func (p nativeProvider) request(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	base, err := nativeBaseURL(p.route.Endpoint)
	if err != nil {
		return nil, err
	}
	client := p.http
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Minute}
	}
	// Do not forward prompts or credentials to a redirect target.
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if p.route.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+p.route.APIKey)
		}
		res, err := copyClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("native model request: %w", err)
		}
		if res.StatusCode >= 200 && res.StatusCode < 300 {
			return res, nil
		}
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 8192))
		res.Body.Close()
		retry := res.StatusCode == 429 || res.StatusCode == 502 || res.StatusCode == 503 || res.StatusCode == 504
		if !retry || attempt == 2 {
			message := string(raw)
			if p.route.APIKey != "" {
				message = strings.ReplaceAll(message, p.route.APIKey, "[redacted]")
			}
			return nil, &nativeHTTPError{res.StatusCode, message}
		}
		delay := time.Duration(attempt+1) * 500 * time.Millisecond
		if seconds, err := strconv.Atoi(res.Header.Get("Retry-After")); err == nil && seconds > 0 && seconds <= 5 {
			delay = time.Duration(seconds) * time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, errors.New("native model retries exhausted")
}

func (p nativeProvider) models(ctx context.Context) ([]string, error) {
	models, err := p.modelsAt(ctx, "/models?prefix=canonical")
	if err == nil && len(models) > 0 {
		return models, nil
	}
	return p.modelsAt(ctx, "/models")
}

func (p nativeProvider) modelsAt(ctx context.Context, path string) ([]string, error) {
	res, err := p.request(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&body); err != nil {
		return nil, err
	}
	var models []string
	for _, m := range body.Data {
		if m.ID != "" {
			models = append(models, m.ID)
		}
	}
	sort.Strings(models)
	return models, nil
}

func (p *nativeProvider) turn(ctx context.Context, messages []nativeMessage, tools []nativeTool, emit StreamHandler) (nativeMessage, error) {
	// Keep the encrypted checkpoint schema backward compatible. Only the wire
	// representation uses multimodal content; neither paths nor image metadata
	// are additional protocol fields sent to the provider.
	type wireMessage struct {
		Role       string           `json:"role"`
		Content    any              `json:"content"`
		Reasoning  string           `json:"reasoning_content,omitempty"`
		ToolCalls  []nativeToolCall `json:"tool_calls,omitempty"`
		ToolCallID string           `json:"tool_call_id,omitempty"`
	}
	wire := make([]wireMessage, len(messages))
	for i, m := range messages {
		var content any = m.Content
		if len(m.Images) > 0 {
			parts := []any{map[string]any{"type": "text", "text": m.Content}}
			for _, img := range m.Images {
				parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]string{"url": img.DataURL}})
			}
			content = parts
		}
		wire[i] = wireMessage{m.Role, content, m.Reasoning, m.ToolCalls, m.ToolCallID}
	}
	request := struct {
		Model         string          `json:"model"`
		Messages      []wireMessage   `json:"messages"`
		Tools         []nativeTool    `json:"tools,omitempty"`
		Stream        bool            `json:"stream"`
		MaxTokens     int             `json:"max_tokens"`
		StreamOptions map[string]bool `json:"stream_options,omitempty"`
	}{p.route.Model, wire, tools, true, p.route.OutputTokens, nil}
	if !p.withoutUsage {
		request.StreamOptions = map[string]bool{"include_usage": true}
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return nativeMessage{}, err
	}
	res, err := p.request(ctx, "POST", "/chat/completions", raw)
	// Some compatible servers reject this optional extension. Retry only an
	// explicit validation rejection, before accepting any output or tool calls.
	var rejected *nativeHTTPError
	if errors.As(err, &rejected) && !p.withoutUsage && (rejected.status == 400 || rejected.status == 422) &&
		(strings.Contains(strings.ToLower(rejected.message), "stream_options") || strings.Contains(strings.ToLower(rejected.message), "include_usage")) {
		p.withoutUsage = true
		request.StreamOptions = nil
		raw, _ = json.Marshal(request)
		res, err = p.request(ctx, "POST", "/chat/completions", raw)
	}
	// A gateway may advertise a bare alias but require provider/model on POST.
	// Retry only its explicit provider-resolution error and only when the
	// canonical catalogue has exactly one matching ID; never guess a provider.
	if errors.As(err, &rejected) && rejected.status == 400 && !strings.Contains(p.route.Model, "/") &&
		strings.Contains(strings.ToLower(rejected.message), "unable to determine provider for model") {
		if catalogue, catalogueErr := p.modelsAt(ctx, "/models?prefix=canonical"); catalogueErr == nil {
			if canonical := uniqueCanonicalModel(catalogue, p.route.Model); canonical != "" {
				request.Model = canonical
				raw, _ = json.Marshal(request)
				res, err = p.request(ctx, "POST", "/chat/completions", raw)
				if err == nil {
					p.route.Model = canonical
				}
			}
		}
	}
	if err != nil {
		return nativeMessage{}, err
	}
	defer res.Body.Close()
	result := nativeMessage{Role: "assistant"}
	calls := map[int]*nativeToolCall{}
	finish := ""
	complete := false
	scanner := bufio.NewScanner(io.LimitReader(res.Body, 16<<20))
	scanner.Buffer(make([]byte, 4096), 2<<20)
	consume := func(data string) error {
		if data == "[DONE]" {
			complete = true
			return nil
		}
		var chunk struct {
			Usage *NativeUsage `json:"usage"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
			Choices []struct {
				Delta struct {
					Content      string `json:"content"`
					Reasoning    string `json:"reasoning_content"`
					ReasoningAlt string `json:"reasoning"`
					Calls        []struct {
						Index    int            `json:"index"`
						ID       string         `json:"id"`
						Function nativeFunction `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				Finish string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return fmt.Errorf("invalid model stream JSON: %w", err)
		}
		if chunk.Error != nil {
			if p.route.APIKey != "" {
				chunk.Error.Message = strings.ReplaceAll(chunk.Error.Message, p.route.APIKey, "[redacted]")
			}
			return errors.New(chunk.Error.Message)
		}
		if chunk.Usage != nil && chunk.Usage.PromptTokens > 0 && chunk.Usage.CompletionTokens >= 0 {
			result.Usage = chunk.Usage
		}
		if len(chunk.Choices) == 0 {
			return nil
		}
		c := chunk.Choices[0]
		if c.Finish != "" {
			finish = c.Finish
		}
		result.Content += c.Delta.Content
		thinking := c.Delta.Reasoning
		if thinking == "" {
			thinking = c.Delta.ReasoningAlt
		}
		result.Reasoning += thinking
		if emit != nil {
			if c.Delta.Content != "" {
				emit(StreamEvent{Type: "text", Text: c.Delta.Content})
			}
			if thinking != "" {
				emit(StreamEvent{Type: "reasoning", Text: thinking})
			}
		}
		for _, delta := range c.Delta.Calls {
			if delta.Index < 0 || delta.Index > 63 {
				return errors.New("invalid or excessive tool-call index")
			}
			call := calls[delta.Index]
			if call == nil {
				call = &nativeToolCall{Type: "function"}
				calls[delta.Index] = call
			}
			if delta.ID != "" {
				call.ID = delta.ID
			}
			if delta.Function.Name != "" {
				call.Function.Name += delta.Function.Name
			}
			call.Function.Arguments += delta.Function.Arguments
		}
		return nil
	}
	var data []string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if len(data) > 0 {
				err = consume(strings.Join(data, "\n"))
				data = nil
				if err != nil {
					return result, err
				}
			}
			if complete {
				break
			}
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return result, err
	}
	if len(data) > 0 {
		if err := consume(strings.Join(data, "\n")); err != nil {
			return result, err
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if !complete && finish == "" {
		return result, errors.New("model stream ended before completion")
	}
	if finish == "length" {
		return result, errors.New("model output token limit reached; increase output budget")
	}
	if finish != "stop" && finish != "tool_calls" {
		return result, fmt.Errorf("model returned unsupported finish reason %q", finish)
	}
	ids := map[string]bool{}
	for i := 0; i < len(calls); i++ {
		call := calls[i]
		if call == nil || call.ID == "" || ids[call.ID] || call.Function.Name == "" || !json.Valid([]byte(call.Function.Arguments)) {
			return result, errors.New("incomplete or invalid model tool call")
		}
		ids[call.ID] = true
		result.ToolCalls = append(result.ToolCalls, *call)
	}
	if len(result.ToolCalls) > 0 && finish != "tool_calls" {
		return result, errors.New("tool calls without tool_calls completion")
	}
	if finish == "tool_calls" && len(result.ToolCalls) == 0 {
		return result, errors.New("tool_calls completion without calls")
	}
	return result, nil
}

func uniqueCanonicalModel(catalogue []string, bare string) string {
	var selected string
	for _, id := range catalogue {
		if strings.HasSuffix(id, "/"+bare) {
			if selected != "" && selected != id {
				return ""
			}
			selected = id
		}
	}
	return selected
}
