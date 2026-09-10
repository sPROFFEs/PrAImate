package skills

// Controlled skill context construction. This layer is deliberately independent
// from a CLI adapter: an adapter receives a payload only after Build/Load/Read
// returns a receipt. Native CLIs may report controlled_payload evidence, never
// full_request evidence, because their private prompt/history is unobservable.

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

type ContextCoverage string

const (
	CoverageControlledPayload ContextCoverage = "controlled_payload"
	CoverageFullRequest       ContextCoverage = "full_request"
	CoverageUnknown           ContextCoverage = "unknown"
)

type ContextMeasurement string

const (
	MeasurementTokenizer ContextMeasurement = "tokenizer"
	MeasurementEstimate  ContextMeasurement = "estimate"
	MeasurementBytes     ContextMeasurement = "bytes"
)

type SkillBlockKind string

const (
	BlockCatalogue SkillBlockKind = "catalogue"
	BlockBody      SkillBlockKind = "body"
	BlockResource  SkillBlockKind = "resource"
)

type ContextBudget struct {
	InputLimit, ModelWindow, ReservedOutput, SafetyMargin, NonSkillInput int
	Coverage                                                             ContextCoverage
	Measurement                                                          ContextMeasurement
}

func (b ContextBudget) availableInput() (int, error) {
	if b.InputLimit < 1 || b.ModelWindow < 1 || b.ReservedOutput < 0 || b.SafetyMargin < 0 || b.NonSkillInput < 0 {
		return 0, errors.New("invalid context budget")
	}
	a := min(b.InputLimit, b.ModelWindow-b.ReservedOutput-b.SafetyMargin) - b.NonSkillInput
	if a < 0 {
		return 0, errors.New("context_budget_exceeded: non-skill input exceeds available context")
	}
	return a, nil
}

type ContextBlock struct {
	Ref        string         `json:"ref"`
	Digest     string         `json:"digest"`
	Kind       SkillBlockKind `json:"kind"`
	Text       string         `json:"text,omitempty"`
	Path       string         `json:"path,omitempty"`
	StartLine  int            `json:"start_line,omitempty"`
	EndLine    int            `json:"end_line,omitempty"`
	Tokens     int            `json:"tokens"`
	Bytes      int            `json:"bytes"`
	Epoch      int            `json:"epoch"`
	Activation string         `json:"activation,omitempty"`
}
type SkillReceipt struct {
	Coverage     ContextCoverage    `json:"coverage"`
	Measurement  ContextMeasurement `json:"measurement"`
	ContextEpoch int                `json:"context_epoch"`
	Delivered    []ContextBlock     `json:"delivered"`
	Existing     bool               `json:"existing,omitempty"`
	SkillsTokens int                `json:"skills_tokens"`
	TotalTokens  int                `json:"total_tokens"`
}
type SkillPlan struct {
	Blocks  []ContextBlock `json:"blocks"`
	Receipt SkillReceipt   `json:"receipt"`
}
type ResourceChunk struct {
	Ref, Digest, Path, Text, NextCursor string
	StartLine, EndLine                  int
	Receipt                             SkillReceipt
}
type VersionReader func(context.Context, string, string) (SkillVersion, []PackageFile, error)

// Runtime owns one observed payload. It is not shared across turns/runs; a new
// runtime has a new epoch and residency begins empty. All mutation is serialized
// so simultaneous loads cannot pass a budget check independently.
type Runtime struct {
	mu                                           sync.Mutex
	set                                          ResolvedSkillSet
	read                                         VersionReader
	budget                                       ContextBudget
	epoch, loads, reads, skillTokens, skillBytes int
	blocks                                       map[string]ContextBlock
	files                                        map[string][]PackageFile
}

func NewRuntime(set ResolvedSkillSet, budget ContextBudget, reader VersionReader) (*Runtime, error) {
	if reader == nil {
		return nil, errors.New("skill runtime requires a version reader")
	}
	if set.Config == nil {
		return nil, errors.New("skill runtime requires a resolved v2 selection")
	}
	if set.Config.Enforcement != "controlled" {
		return nil, errors.New("incompatible_transport: runtime requires controlled enforcement")
	}
	if _, err := budget.availableInput(); err != nil {
		return nil, err
	}
	if budget.Coverage != CoverageControlledPayload && budget.Coverage != CoverageFullRequest {
		return nil, errors.New("runtime requires explicit observable coverage")
	}
	if budget.Measurement != MeasurementTokenizer && budget.Measurement != MeasurementEstimate && budget.Measurement != MeasurementBytes {
		return nil, errors.New("runtime requires explicit measurement")
	}
	if budget.Measurement == MeasurementTokenizer {
		return nil, errors.New("tokenizer_unavailable: no tokenizer is installed; request explicit estimate or byte measurement")
	}
	return &Runtime{set: set, read: reader, budget: budget, epoch: 1, blocks: map[string]ContextBlock{}, files: map[string][]PackageFile{}}, nil
}

