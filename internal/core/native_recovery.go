package core

import "math"

const nativeOutputRecoveryPrompt = "\n\nThe previous response exhausted its generation space. Continue the current task using the retained context. Keep reasoning concise and move to the next concrete tool call or answer. Continue any visible partial answer without repeating it. Incomplete tool calls were discarded and never executed; generate a complete call if still needed. Completed tool results describe actions that already happened: inspect state before repeating an action."

func reserveNativeRecoveryOutput(s *nativeSession, extraTokens int) {
	// Leave space for immutable instructions, the latest user request, and
	// a bounded continuation excerpt. The normal compactor handles history.
	essential := []nativeMessage{s.Messages[0]}
	for i := len(s.Messages) - 1; i > 0; i-- {
		if s.Messages[i].Role == "user" {
			essential = append(essential, s.Messages[i])
			break
		}
	}
	input := int(math.Ceil(float64(nativeMessageTokens(essential)+extraTokens)*s.Context.Calibration)) + 768
	maximum := s.Context.Window - s.Context.SafetyReserve - input
	if maximum > s.Context.OutputReserve {
		s.Context.OutputReserve = min(maximum, max(s.Context.OutputReserve*2, s.Context.LastOutputLimit*2))
		s.Context.InputLimit = s.Context.Window - s.Context.SafetyReserve - s.Context.OutputReserve
	}
}
