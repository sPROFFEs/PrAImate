package main

import (
	"errors"
	"fmt"
	"github.com/sPROFFEs/PrAImate/internal/voice"
	"runtime"
	"sync"
	"syscall"
	"unsafe"
)

// CALLBACK_NULL lets the recording buffer remain owned and pinned until Reset
// returns. No WebView microphone permission or external recorder is required.
var waveDLL = syscall.NewLazyDLL("winmm.dll")
var waveOpen = waveDLL.NewProc("waveInOpen")
var wavePrepare = waveDLL.NewProc("waveInPrepareHeader")
var waveAdd = waveDLL.NewProc("waveInAddBuffer")
var waveStart = waveDLL.NewProc("waveInStart")
var waveReset = waveDLL.NewProc("waveInReset")
var waveUnprepare = waveDLL.NewProc("waveInUnprepareHeader")
var waveClose = waveDLL.NewProc("waveInClose")

type waveFormat struct {
	Tag, Channels      uint16
	Rate, Bytes        uint32
	Align, Bits, Extra uint16
}
type waveHeader struct {
	Data             *byte
	Length, Recorded uint32
	User             uintptr
	Flags, Loops     uint32
	Next             uintptr
	Reserved         uintptr
}

var microphone struct {
	sync.Mutex
	expected, id uint64
	handle       uintptr
	header       *waveHeader
	pcm          []byte
	pins         runtime.Pinner
	prepared     bool
}

func prepareVoiceEnvironment() {}
func prepareVoiceCapture()     {}
func reserveNativeVoiceCapture(id uint64) {
	microphone.Lock()
	defer microphone.Unlock()
	microphone.expected = id
}
func closeWaveLocked() {
	if microphone.handle != 0 {
		waveReset.Call(microphone.handle)
		if microphone.prepared {
			waveUnprepare.Call(microphone.handle, uintptr(unsafe.Pointer(microphone.header)), unsafe.Sizeof(waveHeader{}))
		}
		waveClose.Call(microphone.handle)
	}
	microphone.pins.Unpin()
	microphone.handle = 0
	microphone.header = nil
	microphone.pcm = nil
	microphone.prepared = false
	microphone.id = 0
}
func setVoiceCapturePermission(allowed bool) {
	if allowed {
		return
	}
	microphone.Lock()
	defer microphone.Unlock()
	microphone.expected = 0
	closeWaveLocked()
}
func startNativeVoiceCapture(id uint64) (bool, error) {
	microphone.Lock()
	defer microphone.Unlock()
	if microphone.expected != id {
		return true, errors.New("recording cancelled")
	}
	closeWaveLocked()
	format := waveFormat{Tag: 1, Channels: 1, Rate: 16000, Bytes: 32000, Align: 2, Bits: 16}
	code, _, _ := waveOpen.Call(uintptr(unsafe.Pointer(&microphone.handle)), uintptr(^uint32(0)), uintptr(unsafe.Pointer(&format)), 0, 0, 0)
	if code != 0 {
		return true, fmt.Errorf("microphone unavailable (Windows audio error %d); check microphone privacy settings", code)
	}
	microphone.pcm = make([]byte, 16000*2*120)
	microphone.header = &waveHeader{Data: &microphone.pcm[0], Length: uint32(len(microphone.pcm))}
	microphone.pins.Pin(&microphone.pcm[0])
	microphone.pins.Pin(microphone.header)
	code, _, _ = wavePrepare.Call(microphone.handle, uintptr(unsafe.Pointer(microphone.header)), unsafe.Sizeof(waveHeader{}))
	if code == 0 {
		microphone.prepared = true
		code, _, _ = waveAdd.Call(microphone.handle, uintptr(unsafe.Pointer(microphone.header)), unsafe.Sizeof(waveHeader{}))
	}
	if code == 0 {
		code, _, _ = waveStart.Call(microphone.handle)
	}
	if code != 0 {
		closeWaveLocked()
		return true, fmt.Errorf("microphone capture failed (Windows audio error %d)", code)
	}
	microphone.id = id
	return true, nil
}
func finishNativeVoiceCapture(id uint64) ([]byte, error) {
	microphone.Lock()
	defer microphone.Unlock()
	if microphone.handle == 0 || microphone.id != id {
		return nil, errors.New("recording cancelled")
	}
	waveReset.Call(microphone.handle)
	count := int(microphone.header.Recorded)
	if count > len(microphone.pcm) {
		count = len(microphone.pcm)
	}
	wav, err := voice.EncodePCM(microphone.pcm[:count])
	closeWaveLocked()
	return wav, err
}
