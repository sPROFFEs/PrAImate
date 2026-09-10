package core

// Native skill delivery is intentionally capability-gated.  Interactive CLIs
// own their prompt, discovery rules and resume state, so a CLI name is never
// evidence that it can consume an isolated skill root safely.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"git.jtsec.local/lab/PrAImate/internal/appdata"
	"git.jtsec.local/lab/PrAImate/internal/skills"
)

// NativeSkillCapabilities records behavior demonstrated by a versioned adapter
// test.  It is deliberately separate from CLICapabilities: terminal discovery
// cannot be inferred from a headless adapter's capabilities.
type NativeSkillCapabilities struct {
	NativeSkillDiscovery    bool `json:"native_skill_discovery"`
	ScopedSkillRoot         bool `json:"scoped_skill_root"`
	ActivationObservable    bool `json:"activation_observable"`
	ReferenceReadObservable bool `json:"reference_read_observable"`
	HotUpdate               bool `json:"hot_update"`
	StrictSelectedSet       bool `json:"strict_selected_set"`
	ControlledPayload       bool `json:"controlled_payload"`
	FullRequestVisibility   bool `json:"full_request_visibility"`
}

// NativeSkillAdapter is optional.  A production adapter gets no native skill
// delivery until it implements this interface and its claimed capabilities are
// covered by an adapter characterization test.
type NativeSkillAdapter interface {
	NativeSkillCapabilities() NativeSkillCapabilities
}

func nativeCapabilities(adapter CLIAdapter) (NativeSkillCapabilities, bool) {
	capable, ok := adapter.(NativeSkillAdapter)
	if !ok {
		return NativeSkillCapabilities{}, false
	}
	caps := capable.NativeSkillCapabilities()
	return caps, caps.NativeSkillDiscovery && caps.ScopedSkillRoot
}

// NativeSkillMaterialization is a private, per-launch directory.  It contains
// immutable copies verified through HostSkillStore; it never points a CLI at a
// project, global configuration, credential, AGENTS.md or CLAUDE.md file.
// Read state is unknown unless the adapter emits reliable read events.
type NativeSkillMaterialization struct {
	Root      string
	Manifest  string
	Env       map[string]string
	Receipt   skills.SkillReceipt
	ReadState string // "observable" or "unknown"; not equivalent to used.
	HotUpdate bool
	// RequiresNewSession is the only safe status when a capability does not
	// prove hot updates. It must not be rendered as an applied live change.
	RequiresNewSession bool
	cleanup            nativeSkillCleanup
}

type nativeSkillCleanup struct {
	root  string
	files []nativeSkillFile
}
type nativeSkillFile struct {
	Path string `json:"path"`
	Sum  string `json:"sha256"`
}
type nativeSkillManifest struct {
	Schema string            `json:"schema"`
	Skills []nativeSkillItem `json:"skills"`
}
type nativeSkillItem struct {
	Ref       string `json:"ref"`
	Digest    string `json:"digest"`
	Directory string `json:"directory"`
}

// Cleanup removes only materialized files that still match their original
// digest.  A later user edit leaves the directory intact rather than being
// silently deleted.  It is safe to call more than once.
func (m *NativeSkillMaterialization) Cleanup() error {
	if m == nil || m.cleanup.root == "" {
		return nil
	}
	if _, err := os.Stat(m.cleanup.root); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	expected := make(map[string]string, len(m.cleanup.files))
	expectedDirs := map[string]bool{".": true}
	for _, file := range m.cleanup.files {
		expected[file.Path] = file.Sum
		for dir := filepath.Dir(file.Path); dir != "."; dir = filepath.Dir(dir) {
			expectedDirs[dir] = true
		}
	}
	if err := filepath.WalkDir(m.cleanup.root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(m.cleanup.root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if !expectedDirs[rel] {
				return fmt.Errorf("native skill materialization preserved at %s: directory %s was added", m.cleanup.root, rel)
			}
			return nil
		}
		if _, ok := expected[rel]; !ok {
			return fmt.Errorf("native skill materialization preserved at %s: %s was added", m.cleanup.root, rel)
		}
		return nil
	}); err != nil {
		return err
	}
	for _, expected := range m.cleanup.files {
		body, err := os.ReadFile(filepath.Join(m.cleanup.root, expected.Path))
		if err != nil {
			continue
		}
		sum := sha256.Sum256(body)
		if hex.EncodeToString(sum[:]) != expected.Sum {
			return fmt.Errorf("native skill materialization preserved at %s: %s was modified", m.cleanup.root, expected.Path)
		}
	}
	return os.RemoveAll(m.cleanup.root)
}

