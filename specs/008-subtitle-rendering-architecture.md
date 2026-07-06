> **[이식 상태]** 그대로 이식(유효) · 대상 스택: Go + Wails v2 (Windows/macOS)
> roll-up/dedup/STT heartbeat 무음정리 로직은 언어 무관 이식. 오버레이 창만 SwiftUI→네이티브 창 shim(mac NSPanel/win layered)으로 대체.
> 원본(macOS/Swift): `liveTranslate/specs/008-subtitle-rendering-architecture.md` · 마스터 플랜: [000](000-cross-platform-porting-plan.md)

---

---
id: subtitle-rendering-architecture
title: 실시간 자막 표시 아키텍처 (누적/세그먼트 통합 roll-up + 무음 처리 + 렌더링)
type: design
version: 1.0.0
status: stable
scope: 스트리밍 STT/MT/LLM-번역 출력(증분 delta 또는 세그먼트)을 영화 자막식으로 표시하는 엔진의 설계 기준. 표시 모델(roll-up), 무음 정리, 글로우 렌더링, 프레임 드랍 완화, 중복 제거, 타이밍 상수, 그리고 재사용 체크리스트. 향후 유사 실시간 자막 앱의 참조 표준.
related: [gemini-live-translate-and-audio, apple-speech-offline-translate, translation-pipeline-architecture, logging-policy]
updated: 2026-06-23
---

# 실시간 자막 표시 아키텍처

> **목적**: 이 문서는 liveTranslate의 자막 엔진(`SubtitleEngine`) + 오버레이 뷰(`SubtitleOverlayView`/
> `SubtitleStyle`/`SubtitleOverlayController`)의 설계·핵심 결정·시행착오를 정리한다. 엔진/뷰 구현은
> 코드에 있으므로 여기선 **"왜 이렇게 했는가"** 와 **재사용 시 따라야 할 불변식**에 집중한다.
> 새 앱에서 실시간 자막을 만들 때 이 문서를 기준으로 같은 함정을 피하라.

핵심 구현 파일:
- `Sources/Subtitle/SubtitleEngine.swift` — 텍스트 상태 + 표시 타이밍(`@MainActor @Observable`)
- `Sources/Overlay/SubtitleOverlayView.swift` — SwiftUI 본문(roll-up 클립 + 세로 배치)
- `Sources/Overlay/SubtitleStyle.swift` — 스타일 해석 + 공유 텍스트 렌더(`StyledSubtitleText`)
- `Sources/Overlay/SubtitleOverlayController.swift` — 오버레이 창 수명/위치/표시 정책

---

## 1. 두 가지 입력 모델 — 누적(delta) vs 세그먼트(replace)

실시간 번역 엔진의 텍스트 출력은 **두 종류**다. 자막 엔진은 둘 다 받아 **하나의 roll-up 표시**로 수렴시킨다.

| 모델 | 출처 | 동작 | 수신 메서드 |
|------|------|------|-------------|
| **누적(delta)** | Gemini Live 등 LLM 스트리밍 | 조각을 **이어붙여(append)** 문장이 자람 | `ingestTranslationDelta` / `ingestSourceDelta` |
| **세그먼트(replace)** | Apple SpeechTranscriber/Translation 등 | "현재 가설 전체(volatile)"를 매번 **교체**, `isFinal`로 확정 | `ingestSegment(translation:source:isFinal:)` |

> **불변식**: 한 세션에서 두 경로가 섞이지 않는다(엔진 종류가 둘 중 하나만 방출). 단, **표시 모델은 통일**한다(§3).

### 1.1 입력별 특성과 주의점

- **delta**: 같은 조각을 중복/재전송하거나(모델 반복·오디오 피드백), 누적 형태로 보내기도 한다 → **겹침 억제 머지** 필요(`appendIfMeaningful`: buffer 접미사 ↔ delta 접두사 최대 겹침 k 제거 후 새 부분만 append). 증분(k=0)/누적(전체 겹침)/경계 중복을 한 번에 처리.
- **세그먼트**: volatile은 매 오디오 버퍼마다 **전체 텍스트를 반복 갱신**한다(같은 텍스트도 재방출). 이 고빈도 갱신이 **무음 감지의 heartbeat**가 된다(§5).

---

## 2. 아키텍처 — 엔진/뷰/컨트롤러 분리

```
PipelineEvent (provider) ─▶ AppState.handle(_:)  [MainActor]
   ├ .translatedText/.sourceText(delta)  ─▶ SubtitleEngine.ingest*Delta
   ├ .translatedSegment/.sourceSegment   ─▶ SubtitleEngine.ingestSegment
   ├ .turnComplete/.generationComplete    ─▶ SubtitleEngine.ingest*
   └ (오디오) AudioInputManager.onSpeechStateChange ─▶ SubtitleEngine.noteSpeechActivity

SubtitleEngine (@Observable)  ──관찰──▶  SubtitleOverlayView (SwiftUI)
   - displayTranslation / displaySource / isVisible            - 창은 SubtitleOverlayController가
   - rollupLines / currentTranslation / segmentMode              표시/숨김/배치(캡처 정책 기반)
```

