//go:build !windows

package childproc

// supervise is a no-op on non-Windows platforms. macOS/Linux 자식 정리는
// controller.shutdown()의 명시적 Process.Kill()이 담당한다.
func supervise(pid int) {}
