package vad

// observer.go — 오디오 도메인 화자 경계 관찰(observe-only).
//
// 왜 필요한가:
//
//	자막 엔진의 "델타 갭" 기반 턴 경계는 구조적 한계가 있다. (a) 원문/번역 델타가 인터리빙
//	되면서 서로 타이머를 리셋해 실제 발화 경계가 갭으로 드러나지 않고, (b) 서버 스트리밍
//	주기 자체가 세션마다 0.8~1.3s로 변동해 임계와 겹친다. 그래서 텍스트 도착 간격 대신
//	**실제 오디오 무음**을 직접 재서 경계를 잡는다.
//
// 왜 게이트와 판정을 분리하는가(실측 근거):
//
//	3.5분 인터뷰 세션에서 전이가 onset/offset 각 4회뿐이었다(48s·79s 구간을 연속 발화로 인식).
//	원인은 두 가지였다.
//	  (a) 게이트 여운(hangover) 750ms — 대화 중 화자 교대 갭은 보통 200~600ms라 여운 안에
//	      묻혀 '발화 종료'가 아예 관측되지 않는다. → 관찰 전용 여운을 300ms로 짧게 가져간다.
//	      **게이팅 경로의 750ms는 첫/끝 음절 보존 계약이라 그대로 둔다.**
//	  (b) 고정 임계 0.01 — 룸톤·배경음악이 그 위에 있으면 영원히 '발화 중'이다.
//	      → 최근 무음 프레임들의 하위 백분위를 노이즈 플로어로 추적해 임계를 적응시킨다.
//	      **게이팅은 전송량 계약이 바뀌지 않도록 고정 임계를 유지하고, 관찰만 적응형이다.**
//
// 계약:
//   - 관찰은 오디오를 절대 막거나 변형하지 않는다(gatedSource가 원본 청크를 그대로 흘린다).
//   - 클라이언트 VAD 게이팅 on/off와 무관하게 캡처가 도는 동안 항상 관찰한다(서버 VAD 모드 포함).
//   - 시간은 청크 샘플 수로만 환산한다(16kHz 1600샘플=100ms). time.Now()를 쓰지 않아
//     테스트에서 완전히 결정적이다. RMS는 청크당 1회만 계산해 게이트와 공유한다.

import (
	"time"

	"cross-livetranslate/internal/audio"
)

// 관찰 파라미터. 프레임 = 100ms(audio.ChunkSamples).
const (
	// DefaultBoundarySilence 는 화자 경계 후보로 볼 최소 실무음 지속시간(기본 400ms).
	// 실측상 대화 중 화자 교대 갭은 200~600ms에 분포한다 — 1.4s 같은 긴 쉼만 잡던 700ms에서
	// 낮춰 실제 교대를 잡는다(한 화자의 문장 내 호흡 200~300ms는 걸러진다).
	DefaultBoundarySilence = 400 * time.Millisecond
	// ObserveHangoverFrames 는 관찰 전용 발화 종료 여운(3프레임=300ms).
	// 게이팅 여운(DefaultHangoverFrames=8, 750ms)과 분리된 값이다.
	ObserveHangoverFrames = 3
	// ObserveOnsetFrames 는 발화 시작 확정에 필요한 연속 고에너지 프레임 수(2=200ms).
	ObserveOnsetFrames = 2
	// FloorWindowFrames 는 노이즈 플로어 추정에 쓰는 최근 무음 프레임 수(50=5s).
	FloorWindowFrames = 50
	// FloorWarmupFrames 는 적응 임계로 전환하기 전 필요한 최소 표본 수(20=2s).
	// 그 전에는 기존 고정 임계(DefaultRMSThreshold)로 폴백한다.
	FloorWarmupFrames = 20
	// FloorRecalibrateFrames 는 발화가 이만큼 연속되면 플로어 표본을 다시 받는 재보정 임계
	// (300프레임=30s). 환경 소음이 바뀐 뒤 플로어가 낡은 값으로 굳는 것을 막는다.
	FloorRecalibrateFrames = 300
	// FloorPercentile 은 노이즈 플로어로 쓸 하위 백분위(0.2 = 20%).
	FloorPercentile = 0.2
	// FloorOnMultiplier 는 발화 시작 임계 배수(플로어 × 3).
	FloorOnMultiplier = 3.0
	// FloorOffMultiplier 는 발화 종료 임계 배수(플로어 × 2) — 히스테리시스.
	FloorOffMultiplier = 2.0
	// AbsoluteFloorMin 은 적응 임계의 절대 하한(조용한 소스 대비, 기존 0.01보다 낮다).
	AbsoluteFloorMin = 0.004
	// AdaptiveOnMax 는 적응 발화 시작 임계의 **상한**이다. 플로어 추정이 발화 레벨까지
	// 끌려 올라가면(예: 캡처 시작 시점부터 누가 계속 말하는 중이라 워밍업 표본이 전부 발화)
	// 임계가 발화보다 높아져 아무것도 못 듣는 상태로 굳을 수 있다. 이 상한이 그 최악을 막는다:
	// 임계가 여기서 멈추므로 발화는 계속 '발화'로 유지되고(=기존 동작), 잘못된 종료가 없다.
	// 룸톤·배경음악(RMS 0.02~0.03)까지는 상한에 걸리지 않고 적응된다.
	AdaptiveOnMax = 0.09
)