func nativeSkillRoot() (string, error) {
	base, err := appdata.Root()
	if err != nil {
		return "", err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return filepath.Join(base, "native-skill-sessions", hex.EncodeToString(nonce[:])), nil
}

func nativeSkillDirectory(ref, digest string) string {
	sum := sha256.Sum256([]byte(ref + "\x00" + digest))
	return hex.EncodeToString(sum[:16])
}

func writeNativeSkillFile(root, relative string, body []byte, executable bool) (nativeSkillFile, error) {
	clean := filepath.Clean(relative)
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nativeSkillFile{}, errors.New("invalid native skill materialization path")
	}
	path := filepath.Join(root, clean)
	if rel, err := filepath.Rel(root, path); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nativeSkillFile{}, errors.New("native skill materialization escaped its root")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nativeSkillFile{}, err
	}
	mode := os.FileMode(0600)
	if executable {
		mode = 0700 // preservation of package intent, never execution approval.
	}
	if err := os.WriteFile(path, body, mode); err != nil {
		return nativeSkillFile{}, err
	}
	sum := sha256.Sum256(body)
	return nativeSkillFile{Path: clean, Sum: hex.EncodeToString(sum[:])}, nil
}

// MaterializeNativeSkills makes exactly a resolved, approved set visible to a
// capable CLI.  It is a generic adapter protocol: PRAIMATE_SKILL_ROOT and
// PRAIMATE_SKILL_MANIFEST are the only launch inputs.  It does not inject an
// inline body, so callers must choose exactly one transport per launch.
func MaterializeNativeSkills(ctx context.Context, adapter CLIAdapter, set skills.ResolvedSkillSet, strict bool) (*NativeSkillMaterialization, error) {
	caps, supported := nativeCapabilities(adapter)
	if !supported {
		return nil, errors.New("native_skill_unsupported: this CLI adapter has no verified scoped skill-root integration; use controlled chat/workflow delivery or start without v2 skills")
	}
	if err := ValidateNativeSkillTransport(adapter, set, strict); err != nil {
		return nil, err
	}
	return materializeResolvedSkills(ctx, set, caps)
}

func materializeResolvedSkills(ctx context.Context, set skills.ResolvedSkillSet, caps NativeSkillCapabilities) (*NativeSkillMaterialization, error) {
	root, err := nativeSkillRoot()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	fail := func(e error) (*NativeSkillMaterialization, error) { _ = os.RemoveAll(root); return nil, e }
	store, err := OpenSkillStore(skills.PackageLimits{})
	if err != nil {
		return fail(err)
	}
	defer store.Close()

	bindings := append([]skills.ResolvedSkillBinding(nil), set.Bindings...)
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].Version.Ref < bindings[j].Version.Ref })
	manifest := nativeSkillManifest{Schema: "praimate.native-skills/v1"}
	cleanup := nativeSkillCleanup{root: root}
	var delivered []skills.ContextBlock
	for _, binding := range bindings {
		if binding.Binding.Activation == "off" {
			continue
		}
		version, files, err := store.ReadVersion(ctx, binding.Version.Ref, binding.Version.Digest)
		if err != nil || version != binding.Version {
			if err == nil {
				err = errors.New("integrity_mismatch: native skill version changed")
			}
			return fail(err)
		}
		dir := nativeSkillDirectory(version.Ref, version.Digest)
		manifest.Skills = append(manifest.Skills, nativeSkillItem{Ref: version.Ref, Digest: version.Digest, Directory: dir})
		for _, file := range files {
			entry, err := writeNativeSkillFile(root, filepath.Join("skills", dir, filepath.FromSlash(file.Path)), file.Content, file.Executable)
			if err != nil {
				return fail(err)
			}
			cleanup.files = append(cleanup.files, entry)
		}
		delivered = append(delivered, skills.ContextBlock{Ref: version.Ref, Digest: version.Digest, Kind: skills.BlockBody, Activation: binding.Binding.Activation})
	}
	body, err := json.Marshal(manifest)
	if err != nil {
		return fail(err)
	}
	manifestFile, err := writeNativeSkillFile(root, "manifest.json", body, false)
	if err != nil {
		return fail(err)
	}
	cleanup.files = append(cleanup.files, manifestFile)
	state := "unknown"
	if caps.ReferenceReadObservable {
		state = "observable"
	}
	return &NativeSkillMaterialization{
		Root: root, Manifest: filepath.Join(root, "manifest.json"),
		Env:       map[string]string{"PRAIMATE_SKILL_ROOT": root, "PRAIMATE_SKILL_MANIFEST": filepath.Join(root, "manifest.json")},
		Receipt:   skills.SkillReceipt{Coverage: skills.CoverageUnknown, Measurement: skills.MeasurementBytes, ContextEpoch: 1, Delivered: delivered},
		ReadState: state, HotUpdate: caps.HotUpdate, RequiresNewSession: !caps.HotUpdate, cleanup: cleanup,
	}, nil
}

