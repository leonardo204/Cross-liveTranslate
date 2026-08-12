package vad

import (
	"context"
	"reflect"
	"testing"
	"time"

	"cross-livetranslate/internal/audio"
)

// record 는 옵저버가 낸 이벤트를 순서대로 모으는 훅 묶음이다.
type record struct {
	starts     []time.Duration // onset(직전 무음)
	ends       int             // offset 횟수
	boundaries []time.Duration // 화자 경계 후보(무음 길이)
	last       Levels          // 마지막 전이 시점의 임계 스냅샷
}

func (r *record) hooks() Hooks {
	return Hooks{
		OnSpeechStart: func(s time.Duration, st Levels) { r.starts = append(r.starts, s); r.last = st },
		OnSpeechEnd:   func(st Levels) { r.ends++; r.last = st },
		OnBoundary:    func(s time.Duration, st Levels) { r.boundaries = append(r.boundaries, s); r.last = st },
	}
}

// feed 는 프레임 RMS를 옵저버에 흘린다(실제 경로와 동일하게 RMS 1회 계산).
func feed(o *Observer, amp float32, frames int) {
	for i := 0; i < frames; i++ {
		o.Observe(audio.ChunkSamples, amp)
	}
}

// --- 청크 길이 → 시간 환산(오디오 계약 16kHz/1600샘플=100ms) ---

func TestChunkDuration(t *testing.T) {
	if got := chunkDuration(audio.ChunkSamples); got != 100*time.Millisecond {
		t.Fatalf("chunkDuration(1600) = %v, want 100ms", got)
	}
	if got := chunkDuration(800); got != 50*time.Millisecond {
		t.Fatalf("chunkDuration(800) = %v, want 50ms", got)
	}
	if got := chunkDuration(0); got != 0 {
		t.Fatalf("chunkDuration(0) = %v, want 0", got)
	}
}

// --- 노이즈 플로어: 링버퍼 + 하위 백분위(quickselect) ---

func TestNoiseFloorPercentile(t *testing.T) {
	var n noiseFloor
	if n.ready() {
		t.Fatal("표본 0에서 ready=true")
	}
	// 80%는 0.1(발화 수준), 20%는 0.02(배경) → 20th 백분위는 0.02 대역이어야 한다.
	for i := 0; i < FloorWindowFrames; i++ {
		if i%5 == 0 {
			n.push(0.02)
		} else {
			n.push(0.1)
		}
	}
	if !n.ready() {
		t.Fatal("표본 50에서 ready=false")
	}
	if got := n.estimate(); got > 0.03 {
		t.Fatalf("floor = %v, want ≈0.02(하위 20%%)", got)
	}
	// 링버퍼가 오래된 표본을 밀어낸다 — 전부 조용해지면 플로어도 내려간다.
	for i := 0; i < FloorWindowFrames; i++ {
		n.push(0.001)
	}
	if got := n.estimate(); got > 0.002 {
		t.Fatalf("갱신 후 floor = %v, want ≈0.001", got)
	}
}

func TestQuickSelect(t *testing.T) {
	src := []float32{0.9, 0.1, 0.5, 0.3, 0.7, 0.2, 0.8, 0.4, 0.6, 0.05}
	want := []float32{0.05, 0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9}
	for k := range want {
		buf := append([]float32(nil), src...)
		if got := quickSelect(buf, k); got != want[k] {
			t.Fatalf("quickSelect(k=%d) = %v, want %v", k, got, want[k])
		}
	}
}