// Hooks 는 옵저버가 알리는 발화 전이/경계 콜백이다. nil 필드는 호출되지 않는다.
// 오디오 dispatch goroutine에서 동기 호출되므로 훅은 반드시 가벼워야 한다(로깅/atomic 정도).
type Hooks struct {
	// OnSpeechStart 는 발화 시작(onset) 시 직전 무음 지속시간 + 판정 상태와 함께 호출된다.
	OnSpeechStart func(silence time.Duration, st Levels)
	// OnSpeechEnd 는 발화 종료(관찰 여운 300ms 소진) 시 호출된다.
	OnSpeechEnd func(st Levels)
	// OnBoundary 는 임계 이상의 무음 뒤에 새 발화가 시작된 경우(화자 경계 후보) 호출된다.
	// OnSpeechStart 직후에 온다.
	OnBoundary func(silence time.Duration, st Levels)
}

// any 는 훅이 하나라도 설정돼 있는지 보고한다.
func (h Hooks) any() bool {
	return h.OnSpeechStart != nil || h.OnSpeechEnd != nil || h.OnBoundary != nil
}

// Levels 는 전이 시점의 적응 판정 상태다(로그용 스냅샷 — 매 프레임이 아니라 전이/경계에서만
// 기록해 로그 폭주를 막는다).
type Levels struct {
	RMS      float32 // 그 프레임의 RMS.
	Floor    float32 // 추정된 노이즈 플로어(워밍업 중이면 0).
	OnLevel  float32 // 현재 발화 시작 임계.
	OffLevel float32 // 현재 발화 종료 임계.
	Adaptive bool    // false면 워밍업 폴백(고정 임계) 상태.
}

// Observer 는 프레임 RMS로부터 발화 전이와 화자 경계 후보를 잡아내는 순수 상태머신이다.
// 단일 goroutine(오디오 dispatch)에서 직렬 호출된다 — 내부 상태에 락이 없다.
type Observer struct {
	// BoundarySilence 는 경계 판정 임계다. 0 이하면 경계 후보를 내지 않는다(전이 훅은 유지).
	// 호출자가 런타임에 갱신할 수 있다(설정 변경 반영).
	BoundarySilence time.Duration

	hooks Hooks
	floor noiseFloor

	speaking  bool // 관찰 기준 발화 상태(게이팅 상태와 별개).
	hadSpeech bool // 한 번이라도 발화가 끝난 적이 있는지(첫 onset은 경계가 아니다).
	speechRun int  // 연속 고에너지 프레임 수(onset 확정 카운터).
	hangover  int  // 남은 관찰 여운 프레임 수.
	// speakingFrames 는 현재 발화가 몇 프레임째 이어지는지(플로어 재보정 판단용).
	speakingFrames int

	quiet     time.Duration // 진행 중인 연속 무음 길이.
	lastQuiet time.Duration // 직전 무음 구간의 총 길이(onset 시 보고).
}

// NewObserver returns an observer with the given boundary threshold and hooks.
func NewObserver(boundarySilence time.Duration, hooks Hooks) *Observer {
	return &Observer{BoundarySilence: boundarySilence, hooks: hooks}
}

