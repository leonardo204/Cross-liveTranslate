package subtitle

import (
	"testing"
	"time"
)

func sonioxEngine() *Engine {
	e := New()
	e.SpeakerDriven = true
	e.ExactDeltas = true
	e.QuestionBoundary = false
	e.TurnBoundarySilence = 0
	return e
}

// 화자가 바뀔 때만 줄이 나뉘고 색이 바뀐다. 앞 화자의 번역이 끝나기 전에 온 다음 화자의 원문은
// 앞 줄에 섞이지 않고 새 줄로 넘어간다.
func TestSpeakerChangeSplitsAndCarriesNextSource(t *testing.T) {
	e := sonioxEngine()
	var rec []string
	e.OnConfirmedLabeledLine = func(label, src, tr string) { rec = append(rec, label+"|"+src+"|"+tr) }

	e.IngestSpeakerSource("Quindi", "1", "화자 1 · IT")
	e.IngestSpeakerTranslation("그래서", "1", "화자 1 · IT")
	e.IngestSpeakerSource(" Yes.", "2", "화자 2 · EN") // 다음 화자 원문이 먼저 도착
	e.IngestSpeakerTranslation(" 저희가", "1", "화자 1 · IT")
	e.IngestSpeakerTranslation("네.", "2", "화자 2 · EN")

	lines := e.DisplayLines()
	if len(lines) != 2 {
		t.Fatalf("lines=%+v", lines)
	}
	if lines[0].Text != "그래서 저희가" || lines[0].Source != "Quindi" || lines[0].Label != "화자 1 · IT" || lines[0].Speaker != 0 {
		t.Fatalf("첫 줄=%+v", lines[0])
	}
	if lines[1].Text != "네." || lines[1].Source != "Yes." || lines[1].Label != "화자 2 · EN" || lines[1].Speaker != 1 {
		t.Fatalf("둘째 줄=%+v", lines[1])
	}
	if len(rec) != 1 || rec[0] != "화자 1 · IT|Quindi|그래서 저희가" {
		t.Fatalf("녹화=%v", rec)
	}
}

// 무음으로 줄이 확정돼도 같은 화자가 이어 말하면 색은 그대로다.
func TestSpeakerDrivenSilenceKeepsColor(t *testing.T) {
	e := sonioxEngine()
	t0 := time.Unix(1000, 0)
	e.IngestSpeakerTranslation("첫 문장", "1", "화자 1 · IT")
	e.Heartbeat(t0)
	e.Heartbeat(t0.Add(2500 * time.Millisecond)) // 무음 확정
	e.IngestSpeakerTranslation("둘째 문장", "1", "화자 1 · IT")
	for _, l := range e.DisplayLines() {
		if l.Speaker != 0 {
			t.Fatalf("같은 화자인데 색이 바뀌었다: %+v", e.DisplayLines())
		}
	}
}

// 토큰 단위 델타는 겹침 제거 없이 그대로 붙는다("ne" 뒤 "e" 같은 진짜 글자를 지우지 않는다).
func TestExactDeltasKeepRealCharacters(t *testing.T) {
	e := sonioxEngine()
	e.IngestSpeakerTranslation("ne", "1", "")
	e.IngestSpeakerTranslation("e", "1", "")
	if got := e.DisplayLines()[0].Text; got != "nee" {
		t.Fatalf("got %q", got)
	}
}

// 미확정 꼬리: 같은 화자면 진행 줄 뒤에, 다음 화자면 새 줄(다음 색)로 보인다.
func TestInterimTail(t *testing.T) {
	e := sonioxEngine()
	e.IngestSpeakerTranslation("그래서", "1", "화자 1 · IT")
	e.SetInterim(" 저희가", "1")
	l := e.DisplayLines()
	if len(l) != 1 || l[0].Tail != " 저희가" {
		t.Fatalf("같은 화자 꼬리=%+v", l)
	}
	e.SetInterim(" 네", "2")
	l = e.DisplayLines()
	if len(l) != 2 || l[1].Tail != "네" || l[1].Speaker != 1 || l[1].Text != "" {
		t.Fatalf("다음 화자 꼬리=%+v", l)
	}
	e.SetInterim("", "")
	if l = e.DisplayLines(); len(l) != 1 || l[0].Tail != "" {
		t.Fatalf("꼬리 지움=%+v", l)
	}
}

// 번역이 오기 전: 원문과 원문 꼬리만으로 줄이 보이고, 다음 화자의 원문은 다음 색 줄로 따로 보인다.
func TestSourceBeforeTranslationIsVisible(t *testing.T) {
	e := sonioxEngine()
	e.SetSourceInterim("Era", "1", "화자 1 · IT")
	if l := e.DisplayLines(); len(l) != 1 || l[0].Label != "화자 1 · IT" || l[0].SourceTail != "Era" {
		t.Fatalf("확정 원문 전 꼬리=%+v", l)
	}
	e.IngestSpeakerSource("Era che", "1", "화자 1 · IT")
	e.SetSourceInterim(" alla fine", "1", "화자 1 · IT")
	l := e.DisplayLines()
	if len(l) != 1 || l[0].Text != "" || l[0].Source != "Era che" || l[0].SourceTail != " alla fine" || l[0].Label != "화자 1 · IT" {
		t.Fatalf("번역 전 줄=%+v", l)
	}
	e.IngestSpeakerTranslation("그건", "1", "화자 1 · IT")
	e.SetSourceInterim(" Sì,", "2", "화자 2 · IT")
	l = e.DisplayLines()
	if len(l) != 2 || l[0].Text != "그건" || l[0].SourceTail != "" || l[1].SourceTail != " Sì," || l[1].Speaker != 1 || l[1].Label != "화자 2 · IT" {
		t.Fatalf("다음 화자 원문 꼬리=%+v", l)
	}
}
