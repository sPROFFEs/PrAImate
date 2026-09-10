package core

import (
	"context"
	"errors"
	"html"
	"strings"

	"git.jtsec.local/lab/PrAImate/internal/skills"
)

func defaultControlledSkillBudget() skills.SkillBudget {
	return skills.SkillBudget{CatalogTokens: 1000, BodyTokens: 4000, ResourceTokens: 2000, TotalTokens: 6000, MaxActive: 3, MaxLoadCallsPerTurn: 4, MaxResourceReadsPerTurn: 6, MaxLoadedBytesFallback: 16384}
}

// Count the serialized wrapper as well as its bodies. These are estimates;
// the independent byte ceiling remains enforceable without a tokenizer.
func validateSkillPayload(payload, nonSkill string, limits skills.SkillBudget, budget skills.ContextBudget) error {
	if len(payload) > limits.MaxLoadedBytesFallback || (len(payload)+2)/3 > limits.TotalTokens {
		return errors.New("context_budget_exceeded: serialized skill payload")
	}
	available := min(budget.InputLimit, budget.ModelWindow-budget.ReservedOutput-budget.SafetyMargin)
	if (len(payload)+len(nonSkill)+2)/3 > available {
		return errors.New("context_budget_exceeded: serialized PrAImate request")
	}
	return nil
}

func validateControlledSkillRequest(settings ChatSettings, system, message string) error {
	if settings.SkillsV2 == nil {
		return nil
	}
	budget := chatRuntimeBudget(settings, "")
	available := min(budget.InputLimit, budget.ModelWindow-budget.ReservedOutput-budget.SafetyMargin)
	if (len(system)+len(message)+2)/3 > available {
		return errors.New("context_budget_exceeded: final serialized PrAImate request")
	}
	return nil
}

// ControlledSkillRuntime is the only P4 entry point for materialising v2
// skill text. It deliberately accepts host-built scopes and budgets, never
// configuration supplied by a model, package or GUI boolean.
type ControlledRuntime struct {
	Runtime     *skills.Runtime
	store       *skills.HostSkillStore
	Config      *skills.SkillConfig
	Diagnostics []skills.SkillDiagnostic
	Bindings    []skills.ResolvedSkillBinding
}

func (r *ControlledRuntime) Close() error {
	if r == nil || r.store == nil {
		return nil
	}
	return r.store.Close()
}

func ControlledSkillRuntime(ctx context.Context, scopes skills.SkillScopes, budget skills.ContextBudget) (*ControlledRuntime, skills.SkillPlan, error) {
	store, err := OpenSkillStore(skills.PackageLimits{})
	if err != nil {
		return nil, skills.SkillPlan{}, err
	}
	fail := func(e error) (*ControlledRuntime, skills.SkillPlan, error) {
		_ = store.Close()
		return nil, skills.SkillPlan{}, e
	}
	policy := skills.SkillResolutionPolicy{Budget: skills.SkillBudget{CatalogTokens: 1000, BodyTokens: 4000, ResourceTokens: 2000, TotalTokens: 6000, MaxActive: 3, MaxLoadCallsPerTurn: 4, MaxResourceReadsPerTurn: 6, MaxLoadedBytesFallback: 16384}, Transport: "controlled", RequireControlled: true}
	set, err := store.ResolveSkills(ctx, scopes, policy)
	if err != nil {
		return fail(err)
	}
	runtime, err := store.NewRuntime(set, budget)
	if err != nil {
		return fail(err)
	}
	plan, err := runtime.Build(ctx)
	if err != nil {
		return fail(err)
	}
	return &ControlledRuntime{Runtime: runtime, store: store, Config: set.Config, Diagnostics: set.Diagnostics, Bindings: set.Bindings}, plan, nil
}

