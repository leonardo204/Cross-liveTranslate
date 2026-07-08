package app

// reconciler.go — desired/actual 상태머신 + epoch 펜싱.
//
// 원본 이식: liveTranslate AppState.swift 의 reconciler(§7 불변식).
//   1. 단일 goroutine 직렬 수렴 — 활성 provider ≤1, teardown 무중첩.
//   2. epoch(세대 토큰) 펜싱 — provider 시작 시 epoch 캡처, 이벤트 소비 시 현재 epoch와
//      불일치면 폐기(재연결/전환 중 옛 이벤트 무효화).
//   3. 정지 시 provider/source 완전 정리.
//
// Swift의 @MainActor 직렬화를 Go에서는 "kick 채널 + 단일 run goroutine"으로 옮긴다.
// 사용자 의도(desired)는 mutex로 보호하고 즉시 갱신하며, 실제 전환(start/stop/swap)은
// run goroutine 하나가 직렬로 수렴시킨다(겹침/좀비 없음).

import (
	"context"
	"sync"
	"sync/atomic"

	"cross-livetranslate/internal/audio"
	"cross-livetranslate/internal/pipeline"
)

// Epoch is a generation token. 각 teardown/재생성마다 증가시켜, 지난 세대의 stale
// 이벤트가 새 세대 상태에 섞이지 않도록 이벤트 소비에서 펜싱한다.
type Epoch = uint64

// ProviderConfig is the comparable configuration that decides whether a running
// provider must be hot-swapped. 값이 달라지면 reconciler가 provider만 안전 교체한다
// (캡처는 유지). 원본 AppState.ProviderConfig 등가.
type ProviderConfig struct {
	Model          string
	TargetLanguage string
	SourceLanguage string
	ShowSource     bool
	KeyFingerprint string
	// EmitOutputAudio requests translated output-audio events(번역 음성 재생). 재생이 켜질
	// 때만 true → 서버가 24kHz PCM을 생성/전송하고 gemini client가 OutputAudio 이벤트를 방출한다.
	// 값이 바뀌면 reconciler가 provider를 핫스왑한다(ProviderConfig 비교에 포함).
	EmitOutputAudio bool
}

// Desired is the user intent (최신). SetDesired/SetRunning 등으로 갱신한다.
type Desired struct {
	Running   bool
	Selection audio.Selection
	Provider  ProviderConfig
}

// ProviderFactory builds a pipeline.Provider for a given config (주입 — 테스트에서 fake).
type ProviderFactory func(cfg ProviderConfig) (pipeline.Provider, error)

// SourceFactory builds an audio.Source for a given selection (주입 — 테스트에서 fake).
// 기본 구현은 audio.SelectSource(cgo)를 감싸면 된다.
type SourceFactory func(sel audio.Selection) (audio.Source, error)

// Options configures a Reconciler. NewProvider/NewSource는 필수, OnEvent는 선택.
type Options struct {
	// NewProvider builds a provider from ProviderConfig.
	NewProvider ProviderFactory
	// NewSource builds a capture source from a Selection.
	NewSource SourceFactory
	// OnEvent receives현재 세대 이벤트만(epoch 펜싱 통과). nil이면 이벤트를 버린다.
	// 콜백 안에서 blocking/재진입(SetDesired는 안전, 직접 reconcile 금지)에 유의.
	OnEvent func(pipeline.Event)
}

// providerRef boxes the currently active provider for the send path (atomic swap).
type providerRef struct{ p pipeline.Provider }

// actualState is the running pipeline snapshot. run goroutine 전용(락 불필요).
type actualState struct {
	running   bool
	cfg       ProviderConfig
	sel       audio.Selection
	provider  pipeline.Provider
	source    audio.Source
	srcCancel context.CancelFunc
}

// Reconciler converges the actual pipeline toward the desired intent, serially.
type Reconciler struct {
	newProvider ProviderFactory
	newSource   SourceFactory
	onEvent     func(pipeline.Event)

	mu      sync.Mutex
	desired Desired

	actual actualState // run goroutine 전용

	epoch   atomic.Uint64
	curProv atomic.Pointer[providerRef]

	kick    chan struct{}
	stopped chan struct{}
	wg      sync.WaitGroup // event pump goroutines

	started atomic.Bool
	cancel  context.CancelFunc
}

