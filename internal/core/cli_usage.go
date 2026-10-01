package core

import "math"

// Only accept explicit provider token fields. Cached and reasoning token
// breakdowns are not added again to the reported input/output totals.
func reportedUsage(fields map[string]any, inputKey, outputKey string) *NativeUsage {
	number := func(key string) (int, bool) {
		v, ok := fields[key].(float64)
		if !ok || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > float64(min(uint64(1<<52), uint64(^uint(0)>>1)/2)) || math.Trunc(v) != v {
			return 0, false
		}
		return int(v), true
	}
	input, inOK := number(inputKey)
	output, outOK := number(outputKey)
	if !inOK || !outOK {
		return nil
	}
	return &NativeUsage{PromptTokens: input, CompletionTokens: output, TotalTokens: input + output}
}

func emitOpenCodeUsage(part map[string]any, emit StreamHandler) {
	fields, _ := part["tokens"].(map[string]any)
	if usage := openCodeUsage(fields); usage != nil {
		emit(StreamEvent{Type: "usage", ID: stringFromMap(part, "id"), Usage: usage, OK: true})
	}
}

// Claude reports cache reads/writes separately from uncached input.
func claudeUsage(fields map[string]any) *NativeUsage {
	u := reportedUsage(fields, "input_tokens", "output_tokens")
	if u == nil {
		return nil
	}
	addUsageParts(u, fields, []string{"cache_read_input_tokens", "cache_creation_input_tokens"}, nil)
	return u
}

// OpenCode separates both cache input and reasoning output from text tokens.
// See its session.getUsage implementation; Codex/OpenAI totals already include
// these categories and must not use this addition.
func openCodeUsage(fields map[string]any) *NativeUsage {
	u := reportedUsage(fields, "input", "output")
	if u == nil {
		return nil
	}
	cache, _ := fields["cache"].(map[string]any)
	addUsageParts(u, cache, []string{"read", "write"}, nil)
	addUsageParts(u, fields, nil, []string{"reasoning"})
	return u
}

func addUsageParts(u *NativeUsage, fields map[string]any, inputs, outputs []string) {
	for _, group := range []struct {
		keys   []string
		target *int
	}{{inputs, &u.PromptTokens}, {outputs, &u.CompletionTokens}} {
		for _, key := range group.keys {
			if n := reportedUsage(map[string]any{"input": fields[key], "output": float64(0)}, "input", "output"); n != nil && n.PromptTokens <= int(^uint(0)>>1)/4-*group.target {
				*group.target += n.PromptTokens
			}
		}
	}
	u.TotalTokens = u.PromptTokens + u.CompletionTokens
}
