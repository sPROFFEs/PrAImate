package launcher

// Post-exit lifecycle. Sits between `cmd.Run()` returning in main() and
// `os.Exit(code)`. Captures the agent's transcript out of its native
// store, runs the rule-based summarizer over it, and persists both
// into the per-launch session dir so the next launch can read them
// (via appendRecentSessionsDirective) and the user can browse them
// from the TUI search screen.

import (
	"context"
	"time"
)

// CapturePostExit is called after the agent process returns. It runs
// transcript capture for whichever AgentID the chat is bound to,
// writes the canonical JSONL into <chat>/sessions/<ts>-<agent>/, and
// generates summary.md / summary.json next to it.
//
// agent is the resolved Agent (with Binary/Version/WpcTarget filled
// in) — caller has it from OpenChat. ws is the chat-as-workspace
// shape. sandboxDir is taken from ws so the capture happens against
// the same cwd the agent ran in. sessionStart should be the wall
// clock from before exec; sessionEnd is "now".
//
// Returns the summary it produced (zero value when nothing was
// captured) and a non-nil error only on unexpected I/O. Locator
// misses and unsupported formats are surfaced via summary.Note, not
// errors — callers shouldn't fail the launch because a transcript
// couldn't be found.
func CapturePostExit(ws Workspace, agent Agent, sessionStart, sessionEnd time.Time) (SessionSummary, error) {
	dir, started := GetLastSession()
	if dir == "" {
		// Nothing for us to write into; decorate.go never reached the
		// recordChatSession step (possible if PrepareSandbox failed
		// before that line). Bail quietly — main() just exits.
		return SessionSummary{}, nil
	}
	if sessionStart.IsZero() {
		sessionStart = started
	}
	if sessionEnd.IsZero() {
		sessionEnd = time.Now().UTC()
	}
	homeOverride := ""
	cap, err := captureTranscript(agent, ws.SandboxDir, sessionStart, dir, homeOverride)
	if err != nil {
		return SessionSummary{}, err
	}
	// Step 2: snapshot the whole per-chat slice of the agent's native
	// store into <ts>-<agent>/native/. Runs in the BACKGROUND so the
	// TUI redraws immediately when the agent exits — slice copies
	// (claude project dirs, opencode message trees) can be tens of
	// MB / hundreds of files on slow disks. Best-effort: failures
	// land in LastMirrorResult.Note, never block the UI.
	StartMirrorOutAsyncWithHome(agent, ws.SandboxDir, dir, homeOverride)
	return WriteSummary(cap, dir, sessionStart, sessionEnd)
}

// ResolveAgentForChat is a thin shim around DetectAgents for the
// post-exit code path. main() doesn't keep the resolved Agent around
// after exec, so we re-detect to get the WpcTarget etc. needed by
// CapturePostExit. We accept a short deadline because this is in the
// critical path between agent exit and process exit.
func ResolveAgentForChat(id AgentID, deadline time.Duration) (Agent, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	for _, a := range DetectAgents(ctx) {
		if a.ID == id {
			return a, true
		}
	}
	return Agent{}, false
}
