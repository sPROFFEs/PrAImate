//go:build !linux && !darwin && !windows

package main

// WKWebView and WebView2 use their native capture permission handling.
// The app still checks Voice Input and bounds every capture lease.
func prepareVoiceCapture()                            {}
func setVoiceCapturePermission(bool)                  {}
func prepareVoiceEnvironment()                        {}
func reserveNativeVoiceCapture(uint64)                {}
func startNativeVoiceCapture(uint64) (bool, error)    { return false, nil }
func finishNativeVoiceCapture(uint64) ([]byte, error) { return nil, nil }