// ControlledPayload is the literal bounded payload. It has no executable
// interpretation and contains no imported permission metadata.
func ControlledPayload(plan skills.SkillPlan) string {
	if len(plan.Blocks) == 0 {
		return ""
	}
	var out strings.Builder
	out.WriteString("<praimate-skills context_epoch=\"")
	out.WriteString(fmtInt(plan.Receipt.ContextEpoch))
	out.WriteString("\">\n")
	for _, block := range plan.Blocks {
		catalogue := block.Kind == skills.BlockCatalogue
		if catalogue {
			out.WriteString("<skill-catalogue ref=\"")
			out.WriteString(block.Ref)
			out.WriteString("\" digest=\"")
			out.WriteString(block.Digest)
			out.WriteString("\">\n")
		} else {
			out.WriteString("<skill ref=\"")
			out.WriteString(block.Ref)
			out.WriteString("\" digest=\"")
			out.WriteString(block.Digest)
			out.WriteString("\" kind=\"")
			out.WriteString(string(block.Kind))
			if block.Kind == skills.BlockResource {
				out.WriteString("\" path=\"")
				out.WriteString(html.EscapeString(block.Path))
				out.WriteString("\" start_line=\"")
				out.WriteString(fmtInt(block.StartLine))
				out.WriteString("\" end_line=\"")
				out.WriteString(fmtInt(block.EndLine))
			}
			out.WriteString("\">\n")
		}
		out.WriteString(block.Text)
		if catalogue {
			out.WriteString("\n</skill-catalogue>\n")
		} else {
			out.WriteString("\n</skill>\n")
		}
	}
	out.WriteString("</praimate-skills>")
	return out.String()
}
func fmtInt(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func runtimeState(status string, receipt skills.SkillReceipt, code string) *SkillRuntimeState {
	state := &SkillRuntimeState{Status: status, Coverage: receipt.Coverage, Measurement: receipt.Measurement, ContextEpoch: receipt.ContextEpoch, SkillsTokens: receipt.SkillsTokens, TotalTokens: receipt.TotalTokens, ErrorCode: code}
	for _, block := range receipt.Delivered {
		state.Delivered = append(state.Delivered, SkillRuntimeDelivery{Ref: block.Ref, Digest: block.Digest, Kind: block.Kind, Tokens: block.Tokens, Bytes: block.Bytes})
	}
	return state
}

func chatRuntimeBudget(settings ChatSettings, nonSkill string) skills.ContextBudget {
	window := 16384
	output := 2048
	if settings.Local != nil {
		if settings.Local.ContextTokens > 0 {
			window = settings.Local.ContextTokens
		}
		if settings.Local.OutputTokens > 0 {
			output = settings.Local.OutputTokens
		}
	}
	return skills.ContextBudget{InputLimit: window, ModelWindow: window, ReservedOutput: output, SafetyMargin: 512, NonSkillInput: (len(nonSkill) + 2) / 3, Coverage: skills.CoverageControlledPayload, Measurement: skills.MeasurementEstimate}
}

// BuildChatSkillPayload repeats final resolution immediately before launching.
// A persisted chat stores a frozen session scope, so edited app/agent defaults
// cannot contaminate it. This returns only sanitized receipt state for storage.
func (c *Core) BuildChatSkillPayload(ctx context.Context, settings ChatSettings, nonSkill string) (string, *SkillRuntimeState, error) {
	if settings.SkillsV2 == nil {
		return "", nil, nil
	}
	if err := c.requireSkillsV2Rollout(ctx); err != nil {
		return "", nil, err
	}
	scope, err := skills.SelectionScope(settings.SkillsV2, settings.SkillsLock)
	if err != nil {
		return "", nil, err
	}
	// These static transports cannot service model-requested skill tools.
	// Do not send a catalogue that advertises nonexistent load/read operations.
	var unavailable []skills.SkillDiagnostic
	config := *scope.Config
	config.Bindings = make([]skills.SkillBinding, 0, len(scope.Config.Bindings))
	for _, binding := range scope.Config.Bindings {
		if binding.Activation == "auto" || binding.Activation == "manual" {
			diagnostic := skills.SkillDiagnostic{Code: "incompatible_transport", Ref: binding.Ref, Remedy: "Use pinned on this surface, or select an explicitly managed agent for dynamic skill.load/read"}
			if !binding.Optional {
				return "", nil, &skills.SkillResolutionError{Diagnostics: []skills.SkillDiagnostic{diagnostic}}
			}
			unavailable = append(unavailable, diagnostic)
			continue
		}
		config.Bindings = append(config.Bindings, binding)
	}
	scope.Config = &config
	runtime, plan, err := ControlledSkillRuntime(ctx, skills.SkillScopes{Session: scope}, chatRuntimeBudget(settings, nonSkill))
	if err != nil {
		return "", nil, err
	}
	defer runtime.Close()
	payload := ControlledPayload(plan)
	state := runtimeState("prepared", plan.Receipt, "")
	state.Diagnostics = append(runtime.Diagnostics, unavailable...)
	if err := validateSkillPayload(payload, nonSkill, runtime.Config.Budget, chatRuntimeBudget(settings, nonSkill)); err != nil {
		return "", nil, err
	}
	return payload, state, nil
}

func (c *Core) BuildWorkflowSkillPayload(ctx context.Context, agent *Agent, workflow *Workflow, settings ChatSettings, nonSkill string) (string, *SkillRuntimeState, error) {
	settings, err := c.workflowSkillSettings(ctx, agent, workflow, settings)
	if err != nil {
		return "", nil, err
	}
	return c.BuildChatSkillPayload(ctx, settings, nonSkill)
}

func (c *Core) workflowSkillSettings(ctx context.Context, agent *Agent, workflow *Workflow, settings ChatSettings) (ChatSettings, error) {
	if settings.SkillsV2 != nil {
		return settings, validateChatSkills(settings)
	}
	appConfig, appLock, err := c.SkillDefaultsV2(ctx)
	if err != nil {
		return settings, err
	}
	app, err := skills.SelectionScope(appConfig, appLock)
	if err != nil {
		return settings, err
	}
	scopes := skills.SkillScopes{Application: app, Session: chatSkillScope(settings)}
	if agent != nil {
		scopes.Agent, err = skills.SelectionScope(agent.Skills, agent.SkillsLock)
		if err != nil {
			return settings, err
		}
	}
	if workflow != nil {
		scopes.Workflow, err = skills.SelectionScope(workflow.Skills, workflow.SkillsLock)
		if err != nil {
			return settings, err
		}
	}
	frozen, _, err := skills.FreezeSkillPreferences(scopes)
	if err != nil {
		return settings, err
	}
	if frozen.Config == nil {
		return settings, nil
	}
	lock, err := skills.DecodeSkillVersionLock(frozen.Lock)
	if err != nil {
		return settings, err
	}
	settings.SkillsV2 = frozen.Config
	settings.SkillsLock = &lock
	return settings, validateChatSkills(settings)
}
