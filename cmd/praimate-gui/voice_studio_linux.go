package main

import (
	"errors"
	"github.com/sPROFFEs/PrAImate/internal/voice"
	"strconv"
	"sync"
)

var studioMic struct {
	sync.Mutex
	id       uint64
	recorder *voice.PCMRecorder
}

func (a *App) beginStudioVoiceCapture() (VoiceCaptureSession, error) {
	lease, err := a.BeginVoiceCapture()
	if err != nil {
		return lease, err
	}
	a.assistantMu.Lock()
	defer a.assistantMu.Unlock()
	if a.voiceCaptureTimer == nil || lease.ID != strconv.FormatUint(a.voiceCaptureGeneration, 10) {
		return lease, errors.New("recording cancelled")
	}
	recorder, err := voice.StartPCMRecorder()
	if err != nil {
		a.endVoiceCaptureLocked()
		return lease, err
	}
	studioMic.Lock()
	studioMic.id = a.voiceCaptureGeneration
	studioMic.recorder = recorder
	studioMic.Unlock()
	lease.Native = true
	return lease, nil
}

func stopStudioMicrophone() {
	studioMic.Lock()
	defer studioMic.Unlock()
	if studioMic.recorder != nil {
		studioMic.recorder.Stop()
		studioMic.recorder = nil
	}
}

func finishStudioMicrophone(id uint64) ([]byte, error) {
	studioMic.Lock()
	defer studioMic.Unlock()
	if studioMic.recorder == nil || studioMic.id != id {
		return nil, errors.New("recording cancelled")
	}
	recorder := studioMic.recorder
	studioMic.recorder = nil
	return recorder.Finish()
}
