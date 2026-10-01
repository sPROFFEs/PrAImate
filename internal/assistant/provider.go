package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type HTTPProvider struct {
	Endpoint, Model, APIKey string
	Managed                 bool
	Client                  *http.Client
	Usage                   func(int, int)
}

func (p *HTTPProvider) Start(context.Context) error { return nil }
func (p *HTTPProvider) Stop(context.Context) error  { return nil }
func (p *HTTPProvider) base() (string, error) {
	u, err := url.Parse(p.Endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("invalid Assistant endpoint")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if u.Path == "" {
		u.Path = "/v1"
	}
	return u.String(), nil
}
func (p *HTTPProvider) client() *http.Client {
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Minute}
	}
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &copy
}
func (p *HTTPProvider) request(ctx context.Context, method, suffix string, body []byte) (*http.Response, error) {
	base, err := p.base()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, base+suffix, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	return p.client().Do(req)
}
func (p *HTTPProvider) Health(ctx context.Context) error {
	response, err := p.request(ctx, "GET", "/models", nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("Assistant endpoint health HTTP %d", response.StatusCode)
	}
	return nil
}
func (p *HTTPProvider) Generate(ctx context.Context, r Request) (string, error) {
	body := map[string]any{"model": p.Model, "stream": false, "max_tokens": r.Config.Output, "temperature": r.Config.Temperature, "top_p": r.Config.TopP, "messages": []map[string]string{{"role": "system", "content": r.System}, {"role": "user", "content": r.Prompt}}, "response_format": map[string]string{"type": "json_object"}}
	// Application intentions need a complete small JSON response, not an
	// unbounded thinking preamble. Compatible local servers can enforce this.
	body["chat_template_kwargs"] = map[string]bool{"enable_thinking": false}
	if p.Managed {
		// Tiny models can omit discriminator fields even in JSON-object mode.
		// The pinned llama.cpp runtime constrains the complete decision shape.
		body["response_format"] = map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "assistant_decision", "strict": true, "schema": map[string]any{"oneOf": []any{
			map[string]any{"type": "object", "properties": map[string]any{"type": map[string]string{"const": "final"}, "message": map[string]string{"type": "string"}}, "required": []string{"type", "message"}, "additionalProperties": false},
			map[string]any{"type": "object", "properties": map[string]any{"type": map[string]string{"const": "action"}, "action": map[string]string{"type": "string"}, "arguments": map[string]any{"type": "object"}}, "required": []string{"type", "action", "arguments"}, "additionalProperties": false},
		}}}}
		body["top_k"] = r.Config.TopK
		body["repeat_penalty"] = r.Config.RepeatPenalty
	}
	for attempt := 0; attempt < 3; attempt++ {
		raw, err := json.Marshal(body)
		if err != nil {
			return "", err
		}
		response, err := p.request(ctx, "POST", "/chat/completions", raw)
		if err != nil {
			return "", err
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
		response.Body.Close()
		if readErr != nil {
			return "", readErr
		}
		if len(data) > 1<<20 {
			return "", errors.New("Assistant model response exceeds 1 MiB")
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			if _, present := body["response_format"]; present && response.StatusCode == 400 && (strings.Contains(string(data), "response_format") || strings.Contains(string(data), "json_object")) {
				delete(body, "response_format")
				continue
			}
			if _, present := body["chat_template_kwargs"]; present && !p.Managed && response.StatusCode == 400 && (strings.Contains(string(data), "chat_template_kwargs") || strings.Contains(string(data), "enable_thinking")) {
				delete(body, "chat_template_kwargs")
				continue
			}
			return "", fmt.Errorf("Assistant endpoint HTTP %d: %s", response.StatusCode, compact(func() string {
				message := string(data)
				if p.APIKey != "" {
					message = strings.ReplaceAll(message, p.APIKey, "[redacted]")
				}
				return message
			}(), 1500))
		}
		var reply struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
				Finish string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				Input  int `json:"prompt_tokens"`
				Output int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(data, &reply); err != nil {
			return "", err
		}
		if reply.Usage != nil && p.Usage != nil {
			p.Usage(reply.Usage.Input, reply.Usage.Output)
		}
		if len(reply.Choices) != 1 {
			return "", errors.New("Assistant endpoint returned no single completion")
		}
		if reply.Choices[0].Finish == "length" {
			return "", errors.New("Assistant output limit reached before a complete structured response; raise Output tokens in Assistant Settings or choose a different model")
		}
		return reply.Choices[0].Message.Content, nil
	}
	return "", errors.New("Assistant endpoint rejected structured output")
}