// Speaking 은 현재(관찰 기준) 발화 상태를 반환한다.
func (o *Observer) Speaking() bool { return o.speaking }

// Silence 는 진행 중인 연속 무음 길이를 반환한다(발화 중이면 0).
func (o *Observer) Silence() time.Duration { return o.quiet }

// Levels 는 현재 판정 임계 스냅샷을 반환한다(테스트/로그용).
func (o *Observer) Levels() Levels { return o.levels(0) }

// Observe 는 청크 1개를 반영한다. samples는 샘플 수(16kHz 기준 1600=100ms), level은 그
// 청크의 RMS다(게이트와 공유해 중복 계산하지 않는다). 오디오 자체는 건드리지 않는다.
func (o *Observer) Observe(samples int, level float32) {
	dur := chunkDuration(samples)
	on, off := o.thresholds()

	// 노이즈 플로어 표본 수집 규칙:
	//   - 평시: **비발화 구간에서만** 갱신한다. 발화가 길게 이어질 때 플로어가 발화 레벨로
	//     끌려 올라가 거짓 종료를 만드는 것을 막는다.
	//   - 워밍업(!ready): 모든 프레임을 넣는다. 배경음이 고정 임계(0.01) 위에 있으면 초기
	//     판정이 계속 '발화'라 비발화 표본이 영영 안 모이는 교착이 생기기 때문이다
	//     (=이번에 고치려는 바로 그 상황). 하위 백분위라 발화가 섞여도 조용한 쪽으로 수렴한다.
	//   - 재보정: 발화가 비정상적으로 길게(30s+) 이어지면 환경 소음이 변했을 수 있으므로
	//     표본을 다시 받아 플로어가 낡은 채로 굳지 않게 한다.
	if !o.speaking || !o.floor.ready() || o.speakingFrames > FloorRecalibrateFrames {
		o.floor.push(level)
	}
	if o.speaking {
		o.speakingFrames++
	} else {
		o.speakingFrames = 0
	}

	// 무음 누적: off 임계 미만이면 무음, on 임계 이상이면 무음 구간 종료.
	if level < off {
		o.quiet += dur
	} else if level >= on && o.quiet > 0 {
		o.lastQuiet = o.quiet // onset 때 보고할 직전 무음 길이.
		o.quiet = 0
	}

	if o.speaking {
		if level >= off {
			o.hangover = ObserveHangoverFrames // 발화 지속 — 여운 리셋.
		} else {
			o.hangover--
			if o.hangover <= 0 {
				// 발화 종료(관찰 기준). 실무음 측정은 조용해진 첫 프레임부터 누적돼 있다.
				o.speaking = false
				o.hadSpeech = true
				o.speechRun = 0
				if o.hooks.OnSpeechEnd != nil {
					o.hooks.OnSpeechEnd(o.levels(level))
				}
			}
		}
		return
	}

	// 미발화 상태 — 연속 고에너지 프레임이 쌓이면 발화 시작.
	if level < on {
		o.speechRun = 0
		return
	}
	o.speechRun++
	if o.speechRun < ObserveOnsetFrames {
		return
	}
	o.speaking = true
	o.speechRun = 0
	o.hangover = ObserveHangoverFrames

	silence := o.lastQuiet
	o.lastQuiet = 0
	if o.hooks.OnSpeechStart != nil {
		o.hooks.OnSpeechStart(silence, o.levels(level))
	}
	// 세션 첫 발화(hadSpeech=false)는 앞선 발화가 없으므로 경계가 아니다.
	if o.hadSpeech && o.BoundarySilence > 0 && silence >= o.BoundarySilence &&
		o.hooks.OnBoundary != nil {
		o.hooks.OnBoundary(silence, o.levels(level))
	}
}

