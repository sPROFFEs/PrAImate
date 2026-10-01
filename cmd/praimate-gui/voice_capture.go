package main

import (
	"encoding/base64"
	"errors"
	"strconv"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/voice"
)

type VoiceCaptureSession struct {
	ID     string `json:"id"`
	Native bool   `json:"native"`
}

// BeginVoiceCapture opens a bounded microphone-only lease on a push-to-talk
// gesture. macOS records natively; other platforms capture in the WebView.
func (a *App) BeginVoiceCapture() (VoiceCaptureSession, error) {
	config, err := a.AssistantConfig()
	if err != nil {
		return VoiceCaptureSession{}, err
	}
	if !config.Voice.Enabled {
		return VoiceCaptureSession{}, errors.New("enable Voice Input first")
	}
	a.assistantMu.Lock()
	if a.assistantClosed {
		a.assistantMu.Unlock()
		return VoiceCaptureSession{}, errors.New("Voice is shutting down")
	}
	if a.voiceCaptureTimer != nil {
		a.assistantMu.Unlock()
		return VoiceCaptureSession{}, errors.New("A microphone recording is already active")
	}
	a.voiceCaptureGeneration++
	generation := a.voiceCaptureGeneration
	reserveNativeVoiceCapture(generation)
	setVoiceCapturePermission(true)
	a.voiceCaptureTimer = time.AfterFunc(3*time.Minute, func() {
		a.assistantMu.Lock()
		defer a.assistantMu.Unlock()
		if a.voiceCaptureGeneration == generation {
			a.endVoiceCaptureLocked()
		}
	})
	a.assistantMu.Unlock()
	native, err := startNativeVoiceCapture(generation)
	a.assistantMu.Lock()
	defer a.assistantMu.Unlock()
	if generation != a.voiceCaptureGeneration || a.voiceCaptureTimer == nil {
		return VoiceCaptureSession{}, errors.New("Recording cancelled")
	}
	if err != nil {
		a.endVoiceCaptureLocked()
		return VoiceCaptureSession{}, err
	}
	return VoiceCaptureSession{ID: strconv.FormatUint(generation, 10), Native: native}, nil
}
func (a *App) EndVoiceCapture(id string) {
	a.assistantMu.Lock()
	defer a.assistantMu.Unlock()
	if id == strconv.FormatUint(a.voiceCaptureGeneration, 10) {
		a.endVoiceCaptureLocked()
	}
}
func (a *App) FinishNativeVoiceCapture(id string) (string, error) {
	a.assistantMu.Lock()
	defer a.assistantMu.Unlock()
	if a.voiceCaptureTimer == nil || id != strconv.FormatUint(a.voiceCaptureGeneration, 10) {
		return "", errors.New("Recording cancelled")
	}
	defer a.endVoiceCaptureLocked()
	wav, err := finishNativeVoiceCapture(a.voiceCaptureGeneration)
	if err != nil {
		return "", err
	}
	if err := voice.ValidateWAV(wav); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(wav), nil
}
func (a *App) endVoiceCaptureLocked() {
	a.voiceCaptureGeneration++
	if a.voiceCaptureTimer != nil {
		a.voiceCaptureTimer.Stop()
		a.voiceCaptureTimer = nil
	}
	setVoiceCapturePermission(false)
}
