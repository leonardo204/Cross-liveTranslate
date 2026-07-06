// Command headless — P2 슬라이스 진입점: (마이크/루프백/장치) → Gemini Live → 콘솔 자막.
//
// 사용법:
//
//	go run ./cmd/headless -target ko [-source en] [-show-source] [-duration 60s]
//	                      [-input auto|mic|loopback|device:<id>] [-list-devices]
//
// reconciler가 provider/source 수명을 오케스트레이트(epoch 펜싱)하고, 자막 엔진이
// delta를 dedup·roll-up해 확정 줄을 stdout에 출력한다. Ctrl-C 또는 -duration 만료 시 정리.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"cross-livetranslate/internal/app"
	"cross-livetranslate/internal/audio"
	"cross-livetranslate/internal/config"
	"cross-livetranslate/internal/gemini"
	"cross-livetranslate/internal/pipeline"
	"cross-livetranslate/internal/subtitle"
)

func main() {
	target := flag.String("target", config.DefaultTargetLanguage, "번역 대상 언어(BCP-47), 예: ko, ja, en")
	source := flag.String("source", config.DefaultSourceLanguage, "원문 언어(BCP-47) 또는 auto(서버 자동 감지)")
	showSource := flag.Bool("show-source", false, "원문 전사도 함께 표시(inputAudioTranscription 요청)")
	duration := flag.Duration("duration", 0, "실행 시간(예: 60s). 0이면 Ctrl-C까지 무한 실행")
	input := flag.String("input", "auto", "입력 소스: auto|mic|loopback|device:<id>")
	listDevices := flag.Bool("list-devices", false, "캡처 장치 열거 후 종료")
	flag.Parse()

	if *listDevices {
		if err := printDevices(); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}

	sel, err := parseSelection(*input)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}

	if err := run(*target, *source, *showSource, *duration, sel); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func printDevices() error {
	devs, err := audio.EnumerateDevices()
	if err != nil {
		return err
	}
	fmt.Printf("캡처 장치 %d개:\n", len(devs))
	for _, d := range devs {
		mark := ""
		if d.IsLoopbackCandidate {
			mark = "  [루프백 후보]"
		}
		fmt.Printf("  - %s%s\n    id=%s\n", d.Name, mark, d.ID)
	}
	return nil
}

func parseSelection(s string) (audio.Selection, error) {
	switch {
	case s == "auto":
		return audio.Selection{Mode: audio.SelectAuto}, nil
	case s == "mic":
		return audio.Selection{Mode: audio.SelectMic}, nil
	case s == "loopback":
		return audio.Selection{Mode: audio.SelectLoopback}, nil
	case strings.HasPrefix(s, "device:"):
		id := strings.TrimPrefix(s, "device:")
		if id == "" {
			return audio.Selection{}, fmt.Errorf("-input device: 뒤에 장치 ID가 필요합니다 (-list-devices로 확인)")
		}
		return audio.Selection{Mode: audio.SelectDevice, DeviceID: id}, nil
	default:
		return audio.Selection{}, fmt.Errorf("알 수 없는 -input 값: %q (auto|mic|loopback|device:<id>)", s)
	}
}

func run(target, source string, showSource bool, duration time.Duration, sel audio.Selection) error {
	apiKey, err := config.APIKey()
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if duration > 0 {
		var tcancel context.CancelFunc
		ctx, tcancel = context.WithTimeout(ctx, duration)
		defer tcancel()
	}

	// 팩토리 주입: reconciler는 gemini/malgo에 직접 의존하지 않는다.
	newProvider := func(cfg app.ProviderConfig) (pipeline.Provider, error) {
		return gemini.NewProvider(gemini.Config{
			APIKey:                    apiKey,
			Model:                     cfg.Model,
			TargetLanguage:            cfg.TargetLanguage,
			SourceLanguage:            cfg.SourceLanguage,
			RequestInputTranscription: cfg.ShowSource,
			// EmitOutputAudio: 번역음성 재생(P4) 전까지 false — 대용량 PCM 이벤트 억제.
		}), nil
	}
	newSource := func(s audio.Selection) (audio.Source, error) {
		return audio.SelectSource(s)
	}

	// 이벤트는 pump goroutine에서 온다 → 단일 소유 goroutine(아래 루프)으로 전달해
	// 자막 엔진 접근을 직렬화(레이스 방지).
	engineEvents := make(chan pipeline.Event, 256)
	onEvent := func(ev pipeline.Event) {
		select {
		case engineEvents <- ev:
		case <-ctx.Done():
		}
	}

	r := app.New(app.Options{NewProvider: newProvider, NewSource: newSource, OnEvent: onEvent})
	r.Start(ctx)
	defer r.Close()

	fmt.Fprintf(os.Stderr, "[headless] model=%s target=%s source=%s input=%s show-source=%v\n",
		config.GeminiModel, target, source, sel.Mode, showSource)
	fmt.Fprintln(os.Stderr, "[headless] 시작 — 말하면 정리된 자막이 출력됩니다. (Ctrl-C 종료)")

	r.SetDesired(app.Desired{
		Running:   true,
		Selection: sel,
		Provider: app.ProviderConfig{
			Model:          config.GeminiModel,
			TargetLanguage: target,
			SourceLanguage: source,
			ShowSource:     showSource,
		},
	})

	// 자막 엔진 소유 루프: 이벤트 + heartbeat(무음 정리)를 한 goroutine에서 처리.
	eng := subtitle.New()
	eng.OnConfirmedLine = func(src, tr string) {
		if tr == "" {
			return
		}
		if showSource && src != "" {
			fmt.Printf("원문: %s\n번역: %s\n", src, tr)
		} else {
			fmt.Printf("%s\n", tr)
		}
	}

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(os.Stderr, "\n[headless] 종료 — 정리 중")
			return nil
		case now := <-ticker.C:
			eng.Heartbeat(now)
		case ev := <-engineEvents:
			applyEvent(eng, ev, showSource)
		}
	}
}

// applyEvent는 단일 엔진 goroutine에서만 호출된다(직렬).
func applyEvent(eng *subtitle.Engine, ev pipeline.Event, showSource bool) {
	switch ev.Kind {
	case pipeline.TranslatedDelta:
		eng.IngestTranslatedDelta(ev.Text)
	case pipeline.SourceDelta:
		eng.IngestSourceDelta(ev.Text)
		if showSource {
			fmt.Fprintf(os.Stderr, "[src] %s\n", ev.Text)
		}
	case pipeline.TurnComplete:
		eng.TurnComplete()
	case pipeline.GenerationComplete:
		eng.GenerationComplete()
	case pipeline.Interrupted:
		eng.Interrupted()
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
		// P2는 번역 음성 재생 없음 — 폐기.
	}
}
