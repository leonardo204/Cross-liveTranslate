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

// ReadLoop scans r line-by-line, unmarshals each line into a SubtitleMsg, and
// invokes fn for every well-formed message. 잘못된 라인은 조용히 건너뛴다(스트림
// 복원력). r이 EOF/에러로 끝나면 반환한다. 호출자는 별도 goroutine에서 구동한다.
//
// 기본 bufio.Scanner 토큰 상한(64KiB)을 넘는 긴 자막 라인도 처리하도록 버퍼를 키운다.
func ReadLoop(r io.Reader, fn func(SubtitleMsg)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
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