// (a) 관찰 여운 300ms → 400ms 갭을 발화 종료 + 경계로 인식한다.
// (게이팅 여운 750ms만 있었다면 이 갭은 통째로 여운에 묻혀 관측되지 않았다.)
func TestObserverDetects400msGap(t *testing.T) {
	var r record
	o := NewObserver(DefaultBoundarySilence, r.hooks())

	feed(o, loud, 10)  // 화자 A 발화
	feed(o, silent, 4) // 400ms 갭 — 3프레임(300ms)에서 offset, 4프레임째까지 무음 누적
	if r.ends != 1 {
		t.Fatalf("offset 횟수 = %d, want 1 (300ms 여운)", r.ends)
	}
	feed(o, loud, 3) // 화자 B 발화

	if len(r.boundaries) != 1 {
		t.Fatalf("경계 후보 = %v, want 1건(400ms ≥ 400ms)", r.boundaries)
	}
	if r.boundaries[0] != 400*time.Millisecond {
		t.Fatalf("보고된 무음 = %v, want 400ms", r.boundaries[0])
	}
}

// 300ms 갭(임계 미만)은 종료로는 잡히더라도 경계는 아니다.
func TestObserverShortGapNoBoundary(t *testing.T) {
	var r record
	o := NewObserver(DefaultBoundarySilence, r.hooks())

	feed(o, loud, 10)
	feed(o, silent, 3) // 300ms — offset은 나지만 경계 임계(400ms) 미만
	feed(o, loud, 3)

	if len(r.boundaries) != 0 {
		t.Fatalf("경계 후보 = %v, want 없음(300ms < 400ms)", r.boundaries)
	}
	if r.ends != 1 || len(r.starts) != 2 {
		t.Fatalf("전이 기록 불일치: ends=%d starts=%d", r.ends, len(r.starts))
	}
}

// 200ms 갭은 관찰 여운(300ms) 안에 묻혀 종료조차 되지 않는다(문장 내 호흡).
func TestObserverBreathGapNotAnEnd(t *testing.T) {
	var r record
	o := NewObserver(DefaultBoundarySilence, r.hooks())

	feed(o, loud, 10)
	feed(o, silent, 2) // 200ms
	feed(o, loud, 10)

	if r.ends != 0 {
		t.Fatalf("offset = %d, want 0 (200ms는 여운 안)", r.ends)
	}
	if len(r.boundaries) != 0 {
		t.Fatalf("경계 후보 = %v, want 없음", r.boundaries)
	}
}

// (b) 배경음(RMS 0.02) 위 발화(RMS 0.1): 적응 플로어가 종료를 감지한다.
// 고정 임계 0.01만 쓰던 기존 판정으로는 배경음이 계속 '발화'라 종료가 영원히 안 잡혔다.
func TestObserverAdaptiveFloorOverBackgroundNoise(t *testing.T) {
	const background = 0.02
	const speech = 0.1

	// 대조군: 고정 임계(0.01)로는 배경음이 발화로 잡혀 종료가 없다.
	if background < DefaultRMSThreshold {
		t.Fatalf("전제 오류: 배경음(%v)이 고정 임계(%v)보다 낮다", background, DefaultRMSThreshold)
	}

	var r record
	o := NewObserver(DefaultBoundarySilence, r.hooks())

	// 배경음만 흐르는 구간. 워밍업 동안에는 고정 임계(0.01)라 배경음이 '발화'로 잡히지만
	// (그게 기존 버그다), 표본이 쌓여 적응 임계로 넘어가는 순간 배경음은 무음으로 재분류되어
	// **발화 종료가 감지된다** — 고정 임계만으로는 영원히 불가능했던 전이다.
	feed(o, background, FloorWarmupFrames+5)
	if !o.Levels().Adaptive {
		t.Fatal("워밍업 후에도 적응 임계로 전환되지 않았다")
	}
	on, off := o.thresholds()
	if !(background < off && speech >= on) {
		t.Fatalf("적응 임계가 배경음/발화를 가르지 못한다: floor=%v on=%v off=%v", o.Levels().Floor, on, off)
	}
	if r.ends != 1 {
		t.Fatalf("배경음 위에서 발화 종료가 감지되지 않았다: ends=%d (고정 임계로는 0)", r.ends)
	}

	startsBefore := len(r.starts)
	feed(o, speech, 10) // 화자 A — 적응 임계(on=%.3f)를 넘겨 발화로 잡힌다.
	if len(r.starts) != startsBefore+1 {
		t.Fatalf("발화 시작이 감지되지 않았다: starts=%d", len(r.starts))
	}
	feed(o, background, 5) // 배경음으로 복귀 = 실무음 500ms
	if r.ends != 2 {
		t.Fatalf("배경음 복귀 시 발화 종료 = %d, want 2", r.ends)
	}
	boundariesBefore := len(r.boundaries)
	feed(o, speech, 3) // 화자 B — 500ms(≥400ms) 뒤 발화 → 경계 후보
	if len(r.boundaries) != boundariesBefore+1 {
		t.Fatalf("경계 후보 = %v, want 직전보다 1건 증가", r.boundaries)
	}
	if got := r.boundaries[len(r.boundaries)-1]; got != 500*time.Millisecond {
		t.Fatalf("보고된 무음 = %v, want 500ms", got)
	}
	if r.last.Floor <= 0 || !r.last.Adaptive {
		t.Fatalf("전이 스냅샷에 플로어가 없다: %+v", r.last)
	}
	if r.last.Floor > 0.03 {
		t.Fatalf("플로어 = %v, want ≈배경음 0.02", r.last.Floor)
	}
}

