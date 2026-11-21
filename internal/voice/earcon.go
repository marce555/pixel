package voice

import (
	"encoding/binary"
	"math"
)

// BuildThinkingAlertWAV synthesizes a discreet, modern, luminous two-tone crystalline
// earcon (E5 -> B5 + F#6 shimmer, 44.1kHz 16-bit mono PCM WAV, ~420ms) indicating
// that Pixel is actively building a response without speaking a transition phrase.
func BuildThinkingAlertWAV() []byte {
	const (
		sampleRate = 44100
		duration   = 0.42
		numSamples = int(sampleRate * duration)
		dataBytes  = numSamples * 2
	)

	buf := make([]byte, 44+dataBytes)
	// RIFF/WAVE header
	copy(buf[0:4], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:8], uint32(36+dataBytes))
	copy(buf[8:12], "WAVE")
	copy(buf[12:16], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:20], 16) // PCM chunk size
	binary.LittleEndian.PutUint16(buf[20:22], 1)  // PCM format
	binary.LittleEndian.PutUint16(buf[22:24], 1)  // Mono
	binary.LittleEndian.PutUint32(buf[24:28], sampleRate)
	binary.LittleEndian.PutUint32(buf[28:32], sampleRate*2)
	binary.LittleEndian.PutUint16(buf[32:34], 2)  // Block align
	binary.LittleEndian.PutUint16(buf[34:36], 16) // Bits per sample
	copy(buf[36:40], "data")
	binary.LittleEndian.PutUint32(buf[40:44], uint32(dataBytes))

	const twoPi = 2.0 * math.Pi
	const masterGain = 0.28

	for i := 0; i < numSamples; i++ {
		t := float64(i) / float64(sampleRate)
		var sample float64

		// Pulse 1: warm E5 (659.25 Hz) with subtle sub-octave and fast crystalline harmonic
		if t < 0.28 {
			att := 1.0
			if t < 0.012 {
				att = 0.5 * (1.0 - math.Cos(math.Pi*t/0.012))
			}
			env := att * math.Exp(-t*11.5)
			p1 := 0.74*math.Sin(twoPi*659.25*t) +
				0.12*math.Sin(twoPi*329.63*t) +
				0.14*math.Sin(twoPi*1318.51*t)*math.Exp(-t*10.5)
			sample += p1 * env
		}

		// Pulse 2: luminous B5 (987.77 Hz) + F#6 (1479.98 Hz) ambient ninth shimmer
		if t >= 0.09 {
			t2 := t - 0.09
			att := 1.0
			if t2 < 0.014 {
				att = 0.5 * (1.0 - math.Cos(math.Pi*t2/0.014))
			}
			env := att * math.Exp(-t2*9.0)
			p2 := 0.80*math.Sin(twoPi*987.77*t2) +
				0.14*math.Sin(twoPi*1479.98*t2)*math.Exp(-t2*5.0) +
				0.06*math.Sin(twoPi*1975.53*t2)*math.Exp(-t2*15.0)
			sample += p2 * env
		}

		// Smooth end fade-out over the last 25ms to guarantee zero click
		if t > duration-0.025 {
			rem := (duration - t) / 0.025
			if rem < 0 {
				rem = 0
			}
			sample *= 0.5 * (1.0 - math.Cos(math.Pi*rem))
		}

		val := sample * masterGain
		if val > 1.0 {
			val = 1.0
		} else if val < -1.0 {
			val = -1.0
		}
		pcm := int16(math.Round(val * 32767.0))
		binary.LittleEndian.PutUint16(buf[44+i*2:46+i*2], uint16(pcm))
	}

	return buf
}
