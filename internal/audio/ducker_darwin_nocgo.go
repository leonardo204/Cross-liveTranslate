//go:build darwin && !cgo

// ducker_darwin_nocgo.go — CGO 비활성 darwin 빌드용 no-op 덕커.
// (실제 CoreAudio 덕킹은 ducker_darwin.go, `darwin && cgo`에서 제공.)
package audio

// NewDucker returns a no-op Ducker (darwin without cgo).
func NewDucker() Ducker { return noopDucker{} }
