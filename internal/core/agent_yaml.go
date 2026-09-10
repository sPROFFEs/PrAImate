package core

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"

	"git.jtsec.local/lab/PrAImate/internal/agentic"
	"git.jtsec.local/lab/PrAImate/internal/skills"
	"gopkg.in/yaml.v3"
)

// AgentSchema is the schema sentinel every YAML agent file must carry.
// Bumped only on breaking format changes; minor additions are made
// backward-compatibly with omitempty.
const AgentSchema = "praimate.agent/v1"
const AgentSchemaV2 = "praimate.agent/v2"

// agentYAML is the on-disk shape; it mirrors Agent but with snake_case
// keys and a leading schema field so future format versions can be
// detected before unmarshalling.
type agentYAML struct {
	Skills          *skills.SkillConfig      `yaml:"skills,omitempty"`
	SkillsLock      *skills.SkillVersionLock `yaml:"skills_lock,omitempty"`
	Schema          string                   `yaml:"schema"`
	ID              string                   `yaml:"id"`
	Name            string                   `yaml:"name"`
	Description     string                   `yaml:"description,omitempty"`
	Icon            string                   `yaml:"icon,omitempty"`
	Instructions    string                   `yaml:"instructions"`
	Supports        []string                 `yaml:"supports"`
	Tools           []string                 `yaml:"tools,omitempty"`
	MCPServers      []string                 `yaml:"mcp_servers,omitempty"`
	Workflows       []workflowYAML           `yaml:"workflows,omitempty"`
	DefaultWorkflow string                   `yaml:"default_workflow,omitempty"`
	Surfaces        []string                 `yaml:"surfaces,omitempty"`
	Knowledge       string                   `yaml:"knowledge,omitempty"`
	Requirements    *AgentRequirements       `yaml:"requirements,omitempty"`
}

type workflowYAML struct {
	FinishEvidence []agentic.EvidenceRequirement `yaml:"finish_evidence,omitempty"`
	Skills         *skills.SkillConfig           `yaml:"skills,omitempty"`
	SkillsLock     *skills.SkillVersionLock      `yaml:"skills_lock,omitempty"`
	Name           string                        `yaml:"name"`
	Description    string                        `yaml:"description,omitempty"`
	Inputs         []workflowInputYAML           `yaml:"inputs,omitempty"`
	Steps          []workflowStepYAML            `yaml:"steps"`
}

type workflowInputYAML struct {
	Name        string `yaml:"name"`
	Prompt      string `yaml:"prompt"`
	Type        string `yaml:"type,omitempty"`
	Required    bool   `yaml:"required,omitempty"`
	Placeholder string `yaml:"placeholder,omitempty"`
	Default     string `yaml:"default,omitempty"`
}

type workflowStepYAML struct {
	Kind      string `yaml:"kind"`
	Template  string `yaml:"template,omitempty"`
	UntilTool string `yaml:"until_tool,omitempty"`
}

// ParseAgentYAML decodes a YAML agent definition from r and validates
// it. Returns a fully-populated Agent or a user-facing error explaining
// the first invalid field.
func ParseAgentYAML(r io.Reader) (*Agent, error) {
	return ParseAgentYAMLForSchema(r, AgentSchemaV2)
}

// Explicit reader capability prevents downgrading a v2 definition to v1.
func ParseAgentYAMLForSchema(r io.Reader, maxSchema string) (*Agent, error) {
	body, err := io.ReadAll(io.LimitReader(r, (4<<20)+1))
	if err != nil {
		return nil, fmt.Errorf("read agent yaml: %w", err)
	}
	var raw agentYAML
	if len(body) > 4<<20 {
		return nil, fmt.Errorf("agent YAML exceeds 4 MiB limit")
	}
	var sentinel struct {
		Schema string `yaml:"schema"`
	}
	if err := yaml.Unmarshal(body, &sentinel); err != nil {
		return nil, err
	}
	if maxSchema != AgentSchema && maxSchema != AgentSchemaV2 {
		return nil, fmt.Errorf("unsupported reader schema")
	}
	if maxSchema == AgentSchema && sentinel.Schema != AgentSchema {
		return nil, fmt.Errorf("unsupported schema for v1 reader")
	}
	d := yaml.NewDecoder(bytes.NewReader(body))
	d.KnownFields(sentinel.Schema == AgentSchemaV2)
	if err := d.Decode(&raw); err != nil {
		return nil, fmt.Errorf("parse agent yaml: %w", err)
	}
	var extra yaml.Node
	if err := d.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("agent requires exactly one YAML document")
	}
	return raw.toAgent()
}

