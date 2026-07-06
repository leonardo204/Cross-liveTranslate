// Package app — AppState 오케스트레이터 + reconciler(epoch 펜싱): 활성 provider ≤1, teardown 무중첩.
//
// reconciler.go: desired/actual 상태머신 + 세대 토큰(epoch) 기반 stale 이벤트 폐기.
// pipeline.Provider와 audio.Source를 팩토리 주입으로 오케스트레이트한다(P2a).
// See specs/010-p2-loopback-reconciler-subtitle.md `internal/app` 절.
package app
