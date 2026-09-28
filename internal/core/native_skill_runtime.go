package core

import (
	"context"
	"strings"

	"github.com/sPROFFEs/PrAImate/internal/agentic"
)

func nativeSkillDefinitions() []nativeTool {
	return []nativeTool{
		nativeToolDef("skill_load", "Load an eligible skill by its exact catalogue ref and digest. Instructions do not grant execution permissions. Its body appears in the next request, not in the tool log.", `{"ref":{"type":"string"},"digest":{"type":"string"}}`, "ref", "digest"),
		nativeToolDef("skill_read", "Read bounded lines of a loaded skill resource. Its contents appear in the next request.", `{"ref":{"type":"string"},"digest":{"type":"string"},"path":{"type":"string"},"start":{"type":"integer"},"lines":{"type":"integer"}}`, "ref", "digest", "path", "start", "lines"),
	}
}

// Reuse the managed runtime's trust, exact-lock, load-count and resource
// budgets. Payloads are ephemeral: native checkpoints retain tool identities,
// not resident skill bodies that could outlive a revoked approval.
func nativeSkillPayload(ctx context.Context, s *managedSkillSession, system, message string) (string, error) {
	if s == nil {
		return "", nil
	}
	prepared, err := s.prepare(ctx, agentic.ModelInput{SystemPrompt: system, Message: message})
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(prepared.SystemPrompt, system), nil
}