**역할 경계(불변식):**
- 엔진은 **"무엇을, 언제까지 보일지"** 만 결정한다(텍스트 상태 + isVisible). 페이드/위치/클립은 뷰가 한다.
- 컨트롤러는 **창 수명/위치/표시 정책**만. 텍스트 내용·가시성 전이는 모른다.
- 창 자체의 표시/숨김은 **캡처 상태**(`applyCapturePolicy(isCapturing:)`)로 결정하고, 텍스트의 **opacity**는
  `engine.isVisible && !displayTranslation.isEmpty`로 결정한다. 이 둘은 **별개 축**이다.

---

## 3. 표시 모델 — 통합 roll-up (B안)

영화 자막처럼 **최근 N줄을 누적해 보여주고**, 새 줄이 들어오면 위로 굴러간다(roll-up). 문장마다 사라지지 않는다.

### 3.1 상태

- `rollupLines: [String]` — 확정 줄들의 FIFO. 상한 `maxRollupHistory`(메모리 가드, 기본 12).
- `currentTranslation: String` — 진행 중(아직 확정 안 된) 줄.
- `segmentMode: Bool` — roll-up 표시 사용 여부. **두 입력 경로 모두 true로 켠다.**

### 3.2 표시 텍스트 계산 (`displayTranslation`)

```
segmentMode면:  rollupLines.suffix(maxLines+2) + (currentTranslation 비었으면 [] else [currentTranslation])  → "\n"으로 join
아니면(폴백):    currentTranslation 우선, 비면 confirmedTranslation   // 테스트 미리보기 등
```

- **`suffix(maxLines+2)`**: 뷰가 어차피 마지막 `maxLines` 시각줄만 클립하므로, 그리기에 넘기는 문장도 그만큼만
  추린다. 누적 전체(12문장 × 글로우 5겹)를 offscreen 렌더 후 버리던 비용을 줄여 **프레임 드랍을 완화**(§6.2).
  `+2`는 줄바꿈을 감안한 안전 마진(짧은 문장=문장당 1줄, 긴 문장=문장당 여러 줄 모두 maxLines 충족).

### 3.3 확정 → roll-up push

- **세그먼트 경로**: `ingestSegment(isFinal: true)` → dedup/collapse 후 `rollupLines`에 직접 push(직전과 동일하면 무시).
- **delta 경로**: `confirmTurn(reason:)`이 charBreak/turnComplete/무음 fallback에서 호출 → 같은 정리 후 push.
  - **과거 모델 폐기**: delta 경로는 원래 `confirmed`+`holdSeconds(2s)` 페이드(1~2줄 교체)였다. 이를 제거하고
    roll-up FIFO로 통일했다. (히스토리: 커밋 756a560 → revert → reapply 32c51fc. 통일이 정답이었음.)

### 3.4 길이 기반 분절 (charBreak)

- 진행 줄이 `maxCharsBeforeBreak`를 넘으면 즉시 확정(push)하고 다음 줄로. **줄임표 없이** 자연 분절.
- 임계 = `charsPerSubtitleLine × subtitleMaxLines`(줄수 설정과 연동) vs 사용자 지정값 중 **큰 값**.
  → 줄수를 2→3으로 올리면 누적 임계도 56→84로 커져 실제로 더 많은 줄이 누적·표시된다(과거엔 고정 50이라
  줄수를 올려도 1줄만 보이던 버그가 있었음).

---

## 4. 중복 제거 — collapseRepeats + dedupGlobalSentences

스트리밍 번역은 **반복**을 잘 흘린다(모델 반복, 오디오 피드백 루프, charBreak 경계 잔여). 2단 정리:

1. **`collapseRepeats`**: 공백 토큰열 **끝의 연속 반복 부분열**을 1회로 붕괴. 최소 3토큰부터(우연한 1~2토큰 보호).
   예: `"A B C A B C"` → `"A B C"`. 한국어(어절)/영어(단어) 혼합 모두 공백 토큰으로 동작.
2. **`dedupGlobalSentences`**: 종결부호(`. ! ? 。 ！ ？`) 기준 문장 분리 후 **이미 등장한 문장의 재등장** 제거(첫 등장만 유지).
   - **진행 중 마지막 조각(종결부호로 안 끝남)은 절대 제거 안 함**(성장 중).
   - 너무 짧은 문장(공백 제거 후 <4자)은 dedup 제외("네." "음." 보호).

