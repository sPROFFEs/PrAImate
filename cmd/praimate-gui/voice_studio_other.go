//go:build !linux

package main

func (a *App) beginStudioVoiceCapture() (VoiceCaptureSession, error) { return a.BeginVoiceCapture() }
