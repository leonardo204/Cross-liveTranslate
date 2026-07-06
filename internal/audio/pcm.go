package audio

import "math"

// Float32ToInt16LE converts Float32 samples in [-1,1] to little-endian Int16 PCM
// bytes. Samples are clamped to [-1,1] then scaled by 32767.
//
// 원본 이식: GeminiLiveClient.floatToInt16LEData (clamp → *32767, LE 하위 바이트 먼저).
// Gemini Live 송신 포맷(16kHz mono Int16 LE PCM)의 변환 단계다.
func Float32ToInt16LE(samples []float32) []byte {
	out := make([]byte, 0, len(samples)*2)
	for _, s := range samples {
		if s > 1 {
			s = 1
		} else if s < -1 {
			s = -1
		}
		v := int16(s * 32767.0)
		// little-endian: 하위 바이트 먼저.
		out = append(out, byte(v), byte(uint16(v)>>8))
	}
	return out
}

// Int16LEToFloat32 is the inverse of Float32ToInt16LE: little-endian Int16 PCM
// bytes → Float32 in [-1,1]. A trailing odd byte (incomplete sample) is ignored.
func Int16LEToFloat32(b []byte) []float32 {
	n := len(b) / 2
	out := make([]float32, n)
	for i := 0; i < n; i++ {
		v := int16(uint16(b[2*i]) | uint16(b[2*i+1])<<8)
		out[i] = float32(v) / 32767.0
	}
	return out
}

// RMS returns the root-mean-square level of the samples (0 for empty input).
// Used for a coarse input-level readout (debug/HUD).
func RMS(samples []float32) float32 {
	if len(samples) == 0 {
		return 0
	}
	var sum float64
	for _, s := range samples {
		sum += float64(s) * float64(s)
	}
	return float32(math.Sqrt(sum / float64(len(samples))))
}