// New constructs a Reconciler. Start(ctx)로 수렴 루프를 가동한 뒤 SetDesired 등으로
// 의도를 갱신한다.
func New(opts Options) *Reconciler {
	return &Reconciler{
		newProvider: opts.NewProvider,
		newSource:   opts.NewSource,
		onEvent:     opts.OnEvent,
		kick:        make(chan struct{}, 1),
		stopped:     make(chan struct{}),
	}
}

// Start launches the single serial reconcile goroutine. 중복 호출은 무시된다.
// ctx 취소 시 파이프라인을 정리하고 루프를 종료한다.
func (r *Reconciler) Start(ctx context.Context) {
	if !r.started.CompareAndSwap(false, true) {
		return
	}
	rctx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	go r.run(rctx)
}

// Close cancels the loop and blocks until the pipeline is fully torn down
// (run goroutine 종료 + 모든 event pump 종료). Start 전 호출은 무시.
func (r *Reconciler) Close() {
	if !r.started.Load() {
		return
	}
	r.cancel()
	<-r.stopped
	r.wg.Wait()
}

// CurrentEpoch returns the current generation token (진단/테스트).
func (r *Reconciler) CurrentEpoch() Epoch { return r.epoch.Load() }

// SetDesired replaces the whole intent and kicks the reconciler.
func (r *Reconciler) SetDesired(d Desired) {
	r.mu.Lock()
	r.desired = d
	r.mu.Unlock()
	r.notify()
}

// SetRunning updates only the run/stop intent.
func (r *Reconciler) SetRunning(running bool) {
	r.mu.Lock()
	r.desired.Running = running
	r.mu.Unlock()
	r.notify()
}

// SetSelection updates only the input-source selection (실행 중이면 소스 재시작).
func (r *Reconciler) SetSelection(sel audio.Selection) {
	r.mu.Lock()
	r.desired.Selection = sel
	r.mu.Unlock()
	r.notify()
}

// SetProviderConfig updates only the provider config (실행 중이면 핫스왑).
func (r *Reconciler) SetProviderConfig(cfg ProviderConfig) {
	r.mu.Lock()
	r.desired.Provider = cfg
	r.mu.Unlock()
	r.notify()
}