// (c) 워밍업: 표본이 부족한 초기에는 기존 고정 임계로 폴백한다.
func TestObserverWarmupFallback(t *testing.T) {
	var r record
	o := NewObserver(DefaultBoundarySilence, r.hooks())

	st := o.Levels()
	if st.Adaptive {
		t.Fatal("표본 0인데 적응 상태다")
	}
	if st.OnLevel != DefaultRMSThreshold {
		t.Fatalf("워밍업 on 임계 = %v, want %v(고정)", st.OnLevel, DefaultRMSThreshold)
	}
	// 워밍업 구간에서도 판정 자체는 정상 동작한다(고정 임계 기준).
	feed(o, silent, FloorWarmupFrames-5)
	if o.Levels().Adaptive {
		t.Fatal("표본 부족인데 적응으로 전환됐다")
	}
	feed(o, loud, 3)
	if len(r.starts) != 1 {
		t.Fatalf("워밍업 중 발화 시작 = %d, want 1", len(r.starts))
	}
	// 표본이 충분해지면 적응으로 전환된다.
	feed(o, silent, FloorWarmupFrames)
	if !o.Levels().Adaptive {
		t.Fatal("표본 충분한데 적응으로 전환되지 않았다")
	}
}

// 연속 발화 = 전이/경계 없음. 발화 중에는 플로어를 갱신하지 않아 거짓 종료가 없다.
func TestObserverContinuousSpeechNoBoundary(t *testing.T) {
	var r record
	o := NewObserver(DefaultBoundarySilence, r.hooks())

	feed(o, loud, 100) // 10초 연속 발화

	if r.ends != 0 {
		t.Fatalf("연속 발화 중 offset = %d, want 0", r.ends)
	}
	if len(r.boundaries) != 0 {
		t.Fatalf("연속 발화에 경계 후보가 생겼다: %v", r.boundaries)
	}
	if len(r.starts) != 1 {
		t.Fatalf("onset 횟수 = %d, want 1", len(r.starts))
	}
}

// 임계 0 이하 → 경계 후보 비활성(전이 훅은 유지).
func TestObserverBoundaryDisabled(t *testing.T) {
	var r record
	o := NewObserver(0, r.hooks())

	feed(o, loud, 10)
	feed(o, silent, 20)
	feed(o, loud, 3)

	if len(r.boundaries) != 0 {
		t.Fatalf("비활성인데 경계 후보가 생겼다: %v", r.boundaries)
	}
	if r.ends != 1 || len(r.starts) != 2 {
		t.Fatalf("전이 훅은 유지돼야 한다: ends=%d starts=%d", r.ends, len(r.starts))
	}
}

