package gemini

import (
	"encoding/json"
	"testing"
)

// setup 직렬화에서 translationConfig가 generationConfig 내부에 위치하고
// top-level에는 없어야 한다(specs/002 A.2: top-level은 1007 거부).
func TestBuildSetup_NestedTranslationConfig(t *testing.T) {
	msg := BuildSetup("models/gemini-3.5-live-translate-preview", "ko", "auto", false, "")
	b, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// 구조를 map으로 재파싱해 위치를 검사.
	var root map[string]any
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	setup, ok := root["setup"].(map[string]any)
	if !ok {
		t.Fatalf("setup 객체 없음: %s", b)
	}

	// top-level translationConfig 금지.
	if _, exists := setup["translationConfig"]; exists {
		t.Errorf("translationConfig가 top-level(setup)에 있음 — 1007 거부 원인: %s", b)
	}

	gc, ok := setup["generationConfig"].(map[string]any)
	if !ok {
		t.Fatalf("generationConfig 없음: %s", b)
	}
	tc, ok := gc["translationConfig"].(map[string]any)
	if !ok {
		t.Fatalf("translationConfig가 generationConfig 내부에 없음: %s", b)
	}
	if tc["targetLanguageCode"] != "ko" {
		t.Errorf("targetLanguageCode: got %v want ko", tc["targetLanguageCode"])
	}
	if tc["echoTargetLanguage"] != true {
		t.Errorf("echoTargetLanguage: got %v want true", tc["echoTargetLanguage"])
	}
	// source=auto → sourceLanguageCode 키 생략.
	if _, exists := tc["sourceLanguageCode"]; exists {
		t.Errorf("source=auto인데 sourceLanguageCode가 전송됨: %s", b)
	}
	// responseModalities = ["AUDIO"].
	rm, ok := gc["responseModalities"].([]any)
	if !ok || len(rm) != 1 || rm[0] != "AUDIO" {
		t.Errorf("responseModalities: got %v want [AUDIO]", gc["responseModalities"])
	}
	// outputAudioTranscription 항상 존재.
	if _, exists := setup["outputAudioTranscription"]; !exists {
		t.Errorf("outputAudioTranscription 누락: %s", b)
	}
	// requestInputTranscription=false → inputAudioTranscription 키 생략.
	if _, exists := setup["inputAudioTranscription"]; exists {
		t.Errorf("inputAudioTranscription이 요청되지 않았는데 전송됨: %s", b)
	}
	// realtimeInputConfig.automaticActivityDetection.disabled=false (서버 VAD ON).
	ric, ok := setup["realtimeInputConfig"].(map[string]any)
	if !ok {
		t.Fatalf("realtimeInputConfig 누락: %s", b)
	}
	aad, ok := ric["automaticActivityDetection"].(map[string]any)
	if !ok || aad["disabled"] != false {
		t.Errorf("automaticActivityDetection.disabled: got %v want false", aad)
	}
}

// -show-source(requestInputTranscription=true) 시 inputAudioTranscription={} 포함.
func TestBuildSetup_InputTranscriptionAndSource(t *testing.T) {
	msg := BuildSetup("m", "ja", "en", true, "handle-xyz")
	b, _ := json.Marshal(msg)
	var root map[string]any
	_ = json.Unmarshal(b, &root)
	setup := root["setup"].(map[string]any)

	if _, exists := setup["inputAudioTranscription"]; !exists {
		t.Errorf("inputAudioTranscription 누락(요청됨): %s", b)
	}
	gc := setup["generationConfig"].(map[string]any)
	tc := gc["translationConfig"].(map[string]any)
	if tc["sourceLanguageCode"] != "en" {
		t.Errorf("sourceLanguageCode: got %v want en", tc["sourceLanguageCode"])
	}
	// 핸들 전달 시 sessionResumption.handle 포함.
	sr := setup["sessionResumption"].(map[string]any)
	if sr["handle"] != "handle-xyz" {
		t.Errorf("sessionResumption.handle: got %v want handle-xyz", sr["handle"])
	}
}

// 핸들 없으면 sessionResumption은 {} (handle 키 생략).
func TestBuildSetup_EmptyResumption(t *testing.T) {
	msg := BuildSetup("m", "ko", "", false, "")
	b, _ := json.Marshal(msg)
	var root map[string]any
	_ = json.Unmarshal(b, &root)
	setup := root["setup"].(map[string]any)
	sr, ok := setup["sessionResumption"].(map[string]any)
	if !ok {
		t.Fatalf("sessionResumption 누락: %s", b)
	}
	if _, exists := sr["handle"]; exists {
		t.Errorf("빈 핸들인데 handle 키 존재: %s", b)
	}
}

