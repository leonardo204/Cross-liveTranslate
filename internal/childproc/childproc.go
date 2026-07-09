// Package childproc ties spawned child processes to the parent's lifetime so
// they cannot outlive it (no orphaned overlay/settings processes after the app
// quits or is force-killed).
//
// On Windows this uses a Job Object with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE: the
// OS terminates every process assigned to the job when the last job handle
// closes — which happens automatically when the parent process exits, even on an
// abrupt Task Manager kill. On other platforms it is a no-op (macOS relies on
// the controller's shutdown() explicitly killing children).
package childproc

// Supervise assigns the child process with the given PID to the parent-lifetime
// job so it is killed when the parent exits. Best-effort: failures are ignored
// (the explicit shutdown() kill remains the primary path). No-op off Windows.
func Supervise(pid int) { supervise(pid) }
