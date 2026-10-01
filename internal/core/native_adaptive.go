package core

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
)

func nativeEssentialMessages(messages []nativeMessage) []nativeMessage {
	essential := []nativeMessage{messages[0]}
	for i := len(messages) - 1; i > 0; i-- {
		if messages[i].Role == "user" {
			essential = append(essential, messages[i])
			break
		}
	}
	return essential
}

// An undiscovered window is a planning threshold, never an input rejection.
// Keep the current task intact and use backend rejections to learn real limits.
// With a known window, automatic reserves yield space to essential input.
func adaptNativeInputBudget(s *nativeSession, extraTokens int) {
	essential := int(math.Ceil(float64(nativeMessageTokens(nativeEssentialMessages(s.Messages))+extraTokens)*s.Context.Calibration)) + 8
	status := &s.Context
	status.InputUncertain = false
	if status.WindowKnown && status.OutputAutomatic && status.Source != "chat override" && essential >= status.Window {
		// A heuristic tokenizer is not authority to reject mandatory input.
		// Ask the backend with the intact task and no inferred output ceiling.
		status.InputUncertain = true
		status.OutputReserve, status.SafetyReserve = 0, 0
		status.InputLimit = essential
		return
	}
	if !status.WindowKnown {
		status.Window = max(status.Window, int(math.Ceil(float64(essential+status.OutputReserve+256)/0.95)))
		status.SafetyReserve = max(256, status.Window/20)
	} else if status.OutputAutomatic && essential < status.Window {
		status.SafetyReserve = min(status.SafetyReserve, max(0, status.Window-essential-128))
		status.OutputReserve = min(status.OutputReserve, max(1, status.Window-status.SafetyReserve-essential))
	}
	status.InputLimit = status.Window - status.SafetyReserve - status.OutputReserve
}

var nativeRejectedWindowPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)maximum context length(?:\s+is|\s+of|\s*:)\s*([0-9]+)`),
	regexp.MustCompile(`(?i)(?:context window|context length|context size)(?:\s+of)?(?:\s+only)?\s*[:=]?\s*([0-9]+)`),
}

// Read explicit serving limits only from a rejected context request. Never
// interpret the requested/completion token count as the model's window.
func nativeRejectedWindow(message string) int {
	var data map[string]any
	if json.Unmarshal([]byte(message), &data) == nil {
		maps := []map[string]any{data}
		if nested, ok := data["error"].(map[string]any); ok {
			maps = append(maps, nested)
		}
		for _, fields := range maps {
			for _, key := range []string{"n_ctx", "max_model_len", "context_length"} {
				if n, ok := fields[key].(float64); ok && n == math.Trunc(n) && validNativeWindow(int(n)) {
					return int(n)
				}
			}
		}
	}
	for _, pattern := range nativeRejectedWindowPatterns {
		match := pattern.FindStringSubmatch(message)
		if len(match) == 2 {
			if n, err := strconv.Atoi(match[1]); err == nil && validNativeWindow(n) {
				return n
			}
		}
	}
	return 0
}
