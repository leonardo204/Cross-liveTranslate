package subtitle

import "testing"

// Flush 는 무음 2초를 기다리지 않고 진행 중인 줄을 확정해 녹화 콜백으로 넘긴다.
func TestFlushConfirmsPendingLine(t *testing.T) {
	e := New()
	var got []string
	e.OnConfirmedLine = func(source, translation string) { got = append(got, translation) }
	e.IngestTranslatedDelta("안녕하세요")
	e.Flush()
	if len(got) != 1 || got[0] != "안녕하세요" {
		t.Fatalf("진행 중 줄이 확정되지 않았다: %v", got)
	}
	e.Flush() // 남은 줄이 없으면 아무 일도 없다.
	if len(got) != 1 {
		t.Fatalf("빈 상태에서 중복 확정: %v", got)
	}
}
