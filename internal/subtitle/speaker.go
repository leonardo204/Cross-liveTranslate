package subtitle

import "strings"

// speaker.go — 실제 화자 번호를 주는 엔진(Soniox)용 입력 경로.
//
// Gemini는 화자 정보가 없어 무음·물음표·델타 간격으로 화자 교대를 **추정**한다. Soniox는
// 토큰마다 화자 번호를 주므로 추정 대신 그 번호로 줄을 나누고 색을 바꾼다(SpeakerDriven).
//
// 순서 문제: Soniox는 한 화자의 원문 토큰을 먼저 보내고 번역 토큰을 뒤에 보낸다. 그래서 앞
// 화자의 번역이 아직 오는 중에 다음 화자의 원문이 먼저 도착할 수 있다. 이 원문을 진행 중 줄에
// 섞지 않도록 nextSource 에 따로 모아 두었다가, 다음 화자의 첫 번역이 와서 줄이 바뀔 때 새 줄의
// 원문으로 넘긴다.

// IngestSpeakerTranslation 은 화자 번호가 붙은 확정 번역 델타를 받는다. label 은 원문 줄 앞에
// 붙일 표시다(예: "화자 2 · IT"). 화자가 바뀌면 진행 중 줄을 확정하고 색을 넘긴다.
func (e *Engine) IngestSpeakerTranslation(text, speaker, label string) {
	if !e.SpeakerDriven || speaker == "" {
		e.IngestTranslatedDelta(text)
		return
	}
	e.applyPendingGenerationReset()
	if e.lineSpeaker != "" && speaker != e.lineSpeaker {
		e.logf("engine.speaker 화자 %s → %s 줄 확정 current=%q", e.lineSpeaker, speaker, preview(e.currentTranslation))
		e.confirmTurn(reasonSpeaker)
		if e.nextSpeaker == speaker {
			e.currentSource = e.nextSource
		}
		e.nextSource, e.nextSpeaker, e.nextLabel = "", "", ""
	}
	e.lineSpeaker = speaker
	if label != "" {
		e.lineLabel = label
	}
	e.IngestTranslatedDelta(text)
}

// IngestSpeakerSource 는 화자 번호가 붙은 확정 원문 델타를 받는다. label 은 그 화자의 표시다.
func (e *Engine) IngestSpeakerSource(text, speaker, label string) {
	if !e.SpeakerDriven || speaker == "" {
		e.IngestSourceDelta(text)
		return
	}
	e.applyPendingGenerationReset()
	if e.lineSpeaker == "" {
		e.lineSpeaker = speaker
	}
	if speaker == e.lineSpeaker {
		if e.lineLabel == "" {
			e.lineLabel = label
		}
		if e.appendIfMeaningful(text, &e.currentSource) {
			e.segmentMode, e.visible = true, true
			e.markActivity()
		}
		return
	}
	if e.nextSpeaker != speaker {
		e.nextSource, e.nextSpeaker = "", speaker
	}
	e.nextLabel = label
	if e.appendIfMeaningful(text, &e.nextSource) {
		e.segmentMode, e.visible = true, true
		e.markActivity()
	}
}

// SetSourceInterim 은 아직 확정되지 않은 원문 꼬리를 바꾼다(빈 문자열이면 꼬리 없음).
// 번역이 오기 전에 "지금 무엇을 알아듣고 있는지"를 원문 줄에 흐리게 보여 주는 데 쓴다.
func (e *Engine) SetSourceInterim(text, speaker, label string) {
	e.srcInterim, e.srcInterimSpk = text, speaker
	if strings.TrimSpace(text) != "" && e.SpeakerDriven && speaker != "" && label != "" {
		// 확정 원문이 오기 전이라도 화자·언어 표시를 먼저 정해 둔다(첫 줄이 표시 없이 뜨지 않게).
		if e.lineSpeaker == "" {
			e.lineSpeaker = speaker
		}
		if speaker == e.lineSpeaker {
			if e.lineLabel == "" {
				e.lineLabel = label
			}
		} else if e.nextSpeaker == "" || e.nextSpeaker == speaker {
			e.nextSpeaker, e.nextLabel = speaker, label
		}
	}
	if strings.TrimSpace(text) != "" {
		e.segmentMode = true
		e.visible = true
		e.markActivity()
	}
}