> 적용 지점: append 시(`appendIfMeaningful`), 확정 시(`confirmTurn`/`ingestSegment` final). **확정 직전 한 번 더** 적용하는 이유: charBreak가 반복 구 완성 전에 끊어 잔여가 남는 경우 방지.

---

## 5. 무음 처리 — STT heartbeat (VAD offset 비의존)

자막을 **언제 비울지**가 가장 까다롭다. 핵심 결론: **VAD 발화 종료(offset)에 의존하지 말고, "엔진이 마지막으로 무언가를 낸 뒤 경과 시간"(heartbeat)으로 판단하라.**

### 5.1 정책

- 일반 대화의 **짧은 공백(2~3초)** → 자막 **유지·누적**(roll-up 지속).
- **연속 무음 N초(`rollupSilenceClearSeconds`, 기본 8s)** 이상일 때만 → 화면 비움 + `rollupLines` 정리.
  다음 발화는 **깨끗한 화면에서 새로 시작**(이전 줄과 합쳐지지 않음 — 일반 자막 앱의 표준).

### 5.2 구현 — `armSilenceClear`

- **모든 ingest(delta/segment)마다 재무장**(cancel + 8s 타이머 새로). 사실상 "마지막 출력 후 경과 시간"을 잰다.
- STT는 말하는 동안 매 오디오 버퍼마다 volatile을 흘리므로(고빈도 heartbeat), 발화가 이어지는 한 타이머가
  계속 재설정돼 **발동하지 않는다**. STT가 임계 동안 **완전히 조용할 때만** 발동 → 진짜 무음에서만 정리.

### 5.3 왜 VAD offset이 아닌가 (시행착오)

- 시스템 오디오 탭(연속 입력/노이즈) 환경에서 **VAD offset이 신뢰 불가**하게 "발화중"으로 굳는 사례 관측
  (세션 내내 offset 미발생, 종료 시 `speaking=true`). VAD offset 기반 정리는 **발동을 못 하거나** 엉뚱하게 발동.
- 초기엔 VAD 기반(speechActive 가드 + 2단 hide/clear)으로 시도 → **발화 도중 자막이 통째로 사라지는 버그**.
  원인: ① VAD onset이 첫 세그먼트보다 먼저 와 `segmentMode` 가드에 걸려 무시됨 ② mid-session reset이 상태를
  고착 ③ `showSource=false`면 원문 volatile이 방출 안 돼 heartbeat 단절. → **STT heartbeat로 전환해 전부 해소.**
- **VAD의 잔존 역할**: `noteSpeechActivity(speaking:)`는 **발화 시작(onset) 시 즉시 표시를 켜는 보조**만. offset은 안 씀.

### 5.4 heartbeat 전제 — 원문 세그먼트 항상 방출

`ComposedTranslationProvider`는 원문(STT) 세그먼트를 **`showSource`와 무관하게 항상** `.sourceSegment`로 방출한다.
화면 표시는 뷰가 `showSourceText`로 게이팅하지만, 이 volatile 스트림이 **무음 감지 heartbeat**다.
showSource=false라고 방출을 막으면 final 사이가 벌어질 때 heartbeat가 끊겨 무음 타이머가 발화 중 오발동한다.

---

## 6. 렌더링

### 6.1 글로우 클립 순서 — 클립 → 효과 (필수)

roll-up은 **마지막 N 시각줄만** 보이도록 클립한다. 외곽선/글로우(다중 `.shadow`)와 클립의 **순서가 핵심**이다.

- ❌ **효과 먼저 → 클립**: 위로 굴러 사라진 줄의 **아래 방향 그림자**가 클립 경계 *아래*(보이는 영역)로 새어,
  글자 없는 **글로우 띠**가 남는다(`.clipped()`는 사각 클립이라 영역 안으로 침범한 그림자를 못 지움).
- ✅ **클립 먼저(글자만) → 효과**: 잘려나간 줄 글자가 클립 단계에서 제거된 뒤 그림자를 계산 → 잔상 없음.

구현: `StyledSubtitleText(clipToBottomLines:)` 경로에서
`Text → lineLimit(nil) → fixedSize(vertical) → frame(maxHeight: lineHeight×N, .bottom) → clipped() → stroke → glow`.
(`fixedSize` 없이 `frame(maxHeight:)`만 쓰면 Text가 제안 높이에 맞춰 **앞(오래된)** 줄만 남겨 최신이 안 보인다 — 반드시 전체 렌더 강제 후 하단 클립.)

### 6.2 프레임 드랍 완화

- **줄 푸시마다 전체 박스 크로스페이드 금지**: `.animation(value: translation)` 제거. 글로우 5겹 멀티라인을 0.25s
  매 프레임 보간 렌더하면 roll-up이 굴러갈 때마다 버벅인다. **표시/숨김(isVisible) 페이드만** 유지.