func blockKey(kind SkillBlockKind, ref, digest, name string) string {
	return string(kind) + "\x00" + ref + "\x00" + digest + "\x00" + name
}
func contextBlockKey(b ContextBlock) string {
	key := blockKey(b.Kind, b.Ref, b.Digest, b.Path)
	if b.Kind == BlockResource {
		key += fmt.Sprintf("\x00%d:%d", b.StartLine, b.EndLine)
	}
	return key
}
func estimate(text string, measurement ContextMeasurement) int {
	if measurement == MeasurementBytes {
		return len(text)
	}
	// This is a heuristic estimate, not a tokenizer or a strict token bound.
	n := utf8.RuneCountInString(text)
	if n == 0 {
		return 0
	}
	return (n + 2) / 3
}
func (r *Runtime) limit(block ContextBlock) error {
	available, err := r.budget.availableInput()
	if err != nil {
		return err
	}
	if r.skillTokens+block.Tokens > r.set.Config.Budget.TotalTokens || r.skillTokens+block.Tokens > available {
		return errors.New("context_budget_exceeded: skill total")
	}
	switch block.Kind {
	case BlockCatalogue:
		if r.sumKind(BlockCatalogue)+block.Tokens > r.set.Config.Budget.CatalogTokens {
			return errors.New("context_budget_exceeded: catalogue")
		}
	case BlockBody:
		if r.sumKind(BlockBody)+block.Tokens > r.set.Config.Budget.BodyTokens {
			return errors.New("context_budget_exceeded: bodies")
		}
	case BlockResource:
		if r.sumKind(BlockResource)+block.Tokens > r.set.Config.Budget.ResourceTokens {
			return errors.New("context_budget_exceeded: resources")
		}
	}
	if r.skillBytes+block.Bytes > r.set.Config.Budget.MaxLoadedBytesFallback {
		return errors.New("context_budget_exceeded: skill bytes")
	}
	return nil
}
func (r *Runtime) sumKind(kind SkillBlockKind) int {
	n := 0
	for _, b := range r.blocks {
		if b.Kind == kind {
			n += b.Tokens
		}
	}
	return n
}
func (r *Runtime) receipt(blocks []ContextBlock, existing bool) SkillReceipt {
	return SkillReceipt{Coverage: r.budget.Coverage, Measurement: r.budget.Measurement, ContextEpoch: r.epoch, Delivered: append([]ContextBlock(nil), blocks...), Existing: existing, SkillsTokens: r.skillTokens, TotalTokens: r.skillTokens + r.budget.NonSkillInput}
}
func (r *Runtime) add(block ContextBlock) error {
	if err := r.limit(block); err != nil {
		return err
	}
	r.blocks[contextBlockKey(block)] = block
	r.skillTokens += block.Tokens
	r.skillBytes += block.Bytes
	return nil
}

func (r *Runtime) binding(ref, digest string) (ResolvedSkillBinding, error) {
	for _, b := range r.set.Bindings {
		if b.Version.Ref == ref && b.Version.Digest == digest {
			return b, nil
		}
	}
	return ResolvedSkillBinding{}, errors.New("skill_not_found: ref/digest is not eligible")
}
func (r *Runtime) readFiles(ctx context.Context, b ResolvedSkillBinding) ([]PackageFile, error) {
	key := b.Version.Ref + "\x00" + b.Version.Digest
	if f, ok := r.files[key]; ok {
		return f, nil
	}
	v, f, err := r.read(ctx, b.Version.Ref, b.Version.Digest)
	if err != nil {
		return nil, err
	}
	if v != b.Version {
		return nil, errors.New("integrity_mismatch: version changed while loading")
	}
	r.files[key] = f
	return f, nil
}

