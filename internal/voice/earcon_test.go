package voice

import (
	"encoding/binary"
	"math"
	"testing"
)

func TestBuildThinkingAlertWAV(t *testing.T) {
	wav := BuildThinkingAlertWAV()
	if len(wav) <= 44 {
		t.Fatalf("expected non-empty WAV payload, got %d bytes", len(wav))
	}
	if string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		t.Fatalf("invalid WAV header: %q %q", wav[0:4], wav[8:12])
	}
	sampleRate := binary.LittleEndian.Uint32(wav[24:28])
	if sampleRate != 44100 {
		t.Fatalf("sampleRate = %d, want 44100", sampleRate)
	}
	dataLen := binary.LittleEndian.Uint32(wav[40:44])
	if int(dataLen) != len(wav)-44 {
		t.Fatalf("dataLen = %d, actual = %d", dataLen, len(wav)-44)
	}

	var maxAmp float64
	numSamples := int(dataLen) / 2
	for i := 0; i < numSamples; i++ {
		s := int16(binary.LittleEndian.Uint16(wav[44+i*2 : 46+i*2]))
		amp := math.Abs(float64(s)) / 32767.0
		if amp > maxAmp {
			maxAmp = amp
		}
	}
	// Must be audible yet discreet (below 0.50 full scale)
	if maxAmp < 0.10 || maxAmp > 0.45 {
		t.Fatalf("unexpected earcon peak amplitude: %.3f (expected discreet 0.10..0.45)", maxAmp)
	}
	lastSample := int16(binary.LittleEndian.Uint16(wav[len(wav)-2:]))
	if math.Abs(float64(lastSample)) > 10 {
		t.Fatalf("expected smooth fade-out to ~0, last sample = %d", lastSample)
	}
}