// notify signals the reconcile loop (버퍼 1 논블로킹 — 누락 없이 수렴).
func (r *Reconciler) notify() {
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

// run is the single serial reconcile goroutine.
func (r *Reconciler) run(ctx context.Context) {
	defer close(r.stopped)
	for {
		select {
		case <-ctx.Done():
			r.performStop() // 최종 정리(멱등)
			return
		case <-r.kick:
			r.reconcile(ctx)
		}
	}
}

// reconcile converges actual toward desired, one transition at a time,
// until no further transition is required. 단일 goroutine이라 teardown 무중첩.
func (r *Reconciler) reconcile(ctx context.Context) {
	for {
		r.mu.Lock()
		d := r.desired
		r.mu.Unlock()

		switch {
		case d.Running && !r.actual.running:
			r.performStart(ctx, d)
		case !d.Running && r.actual.running:
			r.performStop()
		case d.Running && r.actual.running && d.Selection != r.actual.sel:
			// 입력 소스 변경 → 소스 교체 필요. 전체 정지 후 다음 반복이 새 소스로 재시작.
			r.performStop()
		case d.Running && r.actual.running && d.Provider != r.actual.cfg:
			// 구성만 변경 → 캡처 유지한 채 provider만 핫스왑(무중첩).
			r.performSwap(ctx, d)
		default:
			return
		}
	}
}

// performStart brings up a fresh provider + source. 실패 시 의도를 정지로 되돌려
// 안전 수렴한다. 시작 시 epoch를 증가시켜 새 세대 토큰을 만든다.
func (r *Reconciler) performStart(ctx context.Context, d Desired) {
	myEpoch := r.epoch.Add(1)

	prov, err := r.newProvider(d.Provider)
	if err != nil {
		r.emitStartFailure(err)
		r.forceStopIntent()
		return
	}
	src, err := r.newSource(d.Selection)
	if err != nil {
		_ = prov.Stop()
		r.emitStartFailure(err)
		r.forceStopIntent()
		return
	}
	events, err := prov.Start(ctx)
	if err != nil {
		_ = prov.Stop()
		r.emitStartFailure(err)
		r.forceStopIntent()
		return
	}
	r.pump(myEpoch, events)

	// 이 시점부터 캡처 청크가 새 provider로 흐른다(atomic 발행).
	r.curProv.Store(&providerRef{p: prov})

	srcCtx, srcCancel := context.WithCancel(ctx)
	if err := src.Start(srcCtx, r.dispatchChunk); err != nil {
		srcCancel()
		r.curProv.Store(nil)
		_ = prov.Stop()
		r.emitStartFailure(err)
		r.forceStopIntent()
		return
	}

	r.actual = actualState{
		running:   true,
		cfg:       d.Provider,
		sel:       d.Selection,
		provider:  prov,
		source:    src,
		srcCancel: srcCancel,
	}
}

// performStop tears down the current pipeline. epoch를 증가시켜 이 세대의 잔여
// 이벤트를 폐기(fence)한다. 멱등(정지 상태에서 호출해도 무해).
func (r *Reconciler) performStop() {
	r.epoch.Add(1) // 진행 중 pump가 stale로 버리도록 세대 전진
	a := r.actual
	r.curProv.Store(nil)
	if a.source != nil {
		_ = a.source.Stop()
	}
	if a.srcCancel != nil {
		a.srcCancel()
	}
	if a.provider != nil {
		_ = a.provider.Stop()
	}
	r.actual = actualState{}
}

// performSwap replaces the provider while keeping the capture source running
// (핫스왑 — 입력 오디오는 엔진 무관 공유 자원이라 끊지 않는다). 이전 provider.Stop()을
// 끝까지 대기한 뒤(무중첩) 새 구성으로 재시작한다.
func (r *Reconciler) performSwap(ctx context.Context, d Desired) {
	myEpoch := r.epoch.Add(1) // 이전 provider 이벤트 fence + 새 세대 토큰

	r.curProv.Store(nil) // 이전 provider로의 send 중단
	if old := r.actual.provider; old != nil {
		_ = old.Stop() // 완전 정지까지 대기(무중첩)
	}
	r.actual.provider = nil

	prov, err := r.newProvider(d.Provider)
	if err != nil {
		// provider 없이 캡처만 남는다 → 의도를 정지로 돌려 다음 반복이 performStop으로 수렴.
		r.forceStopIntent()
		return
	}
	events, err := prov.Start(ctx)
	if err != nil {
		_ = prov.Stop()
		r.forceStopIntent()
		return
	}
	r.pump(myEpoch, events)
	r.curProv.Store(&providerRef{p: prov})
	r.actual.provider = prov
	r.actual.cfg = d.Provider
}

// dispatchChunk forwards a captured chunk to the currently active provider.
// 실시간 오디오 goroutine에서 호출된다(atomic 로드 — 락 없음). 전환 중 nil이면 드롭.
func (r *Reconciler) dispatchChunk(c audio.Chunk) {
	if ref := r.curProv.Load(); ref != nil {
		_ = ref.p.Send(c)
	}
}

// pump drains a provider's event stream, discarding events from superseded
// generations (epoch 펜싱). 채널이 닫히면(provider.Stop) 종료한다.
func (r *Reconciler) pump(myEpoch Epoch, events <-chan pipeline.Event) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		for ev := range events {
			if r.epoch.Load() != myEpoch {
				continue // 지난 세대 이벤트 폐기
			}
			if r.onEvent != nil {
				r.onEvent(ev)
			}
		}
	}()
}

// forceStopIntent flips the desired intent to stopped (시작/스왑 실패 시 안전 수렴).
// reconcile 루프가 다음 반복에서 정리한다(같은 goroutine).
func (r *Reconciler) forceStopIntent() {
	r.mu.Lock()
	r.desired.Running = false
	r.mu.Unlock()
}

// emitStartFailure surfaces a start-time failure(프로바이더/소스 생성·시작 실패)를
// PermanentFailure 이벤트로 방출한다. 이것이 없으면 실패 시 조용히 정지 의도로만 되돌아가
// 제어 HUD가 "연결 중…"에 영구히 멈춘다(원인 불명). 이제 controller가 이 이벤트를 로그로
// 남기고 상태를 "오류"로 표시한다. run goroutine 단독 호출(pump와 동일 goroutine).
func (r *Reconciler) emitStartFailure(err error) {
	if r.onEvent != nil {
		r.onEvent(pipeline.Event{Kind: pipeline.PermanentFailure, Err: err})
	}
}