// Build emits bounded catalogue metadata and bodies for pinned bindings. Auto
// bindings are discoverable but are never silently loaded.
func (r *Runtime) Build(ctx context.Context) (plan SkillPlan, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rollback := r.rollbackPoint()
	defer func() {
		if err != nil {
			rollback()
		}
	}()
	var delivered []ContextBlock
	for _, b := range r.set.Bindings {
		if b.Binding.Activation != "auto" {
			continue
		}
		f, err := r.readFiles(ctx, b)
		if err != nil {
			return SkillPlan{}, err
		}
		var manifest PackageManifest
		for _, x := range f {
			if x.Path == "SKILL.md" {
				manifest, err = ParsePackageManifest(x.Content)
				break
			}
		}
		if err != nil {
			return SkillPlan{}, err
		}
		block := ContextBlock{Ref: b.Version.Ref, Digest: b.Version.Digest, Kind: BlockCatalogue, Text: manifest.Name + ": " + manifest.Description, Activation: "auto", Epoch: r.epoch}
		block.Bytes = len(block.Text)
		block.Tokens = estimate(block.Text, r.budget.Measurement)
		key := blockKey(block.Kind, block.Ref, block.Digest, "")
		if _, ok := r.blocks[key]; !ok {
			if err := r.add(block); err != nil {
				return SkillPlan{}, err
			}
			delivered = append(delivered, block)
		}
	}
	for _, b := range r.set.Bindings {
		if b.Binding.Activation == "pinned" {
			blocks, err := r.loadClosure(ctx, b, map[string]bool{}, map[string]bool{})
			if err != nil {
				return SkillPlan{}, err
			}
			delivered = append(delivered, blocks...)
		}
	}
	return SkillPlan{Blocks: append([]ContextBlock(nil), delivered...), Receipt: r.receipt(delivered, false)}, nil
}
func (r *Runtime) loadLocked(ctx context.Context, b ResolvedSkillBinding) (SkillPlan, error) {
	key := blockKey(BlockBody, b.Version.Ref, b.Version.Digest, "")
	if block, ok := r.blocks[key]; ok {
		return SkillPlan{Receipt: r.receipt([]ContextBlock{block}, true)}, nil
	}
	if r.loads >= r.set.Config.Budget.MaxLoadCallsPerTurn {
		return SkillPlan{}, errors.New("context_budget_exceeded: skill load calls")
	}
	if r.activeBodies() >= r.set.Config.Budget.MaxActive {
		return SkillPlan{}, errors.New("context_budget_exceeded: max active skills")
	}
	files, err := r.readFiles(ctx, b)
	if err != nil {
		return SkillPlan{}, err
	}
	var body string
	for _, f := range files {
		if f.Path == "SKILL.md" {
			m, parseErr := ParsePackageManifest(f.Content)
			if parseErr != nil {
				return SkillPlan{}, parseErr
			}
			body = m.Body
			break
		}
	}
	if body == "" {
		return SkillPlan{}, errors.New("integrity_mismatch: SKILL.md missing body")
	}
	block := ContextBlock{Ref: b.Version.Ref, Digest: b.Version.Digest, Kind: BlockBody, Text: body, Activation: b.Binding.Activation, Epoch: r.epoch, Bytes: len(body), Tokens: estimate(body, r.budget.Measurement)}
	if err := r.add(block); err != nil {
		return SkillPlan{}, err
	}
	r.loads++
	return SkillPlan{Blocks: []ContextBlock{block}, Receipt: r.receipt([]ContextBlock{block}, false)}, nil
}
func (r *Runtime) activeBodies() int {
	n := 0
	for _, b := range r.blocks {
		if b.Kind == BlockBody {
			n++
		}
	}
	return n
}

func (r *Runtime) rollbackPoint() func() {
	blocks := make(map[string]ContextBlock, len(r.blocks))
	for k, v := range r.blocks {
		blocks[k] = v
	}
	loads, reads, tokens, size := r.loads, r.reads, r.skillTokens, r.skillBytes
	return func() { r.blocks, r.loads, r.reads, r.skillTokens, r.skillBytes = blocks, loads, reads, tokens, size }
}

