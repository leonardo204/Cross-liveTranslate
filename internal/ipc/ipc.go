// Package ipc — controller ↔ overlay 프로세스 간 자막 프로토콜(순수, 크로스빌드 안전).
//
// 2-프로세스 아키텍처(specs/011-p3-overlay-ui-architecture.md P3b)에서 controller가
// overlay 자식 프로세스를 spawn하고, reconciler→자막엔진이 만든 자막 스냅샷을
// 자식의 stdin으로 JSON 라인(NDJSON)으로 push한다. overlay는 stdin을 ReadLoop로
// 스캔해 각 메시지를 프론트("subtitle:update")로 전달한다.
//
// 프로토콜은 라인 단위(줄바꿈 구분) JSON이라 부분 읽기/버퍼링 없이 스트리밍된다.
// cgo/네트워크 의존이 없어 windows 크로스빌드에서도 그대로 컴파일된다.
package ipc

import (
	"bufio"
	"encoding/json"
	"io"
)

// 메시지 타입 태그. 한 stdin 스트림에 자막(텍스트)과 스타일(위치/렌더 속성) 두 종류의
// 메시지가 섞여 흐르므로, JSON 라인의 "type" 필드로 구분한다. 하위호환을 위해 type이
// 비어 있으면 자막 메시지로 간주한다(기존 SubtitleMsg 라인은 type 없이 흐른다).
const (
	TypeSubtitle = "subtitle"
	TypeStyle    = "style"
	// TypeControl carries out-of-band process-control commands between the
	// controller and its child processes (U1 3-role): controller → settings
	// child(show/hide/quit), settings child → controller("changed" 설정 반영 신호).
	TypeControl = "control"
)

// SubtitleMsg is a single subtitle snapshot pushed controller → overlay.
//
//	Lines   — roll-up 확정 줄 + 진행 중 줄(위→아래 표시 순서).
//	Source  — 진행 중 원문(원문 동시 표시가 켜졌을 때만 비어있지 않음).
//	Visible — 자막을 화면에 보여야 하는지(false면 오버레이 숨김).
type SubtitleMsg struct {
	Lines   []string `json:"lines"`
	Source  string   `json:"source,omitempty"`
	Visible bool     `json:"visible"`
}

// StyleMsg carries the subtitle rendering style + placement pushed controller →
// overlay. 원본 SubtitleStyle.swift(폰트/색/외곽선/글로우/배경/정렬/줄수) +
// SubtitleOverlayController(모니터/수직위치)에서 이식한 속성을 오버레이 프론트가 받아
// CSS로 실시간 반영한다. 색은 sRGB 8bit "#RRGGBBAA" 문자열.
type StyleMsg struct {
	FontFamily    string  `json:"fontFamily"`    // ""=시스템 rounded 스택.
	FontSize      float64 `json:"fontSize"`      // pt→px.
	FontWeight    string  `json:"fontWeight"`    // regular|medium|semibold|bold|heavy|black.
	TextColor     string  `json:"textColor"`     // #RRGGBBAA.
	StrokeEnabled bool    `json:"strokeEnabled"` // 외곽선(다중 그림자).
	StrokeColor   string  `json:"strokeColor"`   // #RRGGBBAA.
	StrokeWidth   float64 `json:"strokeWidth"`   // -webkit-text-stroke 두께(px).
	GlowEnabled   bool    `json:"glowEnabled"`   // 글로우(blur shadow).
	GlowColor     string  `json:"glowColor"`     // #RRGGBBAA.
	GlowRadius    float64 `json:"glowRadius"`    // blur 반경(px).
	BgEnabled     bool    `json:"bgEnabled"`     // 배경 박스.
	BgColor       string  `json:"bgColor"`       // #RRGGBBAA(RGB만 사용, 알파는 BgOpacity).
	BgOpacity     float64 `json:"bgOpacity"`     // 0..1 배경 불투명도.
	Align         string  `json:"align"`         // leading|center|trailing.
	MaxLines      int     `json:"maxLines"`      // 표시 최대 줄수.
	MonitorIndex  int     `json:"monitorIndex"`  // 0=주 화면(오버레이 재커버 대상).
	Vertical      string  `json:"vertical"`      // top|middle|bottom.
}

// ControlMsg carries a process-control command over the same NDJSON stream
// used for subtitles/styles. Cmd is one of: "show"|"hide"|"quit"(controller →
// settings child) 또는 "changed"(settings child → controller, 설정 파일이 변경되어
// controller가 reload+반영해야 함을 알린다).
type ControlMsg struct {
	Cmd string `json:"cmd"`
}

