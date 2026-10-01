package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/artifacts"
	"github.com/sPROFFEs/PrAImate/internal/assistant"
	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/studio"
	"github.com/sPROFFEs/PrAImate/internal/voice"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const assistantConfigKey = "application_assistant_v1"
const assistantStateKey = "application_assistant_session_v1"

func (a *App) AssistantConfig() (assistant.Config, error) {
	c, err := a.requireCore()
	if err != nil {
		return assistant.Config{}, err
	}
	config := assistant.DefaultConfig()
	raw, err := c.GetSetting(context.Background(), core.ScopeGUI, assistantConfigKey)
	if err != nil {
		return config, err
	}
	if len(raw) > 0 {
		err = json.Unmarshal(raw, &config)
	}
	return config, err
}
func (a *App) SaveAssistantConfig(body string) error {
	c, err := a.requireCore()
	if err != nil {
		return err
	}
	if len(body) > 16384 {
		return errors.New("Assistant configuration is too large")
	}
	config := assistant.DefaultConfig()
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return errors.New("unexpected trailing Assistant configuration")
	}
	if err := config.Validate(); err != nil {
		return err
	}
	if config.Enabled && config.ModelID == "existing" {
		if _, err := c.ResolveNativeWorkerRoute(context.Background(), config.Endpoint, config.Model); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return err
	}
	if err := c.SetSetting(context.Background(), core.ScopeGUI, assistantConfigKey, raw); err != nil {
		return err
	}
	if !config.Enabled {
		a.CancelAssistant()
		a.assistantMu.Lock()
		provider := a.assistantProvider
		a.assistantProvider = nil
		a.assistantFingerprint = ""
		a.assistantMu.Unlock()
		if provider != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = provider.Stop(ctx)
		}
	}
	if !config.Voice.Enabled {
		a.CancelVoice()
		a.assistantMu.Lock()
		speech := a.voiceService
		a.voiceService = nil
		a.assistantMu.Unlock()
		if speech != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = speech.Stop(ctx)
		}
	}
	if a.ctx != nil {
		wruntime.EventsEmit(a.ctx, "assistant:config", config)
	}
	studio.PublishDesktopAssistant(a.core, "assistant.config.changed", config)
	return nil
}
func (a *App) AssistantSnapshot() (assistant.State, error) {
	idle := a.assistantRunMu.TryLock()
	if idle {
		defer a.assistantRunMu.Unlock()
	}
	c, err := a.requireCore()
	if err != nil {
		return assistant.State{}, err
	}
	raw, err := c.GetSetting(context.Background(), core.ScopeGUI, assistantStateKey)
	if err != nil {
		return assistant.State{}, err
	}
	state := assistant.State{Messages: []assistant.Message{}, Activity: []assistant.Audit{}}
	if len(raw) > 0 {
		err = json.Unmarshal(raw, &state)
	}
	if err == nil && idle && state.Task != nil && state.Task.Status == "running" {
		state.Task.Status = "interrupted"
		raw, _ := json.Marshal(state)
		err = c.SetSetting(context.Background(), core.ScopeGUI, assistantStateKey, raw)
	}
	return state, err
}
func (a *App) ensureAssistant() (*assistant.Service, error) {
	if _, err := a.requireCore(); err != nil {
		return nil, err
	}
	a.assistantMu.Lock()
	defer a.assistantMu.Unlock()
	if a.assistantClosed {
		return nil, errors.New("Assistant is shutting down")
	}
	if a.assistantService != nil {
		return a.assistantService, nil
	}
	r := a.assistantActions()
	a.assistantService = assistant.New(assistant.Options{Registry: r, Config: func(context.Context) (assistant.Config, error) { return a.AssistantConfig() }, Provider: a.getAssistantProvider, Load: func(context.Context) (assistant.State, error) { return a.AssistantSnapshot() }, Save: func(ctx context.Context, state assistant.State) error {
		raw, err := json.Marshal(state)
		if err != nil {
			return err
		}
		if len(raw) > 2<<20 {
			return errors.New("Assistant session exceeds persistence limit")
		}
		return a.core.SetSetting(ctx, core.ScopeGUI, assistantStateKey, raw)
	}, Approve: func(ctx context.Context, name string, args map[string]any) (bool, error) {
		broker := a.approvalProvider("assistant-operator")
		if broker == nil || broker.Request == nil {
			return false, errors.New("Assistant approval broker is unavailable")
		}
		return broker.Request(ctx, name, args)
	}, Emit: func(event assistant.Event) {
		studio.PublishDesktopAssistant(a.core, "assistant.event", event)
		if a.ctx != nil {
			wruntime.EventsEmit(a.ctx, "assistant:event", event)
		}
	}})
	return a.assistantService, nil
}
func (a *App) getAssistantProvider(ctx context.Context, config assistant.Config) (assistant.Provider, error) {
	fingerprintRaw, _ := json.Marshal([]any{config.ModelID, config.Endpoint, config.Model, config.Context, config.Threads, config.GPULayers, config.Batch, config.KeepLoaded, config.IdleSeconds})
	fingerprint := string(fingerprintRaw)
	a.assistantMu.Lock()
	defer a.assistantMu.Unlock()
	if a.assistantProvider != nil && a.assistantFingerprint == fingerprint {
		return a.assistantProvider, nil
	}
	if a.assistantProvider != nil {
		if err := a.assistantProvider.Stop(ctx); err != nil {
			return nil, err
		}
		a.assistantProvider = nil
	}
	usage := func(input, output int) {
		a.assistantMu.Lock()
		recorder := a.assistantUsage
		a.assistantMu.Unlock()
		if recorder != nil {
			recorder.Observe(core.StreamEvent{Type: "usage", Usage: &core.NativeUsage{PromptTokens: input, CompletionTokens: output}})
		}
	}
	if config.ModelID == "existing" {
		route, err := a.core.ResolveNativeWorkerRoute(ctx, config.Endpoint, config.Model)
		if err != nil {
			return nil, err
		}
		a.assistantProvider = &assistant.HTTPProvider{Endpoint: route.Endpoint, Model: route.Model, APIKey: route.APIKey, Usage: usage}
	} else {
		var definition *artifacts.Definition
		for _, d := range artifacts.Catalog() {
			if d.ID == config.ModelID {
				copy := d
				definition = &copy
				break
			}
		}
		if definition == nil {
			return nil, errors.New("unsupported managed Assistant model")
		}
		model, runtime, err := a.core.ManagedModelPaths(ctx, definition.ArtifactID)
		if err != nil {
			return nil, fmt.Errorf("install and verify the selected Assistant model/runtime first: %w", err)
		}
		firstStart := true
		a.assistantProvider = &assistant.LlamaProvider{Runtime: runtime.Path, ModelPath: model.Path, Config: config, Usage: usage, BeforeStart: func(ctx context.Context) error {
			if firstStart {
				firstStart = false
				return ctx.Err()
			}
			_, _, err := a.core.ManagedModelPaths(ctx, definition.ArtifactID)
			return err
		}}
	}
	a.assistantFingerprint = fingerprint
	return a.assistantProvider, nil
}
func (a *App) SendAssistant(message, contextJSON string) (state assistant.State, runErr error) {
	if !a.assistantRunMu.TryLock() {
		return state, errors.New("Assistant is already working")
	}
	defer a.assistantRunMu.Unlock()
	s, err := a.ensureAssistant()
	if err != nil {
		return state, err
	}
	config, err := a.AssistantConfig()
	if err != nil {
		return state, err
	}
	ui := map[string]any{}
	if len(contextJSON) > 4096 {
		return state, errors.New("Assistant UI context is too large")
	}
	var supplied map[string]any
	if contextJSON != "" {
		if err := json.Unmarshal([]byte(contextJSON), &supplied); err != nil {
			return state, err
		}
	}
	for _, key := range []string{"page", "chat_id", "agent_id", "worker_id", "project"} {
		if value, ok := supplied[key].(string); ok && len(value) < 1024 {
			ui[key] = value
		}
	}
	usageModel := config.Model
	if usageModel == "" {
		usageModel = config.ModelID
	}
	recorder, err := a.core.BeginUsage(context.Background(), "assistant", usageModel, "assistant")
	if err != nil {
		return state, err
	}
	a.assistantMu.Lock()
	a.assistantUsage = recorder
	a.assistantMu.Unlock()
	defer func() {
		_ = recorder.Finish(runErr)
		a.assistantMu.Lock()
		a.assistantUsage = nil
		a.assistantMu.Unlock()
	}()
	return s.Run(context.Background(), message, ui)
}