func runtimeRelations(files []PackageFile) (dependencies, conflicts []string, err error) {
	for _, file := range files {
		if file.Path != "SKILL.md" {
			continue
		}
		manifest, err := ParsePackageManifest(file.Content)
		if err != nil {
			return nil, nil, err
		}
		parse := func(key string) ([]string, error) {
			raw := strings.TrimSpace(manifest.Metadata[key])
			if raw == "" {
				return nil, nil
			}
			out := strings.Split(raw, ",")
			seen := map[string]bool{}
			for i := range out {
				out[i] = strings.TrimSpace(out[i])
				if !portableSkillRef.MatchString(out[i]) || seen[out[i]] {
					return nil, errors.New("invalid skill dependency metadata")
				}
				seen[out[i]] = true
			}
			return out, nil
		}
		dependencies, err = parse("praimate.dependencies")
		if err != nil {
			return nil, nil, err
		}
		conflicts, err = parse("praimate.conflicts")
		return dependencies, conflicts, err
	}
	return nil, nil, errors.New("integrity_mismatch: SKILL.md missing")
}
func (r *Runtime) boundByRef(ref string) (ResolvedSkillBinding, error) {
	for _, b := range r.set.Bindings {
		if b.Version.Ref == ref {
			return b, nil
		}
	}
	return ResolvedSkillBinding{}, errors.New("dependency_missing: dependency is not an eligible locked binding")
}
func (r *Runtime) loadClosure(ctx context.Context, b ResolvedSkillBinding, visiting, done map[string]bool) ([]ContextBlock, error) {
	if done[b.Version.Ref] {
		return nil, nil
	}
	if visiting[b.Version.Ref] {
		return nil, errors.New("dependency_cycle")
	}
	visiting[b.Version.Ref] = true
	defer delete(visiting, b.Version.Ref)
	files, err := r.readFiles(ctx, b)
	if err != nil {
		return nil, err
	}
	deps, conflicts, err := runtimeRelations(files)
	if err != nil {
		return nil, err
	}
	var out []ContextBlock
	for _, ref := range deps {
		dep, err := r.boundByRef(ref)
		if err != nil {
			return nil, err
		}
		if dep.Binding.Activation == "off" {
			return nil, errors.New("dependency_missing: dependency is off")
		}
		blocks, err := r.loadClosure(ctx, dep, visiting, done)
		if err != nil {
			return nil, err
		}
		out = append(out, blocks...)
	}
	// Evaluate after dependencies so a closure cannot load a conflicting pair.
	// Conflicts are symmetric even if only one package declares the relation.
	for _, loaded := range r.blocks {
		if loaded.Kind != BlockBody || loaded.Ref == b.Version.Ref {
			continue
		}
		for _, ref := range conflicts {
			if ref == loaded.Ref {
				return nil, errors.New("skill_conflict")
			}
		}
		other, err := r.binding(loaded.Ref, loaded.Digest)
		if err != nil {
			return nil, err
		}
		otherFiles, err := r.readFiles(ctx, other)
		if err != nil {
			return nil, err
		}
		_, otherConflicts, err := runtimeRelations(otherFiles)
		if err != nil {
			return nil, err
		}
		for _, ref := range otherConflicts {
			if ref == b.Version.Ref {
				return nil, errors.New("skill_conflict")
			}
		}
	}
	plan, err := r.loadLocked(ctx, b)
	if err != nil {
		return nil, err
	}
	done[b.Version.Ref] = true
	return append(out, plan.Blocks...), nil
}
func (r *Runtime) Load(ctx context.Context, ref, digest string) (SkillPlan, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, err := r.binding(ref, digest)
	if err != nil {
		return SkillPlan{}, err
	}
	if b.Binding.Activation == "off" {
		return SkillPlan{}, errors.New("skill_not_found: binding is off")
	}
	if resident, ok := r.blocks[blockKey(BlockBody, ref, digest, "")]; ok {
		return SkillPlan{Receipt: r.receipt([]ContextBlock{resident}, true)}, nil
	}
	rollback := r.rollbackPoint()
	blocks, err := r.loadClosure(ctx, b, map[string]bool{}, map[string]bool{})
	if err != nil {
		rollback()
		return SkillPlan{}, err
	}
	return SkillPlan{Blocks: blocks, Receipt: r.receipt(blocks, false)}, nil
}