// 세션 첫 발화는 경계가 아니다(앞선 발화가 없다).
func TestObserverFirstOnsetIsNotBoundary(t *testing.T) {
	var r record
	o := NewObserver(DefaultBoundarySilence, r.hooks())
	feed(o, silent, 30) // 긴 초기 무음
	feed(o, loud, 5)
	if len(r.starts) != 1 {
		t.Fatalf("onset = %d, want 1", len(r.starts))
	}
	if len(r.boundaries) != 0 {
		t.Fatalf("첫 발화가 경계로 잡혔다: %v", r.boundaries)
	}
}

// (d) 게이팅 경로의 여운(750ms)은 그대로다 — 관찰 여운 단축이 전송 계약을 바꾸지 않는다.
func TestGatingHangoverUnchanged(t *testing.T) {
	if DefaultHangoverFrames != 8 {
		t.Fatalf("게이팅 hangover = %d 프레임, want 8(750ms 계약)", DefaultHangoverFrames)
	}
	if ObserveHangoverFrames != 3 {
		t.Fatalf("관찰 hangover = %d 프레임, want 3(300ms)", ObserveHangoverFrames)
	}

	base := &fakeSource{}
	var r record
	wrapped := WrapSourceObserved(base, true, DefaultBoundarySilence, r.hooks())
	var forwarded int
	_ = wrapped.Start(context.Background(), func(audio.Chunk) { forwarded++ })

	for i := 0; i < 5; i++ { // 발화
		base.emit(frame(loud))
	}
	got := forwarded
	forwarded = 0
	if got == 0 {
		t.Fatal("발화가 통과하지 않았다")
	}
	for i := 0; i < 12; i++ { // 무음
		base.emit(frame(silent))
	}
	// 게이팅은 여전히 8프레임(750ms)을 여운으로 흘려보낸다.
	if forwarded != DefaultHangoverFrames {
		t.Fatalf("여운 통과 프레임 = %d, want %d (게이팅 750ms 불변)", forwarded, DefaultHangoverFrames)
	}
	// 반면 관찰은 3프레임에서 이미 종료를 잡았다.
	if r.ends != 1 {
		t.Fatalf("관찰 offset = %d, want 1", r.ends)
	}
}

// (e) 관찰 전용 래핑은 오디오를 변형하지 않는다 — 원본 청크가 그대로(같은 슬라이스) 통과.
func TestWrapSourceObservedPassesAudioUnmodified(t *testing.T) {
	base := &fakeSource{}
	var r record
	wrapped := WrapSourceObserved(base, false, DefaultBoundarySilence, r.hooks())
	if wrapped == audio.Source(base) {
		t.Fatal("관찰 훅이 있으면 래퍼가 필요하다")
	}

	var got []audio.Chunk
	_ = wrapped.Start(context.Background(), func(c audio.Chunk) { got = append(got, c) })

	var sent []audio.Chunk
	emit := func(amp float32, n int) {
		for i := 0; i < n; i++ {
			c := frame(amp)
			sent = append(sent, c)
			base.emit(c)
		}
	}
	emit(silent, 3) // 무음도 통과해야 한다(게이팅 아님).
	emit(loud, 10)
	emit(silent, 6) // offset + 실무음 600ms
	emit(loud, 3)   // 경계 후보

	if len(got) != len(sent) {
		t.Fatalf("통과 청크 수 = %d, want %d (관찰은 오디오를 막지 않는다)", len(got), len(sent))
	}
	for i := range sent {
		if !reflect.DeepEqual([]float32(got[i]), []float32(sent[i])) {
			t.Fatalf("청크 %d 내용이 변형됐다", i)
		}
		if &got[i][0] != &sent[i][0] {
			t.Fatalf("청크 %d 가 복사됐다 — 원본을 그대로 넘겨야 한다(지연/할당 금지)", i)
		}
	}
	if len(r.boundaries) != 1 {
		t.Fatalf("관찰 전용 모드에서 경계 후보 = %v, want 1건", r.boundaries)
	}
}

