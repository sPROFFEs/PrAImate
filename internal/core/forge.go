package core

// FORGE is a first consumer of v2 skills, not a privileged exception.  This
// file imports only the kit's own, local skills. External candidates remain
// separate reviewed inputs with pinned provenance.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"git.jtsec.local/lab/PrAImate/internal/skills"
)

const forgeIndexFile = "skills/own/index.json"

type forgeKitIndex struct {
	Status string          `json:"status"`
	Skills []forgeKitSkill `json:"skills"`
}
type forgeKitSkill struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Ref  string `json:"ref"`
}

// ForgeInstallResult is intentionally identity-only. It contains no skill
// body, private project path, model request, approval secret or execution log.
type ForgeInstallResult struct {
	Versions []skills.SkillVersion    `json:"versions"`
	Lock     *skills.SkillVersionLock `json:"lock"`
	Agent    *Agent                   `json:"agent,omitempty"`
}

type ForgeSkillPreview struct {
	Ref      string   `json:"ref"`
	Digest   string   `json:"digest"`
	Markdown string   `json:"markdown"`
	Files    []string `json:"files"`
}

type ForgeKitPreview struct {
	ReviewDigest string              `json:"review_digest"`
	AgentYAML    string              `json:"agent_yaml"`
	Skills       []ForgeSkillPreview `json:"skills"`
}

func readForgeFile(root, relative string) ([]byte, error) {
	if _, err := forgePath(root, relative); err != nil {
		return nil, err
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	f, err := dir.Open(relative)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4<<20 {
		return nil, errors.New("invalid or oversized FORGE file")
	}
	body, err := io.ReadAll(io.LimitReader(f, (4<<20)+1))
	if len(body) > 4<<20 {
		return nil, errors.New("FORGE file size limit exceeded")
	}
	return body, err
}

// InspectForgeKit never grants trust. The review digest binds the baseline and
// all immutable skill contents, including resources, to the later approval.
func InspectForgeKit(ctx context.Context, kitRoot string) (*ForgeKitPreview, error) {
	preview, _, err := inspectForgeKit(ctx, kitRoot)
	return preview, err
}

func inspectForgeKit(ctx context.Context, kitRoot string) (*ForgeKitPreview, []preparedForgeSkill, error) {
	body, err := readForgeFile(kitRoot, "forge/current/agent.yaml")
	if err != nil {
		return nil, nil, err
	}
	if _, err := ParseAgentYAML(bytes.NewReader(body)); err != nil {
		return nil, nil, err
	}
	prepared, err := prepareForgeSkills(ctx, kitRoot)
	if err != nil {
		return nil, nil, err
	}
	preview := &ForgeKitPreview{AgentYAML: string(body)}
	hash := sha256.New()
	hash.Write(body)
	for _, entry := range prepared {
		skill := ForgeSkillPreview{Ref: entry.item.Ref, Digest: entry.pack.Digest()}
		hash.Write([]byte("\x00" + skill.Ref + "\x00" + skill.Digest))
		for _, file := range entry.pack.Files() {
			skill.Files = append(skill.Files, file.Path)
			if file.Path == "SKILL.md" {
				skill.Markdown = string(file.Content)
			}
		}
		preview.Skills = append(preview.Skills, skill)
	}
	preview.ReviewDigest = "sha256:" + hex.EncodeToString(hash.Sum(nil))
	return preview, prepared, nil
}

func forgePath(root, relative string) (string, error) {
	if strings.TrimSpace(root) == "" || filepath.IsAbs(relative) {
		return "", errors.New("invalid FORGE kit path")
	}
	clean := filepath.Clean(relative)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("FORGE kit path escapes its root")
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("FORGE kit root must be a real directory")
	}
	path := filepath.Join(root, clean)
	if rel, err := filepath.Rel(root, path); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("FORGE kit path escapes its root")
	}
	current := root
	for _, component := range strings.Split(clean, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("FORGE kit cannot contain symlink paths")
		}
	}
	return path, nil
}