func (r *Runtime) Read(ctx context.Context, ref, digest, relative string, start, maxLines int) (ResourceChunk, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if start < 1 || maxLines < 1 || maxLines > 1000 {
		return ResourceChunk{}, errors.New("invalid resource range")
	}
	if path.IsAbs(relative) || strings.Contains(relative, "\\") || relative == "." || strings.HasPrefix(relative, "../") || strings.Contains(relative, "/../") {
		return ResourceChunk{}, errors.New("resource_outside_bundle")
	}
	b, err := r.binding(ref, digest)
	if err != nil {
		return ResourceChunk{}, err
	}
	if _, ok := r.blocks[blockKey(BlockBody, ref, digest, "")]; !ok {
		return ResourceChunk{}, errors.New("skill_not_found: load the exact skill before reading resources")
	}
	files, err := r.readFiles(ctx, b)
	if err != nil {
		return ResourceChunk{}, err
	}
	var data []byte
	found := false
	for _, f := range files {
		if f.Path == relative {
			data = f.Content
			found = true
			break
		}
	}
	if !found {
		return ResourceChunk{}, errors.New("skill_not_found: resource is not in the locked bundle")
	}
	if !utf8.Valid(data) {
		return ResourceChunk{}, errors.New("resource_outside_bundle: binary assets are not text context")
	}
	lines := strings.Split(string(data), "\n")
	if start > len(lines)+1 {
		return ResourceChunk{}, errors.New("invalid resource range")
	}
	end := min(len(lines), start+maxLines-1)
	next := ""
	if end < len(lines) {
		next = fmt.Sprintf("%d", end+1)
	}
	text := strings.Join(lines[start-1:end], "\n")
	block := ContextBlock{Ref: ref, Digest: digest, Kind: BlockResource, Path: relative, StartLine: start, EndLine: end, Text: text, Epoch: r.epoch, Bytes: len(text), Tokens: estimate(text, r.budget.Measurement)}
	if resident, ok := r.blocks[contextBlockKey(block)]; ok {
		return ResourceChunk{Ref: ref, Digest: digest, Path: relative, StartLine: start, EndLine: end, NextCursor: next, Receipt: r.receipt([]ContextBlock{resident}, true)}, nil
	}
	if r.reads >= r.set.Config.Budget.MaxResourceReadsPerTurn {
		return ResourceChunk{}, errors.New("context_budget_exceeded: resource read calls")
	}
	if err := r.add(block); err != nil {
		return ResourceChunk{}, err
	}
	r.reads++
	return ResourceChunk{Ref: ref, Digest: digest, Path: relative, Text: text, StartLine: start, EndLine: end, NextCursor: next, Receipt: r.receipt([]ContextBlock{block}, false)}, nil
}

// Compact invalidates all residence. The next load emits a new body in a new
// epoch; previous native/private context is not represented as removed.
func (r *Runtime) Compact() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.epoch++
	r.loads = 0
	r.reads = 0
	r.skillTokens = 0
	r.skillBytes = 0
	r.blocks = map[string]ContextBlock{}
}
func (r *Runtime) Blocks() []ContextBlock {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ContextBlock, 0, len(r.blocks))
	for _, b := range r.blocks {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool {
		return contextBlockKey(out[i]) < contextBlockKey(out[j])
	})
	return out
}

// Payload starts a new observable request containing all retained blocks.
// Unlike native resume, the caller must actually send this complete payload.
// Recheck against the current non-skill input/model budget before returning it.
func (r *Runtime) Payload(budget ContextBudget) (SkillPlan, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	available, err := budget.availableInput()
	if err != nil {
		return SkillPlan{}, err
	}
	if budget.Coverage != r.budget.Coverage || budget.Measurement != r.budget.Measurement {
		return SkillPlan{}, errors.New("context_resync_required: measurement or coverage changed")
	}
	if r.skillTokens > available {
		return SkillPlan{}, errors.New("context_budget_exceeded: new request")
	}
	r.budget = budget
	r.epoch++
	r.loads, r.reads = 0, 0
	blocks := make([]ContextBlock, 0, len(r.blocks))
	for key, block := range r.blocks {
		block.Epoch = r.epoch
		r.blocks[key] = block
		blocks = append(blocks, block)
	}
	sort.Slice(blocks, func(i, j int) bool { return contextBlockKey(blocks[i]) < contextBlockKey(blocks[j]) })
	return SkillPlan{Blocks: blocks, Receipt: r.receipt(blocks, false)}, nil
}

// NewRuntime opens verified package bytes only through this host service. The
// returned reader rechecks the immutable generation on every first access.
func (s *HostSkillStore) NewRuntime(set ResolvedSkillSet, budget ContextBudget) (*Runtime, error) {
	return NewRuntime(set, budget, s.ReadVersion)
}
