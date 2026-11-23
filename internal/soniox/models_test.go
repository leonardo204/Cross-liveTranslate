package soniox

import "testing"

func TestParseRealtimeModels(t *testing.T) {
	body := []byte(`{"models":[
		{"id":"stt-async-v6","name":"Async v6","aliased_model_id":null,"transcription_mode":"async"},
		{"id":"stt-rt-v5","name":"Real-time v5","aliased_model_id":null,"transcription_mode":"real_time"},
		{"id":"stt-rt-v4","name":"","aliased_model_id":"stt-rt-v5","transcription_mode":"real_time"},
		{"id":"stt-rt-v6","name":"Real-time v6","aliased_model_id":null,"transcription_mode":"real_time"},
		{"id":"stt-rt-v10","name":"Real-time v10","aliased_model_id":null,"transcription_mode":"real_time"},
		{"id":"stt-rt-preview","name":"","aliased_model_id":null,"transcription_mode":"real_time"}
	]}`)
	got, err := parseRealtimeModels(body)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"stt-rt-v10", "stt-rt-v6", "stt-rt-v5", "stt-rt-preview"}
	if len(got) != len(want) {
		t.Fatalf("got %d models %+v, want %v", len(got), got, want)
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Errorf("models[%d]=%s, want %s", i, got[i].ID, id)
		}
	}
	if got[3].Name != "stt-rt-preview" {
		t.Errorf("빈 이름은 id 로 채워야 한다: %q", got[3].Name)
	}
	if LatestModel(got) != "stt-rt-v10" {
		t.Errorf("LatestModel=%s", LatestModel(got))
	}
	if LatestModel(nil) != DefaultModel {
		t.Errorf("빈 목록이면 DefaultModel")
	}
}