// SetInterim 은 아직 확정되지 않은 번역 꼬리를 바꾼다(빈 문자열이면 꼬리 없음). 꼬리는 확정
// 줄에 들어가지 않고 화면에만 흐리게 보인다. 꼬리가 바뀌는 동안은 말이 이어지는 것이므로
// 무음 판정을 미룬다.
func (e *Engine) SetInterim(text, speaker string) {
	e.interim, e.interimSpeaker = text, speaker
	if strings.TrimSpace(text) != "" {
		e.segmentMode = true
		e.visible = true
		e.markActivity()
	}
}

// sameLine reports 이 화자(꼬리의 주인)가 진행 중 줄의 화자인지. 화자를 모르면 같은 줄로 본다.
func (e *Engine) sameLine(speaker string) bool {
	return !e.SpeakerDriven || speaker == "" || e.lineSpeaker == "" || speaker == e.lineSpeaker
}

// appendPending 은 아직 확정되지 않은 것들(번역 꼬리·원문 꼬리·번역 전 원문)을 표시 줄 목록에
// 붙인다. hadCurrent 면 마지막 줄이 진행 중 줄이다.
//
//	진행 중 줄 — 번역 꼬리·원문 꼬리를 그 줄 뒤에 흐리게. 번역이 아직 없고 원문만 있으면
//	             원문만 있는 줄을 하나 만든다(무엇을 알아듣고 있는지 바로 보이게).
//	다음 화자   — 앞 화자의 번역이 끝나기 전에 온 원문·꼬리는 새 줄(다음 색)에 따로 보인다.
func (e *Engine) appendPending(out []Line, hadCurrent bool) []Line {
	tr, src := e.interim, e.srcInterim
	if strings.TrimSpace(tr) == "" {
		tr = ""
	}
	if strings.TrimSpace(src) == "" {
		src = ""
	}
	curTr, curSrc, nextTr, nextSrc := "", "", "", ""
	if tr != "" {
		if e.sameLine(e.interimSpeaker) {
			curTr = tr
		} else {
			nextTr = tr
		}
	}
	if src != "" {
		if e.sameLine(e.srcInterimSpk) {
			curSrc = src
		} else {
			nextSrc = src
		}
	}
	switch {
	case hadCurrent && len(out) > 0:
		last := &out[len(out)-1]
		last.Tail = curTr
		last.SourceTail = curSrc
	case curTr != "" || (e.SpeakerDriven && (e.currentSource != "" || curSrc != "")):
		out = append(out, Line{
			Tail:       strings.TrimLeft(curTr, " \t"),
			Source:     strings.TrimSpace(e.currentSource),
			SourceTail: curSrc,
			Label:      e.lineLabel,
			Speaker:    e.speakerOf(e.curSpeaker),
		})
	}
	if e.SpeakerDriven && (nextTr != "" || nextSrc != "" || e.nextSource != "") {
		out = append(out, Line{
			Tail:       strings.TrimLeft(nextTr, " \t"),
			Source:     strings.TrimSpace(e.nextSource),
			SourceTail: nextSrc,
			Label:      e.nextLabel,
			Speaker:    e.speakerOf(e.curSpeaker ^ 1),
		})
	}
	return out
}

func (e *Engine) resetSpeakerState() {
	e.lineSpeaker, e.lineLabel = "", ""
	e.nextSource, e.nextSpeaker = "", ""
	e.interim, e.interimSpeaker = "", ""
	e.srcInterim, e.srcInterimSpk, e.nextLabel = "", "", ""
}