// PrepareTerminalSkillFallback builds the same controlled, pinned payload used
// by Chat/Studio and materializes its resources for a temporary project-context
// file. This is compatible delivery: the CLI's actual read remains unknown.
func (c *Core) PrepareTerminalSkillFallback(ctx context.Context, agent *Agent) (string, *NativeSkillMaterialization, bool, error) {
	appConfig, appLock, err := c.SkillDefaultsV2(ctx)
	if err != nil {
		return "", nil, false, err
	}
	app, err := skills.SelectionScope(appConfig, appLock)
	if err != nil {
		return "", nil, false, err
	}
	scopes := skills.SkillScopes{Application: app}
	if agent != nil {
		scopes.Agent, err = skills.SelectionScope(agent.Skills, agent.SkillsLock)
		if err != nil {
			return "", nil, false, err
		}
	}
	frozen, _, err := skills.FreezeSkillPreferences(scopes)
	if err != nil {
		return "", nil, false, err
	}
	if frozen.Config == nil || !frozen.Config.Configured {
		return "", nil, false, nil
	}
	lock, err := skills.DecodeSkillVersionLock(frozen.Lock)
	if err != nil {
		return "", nil, false, err
	}
	settings := ChatSettings{SkillsV2: frozen.Config, SkillsLock: &lock}
	payload, _, err := c.BuildChatSkillPayload(ctx, settings, "")
	if err != nil {
		return "", nil, true, fmt.Errorf("terminal_skill_fallback: %w", err)
	}
	set, err := c.PreviewBoundSkills(ctx, nil, nil, settings)
	if err != nil {
		return "", nil, true, err
	}
	materialized, err := materializeResolvedSkills(ctx, set, NativeSkillCapabilities{})
	if err != nil {
		return "", nil, true, err
	}
	if payload != "" {
		payload += "\n\nPrAImate materialized the exact reviewed skill resources for this session. The manifest is at " + materialized.Manifest + ". Read only resources listed there when the skill instructions require them."
	}
	return payload, materialized, true, nil
}

// PrepareTerminalSkillFallbackForSettings uses a chat's already-frozen skill
// snapshot. It prevents a native terminal from racing ahead of selections made
// in its creation dialog.
func (c *Core) PrepareTerminalSkillFallbackForSettings(ctx context.Context, settings ChatSettings) (string, *NativeSkillMaterialization, bool, error) {
	if err := validateChatSkills(settings); err != nil {
		return "", nil, false, err
	}
	if settings.SkillsV2 == nil || !settings.SkillsV2.Configured {
		return "", nil, false, nil
	}
	payload, _, err := c.BuildChatSkillPayload(ctx, settings, "")
	if err != nil {
		return "", nil, true, fmt.Errorf("terminal_skill_fallback: %w", err)
	}
	set, err := c.PreviewBoundSkills(ctx, nil, nil, settings)
	if err != nil {
		return "", nil, true, err
	}
	materialized, err := materializeResolvedSkills(ctx, set, NativeSkillCapabilities{})
	if err != nil {
		return "", nil, true, err
	}
	if payload != "" {
		payload += "\n\nPrAImate materialized the exact reviewed skill resources for this session. The manifest is at " + materialized.Manifest + ". Read only resources listed there when the skill instructions require them."
	}
	return payload, materialized, true, nil
}