// realtimeInput 오디오 메시지 직렬화 형태 검증.
func TestBuildAudioMessage(t *testing.T) {
	b, _ := json.Marshal(BuildAudioMessage("QUJD"))
	var root map[string]any
	_ = json.Unmarshal(b, &root)
	ri := root["realtimeInput"].(map[string]any)
	audio := ri["audio"].(map[string]any)
	if audio["data"] != "QUJD" {
		t.Errorf("data: got %v", audio["data"])
	}
	if audio["mimeType"] != "audio/pcm;rate=16000" {
		t.Errorf("mimeType: got %v want audio/pcm;rate=16000", audio["mimeType"])
	}
}

// 대표 수신 JSON들이 올바른 필드로 언마샬/디스패치되는지 검증.
func TestServerMessage_Dispatch(t *testing.T) {
	cases := []struct {
		name   string
		json   string
		verify func(t *testing.T, m ServerMessage)
	}{
		{
			name: "setupComplete",
			json: `{"setupComplete":{}}`,
			verify: func(t *testing.T, m ServerMessage) {
				if m.SetupComplete == nil {
					t.Errorf("setupComplete nil")
				}
			},
		},
		{
			name: "outputTranscription(번역 delta)",
			json: `{"serverContent":{"outputTranscription":{"text":"안녕하세요"}}}`,
			verify: func(t *testing.T, m ServerMessage) {
				if m.ServerContent == nil || m.ServerContent.OutputTranscription == nil ||
					m.ServerContent.OutputTranscription.Text != "안녕하세요" {
					t.Errorf("outputTranscription 파싱 실패: %+v", m.ServerContent)
				}
			},
		},
		{
			name: "inputTranscription(원문 delta)",
			json: `{"serverContent":{"inputTranscription":{"text":"hello","languageCode":"en-US"}}}`,
			verify: func(t *testing.T, m ServerMessage) {
				it := m.ServerContent.InputTranscription
				if it == nil || it.Text != "hello" || it.LanguageCode != "en-US" {
					t.Errorf("inputTranscription 파싱 실패: %+v", it)
				}
			},
		},
		{
			name: "modelTurn(출력 오디오)",
			json: `{"serverContent":{"modelTurn":{"parts":[{"inlineData":{"data":"AAECAw==","mimeType":"audio/pcm;rate=24000"}}]}}}`,
			verify: func(t *testing.T, m ServerMessage) {
				parts := m.ServerContent.ModelTurn.Parts
				if len(parts) != 1 || parts[0].InlineData == nil ||
					parts[0].InlineData.MimeType != "audio/pcm;rate=24000" {
					t.Errorf("modelTurn 파싱 실패: %+v", parts)
				}
			},
		},
		{
			name: "turn/generation 경계",
			json: `{"serverContent":{"turnComplete":true,"generationComplete":true,"interrupted":true}}`,
			verify: func(t *testing.T, m ServerMessage) {
				c := m.ServerContent
				if !c.TurnComplete || !c.GenerationComplete || !c.Interrupted {
					t.Errorf("경계 플래그 파싱 실패: %+v", c)
				}
			},
		},
		{
			name: "usageMetadata(AUDIO 우선)",
			json: `{"usageMetadata":{"totalTokenCount":100,"responseTokenCount":40,"responseTokensDetails":[{"modality":"AUDIO","tokenCount":30}]}}`,
			verify: func(t *testing.T, m ServerMessage) {
				if m.UsageMetadata == nil {
					t.Fatalf("usageMetadata nil")
				}
				if got := m.UsageMetadata.OutputAudioTokens(); got != 30 {
					t.Errorf("OutputAudioTokens: got %d want 30 (AUDIO 우선)", got)
				}
			},
		},
		{
			name: "usageMetadata(폴백)",
			json: `{"usageMetadata":{"totalTokenCount":100,"responseTokenCount":40}}`,
			verify: func(t *testing.T, m ServerMessage) {
				if got := m.UsageMetadata.OutputAudioTokens(); got != 40 {
					t.Errorf("OutputAudioTokens 폴백: got %d want 40", got)
				}
			},
		},
		{
			name: "goAway",
			json: `{"goAway":{"timeLeft":"5s"}}`,
			verify: func(t *testing.T, m ServerMessage) {
				if m.GoAway == nil || m.GoAway.TimeLeft != "5s" {
					t.Errorf("goAway 파싱 실패: %+v", m.GoAway)
				}
			},
		},
		{
			name: "sessionResumptionUpdate",
			json: `{"sessionResumptionUpdate":{"newHandle":"h1","resumable":true}}`,
			verify: func(t *testing.T, m ServerMessage) {
				u := m.SessionResumptionUpdate
				if u == nil || u.NewHandle != "h1" || !u.Resumable {
					t.Errorf("sessionResumptionUpdate 파싱 실패: %+v", u)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var m ServerMessage
			if err := json.Unmarshal([]byte(tc.json), &m); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			tc.verify(t, m)
		})
	}
}
