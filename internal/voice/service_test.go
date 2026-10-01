package voice

import (
	"context"
	"encoding/binary"
	"testing"
)

func fixtureWAV() []byte {
	b := make([]byte, 48)
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], 40)
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], 16000)
	binary.LittleEndian.PutUint32(b[28:], 32000)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], 4)
	return b
}
func TestBoundedVoiceFormat(t *testing.T) {
	if err := ValidateWAV(fixtureWAV()); err != nil {
		t.Fatal(err)
	}
	for _, b := range [][]byte{nil, []byte("audio"), append(fixtureWAV(), 0), make([]byte, MaxAudioBytes+1)} {
		if ValidateWAV(b) == nil {
			t.Fatal("accepted invalid audio")
		}
	}
	b := fixtureWAV()
	binary.LittleEndian.PutUint32(b[24:], 48000)
	if ValidateWAV(b) == nil {
		t.Fatal("accepted wrong rate")
	}
}
func TestStopPreventsLaterSpawn(t *testing.T) {
	s := &Service{}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Transcribe(context.Background(), Config{Runtime: "must-not-execute"}, fixtureWAV(), nil); err == nil {
		t.Fatal("stopped voice service spawned")
	}
}

func TestNativePCMProducesCanonicalWAV(t *testing.T) {
	wav, err := EncodePCM(make([]byte, 32000))
	if err != nil || len(wav) != 32044 {
		t.Fatalf("wav length %d, error %v", len(wav), err)
	}
	if err := ValidateWAV(wav); err != nil {
		t.Fatal(err)
	}
	if _, err := EncodePCM(nil); err == nil {
		t.Fatal("empty recording accepted")
	}
	if _, err := EncodePCM(make([]byte, 16000*2*120+2)); err == nil {
		t.Fatal("recording limit not enforced")
	}
}