// subtitleEnvelope / styleEnvelope embed the payload alongside a "type" tag so
// the two message kinds share a single NDJSON stream without ambiguity.
// 임베딩으로 필드가 평탄화되어 {"type":"subtitle","lines":...} 형태로 직렬화된다.
type subtitleEnvelope struct {
	Type string `json:"type"`
	SubtitleMsg
}

type styleEnvelope struct {
	Type string `json:"type"`
	StyleMsg
}

type controlEnvelope struct {
	Type string `json:"type"`
	ControlMsg
}

// typePeek extracts just the "type" tag from a raw JSON line for dispatch.
type typePeek struct {
	Type string `json:"type"`
}

// WriteMsg marshals m to JSON and writes it as a single newline-terminated line.
// 부분 쓰기 없이 한 번의 Write로 라인을 방출한다(스캐너 측 라인 경계 보장).
func WriteMsg(w io.Writer, m SubtitleMsg) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}

// WriteSubtitle writes a type-tagged subtitle line (production path). 하위호환
// WriteMsg와 달리 "type":"subtitle"을 붙여 Dispatch가 명확히 분기하게 한다.
func WriteSubtitle(w io.Writer, m SubtitleMsg) error {
	return writeLine(w, subtitleEnvelope{Type: TypeSubtitle, SubtitleMsg: m})
}

// WriteStyle marshals a StyleMsg with a "style" type tag and writes it as one
// newline-terminated line. controller의 stdin 단일 writer(runLoop)에서만 호출한다.
func WriteStyle(w io.Writer, m StyleMsg) error {
	return writeLine(w, styleEnvelope{Type: TypeStyle, StyleMsg: m})
}

// WriteControl marshals a ControlMsg with a "control" type tag and writes it as
// one newline-terminated line. controller가 settings 자식 stdin에, settings 자식이
// controller가 읽는 stdout에 각각 단일 writer로 쓴다.
func WriteControl(w io.Writer, m ControlMsg) error {
	return writeLine(w, controlEnvelope{Type: TypeControl, ControlMsg: m})
}

// writeLine marshals v and emits it as a single newline-terminated line.
func writeLine(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}

// Handler carries the per-type callbacks Dispatch invokes. nil callbacks are
// skipped(해당 타입 무시).
type Handler struct {
	OnSubtitle func(SubtitleMsg)
	OnStyle    func(StyleMsg)
	OnControl  func(ControlMsg)
}

// Dispatch scans r line-by-line and routes each well-formed JSON line to the
// matching Handler callback by its "type" tag. type이 비어 있거나 "subtitle"이면
// OnSubtitle로, "style"이면 OnStyle로 보낸다(하위호환: 태그 없는 SubtitleMsg 라인도
// 자막으로 처리). 잘못된 라인은 조용히 건너뛴다(스트림 복원력). 호출자는 별도 goroutine.
func Dispatch(r io.Reader, h Handler) {
	sc := newScanner(r)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var tp typePeek
		if err := json.Unmarshal(line, &tp); err != nil {
			continue
		}
		switch tp.Type {
		case TypeStyle:
			var m StyleMsg
			if err := json.Unmarshal(line, &m); err != nil {
				continue
			}
			if h.OnStyle != nil {
				h.OnStyle(m)
			}
		case TypeControl:
			var m ControlMsg
			if err := json.Unmarshal(line, &m); err != nil {
				continue
			}
			if h.OnControl != nil {
				h.OnControl(m)
			}
		default: // "" or "subtitle"
			var m SubtitleMsg
			if err := json.Unmarshal(line, &m); err != nil {
				continue
			}
			if h.OnSubtitle != nil {
				h.OnSubtitle(m)
			}
		}
	}
}

// ReadLoop scans r line-by-line, unmarshals each line into a SubtitleMsg, and
// invokes fn for every well-formed message. 잘못된 라인은 조용히 건너뛴다(스트림
// 복원력). r이 EOF/에러로 끝나면 반환한다. 호출자는 별도 goroutine에서 구동한다.
//
// 기본 bufio.Scanner 토큰 상한(64KiB)을 넘는 긴 자막 라인도 처리하도록 버퍼를 키운다.
func ReadLoop(r io.Reader, fn func(SubtitleMsg)) {
	sc := newScanner(r)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var m SubtitleMsg
		if err := json.Unmarshal(line, &m); err != nil {
			continue // 손상/부분 라인 무시.
		}
		if fn != nil {
			fn(m)
		}
	}
}

// newScanner returns a line scanner with an enlarged token buffer (1 MiB) so
// long subtitle lines beyond bufio's 64KiB default still stream intact.
func newScanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	return sc
}
