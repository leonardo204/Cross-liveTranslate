package soniox

import (
	"encoding/json"
	"fmt"
	"strings"

	"cross-livetranslate/internal/pipeline"
	"cross-livetranslate/internal/txlog"
)

// token mirrors one Soniox response token.
type token struct {
	Text    string `json:"text"`
	Final   bool   `json:"is_final"`
	Status  string `json:"translation_status"` // "original" | "translation" | "none" | ""
	Lang    string `json:"language"`
	Speaker any    `json:"speaker"` // 문서상 문자열이지만 숫자로 오는 경우도 받아 둔다.
}

type response struct {
	Tokens    []token `json:"tokens"`
	ErrorCode int     `json:"error_code"`
	ErrorMsg  string  `json:"error_message"`
	Finished  bool    `json:"finished"`
}

// isControl reports Soniox 제어 토큰(<end>, <fin>)이다. 자막에 그리지 않는다.
func isControl(t string) bool {
	t = strings.TrimSpace(t)
	return t == "<end>" || t == "<fin>"
}

func speakerOf(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return fmt.Sprintf("%d", int(x))
	default:
		return fmt.Sprint(x)
	}
}

// convState 는 메시지 사이에 이어지는 변환 상태다(수신 goroutine 전용).
type convState struct {
	srcLang    map[string]string // 화자 → 최근 원문 언어
	lastLang   string            // 화자 정보가 없을 때 쓰는 최근 원문 언어
	trTailKey  string            // 직전에 보낸 번역 꼬리(화자+글자). 같으면 다시 보내지 않는다.
	srcTailKey string            // 직전에 보낸 원문 꼬리
}

func newConvState() *convState { return &convState{srcLang: map[string]string{}} }

// tailEvent 는 꼬리가 바뀌었으면 이벤트를 하나 돌려준다(빈 꼬리도 "지움"으로 한 번 보낸다).
func tailEvent(kind pipeline.Kind, text, speaker, lang string, prev *string) (pipeline.Event, bool) {
	key := ""
	if text != "" {
		key = speaker + "\x00" + text
	}
	if key == *prev {
		return pipeline.Event{}, false
	}
	*prev = key
	return pipeline.Event{Kind: kind, Text: text, Speaker: speaker, Lang: lang}, true
}

// convert turns one message's tokens into pipeline events (순수 — 테스트 대상).
//
// 확정 토큰은 같은 종류·같은 화자·같은 언어가 이어지는 동안 한 이벤트로 묶는다. 미확정 토큰은
// 원문·번역 각각 이어 붙여 꼬리 이벤트(SourceInterim / TranslatedInterim) 한 건으로 보낸다.
// 번역 토큰의 언어는 목표 언어라서, 자막 표시용 원문 언어는 그 화자의 원문 토큰에서 가져온다.
// 반환값: 이벤트, 확정 토큰 수(비용 계산용).
func convert(toks []token, st *convState) ([]pipeline.Event, int) {
	var out []pipeline.Event
	var trTail, srcTail strings.Builder
	trSpeaker, srcSpeaker, srcTailLang := "", "", ""
	finals := 0
	for _, t := range toks {
		if t.Text == "" || isControl(t.Text) {
			continue
		}
		sp := speakerOf(t.Speaker)
		isTr := t.Status == "translation"
		if !isTr && t.Lang != "" {
			if sp != "" {
				st.srcLang[sp] = t.Lang
			}
			st.lastLang = t.Lang
		}
		if !t.Final {
			if isTr {
				if trTail.Len() == 0 {
					trSpeaker = sp
				}
				trTail.WriteString(t.Text)
			} else {
				if srcTail.Len() == 0 {
					srcSpeaker, srcTailLang = sp, t.Lang
				}
				srcTail.WriteString(t.Text)
			}
			continue
		}
		finals++
		kind := pipeline.SourceDelta
		lang := t.Lang
		if isTr {
			kind = pipeline.TranslatedDelta
			lang = st.srcLang[sp]
			if lang == "" {
				lang = st.lastLang
			}
		}
		if n := len(out); n > 0 && out[n-1].Kind == kind && out[n-1].Speaker == sp && out[n-1].Lang == lang {
			out[n-1].Text += t.Text
			continue
		}
		out = append(out, pipeline.Event{Kind: kind, Text: t.Text, Speaker: sp, Lang: lang})
	}
	if ev, ok := tailEvent(pipeline.SourceInterim, srcTail.String(), srcSpeaker, srcTailLang, &st.srcTailKey); ok {
		out = append(out, ev)
	}
	if ev, ok := tailEvent(pipeline.TranslatedInterim, trTail.String(), trSpeaker, "", &st.trTailKey); ok {
		out = append(out, ev)
	}
	return out, finals
}

// handleMessage parses one server message. done=true면 이 연결을 끝낸다(반환 결과대로).
func (c *Client) handleMessage(data []byte) (result, bool) {
	var m response
	if err := json.Unmarshal(data, &m); err != nil {
		return 0, false
	}
	if m.ErrorCode != 0 {
		txlog.Logf("soniox.conn", "서버 오류 code=%d msg=%q", m.ErrorCode, m.ErrorMsg)
		switch m.ErrorCode {
		case 401, 403:
			c.emit(pipeline.Event{Kind: pipeline.PermanentFailure,
				Err: fmt.Errorf("soniox: API 키가 거부되었습니다 (%d)", m.ErrorCode)})
			return resultPermanent, true
		case 402:
			c.emit(pipeline.Event{Kind: pipeline.PermanentFailure,
				Err: fmt.Errorf("soniox: 계정 잔액이 없습니다 — Soniox 콘솔에서 충전해 주세요 (402)")})
			return resultPermanent, true
		case 400:
			c.emit(pipeline.Event{Kind: pipeline.PermanentFailure,
				Err: fmt.Errorf("soniox: 설정이 거부되었습니다 — 용어집 크기나 언어 코드를 확인하세요 (%s)", m.ErrorMsg)})
			return resultPermanent, true
		}
		return resultDisconnected, true
	}
	evs, finals := convert(m.Tokens, c.conv)
	for _, ev := range evs {
		c.emit(ev)
	}
	if finals > 0 {
		c.emit(pipeline.Event{Kind: pipeline.Usage, Usage: &pipeline.UsageInfo{OutputTextTokens: finals}})
	}
	if m.Finished {
		if c.ctx.Err() != nil {
			return resultStopped, true
		}
		return resultDisconnected, true
	}
	return 0, false
}
