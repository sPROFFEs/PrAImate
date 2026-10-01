package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/sPROFFEs/PrAImate/internal/studio"
)

func (a *App) connectStudioAssistant() {
	studio.SetDesktopAssistantHooks(a.core, &studio.AssistantHooks{Call: a.studioAssistantCall})
}

func (a *App) studioAssistantCall(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var p struct {
		ID       string          `json:"id"`
		Message  string          `json:"message"`
		Audio    string          `json:"audio"`
		Context  json.RawMessage `json:"context"`
		Config   json.RawMessage `json:"config"`
		Allow    bool            `json:"allow"`
		Remember bool            `json:"remember"`
	}
	if len(raw) > 6<<20 {
		return nil, errors.New("Assistant request is too large")
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if len(p.Context) == 0 {
		p.Context = json.RawMessage(`{}`)
	}
	switch method {
	case "assistant.config":
		return a.AssistantConfig()
	case "assistant.configure":
		return true, a.SaveAssistantConfig(string(p.Config))
	case "assistant.snapshot":
		return a.AssistantSnapshot()
	case "assistant.send":
		return a.SendAssistant(p.Message, string(p.Context))
	case "assistant.cancel":
		a.CancelAssistant()
		return true, nil
	case "assistant.clear":
		return true, a.ClearAssistantHistory()
	case "assistant.health":
		return true, a.AssistantHealth()
	case "assistant.artifacts":
		return a.AssistantArtifactCatalog()
	case "assistant.artifacts.install":
		return a.InstallAssistantArtifact(p.ID)
	case "assistant.artifacts.verify":
		return a.VerifyAssistantArtifact(p.ID)
	case "assistant.artifacts.remove":
		return true, a.RemoveAssistantArtifact(p.ID)
	case "assistant.artifacts.cancel":
		a.CancelAssistantArtifactInstall()
		return true, nil
	case "assistant.approve":
		// Only assistant approvals are exposed through this bridge.
		a.approvalMu.Lock()
		broker := a.approval
		a.approvalMu.Unlock()
		if broker == nil {
			return nil, errors.New("approval is no longer pending")
		}
		broker.mu.Lock()
		scope := broker.scopes[p.ID]
		broker.mu.Unlock()
		if !strings.HasPrefix(scope, "assistant-") {
			return nil, errors.New("approval is no longer pending")
		}
		a.ResolveApproval(p.ID, p.Allow, p.Remember)
		return true, nil
	case "voice.begin":
		return a.beginStudioVoiceCapture()
	case "voice.end":
		a.EndVoiceCapture(p.ID)
		return true, nil
	case "voice.finish":
		return a.FinishNativeVoiceCapture(p.ID)
	case "voice.transcribe":
		return a.TranscribeVoice(p.Audio, string(p.Context))
	case "voice.cancel":
		a.CancelVoice()
		return true, nil
	}
	return nil, errors.New("unknown Assistant method")
}
