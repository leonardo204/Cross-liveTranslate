package app

// reconciler_test.go — fake Provider/Source로 reconciler 불변식을 검증한다.
//   (a) 전환(핫스왑) 시 이전 provider teardown 정확히 1회 + 캡처 소스 유지.
//   (b) 활성 provider 동시 ≤1.
//   (c) 지난 epoch(세대) 이벤트 폐기(stale 펜싱).
//   (d) 정지 시 provider/source 완전 정리 + 이후 청크 드롭.
//   (e) 캡처 청크가 현재 provider.Send로 배선.
//   (f) 입력 소스 변경 시 소스 재시작.
// `go test -race`로 동시성 안전성까지 확인한다.

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cross-livetranslate/internal/audio"
	"cross-livetranslate/internal/pipeline"
)

// liveCounter tracks current + max-concurrent live providers.
type liveCounter struct {
	n   atomic.Int64
	max atomic.Int64
}

func (l *liveCounter) inc() {
	v := l.n.Add(1)
	for {
		m := l.max.Load()
		if v <= m || l.max.CompareAndSwap(m, v) {
			return
		}
	}
}
func (l *liveCounter) dec() { l.n.Add(-1) }

type fakeProvider struct {
	events    chan pipeline.Event
	sends     atomic.Int64
	stops     atomic.Int64
	started   atomic.Bool
	startedCh chan struct{}
	live      *liveCounter
	closeOnce sync.Once
}

func newFakeProvider(live *liveCounter) *fakeProvider {
	return &fakeProvider{
		events:    make(chan pipeline.Event), // 언버퍼드: send가 pump 수신과 동기화(펜싱 테스트 결정적).
		startedCh: make(chan struct{}),
		live:      live,
	}
}

func (f *fakeProvider) Start(context.Context) (<-chan pipeline.Event, error) {
	f.started.Store(true)
	f.live.inc()
	close(f.startedCh)
	return f.events, nil
}
func (f *fakeProvider) Send(audio.Chunk) error { f.sends.Add(1); return nil }
func (f *fakeProvider) Stop() error {
	f.stops.Add(1)
	if f.started.Load() {
		f.live.dec()
	}
	f.closeOnce.Do(func() { close(f.events) })
	return nil
}

type fakeSource struct {
	stops     atomic.Int64
	startErr  error
	startedCh chan struct{}
	mu        sync.Mutex
	onChunk   func(audio.Chunk)
	closeOnce sync.Once
}

func newFakeSource() *fakeSource { return &fakeSource{startedCh: make(chan struct{})} }

func (s *fakeSource) Start(_ context.Context, onChunk func(audio.Chunk)) error {
	if s.startErr != nil {
		return s.startErr
	}
	s.mu.Lock()
	s.onChunk = onChunk
	s.mu.Unlock()
	s.closeOnce.Do(func() { close(s.startedCh) })
	return nil
}
func (s *fakeSource) Stop() error { s.stops.Add(1); return nil }
func (s *fakeSource) emit(c audio.Chunk) {
	s.mu.Lock()
	cb := s.onChunk
	s.mu.Unlock()
	if cb != nil {
		cb(c)
	}
}

// --- helpers ---

func waitClosed(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for channel close")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if cond() {
			return
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for condition")
		case <-time.After(2 * time.Millisecond):
		}
	}
}

func recvEvent(t *testing.T, ch <-chan pipeline.Event) pipeline.Event {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for event")
		return pipeline.Event{}
	}
}

// seqProviders hands out prebuilt providers in order (핫스왑 시퀀스 제어).
func seqProviders(ps ...*fakeProvider) (ProviderFactory, *int) {
	var mu sync.Mutex
	idx := 0
	f := func(ProviderConfig) (pipeline.Provider, error) {
		mu.Lock()
		defer mu.Unlock()
		p := ps[idx]
		idx++
		return p, nil
	}
	return f, &idx
}

func seqSources(ss ...*fakeSource) SourceFactory {
	var mu sync.Mutex
	idx := 0
	return func(audio.Selection) (audio.Source, error) {
		mu.Lock()
		defer mu.Unlock()
		s := ss[idx]
		idx++
		return s, nil
	}
}

// (a)+(b): 핫스왑 시 이전 provider 정확히 1회 teardown, 캡처 소스 유지, 동시 활성 ≤1.
func TestReconciler_SwapTearsDownPreviousOnceKeepsSource(t *testing.T) {
	live := &liveCounter{}
	pA, pB := newFakeProvider(live), newFakeProvider(live)
	src := newFakeSource()
	provFactory, _ := seqProviders(pA, pB)

	r := New(Options{
		NewProvider: provFactory,
		NewSource:   func(audio.Selection) (audio.Source, error) { return src, nil },
	})
	r.Start(context.Background())
	defer r.Close()

	r.SetDesired(Desired{Running: true, Provider: ProviderConfig{Model: "a"}})
	waitClosed(t, pA.startedCh)
	waitClosed(t, src.startedCh)

	// 구성만 변경 → 핫스왑(캡처 유지).
	r.SetProviderConfig(ProviderConfig{Model: "b"})
	waitClosed(t, pB.startedCh)

	waitFor(t, func() bool { return pA.stops.Load() == 1 })
	if got := pA.stops.Load(); got != 1 {
		t.Fatalf("이전 provider teardown 횟수 = %d, want 1", got)
	}
	if got := src.stops.Load(); got != 0 {
		t.Fatalf("핫스왑 중 캡처 소스가 정지됨: stops=%d, want 0", got)
	}
	if got := live.max.Load(); got > 1 {
		t.Fatalf("동시 활성 provider = %d, want <=1", got)
	}
	waitFor(t, func() bool { return live.n.Load() == 1 })
}

