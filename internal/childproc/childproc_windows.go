//go:build windows

package childproc

import (
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// job is a process-wide Job Object created lazily on first Supervise. It is
// configured with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE so that when this (parent)
// process exits and its last handle to the job closes, Windows terminates every
// process still assigned to the job — the overlay/settings children. The handle
// is intentionally never closed: it lives for the parent process lifetime.
var (
	jobOnce sync.Once
	jobH    windows.Handle
	jobErr  error
)

func ensureJob() (windows.Handle, error) {
	jobOnce.Do(func() {
		h, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			jobErr = err
			return
		}
		// Kill all assigned processes when the last job handle closes (parent exit).
		var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
		if _, err := windows.SetInformationJobObject(
			h,
			windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)),
			uint32(unsafe.Sizeof(info)),
		); err != nil {
			windows.CloseHandle(h)
			jobErr = err
			return
		}
		jobH = h
	})
	return jobH, jobErr
}

// supervise opens a handle to the child process and assigns it to the kill-on-
// close job. Best-effort: any failure leaves the explicit shutdown() kill as the
// fallback path.
func supervise(pid int) {
	h, err := ensureJob()
	if err != nil || h == 0 {
		return
	}
	// PROCESS_SET_QUOTA|PROCESS_TERMINATE are the rights AssignProcessToJobObject needs.
	const access = windows.PROCESS_SET_QUOTA | windows.PROCESS_TERMINATE
	ph, err := windows.OpenProcess(access, false, uint32(pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(ph)
	_ = windows.AssignProcessToJobObject(h, ph)
}
