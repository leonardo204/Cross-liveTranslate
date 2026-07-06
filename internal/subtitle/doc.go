// Package subtitle — 자막 엔진(roll-up/dedup/heartbeat).
//
// 구현은 engine.go 참조. 원본 liveTranslate/Sources/Subtitle/SubtitleEngine.swift
// (+ specs/008) 이식. 순수·결정적 — 시간은 Heartbeat(now)로 주입한다.
package subtitle
