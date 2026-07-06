//go:build !darwin && !windows

// ducker_other.go — 그 외 플랫폼(linux 등) 원음 덕킹 stub(no-op).
package audio

// NewDucker returns a no-op Ducker (unsupported platform).
func NewDucker() Ducker { return noopDucker{} }