// ClearAssistantHistory is a user-facing operation, never a model action.
func (a *App) ClearAssistantHistory() error {
	if !a.assistantRunMu.TryLock() {
		return errors.New("stop Assistant before clearing its history")
	}
	defer a.assistantRunMu.Unlock()
	c, err := a.requireCore()
	if err != nil {
		return err
	}
	a.assistantMu.Lock()
	closed := a.assistantClosed
	a.assistantMu.Unlock()
	if closed {
		return errors.New("Assistant is shutting down")
	}
	raw, _ := json.Marshal(assistant.State{Messages: []assistant.Message{}, Activity: []assistant.Audit{}})
	return c.SetSetting(context.Background(), core.ScopeGUI, assistantStateKey, raw)
}
func (a *App) CancelAssistant() {
	a.assistantMu.Lock()
	s := a.assistantService
	if a.assistantHealthCancel != nil {
		a.assistantHealthCancel()
	}
	a.assistantMu.Unlock()
	if s != nil {
		s.Cancel()
	}
}
func (a *App) AssistantHealth() error {
	if !a.assistantRunMu.TryLock() {
		return errors.New("Assistant is already working")
	}
	defer a.assistantRunMu.Unlock()
	config, err := a.AssistantConfig()
	if err != nil {
		return err
	}
	if !config.Enabled {
		return errors.New("enable Assistant first")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	a.assistantMu.Lock()
	if a.assistantClosed {
		a.assistantMu.Unlock()
		return errors.New("Assistant is shutting down")
	}
	a.assistantHealthCancel = cancel
	a.assistantMu.Unlock()
	defer func() { a.assistantMu.Lock(); a.assistantHealthCancel = nil; a.assistantMu.Unlock() }()
	provider, err := a.getAssistantProvider(ctx, config)
	if err != nil {
		return err
	}
	if err := provider.Start(ctx); err != nil {
		return err
	}
	return provider.Health(ctx)
}
func (a *App) CancelVoice() {
	a.assistantMu.Lock()
	defer a.assistantMu.Unlock()
	a.endVoiceCaptureLocked()
	if a.voiceCancel != nil {
		a.voiceCancel()
	}
}
func (a *App) TranscribeVoice(encoded, contextJSON string) (voice.Result, error) {
	if len(encoded) > ((voice.MaxAudioBytes+2)/3)*4 {
		return voice.Result{}, errors.New("voice recording exceeds two minutes")
	}
	audio, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return voice.Result{}, err
	}
	if err := voice.ValidateWAV(audio); err != nil {
		return voice.Result{}, err
	}
	config, err := a.AssistantConfig()
	if err != nil {
		return voice.Result{}, err
	}
	if !config.Voice.Enabled {
		return voice.Result{}, errors.New("enable Voice Input in Assistant Settings first")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	a.assistantMu.Lock()
	if a.assistantClosed {
		a.assistantMu.Unlock()
		return voice.Result{}, errors.New("Voice is shutting down")
	}
	if a.voiceCancel != nil {
		a.assistantMu.Unlock()
		return voice.Result{}, errors.New("another transcription is active")
	}
	a.voiceCancel = cancel
	if a.voiceService == nil {
		a.voiceService = &voice.Service{}
	}
	speech := a.voiceService
	a.assistantMu.Unlock()
	defer func() { a.assistantMu.Lock(); a.voiceCancel = nil; a.assistantMu.Unlock() }()
	var definition artifacts.Definition
	for _, d := range artifacts.Catalog() {
		if d.ID == config.Voice.ModelID {
			definition = d
			break
		}
	}
	model, runtime, err := a.core.ManagedModelPaths(ctx, definition.ArtifactID)
	if err != nil {
		return voice.Result{}, fmt.Errorf("install and verify the selected Whisper model/runtime first: %w", err)
	}
	vocabulary := []string{}
	if len(contextJSON) < 4096 {
		var supplied map[string]string
		if json.Unmarshal([]byte(contextJSON), &supplied) == nil {
			agentID := supplied["agent_id"]
			project := supplied["project"]
			if id := supplied["chat_id"]; id != "" {
				if chat, err := a.core.GetChat(ctx, id); err == nil {
					if agentID == "" {
						agentID = chat.AgentID
					}
					if project == "" {
						project = chat.WorkspacePath
					}
				}
			}
			if agentID != "" {
				if agent, err := a.core.GetAgent(ctx, agentID); err == nil {
					vocabulary = append(vocabulary, agent.Name)
				}
			}
			if project != "" {
				vocabulary = append(vocabulary, filepath.Base(project))
			}
		}
	}
	vocabulary = append(vocabulary, "PrAImate", "MCP", "Graphify", "Reasoner", "Middle", "Fast", "Codex", "OpenCode", "Qwen", "LFM")
	if agents, err := a.ListAgents(); err == nil {
		for _, agent := range agents {
			if len(vocabulary) >= 18 {
				break
			}
			vocabulary = append(vocabulary, agent.Name)
		}
	}
	if skills, err := a.InstalledSkillVersionsV2(); err == nil {
		for _, skill := range skills {
			if len(vocabulary) >= 24 {
				break
			}
			vocabulary = append(vocabulary, skill.Name)
		}
	}
	if servers, err := a.MCPServers(); err == nil {
		for _, server := range servers {
			if len(vocabulary) >= 30 {
				break
			}
			vocabulary = append(vocabulary, server.Name)
		}
	}
	if a.ctx != nil {
		wruntime.EventsEmit(a.ctx, "voice:state", "transcribing")
	}
	return speech.Transcribe(ctx, voice.Config{Runtime: runtime.Path, Model: model.Path, Language: config.Voice.Language, Threads: config.Voice.Threads, KeepLoaded: config.Voice.KeepLoaded}, audio, vocabulary)
}
func (a *App) stopAssistantServices(ctx context.Context) error {
	a.assistantMu.Lock()
	a.assistantClosed = true
	a.assistantMu.Unlock()
	a.CancelAssistant()
	a.CancelVoice()
	a.assistantMu.Lock()
	s, provider, speech := a.assistantService, a.assistantProvider, a.voiceService
	a.assistantMu.Unlock()
	var failures []error
	if s != nil {
		if err := s.Stop(ctx); err != nil {
			failures = append(failures, err)
		}
	}
	if a.workers != nil {
		if err := a.workers.Stop(ctx); err != nil {
			failures = append(failures, err)
		}
	}
	if speech != nil {
		if err := speech.Stop(ctx); err != nil {
			failures = append(failures, err)
		}
	}
	if provider != nil {
		failures = append(failures, provider.Stop(ctx))
	}
	return errors.Join(failures...)
}
