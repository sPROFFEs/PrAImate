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
	if usage := reportedUsage(fields, "input", "output"); usage != nil {
		emit(StreamEvent{Type: "usage", ID: stringFromMap(part, "id"), Usage: usage, OK: true})
	}
}