// thresholds 는 현재 발화 시작/종료 임계를 반환한다.
// 워밍업(플로어 표본 부족)에는 기존 고정 임계(0.01)로 폴백하고, 적응 구간에서는
// max(절대 하한, 플로어×3)을 on 임계로, 그 2/3를 off 임계로 쓴다(항상 히스테리시스 유지).
func (o *Observer) thresholds() (on, off float32) {
	if !o.floor.ready() {
		return DefaultRMSThreshold, DefaultRMSThreshold * FloorOffMultiplier / FloorOnMultiplier
	}
	f := o.floor.estimate()
	on = f * FloorOnMultiplier
	if on < AbsoluteFloorMin {
		on = AbsoluteFloorMin
	}
	if on > AdaptiveOnMax {
		on = AdaptiveOnMax // 발화 레벨까지 끌려 올라간 플로어로 귀먹는 것 방지.
	}
	return on, on * FloorOffMultiplier / FloorOnMultiplier
}

// levels 는 로그용 스냅샷을 만든다.
func (o *Observer) levels(level float32) Levels {
	on, off := o.thresholds()
	st := Levels{RMS: level, OnLevel: on, OffLevel: off, Adaptive: o.floor.ready()}
	if st.Adaptive {
		st.Floor = o.floor.estimate()
	}
	return st
}

// chunkDuration 은 샘플 수를 오디오 계약(16kHz)에 따라 시간으로 환산한다.
// 1600샘플 → 정확히 100ms. 부분 청크(마지막 조각)도 비례 계산된다.
func chunkDuration(samples int) time.Duration {
	if samples <= 0 {
		return 0
	}
	return time.Duration(samples) * time.Second / time.Duration(audio.SampleRate)
}

// -----------------------------------------------------------------------------
// 노이즈 플로어 추정 (링버퍼 + quickselect 백분위)
// -----------------------------------------------------------------------------

// noiseFloor 는 최근 FloorWindowFrames개 비발화 프레임 RMS의 하위 백분위를 추정한다.
// 링버퍼에 값을 덮어쓰고(할당 없음), 추정은 quickselect로 평균 O(n)이다(정렬 없음).
type noiseFloor struct {
	buf   [FloorWindowFrames]float32
	next  int
	count int // 채워진 표본 수(상한 FloorWindowFrames).

	scratch [FloorWindowFrames]float32 // quickselect 작업용(할당 회피).
	cache   float32
	cacheOK bool
}

func (n *noiseFloor) push(v float32) {
	n.buf[n.next] = v
	n.next = (n.next + 1) % FloorWindowFrames
	if n.count < FloorWindowFrames {
		n.count++
	}
	n.cacheOK = false
}

// ready 는 적응 임계로 전환할 만큼 표본이 쌓였는지 보고한다.
func (n *noiseFloor) ready() bool { return n.count >= FloorWarmupFrames }

// estimate 는 하위 백분위(FloorPercentile) 값을 반환한다. 같은 표본에 대해 재계산하지 않는다.
func (n *noiseFloor) estimate() float32 {
	if n.count == 0 {
		return 0
	}
	if n.cacheOK {
		return n.cache
	}
	copy(n.scratch[:n.count], n.buf[:n.count])
	// 하위 백분위 인덱스(0-based). count-1 기준이라 표본 50·20%면 9번째(=하위 10개 중 마지막).
	k := int(float64(n.count-1) * FloorPercentile)
	if k >= n.count {
		k = n.count - 1
	}
	if k < 0 {
		k = 0
	}
	n.cache = quickSelect(n.scratch[:n.count], k)
	n.cacheOK = true
	return n.cache
}

// quickSelect 는 s를 부분 정렬해 k번째(0-based) 작은 값을 반환한다(평균 O(n)).
// 피벗은 중앙 원소 고정 — 난수 없이 결정적이다.
func quickSelect(s []float32, k int) float32 {
	lo, hi := 0, len(s)-1
	for lo < hi {
		p := partition(s, lo, hi)
		switch {
		case k == p:
			return s[p]
		case k < p:
			hi = p - 1
		default:
			lo = p + 1
		}
	}
	return s[lo]
}

// partition 은 Lomuto 분할(피벗=중앙 원소)로 s[lo:hi]를 나누고 피벗 최종 위치를 반환한다.
func partition(s []float32, lo, hi int) int {
	mid := lo + (hi-lo)/2
	s[mid], s[hi] = s[hi], s[mid]
	pivot := s[hi]
	i := lo
	for j := lo; j < hi; j++ {
		if s[j] < pivot {
			s[i], s[j] = s[j], s[i]
			i++
		}
	}
	s[i], s[hi] = s[hi], s[i]
	return i
}