// LoadAgentFile is a thin convenience around ParseAgentYAML that also
// records the source path on the returned Agent.
func LoadAgentFile(path string) (*Agent, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	a, err := ParseAgentYAML(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	a.SourcePath = path
	return a, nil
}

// MarshalAgentYAML produces the canonical YAML wire format for a, with
// the schema sentinel prepended. Used by `praimate agent export` and
// the GUI's "share agent" affordance.
func MarshalAgentYAML(a *Agent) ([]byte, error) {
	if err := a.Validate(); err != nil {
		return nil, err
	}
	out := agentYAML{
		Schema:          AgentSchema,
		ID:              a.ID,
		Name:            a.Name,
		Description:     a.Description,
		Icon:            a.Icon,
		Instructions:    a.Instructions,
		Supports:        a.Supports,
		Tools:           a.Tools,
		MCPServers:      a.MCPServers,
		DefaultWorkflow: a.DefaultWorkflow,
		Surfaces:        a.Surfaces,
		Knowledge:       a.Knowledge,
		Requirements:    a.Requirements,
	}
	if a.Schema == AgentSchemaV2 {
		out.Schema = AgentSchemaV2
	}
	out.Skills = a.Skills
	out.SkillsLock = a.SkillsLock
	for _, w := range a.Workflows {
		wy := workflowYAML{Name: w.Name, Description: w.Description}
		wy.FinishEvidence = w.FinishEvidence
		wy.Skills = w.Skills
		wy.SkillsLock = w.SkillsLock
		for _, in := range w.Inputs {
			wy.Inputs = append(wy.Inputs, workflowInputYAML{
				Name: in.Name, Prompt: in.Prompt, Type: in.Type,
				Required: in.Required, Placeholder: in.Placeholder, Default: in.Default,
			})
		}
		for _, st := range w.Steps {
			wy.Steps = append(wy.Steps, workflowStepYAML{
				Kind: string(st.Kind), Template: st.Template, UntilTool: st.UntilTool,
			})
		}
		out.Workflows = append(out.Workflows, wy)
	}
	return yaml.Marshal(out)
}

func (raw *agentYAML) toAgent() (*Agent, error) {
	if raw.Schema == "" {
		return nil, fmt.Errorf("missing schema field; expected %q", AgentSchema)
	}
	if raw.Schema != AgentSchema && raw.Schema != AgentSchemaV2 {
		return nil, fmt.Errorf("unsupported schema %q; this binary speaks %q", raw.Schema, AgentSchema)
	}
	a := &Agent{
		ID:              raw.ID,
		Name:            raw.Name,
		Description:     raw.Description,
		Icon:            raw.Icon,
		Instructions:    raw.Instructions,
		Supports:        raw.Supports,
		Tools:           raw.Tools,
		DefaultWorkflow: raw.DefaultWorkflow,
		Surfaces:        raw.Surfaces,
		Knowledge:       raw.Knowledge,
		Requirements:    raw.Requirements,
	}
	if raw.Schema == AgentSchemaV2 {
		a.Schema = raw.Schema
	}
	a.Skills = raw.Skills
	a.SkillsLock = raw.SkillsLock

	// Standardize MCP Server IDs (snake_case, kebab-case, mixed-case -> kebab-case slug)
	// so that user-written agent YAML resolves reliably to the correct backend IDs.
	for _, id := range raw.MCPServers {
		slug := MCPSlug(id)
		if slug != "" {
			a.MCPServers = append(a.MCPServers, slug)
		}
	}
	for _, wy := range raw.Workflows {
		w := Workflow{Name: wy.Name, Description: wy.Description}
		w.Skills = wy.Skills
		w.SkillsLock = wy.SkillsLock
		w.FinishEvidence = wy.FinishEvidence
		for _, in := range wy.Inputs {
			w.Inputs = append(w.Inputs, WorkflowInput{
				Name: in.Name, Prompt: in.Prompt, Type: in.Type,
				Required: in.Required, Placeholder: in.Placeholder, Default: in.Default,
			})
		}
		for _, st := range wy.Steps {
			w.Steps = append(w.Steps, WorkflowStep{
				Kind: StepKind(st.Kind), Template: st.Template, UntilTool: st.UntilTool,
			})
		}
		a.Workflows = append(a.Workflows, w)
	}
	if err := a.Validate(); err != nil {
		return nil, err
	}
	return a, nil
}

// Validate checks that every required field is set and every enum value
// is one this binary understands. Returns the FIRST problem found so
// users iterate one fix at a time rather than getting a paragraph.
func (a *Agent) Validate() error {
	if a.Schema == AgentSchemaV2 && (len(a.ID) > 128 || sanitizeAgentID(a.ID) != a.ID || a.ID == "") {
		return fmt.Errorf("v2 agent id must be a portable lowercase slug")
	}
	if a.Schema != "" && a.Schema != AgentSchema && a.Schema != AgentSchemaV2 {
		return fmt.Errorf("unsupported agent schema")
	}
	if a.Schema != AgentSchemaV2 && (a.Skills != nil || a.SkillsLock != nil) {
		return fmt.Errorf("skills require agent/v2; v1 cannot silently discard them")
	}
	if _, err := skills.SelectionScope(a.Skills, a.SkillsLock); err != nil {
		return err
	}
	if a.ID == "" {
		return fmt.Errorf("agent: id is required")
	}
	if a.Name == "" {
		return fmt.Errorf("agent %q: name is required", a.ID)
	}
	if a.Instructions == "" {
		return fmt.Errorf("agent %q: instructions are required", a.ID)
	}
	if len(a.Supports) == 0 {
		return fmt.Errorf("agent %q: supports must list at least one CLI agent", a.ID)
	}
	for _, cli := range a.Supports {
		if !isKnownCLI(cli) {
			return fmt.Errorf("agent %q: unknown CLI %q in supports", a.ID, cli)
		}
	}
	seenWorkflow := map[string]bool{}
	for i := range a.Workflows {
		w := &a.Workflows[i]
		if err := agentic.ValidateEvidenceRequirements(w.FinishEvidence); err != nil {
			return fmt.Errorf("workflow %s: %w", w.Name, err)
		}
		if a.Schema != AgentSchemaV2 && (w.Skills != nil || w.SkillsLock != nil || len(w.FinishEvidence) > 0) {
			return fmt.Errorf("workflow skills require agent/v2")
		}
		if _, err := skills.SelectionScope(w.Skills, w.SkillsLock); err != nil {
			return err
		}
		if w.Name == "" {
			return fmt.Errorf("agent %q: workflow #%d missing name", a.ID, i+1)
		}
		if seenWorkflow[w.Name] {
			return fmt.Errorf("agent %q: duplicate workflow name %q", a.ID, w.Name)
		}
		seenWorkflow[w.Name] = true
		if err := w.validate(a.ID); err != nil {
			return err
		}
	}
	if a.DefaultWorkflow != "" && !seenWorkflow[a.DefaultWorkflow] {
		return fmt.Errorf("agent %q: default_workflow %q does not match any workflow",
			a.ID, a.DefaultWorkflow)
	}
	for _, s := range a.Surfaces {
		ok := false
		for _, known := range AllSurfaces {
			if s == known {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("agent %q: unknown surface %q (want chat, terminal or editor)", a.ID, s)
		}
	}
	switch a.Knowledge {
	case "", "raw", "rag":
	default:
		return fmt.Errorf("agent %q: unknown knowledge mode %q (want raw or rag)", a.ID, a.Knowledge)
	}
	if r := a.Requirements; r != nil {
		switch r.OS {
		case "linux", "windows":
		default:
			return fmt.Errorf("agent %q: requirements os must be linux or windows", a.ID)
		}
		if r.Script == "" || strings.ContainsAny(r.Script, `/\\`) {
			return fmt.Errorf("agent %q: requirements script must be a filename", a.ID)
		}
	}
	return nil
}

func (w *Workflow) validate(agentID string) error {
	if len(w.Steps) == 0 {
		return fmt.Errorf("agent %q workflow %q: steps must not be empty", agentID, w.Name)
	}
	seenInput := map[string]bool{}
	for _, in := range w.Inputs {
		if in.Name == "" {
			return fmt.Errorf("agent %q workflow %q: input is missing name", agentID, w.Name)
		}
		if seenInput[in.Name] {
			return fmt.Errorf("agent %q workflow %q: duplicate input name %q", agentID, w.Name, in.Name)
		}
		seenInput[in.Name] = true
		if in.Type != "" && !isKnownInputType(in.Type) {
			return fmt.Errorf("agent %q workflow %q input %q: unknown type %q",
				agentID, w.Name, in.Name, in.Type)
		}
	}
	for i, st := range w.Steps {
		if !isKnownStepKind(st.Kind) {
			return fmt.Errorf("agent %q workflow %q step #%d: unknown kind %q (allowed: %s)",
				agentID, w.Name, i+1, st.Kind, strings.Join(stepKindStrings(), ", "))
		}
		if st.Kind == StepUserMessage && strings.TrimSpace(st.Template) == "" {
			return fmt.Errorf("agent %q workflow %q step #%d: user_message requires a template",
				agentID, w.Name, i+1)
		}
	}
	return nil
}

func isKnownCLI(name string) bool {
	switch name {
	case "claude", "codex", "opencode", "openclaude", "praimate-code":
		return true
	}
	return false
}

func isKnownInputType(t string) bool {
	switch t {
	case "string", "text", "int", "bool":
		return true
	}
	return false
}

func isKnownStepKind(k StepKind) bool {
	for _, ok := range AllStepKinds {
		if k == ok {
			return true
		}
	}
	return false
}

func stepKindStrings() []string {
	out := make([]string, len(AllStepKinds))
	for i, k := range AllStepKinds {
		out[i] = string(k)
	}
	return out
}
