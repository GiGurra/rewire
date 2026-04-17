package toolexec

import "golang.org/x/sys/unix"

// parentStartTime returns the given process's start time as Unix
// seconds, via sysctl kern.proc.pid.<N>. The value is opaque to the
// caller — it's only ever compared for equality against another
// value produced by this same function on the same machine.
//
// The timeval's microsecond component is deliberately discarded:
// we only need equality against a value we produced ourselves, and
// whole seconds is enough to distinguish any two processes that
// aren't started in the same second (and two different processes
// getting the same PID within the same wall-clock second would
// require the first to have exited within that second, which is
// extraordinarily rare — and even in that case, the ModuleRoot
// check in cacheHeader catches the cross-project instance).
func parentStartTime(pid int) (int64, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, err
	}
	return kp.Proc.P_starttime.Sec, nil
}