func readForgeIndex(root string) (forgeKitIndex, error) {
	var index forgeKitIndex
	body, err := readForgeFile(root, forgeIndexFile)
	if err != nil {
		return index, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&index); err != nil {
		return index, fmt.Errorf("invalid FORGE index: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return index, errors.New("FORGE index has multiple JSON values")
	} else if !errors.Is(err, io.EOF) {
		return index, fmt.Errorf("invalid FORGE index: %w", err)
	}
	if index.Status != "original_local_skills_for_testing" || len(index.Skills) == 0 || len(index.Skills) > 32 {
		return index, errors.New("invalid FORGE index status or skill count")
	}
	seen := map[string]bool{}
	for _, item := range index.Skills {
		if item.Name == "" || item.Ref != "local/"+item.Name || item.Path != item.Name+"/SKILL.md" || seen[item.Name] {
			return index, errors.New("invalid or duplicate FORGE skill identity")
		}
		seen[item.Name] = true
	}
	return index, nil
}

// InstallForgeOwnSkills imports the local, original kit skills after an
// explicit reviewed-trust decision. It neither fetches candidates nor executes
// scripts. The resulting lock is built from the installed immutable versions,
// never copied from a design example.
func InstallForgeOwnSkills(ctx context.Context, kitRoot string, reviewedTrust bool, expected ...string) (*ForgeInstallResult, error) {
	if !reviewedTrust {
		return nil, errors.New("forge_trust_review_required: review the exact local skill versions before authorizing them")
	}
	preview, preparedSkills, err := inspectForgeKit(ctx, kitRoot)
	if err != nil {
		return nil, err
	}
	if len(expected) != 1 || expected[0] != preview.ReviewDigest {
		return nil, errors.New("forge_review_changed: inspect and approve the exact kit digest before installing")
	}
	return installPreparedForgeSkills(ctx, preparedSkills)
}

type preparedForgeSkill struct {
	item forgeKitSkill
	pack skills.SelectedPackage
}

func prepareForgeSkills(ctx context.Context, kitRoot string) ([]preparedForgeSkill, error) {
	index, err := readForgeIndex(kitRoot)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(kitRoot)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	preparedSkills := make([]preparedForgeSkill, 0, len(index.Skills))
	for _, item := range index.Skills {
		relative := filepath.ToSlash(filepath.Dir(filepath.Join("skills/own", item.Path)))
		_, err := forgePath(kitRoot, relative)
		if err != nil {
			return nil, err
		}
		candidates, _, err := skills.InspectPackageSubdirectory(ctx, root, relative, skills.PackageLimits{})
		if err != nil {
			return nil, fmt.Errorf("inspect FORGE %s: %w", item.Name, err)
		}
		if len(candidates) != 1 || candidates[0].Subpath != "." || candidates[0].Manifest.Name != item.Name {
			return nil, fmt.Errorf("FORGE %s must contain exactly one matching root skill", item.Name)
		}
		selected, err := skills.SelectPackages(ctx, candidates, []skills.PackageSelection{{Candidate: 0, ExpectedDigest: candidates[0].Digest}}, skills.PackageLimits{})
		if err != nil {
			return nil, err
		}
		preparedSkills = append(preparedSkills, preparedForgeSkill{item, selected[0]})
	}
	sort.Slice(preparedSkills, func(i, j int) bool { return preparedSkills[i].item.Ref < preparedSkills[j].item.Ref })
	return preparedSkills, nil
}

func installPreparedForgeSkills(ctx context.Context, preparedSkills []preparedForgeSkill) (*ForgeInstallResult, error) {
	store, err := OpenSkillStore(skills.PackageLimits{})
	if err != nil {
		return nil, err
	}
	defer store.Close()
	view, err := store.View(ctx)
	if err != nil {
		return nil, err
	}
	versions := make([]skills.SkillVersion, 0, len(preparedSkills))
	if _, err := store.Update(ctx, view.Revision, func(tx *skills.SkillHostTransaction) error {
		for _, entry := range preparedSkills {
			version, err := tx.RegisterSource(ctx, "own:"+entry.item.Name, entry.item.Ref, entry.pack, skills.SourceProvenance{Kind: "own", Subpath: entry.item.Name})
			if err != nil {
				return err
			}
			if err := tx.Approve(ctx, version.Ref, version.Digest, true); err != nil {
				return err
			}
			versions = append(versions, version)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	lockBody, err := store.ExportLock(ctx, versions)
	if err != nil {
		return nil, err
	}
	lock, err := skills.DecodeSkillVersionLock(lockBody)
	if err != nil {
		return nil, err
	}
	return &ForgeInstallResult{Versions: versions, Lock: &lock}, nil
}

func forgeBudget() skills.SkillBudget {
	return defaultControlledSkillBudget()
}

func forgeWorkflowTemplate(w Workflow) string {
	var b strings.Builder
	b.WriteString("FORGE workflow: ")
	b.WriteString(w.Name)
	b.WriteString("\n")
	for _, input := range w.Inputs {
		b.WriteString(input.Name)
		b.WriteString(": {{ .")
		b.WriteString(input.Name)
		b.WriteString(" }}\n")
	}
	b.WriteString("Apply only the workflow-selected skill and the agent policy. Report observed evidence and pending work; do not infer authorization.")
	return b.String()
}

var forgeWorkflowRefs = map[string]string{
	"Analizar": "local/forge-context", "Desarrollar": "local/forge-tdd", "Depurar": "local/forge-debugging",
	"Revisar": "local/forge-review", "Auditar seguridad": "local/forge-security", "Preparar deploy": "local/forge-release", "Guardar contexto": "local/forge-handoff",
}

// InstallForgeAgent builds a v2 agent from the v1 baseline while keeping one
// selected skill per workflow. It does not transplant the target example's
// proposed schema, and avoids duplicating its skill checklists in templates.
func (c *Core) InstallForgeAgent(ctx context.Context, kitRoot string, reviewedTrust bool, expected ...string) (*ForgeInstallResult, error) {
	if c.store == nil {
		return nil, errors.New("FORGE agent installation requires an open store")
	}
	preview, prepared, err := inspectForgeKit(ctx, kitRoot)
	if err != nil {
		return nil, err
	}
	baseline, err := ParseAgentYAML(strings.NewReader(preview.AgentYAML))
	if err != nil {
		return nil, fmt.Errorf("read FORGE baseline: %w", err)
	}
	// Validate the baseline's mappings before approving anything in the skill
	// host. A malformed or newer kit must not leave a partial installation.
	for _, workflow := range baseline.Workflows {
		if _, ok := forgeWorkflowRefs[workflow.Name]; !ok {
			return nil, fmt.Errorf("FORGE baseline has no reviewed skill mapping for workflow %q", workflow.Name)
		}
	}
	if !reviewedTrust {
		return nil, errors.New("forge_trust_review_required")
	}
	if len(expected) != 1 || expected[0] != preview.ReviewDigest {
		return nil, errors.New("forge_review_changed: inspect and approve this exact kit before installing")
	}
	installed, err := installPreparedForgeSkills(ctx, prepared)
	if err != nil {
		return nil, err
	}
	byRef := map[string]skills.SkillVersionLockEntry{}
	for _, entry := range installed.Lock.Entries {
		byRef[entry.Ref] = entry
	}
	for i := range baseline.Workflows {
		w := &baseline.Workflows[i]
		ref := forgeWorkflowRefs[w.Name]
		entry, ok := byRef[ref]
		if !ok {
			return nil, fmt.Errorf("FORGE lock lacks %s", ref)
		}
		w.Skills = &skills.SkillConfig{Schema: skills.SkillConfigSchema, Configured: true, Lockfile: "skill-locks/" + strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(w.Name, " ", "-"), "á", "a")) + ".json", Enforcement: "controlled", Bindings: []skills.SkillBinding{{Ref: ref, Activation: "pinned"}}, Budget: forgeBudget()}
		w.SkillsLock = &skills.SkillVersionLock{Schema: installed.Lock.Schema, DigestAlgorithm: installed.Lock.DigestAlgorithm, Entries: []skills.SkillVersionLockEntry{entry}}
		w.Steps = []WorkflowStep{{Kind: StepUserMessage, Template: forgeWorkflowTemplate(*w)}}
	}
	baseline.Schema = AgentSchemaV2
	baseline.ID = "forge-dev-v2"
	baseline.Name = "FORGE - Skills controladas"
	// The common context and verification procedures are agent-wide so a plain
	// Chat, Studio or Terminal launch has useful FORGE behavior without asking
	// the user to configure skills again. Specialized procedures remain scoped
	// to their workflows, keeping the active set within the runtime budget.
	commonRefs := []string{"local/forge-context", "local/forge-simplicity", "local/forge-verification"}
	baseline.Skills = &skills.SkillConfig{Schema: skills.SkillConfigSchema, Configured: true, Lockfile: "skills.lock.json", Enforcement: "controlled", Bindings: []skills.SkillBinding{}, Budget: forgeBudget()}
	baseline.SkillsLock = &skills.SkillVersionLock{Schema: installed.Lock.Schema, DigestAlgorithm: installed.Lock.DigestAlgorithm, Entries: []skills.SkillVersionLockEntry{}}
	for _, ref := range commonRefs {
		entry, ok := byRef[ref]
		if !ok {
			return nil, fmt.Errorf("FORGE lock lacks common skill %s", ref)
		}
		baseline.Skills.Bindings = append(baseline.Skills.Bindings, skills.SkillBinding{Ref: ref, Activation: "pinned"})
		baseline.SkillsLock.Entries = append(baseline.SkillsLock.Entries, entry)
	}
	if err := baseline.Validate(); err != nil {
		return nil, err
	}
	stored, err := c.upsertAgent(ctx, baseline)
	if err != nil {
		return nil, err
	}
	if err := c.SetSkillsV2RolloutState(ctx, true); err != nil {
		return nil, fmt.Errorf("FORGE installed but versioned skills could not be enabled: %w", err)
	}
	installed.Agent = stored
	return installed, nil
}

// ExternalSkillReview is a conservative inspection result. It identifies
// compatibility blockers and probable dependencies but does not download,
// authorize or execute anything.
type ExternalSkillReview struct {
	RequiresExplicitFork bool     `json:"requires_explicit_fork"`
	ProbableDependencies []string `json:"probable_dependencies,omitempty"`
	Diagnostics          []string `json:"diagnostics,omitempty"`
}

func ReviewExternalSkill(files []skills.PackageFile) ExternalSkillReview {
	var review ExternalSkillReview
	for _, file := range files {
		if file.Path != "SKILL.md" {
			continue
		}
		body := strings.ToLower(string(file.Content))
		if strings.Contains(body, "subagent") || strings.Contains(body, "sub-agent") || strings.Contains(body, "delegate") {
			review.RequiresExplicitFork = true
			review.Diagnostics = append(review.Diagnostics, "delegation_not_supported: original requires independently observable delegation")
		}
		for _, name := range []string{"grill-with-docs", "domain-modeling", "tdd", "diagnosing-bugs"} {
			if strings.Contains(body, name) {
				review.ProbableDependencies = append(review.ProbableDependencies, name)
			}
		}
	}
	sort.Strings(review.ProbableDependencies)
	return review
}
