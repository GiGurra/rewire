package toolexec

import "golang.org/x/sys/windows"

// parentStartTime opens a limited handle to the given process and
// reads its creation time via GetProcessTimes. Returned as Unix
// nanoseconds; opaque to the caller — only equality against a value
// from this same function matters.
//
// PROCESS_QUERY_LIMITED_INFORMATION is the minimal access right
// available from Windows Vista onward and suffices for reading
// timing information without elevation. If the process has exited or
// we lack access, OpenProcess fails and the cache is invalidated.
func parentStartTime(pid int) (int64, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(h)
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return 0, err
	}
	return creation.Nanoseconds(), nil
}