// ResolveNativeSkillSet resolves the same frozen scope used by other surfaces.
// It does not materialize anything and therefore remains useful for preflight.
func ResolveNativeSkillSet(ctx context.Context, scopes skills.SkillScopes) (skills.ResolvedSkillSet, error) {
	store, err := OpenSkillStore(skills.PackageLimits{})
	if err != nil {
		return skills.ResolvedSkillSet{}, err
	}
	defer store.Close()
	return store.ResolveSkills(ctx, scopes, skills.SkillResolutionPolicy{Budget: skills.SkillBudget{CatalogTokens: 1000, BodyTokens: 4000, ResourceTokens: 2000, TotalTokens: 6000, MaxActive: 3, MaxLoadCallsPerTurn: 4, MaxResourceReadsPerTurn: 6, MaxLoadedBytesFallback: 16384}, Transport: "compatible"})
}

// ValidateNativeSkillTransport is the non-mutating terminal preflight.  It is
// intentionally separate from MaterializeNativeSkills so a renderer can fail
// before any native session directory is created.
func ValidateNativeSkillTransport(adapter CLIAdapter, set skills.ResolvedSkillSet, strict bool) error {
	caps, supported := nativeCapabilities(adapter)
	if !supported {
		return errors.New("native_skill_unsupported: this CLI adapter has no verified scoped skill-root integration; use controlled chat/workflow delivery or start without v2 skills")
	}
	if strict && !caps.StrictSelectedSet {
		return errors.New("native_skill_strict_unavailable: the CLI may discover skills outside PrAImate's selected set")
	}
	if set.Config == nil || set.Config.Enforcement != "compatible" {
		return errors.New("incompatible_transport: native materialization requires a compatible skill configuration")
	}
	return nil
}

// NativeTerminalSkillSet returns the effective application/agent selection for
// a fresh terminal.  There is no session lock until a verified native adapter
// exists, so this method is preflight-only and never claims a selection was
// applied to an interactive CLI.
func (c *Core) NativeTerminalSkillSet(ctx context.Context, agent *Agent) (skills.ResolvedSkillSet, bool, error) {
	appConfig, appLock, err := c.SkillDefaultsV2(ctx)
	if err != nil {
		return skills.ResolvedSkillSet{}, false, err
	}
	app, err := skills.SelectionScope(appConfig, appLock)
	if err != nil {
		return skills.ResolvedSkillSet{}, false, err
	}
	scopes := skills.SkillScopes{Application: app}
	if agent != nil {
		scopes.Agent, err = skills.SelectionScope(agent.Skills, agent.SkillsLock)
		if err != nil {
			return skills.ResolvedSkillSet{}, false, err
		}
	}
	frozen, _, err := skills.FreezeSkillPreferences(scopes)
	if err != nil {
		return skills.ResolvedSkillSet{}, false, err
	}
	if frozen.Config == nil || !frozen.Config.Configured {
		return skills.ResolvedSkillSet{}, false, nil
	}
	set, err := ResolveNativeSkillSet(ctx, skills.SkillScopes{Session: frozen})
	return set, true, err
}

// nativeSkillLeases protects the fallback shape used by an adapter that can
// only bind discovery to a project.  Current materialization refuses that
// shape; the lease is here so an explicitly characterized future adapter has a
// collision-safe primitive rather than reintroducing project-file writes.
var nativeSkillLeases = struct {
	sync.Mutex
	holders map[string]string
}{holders: map[string]string{}}

func acquireNativeSkillLease(cwd, owner string) (func(), error) {
	cwd, err := filepath.Abs(cwd)
	if err != nil {
		return nil, err
	}
	nativeSkillLeases.Lock()
	defer nativeSkillLeases.Unlock()
	if existing := nativeSkillLeases.holders[cwd]; existing != "" && existing != owner {
		return nil, fmt.Errorf("native_skill_lease_conflict: %s already has a different active skill selection", cwd)
	}
	nativeSkillLeases.holders[cwd] = owner
	return func() {
		nativeSkillLeases.Lock()
		defer nativeSkillLeases.Unlock()
		if nativeSkillLeases.holders[cwd] == owner {
			delete(nativeSkillLeases.holders, cwd)
		}
	}, nil
}