- **안 보이는 줄 렌더 최소화**: `displayTranslation`이 `suffix(maxLines+2)`만 넘김(§3.2).
- `.drawingGroup()`는 **쓰지 말 것**: 뷰 bounds로 래스터화하면서 §6.1에서 살린 글로우가 다시 잘릴 위험.

### 6.3 세로 배치 + 실측 높이

- 화면을 상/중/하 3등분, 영역 내 offset(0~1)으로 연속 이동. 전역 비율 `t=(영역+offset)/3`, `travel=화면높이−박스높이`,
  `topPad=travel×t`. t=1이면 박스 하단이 화면 바닥에 정확히 닿는다.
- 박스 높이는 **PreferenceKey 실측값** 우선(짧은 1줄 자막도 바닥까지 정확히). 미측정 시 추정값 폴백.
- 스타일/위치는 뷰가 `@Observable settings`를 **직접 관찰** → 슬라이더 드래그도 호스팅 뷰 재생성 없이 즉시 반영.

---

## 7. 타이밍 상수 (튜닝 지점)

| 상수 | 기본 | 의미 |
|------|------|------|
| `rollupSilenceClearSeconds` | 8.0s | 연속 무음 이 시간 → 화면 비움 + 누적 정리. 일반 공백(2~3s)보다 충분히 큰 마진. |
| `silenceTimeout` | 2.0s | delta 경로: 마지막 조각 후 이 시간 무음이면 진행 줄을 자동 확정(roll-up push). |
| `maxRollupHistory` | 12 | rollupLines 메모리 상한(표시 줄수 제한은 뷰가 별도로 클립). |
| `maxCharsBeforeBreak` | `charsPerLine×maxLines` | 진행 줄 길이 임계(줄수 설정 연동). |

---

## 8. 핵심 함정 / 교훈 (이 세션에서 실제로 밟은 것들)

1. **동일 언어쌍 번역**: src==tgt(예 ko→ko)은 Apple Translation이 거부. 폴백 `?? text`로 **원문이 번역 슬롯에 누출**.
   → 엔진 단계에서 base 언어 비교 후 **번역 생략(전사 통과)**. 에러 스팸 제거 + 동일언어 입력 우아하게 처리.
2. **무음 기준을 cadence/VAD offset에 두면 발화 중 사라진다** → STT heartbeat(§5).
3. **글로우 잔상** → 클립을 효과보다 먼저(§6.1).
4. **roll-up 크로스페이드 버벅임** → 텍스트 변경 애니메이션 제거(§6.2).
5. **표시 모델 분기(delta=1줄, segment=roll-up)는 일관성 붕괴** → 둘 다 roll-up으로 통일(§3.3).
6. **stale 빌드 주의**: 코드 변경 후 **재빌드+앱 재시작** 안 하면 옛 동작이 그대로 — "안 됨"의 흔한 오인 원인.
   동작 검증 전 반드시 `make build` + 앱 재실행.
7. **isVisible(텍스트 opacity)와 창 표시(캡처 정책)는 별개 축** — 한쪽만 보고 "안 보임"을 진단하지 말 것(§2).

---

## 9. 재사용 체크리스트 (새 앱에서 자막 구현 시)

- [ ] 입력이 **누적(delta)** 인가 **세그먼트(replace)** 인가? 둘 다면 표시 모델을 **하나로 통일**.
- [ ] **겹침 억제 머지** + **2단 dedup**(즉시 반복 / 전역 문장)으로 반복/피드백 방어.
- [ ] roll-up FIFO + **마지막 N 시각줄 클립**(데이터는 `suffix(N+2)`만 렌더).
- [ ] 무음 정리는 **출력 heartbeat 기반**(매 ingest 재무장, N초 무음에만 발동). **VAD offset 의존 금지.**
- [ ] heartbeat 단절 방지: 표시 안 하는 신호(원문 volatile 등)도 **항상 방출**.
- [ ] 글로우/외곽선은 **클립 → 효과** 순서.
- [ ] 텍스트 변경에 **전체 크로스페이드 금지**(표시/숨김 페이드만).
- [ ] 엔진(상태/타이밍) · 뷰(렌더/위치) · 컨트롤러(창) **역할 분리**, isVisible과 창 표시 **별개 축**.
- [ ] 동일 언어쌍·미지원 조합은 **우아한 폴백**(전사 통과 등), 에러 스팸/누출 차단.
- [ ] 검증: **빌드+재실행** 후 실로그로 확인(무음 타이머가 진짜 침묵에만 찍히는지 등).

---

*최종 업데이트: 2026-06-23 — Apple(세그먼트)·Gemini(delta) 양 경로 roll-up 통일 완료 시점 기준.*
