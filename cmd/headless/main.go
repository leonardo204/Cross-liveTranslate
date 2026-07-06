// Command headless — P1 수직 슬라이스 진입점: 마이크 → Gemini Live → 콘솔 자막.
//
// 사용법:
//
//	go run ./cmd/headless -target ko [-source en] [-show-source] [-duration 60s]
//
// 키 조회(config.APIKey) → gemini Provider.Start → audio.Source.Start(onChunk→Send)
// → 이벤트 루프에서 원문/번역 delta를 콘솔에 출력. Ctrl-C 또는 -duration 만료 시 정리.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sync/atomic"
	"time"

	"cross-livetranslate/internal/audio"
	"cross-livetranslate/internal/config"
	"cross-livetranslate/internal/gemini"
	"cross-livetranslate/internal/pipeline"
)

func main() {
	target := flag.String("target", config.DefaultTargetLanguage, "번역 대상 언어(BCP-47), 예: ko, ja, en")
	source := flag.String("source", config.DefaultSourceLanguage, "원문 언어(BCP-47) 또는 auto(서버 자동 감지)")
	showSource := flag.Bool("show-source", false, "원문 전사도 함께 표시(inputAudioTranscription 요청)")
	duration := flag.Duration("duration", 0, "실행 시간(예: 60s). 0이면 Ctrl-C까지 무한 실행")
	flag.Parse()

	if err := run(*target, *source, *showSource, *duration); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(target, source string, showSource bool, duration time.Duration) error {
	apiKey, err := config.APIKey()
	if err != nil {
		return err
	}

	// Ctrl-C + 선택적 duration 타임아웃으로 컨텍스트 구성.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if duration > 0 {
		var tcancel context.CancelFunc
		ctx, tcancel = context.WithTimeout(ctx, duration)
		defer tcancel()
	}

	provider := gemini.NewProvider(gemini.Config{
		APIKey:                    apiKey,
		Model:                     config.GeminiModel,
		TargetLanguage:            target,
		SourceLanguage:            source,
		RequestInputTranscription: showSource,
	})

	events, err := provider.Start(ctx)
	if err != nil {
		return fmt.Errorf("provider 시작 실패: %w", err)
	}
	defer provider.Stop()

	fmt.Fprintf(os.Stderr, "[headless] model=%s target=%s source=%s show-source=%v\n",
		config.GeminiModel, target, source, showSource)
	fmt.Fprintln(os.Stderr, "[headless] 마이크 캡처 시작 — 말하면 번역이 아래에 출력됩니다. (Ctrl-C 종료)")

	// 마이크 캡처 시작. onChunk에서 RMS 디버그 + Provider.Send.
	src := audio.NewMalgoSource()
	var chunkCount uint64
	onChunk := func(chunk audio.Chunk) {
		n := atomic.AddUint64(&chunkCount, 1)
		if n == 1 || n%50 == 0 { // ~5초마다 (100ms 청크 기준)
			fmt.Fprintf(os.Stderr, "[audio] chunk=%d rms=%.4f dropped(cap=%d, send=%d)\n",
				n, audio.RMS(chunk), src.Dropped(), provider.DroppedSend())
		}
		_ = provider.Send(chunk)
	}
	if err := src.Start(ctx, onChunk); err != nil {
		return fmt.Errorf("마이크 캡처 시작 실패: %w", err)
	}
	defer src.Stop()

	// 이벤트 루프.
	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(os.Stderr, "\n[headless] 종료 — 정리 중")
			return nil
		case ev, ok := <-events:
			if !ok {
				fmt.Fprintln(os.Stderr, "[headless] 이벤트 스트림 종료")
				return nil
			}
			handleEvent(ev, showSource)
		}
	}
}

func handleEvent(ev pipeline.Event, showSource bool) {
	switch ev.Kind {
	case pipeline.TranslatedDelta:
		// 번역 delta를 그대로 이어 출력(줄바꿈 없이 append). 확정은 자막엔진 몫(P3).
		fmt.Print(ev.Text)
	case pipeline.SourceDelta:
		if showSource {
			fmt.Fprintf(os.Stderr, "[src] %s", ev.Text)
		}
	case pipeline.TurnComplete, pipeline.GenerationComplete:
		// 경계에서 줄바꿈으로 시각적 구분.
		fmt.Println()
	case pipeline.Interrupted:
		fmt.Fprintln(os.Stderr, "[headless] interrupted")
	case pipeline.Usage:
		if ev.Usage != nil {
			fmt.Fprintf(os.Stderr, "[usage] outputAudioTokens=%d total=%d\n",
				ev.Usage.OutputAudioTokens, ev.Usage.TotalTokens)
		}
	case pipeline.State:
		fmt.Fprintf(os.Stderr, "[state] %s", ev.State)
		if ev.Err != nil {
			fmt.Fprintf(os.Stderr, " (%v)", ev.Err)
		}
		fmt.Fprintln(os.Stderr)
	case pipeline.PermanentFailure:
		fmt.Fprintf(os.Stderr, "[headless] permanent failure: %v\n", ev.Err)
	case pipeline.OutputAudio:
		// P1은 번역 음성 재생 없음 — 폐기(이벤트만 관측).
	}
}
