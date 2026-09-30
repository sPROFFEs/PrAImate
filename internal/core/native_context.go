package core

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"unicode"
	"unicode/utf8"
)

// NativeUsage is measured by the endpoint, not inferred from response bytes.
type NativeUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type NativeContextStatus struct {
	Model           string       `json:"model"`
	Source          string       `json:"source,omitempty"`
	Window          int          `json:"window_tokens"`
	OutputReserve   int          `json:"output_reserve_tokens"`
	SafetyReserve   int          `json:"safety_reserve_tokens"`
	InputLimit      int          `json:"input_limit_tokens"`
	EstimatedInput  int          `json:"estimated_input_tokens"`
	LastUsage       *NativeUsage `json:"last_usage,omitempty"`
	Calibration     float64      `json:"calibration"`
	Compactions     int          `json:"compactions"`
	OutputAutomatic bool         `json:"output_automatic"`
	LastOutputLimit int          `json:"last_output_limit_tokens,omitempty"`
}

// No universal tokenizer exists for local/remote routers. This deliberately
// conservative estimate distinguishes words, punctuation and Unicode instead
// of counting JSON escaping or base64 as model tokens. Usage can increase the
// estimate for a particular model; it never makes budgeting less conservative.
func nativeTextTokens(s string) int {
	tokens, word, space := 0, 0, 0
	flush := func() { tokens += (word+2)/3 + (space+3)/4; word, space = 0, 0 }
	for _, r := range s {
		switch {
		case r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			if space > 0 {
				flush()
			}
			word++
		case r == ' ' || r == '\t':
			if word > 0 {
				flush()
			}
			space++
		default:
			flush()
			tokens += max(1, (utf8.RuneLen(r)+1)/2)
		}
	}
	flush()
	return tokens
}

func nativeMessageTokens(messages []nativeMessage) int {
	n := 16 // template/reply priming
	for _, m := range messages {
		n += 12 + nativeTextTokens(m.Role) + nativeTextTokens(m.Content) + nativeTextTokens(m.Reasoning) + nativeTextTokens(m.ToolCallID)
		for _, call := range m.ToolCalls {
			n += 12 + nativeTextTokens(call.ID) + nativeTextTokens(call.Function.Name) + nativeTextTokens(call.Function.Arguments)
		}
		for _, img := range m.Images {
			// Vision encoders vary. Reserve at least 1024 tokens, or one per
			// 32px patch; provider usage calibrates this alongside text.
			n += max(1024, ((img.Width+31)/32)*((img.Height+31)/32))
		}
	}
	return n
}

func nativeContextBudget(route ChatLocalEndpoint, previous NativeContextStatus) NativeContextStatus {
	if previous.Model != route.Model {
		previous = NativeContextStatus{Model: route.Model}
	}
	previous.Window, previous.OutputReserve = route.ContextTokens, route.OutputTokens
	previous.Source = route.ContextSource
	previous.OutputAutomatic = route.OutputAutomatic
	previous.SafetyReserve = max(256, route.ContextTokens/20)
	previous.InputLimit = previous.Window - previous.OutputReserve - previous.SafetyReserve
	if previous.Calibration < 1 || math.IsNaN(previous.Calibration) || math.IsInf(previous.Calibration, 0) {
		previous.Calibration = 1
	}
	return previous
}

// The reserve guarantees some output space during compaction. In automatic
// mode it is a minimum, not a generation ceiling: short prompts can use the
// remaining window for reasoning and the final answer. Estimates already
// include tools, images, skill payloads and upward tokenizer calibration.
func nativeOutputLimit(route ChatLocalEndpoint, status NativeContextStatus) int {
	if !route.OutputAutomatic {
		return route.OutputTokens
	}
	available := status.Window - status.SafetyReserve - status.EstimatedInput
	return min(16384, available)
}

func (s *NativeContextStatus) observe(usage *NativeUsage, uncalibrated int) {
	if usage == nil {
		s.LastUsage = nil
		return
	}
	copyUsage := *usage
	s.LastUsage = &copyUsage
	if uncalibrated > 0 {
		s.Calibration = max(s.Calibration, float64(usage.PromptTokens)*1.1/float64(uncalibrated))
	}
}

func nativeContextEvent(kind string, status NativeContextStatus) StreamEvent {
	raw, _ := json.Marshal(status)
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	detail := fmt.Sprintf("Context ~%d/%d input tokens; %d output + %d safety reserved", status.EstimatedInput, status.InputLimit, status.OutputReserve, status.SafetyReserve)
	if status.OutputAutomatic {
		detail += fmt.Sprintf("; automatic output limit %d (includes reasoning)", status.LastOutputLimit)
	}
	if kind == "usage" && status.LastUsage != nil {
		detail = fmt.Sprintf("Endpoint usage: %d input / %d output tokens", status.LastUsage.PromptTokens, status.LastUsage.CompletionTokens)
	}
	return StreamEvent{Type: kind, Detail: detail, Raw: fields, OK: true}
}

// NativeContext returns only budgeting metadata, never prompts, images or keys.
// LastUsage refers to the last completed request, not the next request's size.
func (c *Core) NativeContext(ctx context.Context, chatID string) (*NativeContextStatus, error) {
	chat, err := c.GetChat(ctx, chatID)
	if err != nil {
		return nil, err
	}
	route, err := c.resolveNativeRoute(ctx, chat.Settings.Local, chat.Settings.Model)
	if err != nil {
		return nil, err
	}
	var session nativeSession
	if nativeIDPattern.MatchString(chat.SessionID) {
		raw, err := c.GetSetting(ctx, ScopeCLI, "native.session."+chat.SessionID)
		if err != nil {
			return nil, err
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &session); err != nil {
				return nil, err
			}
		}
	}
	status := nativeContextBudget(*route, session.Context)
	return &status, nil
}