// (c): 지난 epoch 이벤트 폐기(stale 펜싱).
func TestReconciler_DiscardsStaleEpochEvents(t *testing.T) {
	live := &liveCounter{}
	p := newFakeProvider(live)
	src := newFakeSource()
	delivered := make(chan pipeline.Event, 8)

	r := New(Options{
		NewProvider: func(ProviderConfig) (pipeline.Provider, error) { return p, nil },
		NewSource:   func(audio.Selection) (audio.Source, error) { return src, nil },
		OnEvent:     func(ev pipeline.Event) { delivered <- ev },
	})
	r.Start(context.Background())
	defer r.Close()

	r.SetRunning(true)
	waitClosed(t, p.startedCh)

	// 현재 세대 이벤트 → 전달됨.
	p.events <- pipeline.Event{Kind: pipeline.TranslatedDelta, Text: "live"}
	if got := recvEvent(t, delivered); got.Text != "live" {
		t.Fatalf("현재 세대 이벤트 Text=%q, want live", got.Text)
	}

	// 세대 전진(전환 시뮬레이션). 이후 이 provider 이벤트는 stale → 폐기.
	r.epoch.Add(1)
	p.events <- pipeline.Event{Kind: pipeline.TranslatedDelta, Text: "stale"}
	// 언버퍼드 send 반환 = pump가 수신함. 폐기라 delivered로 오지 않아야 한다.
	select {
	case ev := <-delivered:
		t.Fatalf("stale 이벤트가 전달됨: %q", ev.Text)
	case <-time.After(150 * time.Millisecond):
		// ok — 폐기됨
	}
}

// (d)+(e): 정지 시 완전 정리 + 청크 배선/드롭.
func TestReconciler_StopCleansUpAndChunkWiring(t *testing.T) {
	live := &liveCounter{}
	p := newFakeProvider(live)
	src := newFakeSource()

	r := New(Options{
		NewProvider: func(ProviderConfig) (pipeline.Provider, error) { return p, nil },
		NewSource:   func(audio.Selection) (audio.Source, error) { return src, nil },
	})
	r.Start(context.Background())
	defer r.Close()

	r.SetRunning(true)
	waitClosed(t, p.startedCh)
	waitClosed(t, src.startedCh)

	// (e) 캡처 청크가 현재 provider로 배선된다.
	src.emit(make(audio.Chunk, audio.ChunkSamples))
	waitFor(t, func() bool { return p.sends.Load() >= 1 })

	// (d) 정지 → provider/source 완전 정리.
	r.SetRunning(false)
	waitFor(t, func() bool { return src.stops.Load() == 1 && live.n.Load() == 0 })
	if p.stops.Load() < 1 {
		t.Fatal("정지 시 provider가 정리되지 않음")
	}

	// 정지 후 청크는 드롭(현재 provider 없음).
	before := p.sends.Load()
	src.emit(make(audio.Chunk, audio.ChunkSamples))
	time.Sleep(20 * time.Millisecond)
	if got := p.sends.Load(); got != before {
		t.Fatalf("정지 후 청크가 provider로 전달됨: sends %d→%d", before, got)
	}
}

// (f): 입력 소스 변경 시 소스 재시작(이전 소스 정지 + 새 소스 시작).
func TestReconciler_SelectionChangeRestartsSource(t *testing.T) {
	live := &liveCounter{}
	pA, pB := newFakeProvider(live), newFakeProvider(live)
	sA, sB := newFakeSource(), newFakeSource()
	provFactory, _ := seqProviders(pA, pB)
	srcFactory := seqSources(sA, sB)

	r := New(Options{NewProvider: provFactory, NewSource: srcFactory})
	r.Start(context.Background())
	defer r.Close()

	r.SetDesired(Desired{Running: true, Selection: audio.Selection{Mode: audio.SelectMic}})
	waitClosed(t, pA.startedCh)
	waitClosed(t, sA.startedCh)

	// 입력 소스 변경 → 전체 재시작(소스 교체).
	r.SetSelection(audio.Selection{Mode: audio.SelectLoopback})
	waitClosed(t, sB.startedCh)
	waitClosed(t, pB.startedCh)

	waitFor(t, func() bool { return sA.stops.Load() == 1 })
	if got := live.max.Load(); got > 1 {
		t.Fatalf("동시 활성 provider = %d, want <=1", got)
	}
}

// 빠른 연타(start/stop 반복)에도 좀비/중복 없이 수렴하는지(-race).
func TestReconciler_RapidToggleConverges(t *testing.T) {
	live := &liveCounter{}
	var mu sync.Mutex
	var provs []*fakeProvider
	var srcs []*fakeSource

	r := New(Options{
		NewProvider: func(ProviderConfig) (pipeline.Provider, error) {
			mu.Lock()
			defer mu.Unlock()
			p := newFakeProvider(live)
			provs = append(provs, p)
			return p, nil
		},
		NewSource: func(audio.Selection) (audio.Source, error) {
			mu.Lock()
			defer mu.Unlock()
			s := newFakeSource()
			srcs = append(srcs, s)
			return s, nil
		},
	})
	r.Start(context.Background())

	for i := 0; i < 20; i++ {
		r.SetRunning(i%2 == 0)
	}
	// 최종 의도: 정지(마지막 i=19 → false).
	r.SetRunning(false)
	// 수렴 대기: 활성 provider 0.
	waitFor(t, func() bool { return live.n.Load() == 0 })

	r.Close()
	if got := live.n.Load(); got != 0 {
		t.Fatalf("종료 후 활성 provider = %d, want 0", got)
	}
	if got := live.max.Load(); got > 1 {
		t.Fatalf("동시 활성 provider = %d, want <=1", got)
	}
}
