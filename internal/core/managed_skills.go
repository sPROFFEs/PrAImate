package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"git.jtsec.local/lab/PrAImate/internal/agentic"
	"git.jtsec.local/lab/PrAImate/internal/skills"
)

// managedSkillSession shares a private runtime between the model transport and
// the read-only skill broker. Tool replies contain identities only: bodies and
// resource ranges go into the next bounded payload, never the durable tool log.
type managedSkillSession struct {
	core     *Core
	settings ChatSettings
	runtime  *ControlledRuntime
	state    *SkillRuntimeState
}

func (s *managedSkillSession) Instructions() string {
	return `Skill tools (instructions do not authorize commands):
{"action":"tool","tool":"skill.load","arguments":{"ref":"eligible/ref","digest":"sha256:exact-locked-digest"}}
{"action":"tool","tool":"skill.read","arguments":{"ref":"eligible/ref","digest":"sha256:exact-locked-digest","path":"references/file.md","start":1,"lines":40}}
Loaded bodies and requested resource ranges appear in the next request's praimate-skills blocks. Tool responses confirm preparation only.`
}

func (s *managedSkillSession) ExecuteTool(ctx context.Context, tool string, raw json.RawMessage) (string, error) {
	if err := s.core.requireSkillsV2Rollout(ctx); err != nil {
		return "", err
	}
	if s.runtime == nil {
		return "", fmt.Errorf("skill runtime has not prepared a request")
	}
	var args struct {
		Ref    string `json:"ref"`
		Digest string `json:"digest"`
		Path   string `json:"path,omitempty"`
		Start  int    `json:"start,omitempty"`
		Lines  int    `json:"lines,omitempty"`
	}
	if err := skills.DecodePortableSkillRecord(raw, &args); err != nil {
		return "", err
	}
	if args.Ref == "" || args.Digest == "" {
		return "", fmt.Errorf("skill ref and digest are required")
	}
	switch tool {
	case "skill.load":
		if args.Path != "" || args.Start != 0 || args.Lines != 0 {
			return "", fmt.Errorf("skill.load accepts only ref and digest")
		}
		plan, err := s.runtime.Runtime.Load(ctx, args.Ref, args.Digest)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("skill.load prepared %s@%s; resident=%t", args.Ref, args.Digest, plan.Receipt.Existing), nil
	case "skill.read":
		chunk, err := s.runtime.Runtime.Read(ctx, args.Ref, args.Digest, args.Path, args.Start, args.Lines)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("skill.read prepared %s@%s lines %d-%d; next_cursor=%s", args.Ref, args.Digest, chunk.StartLine, chunk.EndLine, chunk.NextCursor), nil
	default:
		return "", fmt.Errorf("unknown skill tool %q", tool)
	}
}

func (s *managedSkillSession) prepare(ctx context.Context, input agentic.ModelInput) (agentic.ModelInput, error) {
	if err := s.core.requireSkillsV2Rollout(ctx); err != nil {
		return input, err
	}
	// Revalidate trust and exact locks at every outbound turn, including after
	// an approval dialog or a user disabling the local cohort.
	resolved, err := s.core.PreviewBoundSkills(ctx, nil, nil, s.settings)
	if err != nil {
		return input, err
	}
	if s.runtime != nil {
		// Optional bindings can disappear without a resolver error when their
		// approval is revoked. Never send a previously resident revoked body.
		if len(resolved.Bindings) != len(s.runtime.Bindings) {
			return input, fmt.Errorf("context_resync_required: approved skill selection changed during the run")
		}
		for i, binding := range resolved.Bindings {
			old := s.runtime.Bindings[i]
			if binding.Version.Ref != old.Version.Ref || binding.Version.Digest != old.Version.Digest {
				return input, fmt.Errorf("context_resync_required: approved skill selection changed during the run")
			}
		}
	}
	nonSkill := input.SystemPrompt + "\n" + input.Message
	budget := chatRuntimeBudget(s.settings, nonSkill)
	var plan skills.SkillPlan
	if s.runtime == nil {
		scope, err := skills.SelectionScope(s.settings.SkillsV2, s.settings.SkillsLock)
		if err != nil {
			return input, err
		}
		s.runtime, plan, err = ControlledSkillRuntime(ctx, skills.SkillScopes{Session: scope}, budget)
		if err != nil {
			return input, err
		}
	} else {
		plan, err = s.runtime.Runtime.Payload(budget)
		if err != nil {
			return input, err
		}
	}
	payload := ControlledPayload(plan)
	input.SkillBytes = len(payload)
	if err := validateSkillPayload(payload, nonSkill, s.runtime.Config.Budget, budget); err != nil {
		return input, err
	}
	s.state = runtimeState("prepared", plan.Receipt, "")
	s.state.Diagnostics = s.runtime.Diagnostics
	input.SystemPrompt = withSystemContext(input.SystemPrompt, payload)
	if err := validateControlledSkillRequest(s.settings, input.SystemPrompt, input.Message); err != nil {
		return input, err
	}
	return input, nil
}

// No fallback forwards unknown skill operations to the command/MCP broker.
type managedSkillTools struct {
	base   agentic.ToolExecutor
	skills *managedSkillSession
}

func (b *managedSkillTools) Instructions() string {
	return b.base.Instructions() + "\n" + b.skills.Instructions()
}
func (b *managedSkillTools) ExecuteTool(ctx context.Context, tool string, args json.RawMessage) (string, error) {
	if strings.HasPrefix(tool, "skill.") {
		return b.skills.ExecuteTool(ctx, tool, args)
	}
	return b.base.ExecuteTool(ctx, tool, args)
}
