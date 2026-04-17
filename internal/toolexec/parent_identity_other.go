//go:build !linux && !darwin && !windows

package toolexec

import (
	"fmt"
	"runtime"
)

// parentStartTime is unimplemented on platforms other than Linux,
// Darwin, and Windows. Returning an error causes currentHeader to
// fail, which makes loadOrScanMockTargets bypass the cache entirely
// (scan every invocation). Slower, but always correct.
func parentStartTime(pid int) (int64, error) {
	return 0, fmt.Errorf("rewire: parentStartTime not implemented on %s", runtime.GOOS)
}
