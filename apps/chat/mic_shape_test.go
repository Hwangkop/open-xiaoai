package main

import (
	"encoding/binary"
	"math"
	"math/rand"
	"testing"
)

func toPCM16(samples []float64) []byte {
	b := make([]byte, len(samples)*2)
	for i, v := range samples {
		if v > 1 {
			v = 1
		}
		if v < -1 {
			v = -1
		}
		binary.LittleEndian.PutUint16(b[i*2:], uint16(int16(v*32767)))
	}
	return b
}

func TestSpeechShape(t *testing.T) {
	n := micSampleRate // 1s
	// 静音
	sil := toPCM16(make([]float64, n))
	if cv, zcr, ok := speechShape(sil); !ok || cv > 0.05 || zcr > 0.05 {
		t.Fatalf("静音应 CV≈0 ZCR≈0，实际 cv=%.2f zcr=%.2f ok=%v", cv, zcr, ok)
	}
	// 白噪声
	noise := make([]float64, n)
	for i := range noise {
		noise[i] = (rand.Float64()*2 - 1) * 0.02
	}
	if cv, zcr, ok := speechShape(toPCM16(noise)); !ok || zcr < micMaxZCR {
		t.Fatalf("白噪声过零率应很高，实际 cv=%.2f zcr=%.2f", cv, zcr)
	}
	// 类语音：200Hz 载波 + 4Hz 幅度调制
	sp := make([]float64, n)
	for i := range sp {
		tm := float64(i) / float64(micSampleRate)
		env := 0.5 + 0.5*math.Sin(2*math.Pi*4*tm)
		sp[i] = 0.3 * env * math.Sin(2*math.Pi*200*tm)
	}
	cv, zcr, ok := speechShape(toPCM16(sp))
	if !ok || cv < micMinCV || zcr > micMaxZCR {
		t.Fatalf("类语音应通过，实际 cv=%.2f zcr=%.2f ok=%v", cv, zcr, ok)
	}
	t.Logf("类语音 cv=%.2f zcr=%.2f", cv, zcr)
}
