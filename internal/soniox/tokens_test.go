package soniox

import (
	"testing"

	"cross-livetranslate/internal/pipeline"
)

func TestConvertGroupsFinalsAndCarriesSourceLanguage(t *testing.T) {
	st := newConvState()
	toks := []token{
		{Text: "Qu", Final: true, Status: "original", Lang: "it", Speaker: "1"},
		{Text: "indi", Final: true, Status: "original", Lang: "it", Speaker: "1"},
		{Text: "<end>", Final: true},
		{Text: "그래서", Final: true, Status: "translation", Lang: "ko", Speaker: "1"},
		{Text: " Yes", Final: true, Status: "original", Lang: "en", Speaker: float64(2)},
		{Text: " we", Final: false, Status: "original", Lang: "en", Speaker: "2"},
		{Text: " can", Final: false, Status: "original", Lang: "en", Speaker: "2"},
		{Text: " 네", Final: false, Status: "translation", Lang: "ko", Speaker: "2"},
	}
	evs, finals := convert(toks, st)
	if finals != 4 {
		t.Fatalf("finals=%d", finals)
	}
	want := []pipeline.Event{
		{Kind: pipeline.SourceDelta, Text: "Quindi", Speaker: "1", Lang: "it"},
		{Kind: pipeline.TranslatedDelta, Text: "그래서", Speaker: "1", Lang: "it"},
		{Kind: pipeline.SourceDelta, Text: " Yes", Speaker: "2", Lang: "en"},
		{Kind: pipeline.SourceInterim, Text: " we can", Speaker: "2", Lang: "en"},
		{Kind: pipeline.TranslatedInterim, Text: " 네", Speaker: "2"},
	}
	if len(evs) != len(want) {
		t.Fatalf("events=%+v", evs)
	}
	for i := range want {
		if evs[i].Kind != want[i].Kind || evs[i].Text != want[i].Text || evs[i].Speaker != want[i].Speaker || evs[i].Lang != want[i].Lang {
			t.Fatalf("[%d] got %+v want %+v", i, evs[i], want[i])
		}
	}
	// 같은 꼬리는 다시 보내지 않는다.
	evs, _ = convert([]token{
		{Text: " we can", Status: "original", Lang: "en", Speaker: "2"},
		{Text: " 네", Status: "translation", Speaker: "2"},
	}, st)
	if len(evs) != 0 {
		t.Fatalf("중복 꼬리를 보냈다: %+v", evs)
	}
	// 꼬리가 사라지면 빈 꼬리를 한 번씩 보낸다.
	evs, _ = convert(nil, st)
	if len(evs) != 2 || evs[0].Text != "" || evs[1].Text != "" {
		t.Fatalf("빈 꼬리 이벤트: %+v", evs)
	}
	if evs, _ = convert(nil, st); len(evs) != 0 {
		t.Fatalf("빈 꼬리를 두 번 보냈다: %+v", evs)
	}
}