// 게이팅 + 관찰 겸용: RMS는 한 번만 계산되고(게이트/옵저버 공유) forward는 게이팅 계약대로다.
func TestWrapSourceObservedGatingAndObserving(t *testing.T) {
	base := &fakeSource{}
	var r record
	wrapped := WrapSourceObserved(base, true, DefaultBoundarySilence, r.hooks())

	var received int
	_ = wrapped.Start(context.Background(), func(audio.Chunk) { received++ })
	for i := 0; i < 5; i++ {
		base.emit(frame(silent))
	}
	if received != 0 {
		t.Fatalf("게이팅 on이면 무음은 통과하지 않아야 한다: %d", received)
	}
	for i := 0; i < 10; i++ {
		base.emit(frame(loud))
	}
	if received == 0 {
		t.Fatal("발화는 통과해야 한다")
	}
	if len(r.starts) != 1 {
		t.Fatalf("onset 관찰 = %d, want 1", len(r.starts))
	}
}

// 게이팅도 관찰도 필요 없으면 원본을 그대로 반환한다(RMS 비용 0).
func TestWrapSourceObservedBypass(t *testing.T) {
	base := &fakeSource{}
	if got := WrapSourceObserved(base, false, 0, Hooks{}); got != audio.Source(base) {
		t.Fatal("게이팅/관찰이 모두 없으면 원본 그대로여야 한다")
	}
	if got := WrapSourceObserved(base, false, DefaultBoundarySilence, Hooks{}); got != audio.Source(base) {
		t.Fatal("훅이 없으면 관찰할 이유가 없다 — 원본 그대로여야 한다")
	}
}

// 임계 provider: 설정 변경을 재시작 없이 반영한다.
func TestBoundarySilenceFuncLiveUpdate(t *testing.T) {
	base := &fakeSource{}
	var r record
	threshold := 10 * time.Second // 처음엔 사실상 경계 없음.
	wrapped := WrapSourceObserved(base, false, DefaultBoundarySilence, r.hooks())
	SetBoundarySilenceFunc(wrapped, func() time.Duration { return threshold })

	_ = wrapped.Start(context.Background(), func(audio.Chunk) {})
	emit := func(amp float32, n int) {
		for i := 0; i < n; i++ {
			base.emit(frame(amp))
		}
	}
	emit(loud, 10)
	emit(silent, 20)
	emit(loud, 3)
	if len(r.boundaries) != 0 {
		t.Fatalf("임계 10s인데 경계가 잡혔다: %v", r.boundaries)
	}

	threshold = DefaultBoundarySilence // 런타임 변경.
	emit(silent, 20)
	emit(loud, 3)
	if len(r.boundaries) != 1 {
		t.Fatalf("임계 변경 후 경계 = %v, want 1건", r.boundaries)
	}
}

// 적응 상한: 캡처 시작부터 계속 발화가 이어져 플로어가 발화 레벨로 끌려가도, 임계가
// AdaptiveOnMax에서 멈춰 '아무것도 못 듣는' 상태로 굳지 않는다(거짓 종료 없음).
func TestObserverAdaptiveCeilingPreventsDeafness(t *testing.T) {
	var r record
	o := NewObserver(DefaultBoundarySilence, r.hooks())

	feed(o, loud, 100) // 캡처 시작부터 10초 연속 발화(RMS 0.2)
	on, off := o.thresholds()
	if on > AdaptiveOnMax {
		t.Fatalf("on 임계 = %v, want ≤ 상한 %v", on, AdaptiveOnMax)
	}
	if loud < off {
		t.Fatalf("발화(%v)가 off 임계(%v) 아래로 밀렸다 — 귀먹은 상태", loud, off)
	}
	if r.ends != 0 {
		t.Fatalf("거짓 종료 = %d, want 0", r.ends)
	}

	// 발화가 실제로 끝나면 정상적으로 종료가 잡힌다.
	feed(o, silent, 5)
	if r.ends != 1 {
		t.Fatalf("실제 종료 = %d, want 1", r.ends)
	}
}
