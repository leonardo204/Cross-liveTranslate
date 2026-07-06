package ipc

import (
	"bytes"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// TestWriteReadRoundTrip verifies that a sequence of messages written with
// WriteMsg is recovered identically by ReadLoop (라인 경계 + 왕복 무손실).
func TestWriteReadRoundTrip(t *testing.T) {
	msgs := []SubtitleMsg{
		{Lines: []string{"안녕하세요 → Hello"}, Source: "안녕하세요", Visible: true},
		{Lines: []string{"line one", "line two"}, Visible: true},
		{Lines: nil, Visible: false},
		{Lines: []string{"세 번째\t줄 with \"quotes\" and, commas"}, Source: "src", Visible: true},
	}

	var buf bytes.Buffer
	for _, m := range msgs {
		if err := WriteMsg(&buf, m); err != nil {
			t.Fatalf("WriteMsg: %v", err)
		}
	}

	var got []SubtitleMsg
	ReadLoop(&buf, func(m SubtitleMsg) {
		got = append(got, m)
	})

	if len(got) != len(msgs) {
		t.Fatalf("got %d messages, want %d", len(got), len(msgs))
	}
	for i := range msgs {
		if !reflect.DeepEqual(got[i], msgs[i]) {
			t.Errorf("msg[%d] = %#v, want %#v", i, got[i], msgs[i])
		}
	}
}

// TestReadLoopSkipsGarbage ensures malformed lines are skipped without aborting
// the stream (복원력).
func TestReadLoopSkipsGarbage(t *testing.T) {
	stream := strings.Join([]string{
		`{"lines":["ok1"],"visible":true}`,
		`not json at all`,
		``, // 빈 라인
		`{"lines":["ok2"],"visible":false}`,
	}, "\n") + "\n"

	var got []SubtitleMsg
	ReadLoop(strings.NewReader(stream), func(m SubtitleMsg) {
		got = append(got, m)
	})

	if len(got) != 2 {
		t.Fatalf("got %d valid messages, want 2 (%#v)", len(got), got)
	}
	if got[0].Lines[0] != "ok1" || !got[0].Visible {
		t.Errorf("msg[0] = %#v", got[0])
	}
	if got[1].Lines[0] != "ok2" || got[1].Visible {
		t.Errorf("msg[1] = %#v", got[1])
	}
}

// TestReadLoopThroughPipe exercises the concrete controller→overlay wiring: a
// writer goroutine emits over an io.Pipe while ReadLoop consumes on the other end.
func TestReadLoopThroughPipe(t *testing.T) {
	pr, pw := io.Pipe()
	want := []SubtitleMsg{
		{Lines: []string{"a"}, Visible: true},
		{Lines: []string{"b", "c"}, Visible: true},
	}

	var wg sync.WaitGroup
	wg.Add(1)
	var got []SubtitleMsg
	go func() {
		defer wg.Done()
		ReadLoop(pr, func(m SubtitleMsg) { got = append(got, m) })
	}()

	for _, m := range want {
		if err := WriteMsg(pw, m); err != nil {
			t.Errorf("WriteMsg: %v", err)
		}
	}
	pw.Close()
	wg.Wait()

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

// TestStyleRoundTrip verifies a StyleMsg written with WriteStyle is recovered
// identically by Dispatch (모든 스타일/위치 필드 왕복 무손실).
func TestStyleRoundTrip(t *testing.T) {
	want := StyleMsg{
		FontFamily:    "Pretendard",
		FontSize:      34,
		FontWeight:    "bold",
		TextColor:     "#FFFFFFFF",
		StrokeEnabled: true,
		StrokeColor:   "#000000E6",
		StrokeWidth:   2,
		GlowEnabled:   true,
		GlowColor:     "#00E5FFCC",
		GlowRadius:    8,
		BgEnabled:     true,
		BgColor:       "#000000FF",
		BgOpacity:     0.35,
		Align:         "center",
		MaxLines:      2,
		MonitorIndex:  1,
		Vertical:      "bottom",
	}

	var buf bytes.Buffer
	if err := WriteStyle(&buf, want); err != nil {
		t.Fatalf("WriteStyle: %v", err)
	}

	var got []StyleMsg
	Dispatch(&buf, Handler{OnStyle: func(m StyleMsg) { got = append(got, m) }})
	if len(got) != 1 {
		t.Fatalf("got %d style messages, want 1", len(got))
	}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("style = %#v, want %#v", got[0], want)
	}
}

// TestDispatchRoutesByType ensures a mixed stream of subtitle + style lines is
// routed to the correct callbacks, and that untyped subtitle lines (하위호환)
// still reach OnSubtitle.
func TestDispatchRoutesByType(t *testing.T) {
	var buf bytes.Buffer
	// Untyped subtitle (legacy WriteMsg) → must still route to OnSubtitle.
	if err := WriteMsg(&buf, SubtitleMsg{Lines: []string{"legacy"}, Visible: true}); err != nil {
		t.Fatalf("WriteMsg: %v", err)
	}
	// Typed subtitle.
	if err := WriteSubtitle(&buf, SubtitleMsg{Lines: []string{"typed"}, Visible: true}); err != nil {
		t.Fatalf("WriteSubtitle: %v", err)
	}
	// Typed style.
	if err := WriteStyle(&buf, StyleMsg{FontSize: 40, Align: "leading", MonitorIndex: 2}); err != nil {
		t.Fatalf("WriteStyle: %v", err)
	}

	var subs []SubtitleMsg
	var styles []StyleMsg
	Dispatch(&buf, Handler{
		OnSubtitle: func(m SubtitleMsg) { subs = append(subs, m) },
		OnStyle:    func(m StyleMsg) { styles = append(styles, m) },
	})

	if len(subs) != 2 {
		t.Fatalf("got %d subtitle messages, want 2 (%#v)", len(subs), subs)
	}
	if subs[0].Lines[0] != "legacy" || subs[1].Lines[0] != "typed" {
		t.Errorf("subtitle routing wrong: %#v", subs)
	}
	if len(styles) != 1 {
		t.Fatalf("got %d style messages, want 1", len(styles))
	}
	if styles[0].FontSize != 40 || styles[0].Align != "leading" || styles[0].MonitorIndex != 2 {
		t.Errorf("style routing wrong: %#v", styles[0])
	}
}

// TestLongLine verifies a subtitle line beyond the default 64KiB scanner token
// limit still round-trips (버퍼 확장 검증).
func TestLongLine(t *testing.T) {
	long := strings.Repeat("가", 80*1024)
	var buf bytes.Buffer
	if err := WriteMsg(&buf, SubtitleMsg{Lines: []string{long}, Visible: true}); err != nil {
		t.Fatalf("WriteMsg: %v", err)
	}
	var got []SubtitleMsg
	ReadLoop(&buf, func(m SubtitleMsg) { got = append(got, m) })
	if len(got) != 1 || got[0].Lines[0] != long {
		t.Fatalf("long line did not round-trip (got %d msgs)", len(got))
	}
}
