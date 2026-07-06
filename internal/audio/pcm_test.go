package audio

import (
	"math"
	"testing"
)

func TestFloat32ToInt16LE_Clamp(t *testing.T) {
	// 범위 밖 값은 [-1,1]로 클램프된 뒤 변환된다.
	in := []float32{2.0, -2.0, 1.0, -1.0, 0.0}
	got := Float32ToInt16LE(in)
	if len(got) != len(in)*2 {
		t.Fatalf("byte 길이 불일치: got %d want %d", len(got), len(in)*2)
	}
	// 각 샘플을 int16으로 되읽어 검증.
	want := []int16{32767, -32767, 32767, -32767, 0}
	for i, w := range want {
		v := int16(uint16(got[2*i]) | uint16(got[2*i+1])<<8)
		if v != w {
			t.Errorf("샘플[%d]: got %d want %d", i, v, w)
		}
	}
}

func TestInt16LEToFloat32_OddByteIgnored(t *testing.T) {
	// 홀수 바이트(불완전 샘플)는 무시된다.
	b := []byte{0x00, 0x40, 0x7f} // 1개 완전 샘플(0x4000) + 잔여 1바이트
	got := Int16LEToFloat32(b)
	if len(got) != 1 {
		t.Fatalf("샘플 수: got %d want 1", len(got))
	}
	want := float32(0x4000) / 32767.0
	if math.Abs(float64(got[0]-want)) > 1e-6 {
		t.Errorf("변환값: got %v want %v", got[0], want)
	}
}

func TestRoundTrip(t *testing.T) {
	// Float32 → Int16LE → Float32 왕복 오차가 1 LSB(1/32767) 이내여야 한다.
	in := []float32{0, 0.25, -0.25, 0.5, -0.5, 0.999, -0.999, 1.0, -1.0}
	round := Int16LEToFloat32(Float32ToInt16LE(in))
	if len(round) != len(in) {
		t.Fatalf("왕복 길이 불일치: got %d want %d", len(round), len(in))
	}
	const tol = 1.0 / 32767.0
	for i := range in {
		if math.Abs(float64(round[i]-in[i])) > tol {
			t.Errorf("왕복[%d]: got %v want %v (tol %v)", i, round[i], in[i], tol)
		}
	}
}

func TestRMS(t *testing.T) {
	if got := RMS(nil); got != 0 {
		t.Errorf("빈 입력 RMS: got %v want 0", got)
	}
	// 상수 신호의 RMS는 그 절댓값과 같다.
	if got := RMS([]float32{0.5, 0.5, 0.5, 0.5}); math.Abs(float64(got-0.5)) > 1e-6 {
		t.Errorf("상수 RMS: got %v want 0.5", got)
	}
	// ±1 교번 신호의 RMS는 1.
	if got := RMS([]float32{1, -1, 1, -1}); math.Abs(float64(got-1)) > 1e-6 {
		t.Errorf("교번 RMS: got %v want 1", got)
	}
}
