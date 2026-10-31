package launcher

// OpenChat is the high-level "resume this chat" call the TUI uses when
// the user hits Enter on the chat list. It:
//
//   1. Re-detects agents (so a newly installed one shows up without
//      restarting the launcher).
//   2. Looks up the chat's locked agent.
//   3. If unavailable, returns ErrAgentUnavailable so the TUI can route
//      to the install screen instead of crashing on a missing binary.
//   4. Otherwise builds the LaunchPlan (compile + decorate) — caller
//      runs it after Bubble Tea releases the TTY.

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrAgentUnavailable is returned by OpenChat when the chat's locked
// agent isn't installed on this machine.
var ErrAgentUnavailable = errors.New("chat's locked agent isn't available on this machine")

// OpenChatOptions are the per-call knobs for OpenChat. Today only
// SkipResume is meaningful — set by the TUI's "fresh launch" key (F
// on the chat list) so the user can deliberately start over on a chat
// that already has a captured session. Future options (fork, model
// override, …) can grow this struct without churning the signature.
type OpenChatOptions struct {
	// SkipResume, when true, bypasses RestoreNativeSession and any
	// resume args. The captured sessions/ dir is left intact so the
	// next normal launch can still pick up where the user left off.
	SkipResume bool
}

// OpenChat is the default-options form. Auto-resume is ON.
func OpenChat(c Chat) (LaunchPlan, Agent, error) {
	return OpenChatWithOptions(c, OpenChatOptions{})
}

// OpenChatWithOptions returns a LaunchPlan ready to execute. The Agent
// returned alongside is the resolved one (with Available/Version
// populated) so the TUI can render the post-launch summary correctly.
func OpenChatWithOptions(c Chat, opts OpenChatOptions) (LaunchPlan, Agent, error) {
	if c.AgentID == "" {
		return LaunchPlan{}, Agent{}, fmt.Errorf("chat %q has no locked agent", c.Label)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	agents := DetectAgents(ctx)

	var picked Agent
	for _, a := range agents {
		if a.ID == c.AgentID {
			picked = a
			break
		}
	}
	if picked.ID == "" {
		return LaunchPlan{}, Agent{}, fmt.Errorf("chat references unknown agent %q", c.AgentID)
	}
	if !picked.Available {
		return LaunchPlan{}, picked, ErrAgentUnavailable
	}

	plan, err := Plan(c.AsWorkspace(), picked)
	if err != nil {
		return LaunchPlan{}, picked, err
	}

	// Step 3: opt-in mirror-in. When the chat has MirrorAgentState set,
	// we copy the most-recent captured slice back into the agent's
	// home dir BEFORE the resume logic runs — so the agent boots with
	// exactly the per-chat view from the previous session, regardless
	// of what other terminals or chats may have done to its store.
	//
	// SIGKILL fallback: MirrorInSlice with preserveNewerHome=true
	// compares mtimes and refuses to overwrite home-dir files that
	// are newer than the slice. This protects users whose previous
	// PrAImate was killed between agent exit and mirror-out — their
	// last turns survive in the home dir and the next launch picks
	// them up instead of being clobbered by a stale slice.
	//
	// Skipped under SkipResume (the F-key fresh launch path) so the
	// user gets a truly clean slate.
	if !opts.SkipResume && c.Settings.MirrorAgentState {
		// Drain any in-flight mirror-out before reading the slice —
		// otherwise the previous session's snapshot could still be
		// landing on disk and we'd mirror IN a partial copy.
		drainCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = WaitForMirror(drainCtx)
		cancel()
		// LatestSliceDir already returns <sessionsDir>/<ts>-<agent>/native,
		// which is exactly the sliceRoot MirrorInSlice expects (it
		// joins with the per-agent subdir internally).
		if sliceRoot := LatestSliceDir(c.SessionsDir, picked.ID); sliceRoot != "" {
			homeOverride := ""
			LastMirrorInResult = MirrorInSliceWithHome(picked, c.SandboxDir, sliceRoot, true, homeOverride)
		}
	}

	// Best-effort native resume. Restores the most-recent captured
	// rollout for this chat back into the agent's own session store,
	// then appends (or replaces) the CLI args so the agent picks it up.
	// On a fresh chat there's nothing to restore — RestoreNativeSession
	// no-ops with an explanatory Note we surface via LastResume*.
	//
	// SkipResume bypasses the whole thing — the user explicitly asked
	// for a fresh launch via the chat list's `F` key. We leave the
	// sessions/ dir intact so the next normal launch can pick it back
	// up if the user changes their mind.
	var resume ResumePlan
	if opts.SkipResume {
		resume = ResumePlan{Note: "fresh launch — skipped restore of captured session (the sessions/ dir is still on disk)"}
	} else {
		resume = RestoreNativeSession(picked, c)
	}
	launcherMu.Lock()
	LastResumeNote = resume.Note
	LastResumeRestoredTo = resume.RestoredTo
	launcherMu.Unlock()
	if len(resume.Args) > 0 {
		switch picked.ID {
		case AgentCodex:
			// Codex resume is a subcommand. Preserve any unrelated global
			// flags supplied by the launcher before appending it.
			plan.Args = append(plan.Args, resume.Args...)
		default:
			// Claude (and future single-flag resumers) tolerate the
			// resume flag alongside flags like `--model`. Append.
			plan.Args = append(plan.Args, resume.Args...)
		}
	} else {
		// No resume args = fresh launch. Append the Option-C context
		// primer (subject to the chat's DisableContextPrimer toggle).
		// On resume, we skip the primer — the agent already has the
		// prior context in its rehydrated conversation state, so an
		// extra "read MEMORY.md" instruction would just be noise.
		plan = AppendContextPrimer(plan, picked, c.AsWorkspace())
	}
	return plan, picked, nil
}
