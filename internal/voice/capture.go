package voice

import (
	"encoding/binary"
	"errors"
	"io"
	"os/exec"
	"sync"
	"time"
)

// PCMRecorder owns a bounded, memory-only native recorder. It also works when
// an editor WebView cannot request microphone permission (notably VS Code).
type PCMRecorder struct {
	cmd  *exec.Cmd
	done chan struct{}
	pcm  []byte
	err  error
	once sync.Once
}

func StartPCMRecorder() (*PCMRecorder, error) {
	name, err := exec.LookPath("parec")
	args := []string{"--raw", "--format=s16le", "--rate=16000", "--channels=1", "--latency-msec=50"}
	if err != nil {
		name, err = exec.LookPath("arecord")
		args = []string{"-q", "-t", "raw", "-f", "S16_LE", "-r", "16000", "-c", "1", "-d", "120"}
	}
	if err != nil {
		return nil, errors.New("native microphone capture needs PulseAudio parec or ALSA arecord")
	}
	cmd := exec.Command(name, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	r := &PCMRecorder{cmd: cmd, done: make(chan struct{})}
	go func() {
		r.pcm, r.err = io.ReadAll(io.LimitReader(stdout, 16000*2*120))
		_ = cmd.Process.Kill()
		waitErr := cmd.Wait()
		if len(r.pcm) == 0 && r.err == nil {
			r.err = waitErr
		}
		close(r.done)
	}()
	select {
	case <-r.done:
		return nil, errors.New("microphone recorder could not open the audio device; check the system input device")
	case <-time.After(150 * time.Millisecond):
		return r, nil
	}
}

func (r *PCMRecorder) Stop() {
	r.once.Do(func() { _ = r.cmd.Process.Kill() })
	<-r.done
}

func (r *PCMRecorder) Finish() ([]byte, error) {
	r.Stop()
	if r.err != nil && len(r.pcm) == 0 {
		return nil, errors.New("no microphone audio was captured")
	}
	pcm := r.pcm
	r.pcm = nil
	return EncodePCM(pcm)
}

func EncodePCM(pcm []byte) ([]byte, error) {
	count := len(pcm) &^ 1
	if count > 16000*2*120 {
		return nil, errors.New("microphone recording exceeds two minutes")
	}
	if count == 0 {
		return nil, errors.New("no microphone audio was captured")
	}
	wav := make([]byte, 44+count)
	copy(wav, "RIFF")
	binary.LittleEndian.PutUint32(wav[4:], uint32(len(wav)-8))
	copy(wav[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(wav[16:], 16)
	binary.LittleEndian.PutUint16(wav[20:], 1)
	binary.LittleEndian.PutUint16(wav[22:], 1)
	binary.LittleEndian.PutUint32(wav[24:], 16000)
	binary.LittleEndian.PutUint32(wav[28:], 32000)
	binary.LittleEndian.PutUint16(wav[32:], 2)
	binary.LittleEndian.PutUint16(wav[34:], 16)
	copy(wav[36:], "data")
	binary.LittleEndian.PutUint32(wav[40:], uint32(count))
	copy(wav[44:], pcm[:count])
	return wav, ValidateWAV(wav)
}
