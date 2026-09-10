package core

import (
	"git.jtsec.local/lab/PrAImate/internal/agentic"
	"git.jtsec.local/lab/PrAImate/internal/skills"
)

// Agent is the canonical in-memory representation of a PrAImate agent.
// It is what the TUI/GUI receive from Core and what `praimate agent
// import/export` round-trips against the YAML wire format defined in
// agent_yaml.go.
//
// Field order mirrors the YAML order so a manual diff of the two side
// by side is readable. Empty-but-non-nil slices intentionally encode as
// `[]` in YAML so users see they can be edited.
type Agent struct {
	Schema          string                   `json:"schema,omitempty"`
	Skills          *skills.SkillConfig      `json:"skills,omitempty"`
	SkillsLock      *skills.SkillVersionLock `json:"skills_lock,omitempty"`
	ID              string                   `json:"id"`
	Name            string                   `json:"name"`
	Description     string                   `json:"description"`
	Icon            string                   `json:"icon,omitempty"`
	Instructions    string                   `json:"instructions"`
	Supports        []string                 `json:"supports"`
	Tools           []string                 `json:"tools"`
	MCPServers      []string                 `json:"mcp_servers"`
	Workflows       []Workflow               `json:"workflows"`
	DefaultWorkflow string                   `json:"default_workflow,omitempty"`

	// Surfaces gates where the agent can be launched from in the GUI:
	// "chat" (interpreter chat), "terminal" (live CLI terminal),
	// "editor" (live document studio). Empty = allowed everywhere —
	// the backward-compatible default.
	Surfaces []string `json:"surfaces"`

	// Knowledge is the agent's knowledge-base mode: "" (none), "raw"
	// (a folder of documents under the agent's managed dir that the
	// agent reads with its own file tools), "rag" (the same folder
	// plus a graphify knowledge-graph index built into
	// knowledge/.graphify, queried with `graphify query`). The folder
	// path is identical for both modes, so the format can change after
	// the agent exists without breaking anything.
	Knowledge string `json:"knowledge,omitempty"`

	// Requirements describes an optional, user-triggered environment setup
	// script carried by a .praimate-agent pack. It is never run on import.
	Requirements *AgentRequirements `json:"requirements,omitempty"`

	// SourcePath is the YAML file the agent was last imported from, if
	// any. Empty for agents created in-place via the TUI/GUI.
	SourcePath string `json:"source_path,omitempty"`
}

// AgentRequirements is metadata for one platform-specific setup script.
// Script is a filename inside the agent's managed requirements directory.
type AgentRequirements struct {
	OS           string `json:"os" yaml:"os"`
	Script       string `json:"script" yaml:"script"`
	Instructions string `json:"instructions,omitempty" yaml:"instructions,omitempty"`
}

// Workflow is a named, scripted task template inside an agent. Each
// workflow declares its inputs (asked at launch time) and a linear
// sequence of steps to execute through the underlying CLI agent.
//
// Conditional / loop step kinds are explicitly out of scope for 1.0
// (plan §4); only `user_message` and `wait_for_assistant` are valid.
type Workflow struct {
	FinishEvidence []agentic.EvidenceRequirement `json:"finish_evidence,omitempty"`
	Skills         *skills.SkillConfig           `json:"skills,omitempty"`
	SkillsLock     *skills.SkillVersionLock      `json:"skills_lock,omitempty"`
	Name           string                        `json:"name"`
	Description    string                        `json:"description,omitempty"`
	Inputs         []WorkflowInput               `json:"inputs"`
	Steps          []WorkflowStep                `json:"steps"`
}

// WorkflowInput is one prompt the user fills in before launch. Type is
// advisory today (the TUI always uses a text input); the GUI may grow
// richer field renderers.
type WorkflowInput struct {
	Name        string `json:"name"`
	Prompt      string `json:"prompt"`
	Type        string `json:"type"` // "string" | "text" | "int" | "bool"
	Required    bool   `json:"required"`
	Placeholder string `json:"placeholder,omitempty"`
	Default     string `json:"default,omitempty"`
}

// WorkflowStep is one node in the linear script. Exactly one of the
// kind-specific fields is meaningful per Kind value; the rest are
// ignored by the executor.
type WorkflowStep struct {
	Kind StepKind `json:"kind"`

	// Template is the body of a user_message step; rendered with
	// text/template against the inputs map plus a small built-in
	// context (`.agent`, `.workflow`).
	Template string `json:"template,omitempty"`

	// UntilTool optionally narrows a wait_for_assistant step to wait
	// for a specific tool call (e.g. "complete", "clarify"). Empty
	// means "wait for any assistant turn that ends a tool round."
	UntilTool string `json:"until_tool,omitempty"`
}

// StepKind enumerates the step types supported in 1.0. Adding a new
// kind requires updating Validate() and the workflow executor.
type StepKind string

const (
	StepUserMessage      StepKind = "user_message"
	StepWaitForAssistant StepKind = "wait_for_assistant"
)

// AllStepKinds lists every kind the executor understands today. Used
// by Validate() so unknown kinds in user YAML fail fast at import.
var AllStepKinds = []StepKind{StepUserMessage, StepWaitForAssistant}

// AllSurfaces lists the GUI launch surfaces an agent can be gated to.
var AllSurfaces = []string{"chat", "terminal", "editor"}

// AllowsSurface reports whether the agent may be launched from the
// given surface. An empty Surfaces list allows everything.
func (a *Agent) AllowsSurface(surface string) bool {
	if len(a.Surfaces) == 0 {
		return true
	}
	for _, s := range a.Surfaces {
		if s == surface {
			return true
		}
	}
	return false
}

// FindWorkflow returns the named workflow on the agent, or nil if none
// matches. Names are compared case-sensitively to match YAML import
// semantics.
func (a *Agent) FindWorkflow(name string) *Workflow {
	for i := range a.Workflows {
		if a.Workflows[i].Name == name {
			return &a.Workflows[i]
		}
	}
	return nil
}

// ResolveDefaultWorkflow returns the workflow that should run when the
// user launches an agent without picking one. Priority: DefaultWorkflow
// if set and present; else the only workflow if there is exactly one;
// else nil.
func (a *Agent) ResolveDefaultWorkflow() *Workflow {
	if a.DefaultWorkflow != "" {
		if w := a.FindWorkflow(a.DefaultWorkflow); w != nil {
			return w
		}
	}
	if len(a.Workflows) == 1 {
		return &a.Workflows[0]
	}
	return nil
}
