package main

// #cgo LDFLAGS: -framework AVFoundation -framework AudioToolbox
// #cgo CFLAGS: -fblocks
// #include <stdlib.h>
// #include <stdint.h>
// void praimate_voice_reserve(uint64_t session);
// int praimate_voice_start(uint64_t session);
// void praimate_voice_cancel(void);
// void *praimate_voice_finish(uint64_t session, int *length);
import "C"
import (
	"errors"
	"unsafe"
)

// Native capture avoids WKWebView's custom-scheme media restrictions. Audio
// stays in bounded memory and follows the same editable-transcript UI policy.
func prepareVoiceEnvironment()                 {}
func prepareVoiceCapture()                     {}
func reserveNativeVoiceCapture(session uint64) { C.praimate_voice_reserve(C.uint64_t(session)) }
func setVoiceCapturePermission(allowed bool) {
	if !allowed {
		C.praimate_voice_cancel()
	}
}
func startNativeVoiceCapture(session uint64) (bool, error) {
	if C.praimate_voice_start(C.uint64_t(session)) != 1 {
		return true, errors.New("microphone access was denied, cancelled, or no compatible audio input is available")
	}
	return true, nil
}
func finishNativeVoiceCapture(session uint64) ([]byte, error) {
	var length C.int
	ptr := C.praimate_voice_finish(C.uint64_t(session), &length)
	if ptr == nil {
		return nil, errors.New("no microphone audio was captured")
	}
	defer C.free(ptr)
	return C.GoBytes(unsafe.Pointer(ptr), length), nil
}
