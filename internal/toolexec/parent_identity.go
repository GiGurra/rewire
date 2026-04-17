package toolexec

import "os"

// cacheHeader is the identity tuple written into every scan cache
// file. On read, we recompute the header for the current environment
// and compare; any mismatch means the cache belongs to a different
// build session and must be treated as absent.
//
// ParentPID + ParentStartTime is the canonical "same process"
// identifier on Unix and Windows — a reissued PID will not carry the
// same start time within one boot, so (pid, starttime) uniquely
// identifies a process. ModuleRoot guards the cross-project case:
// if /tmp/rewire-<ppid>/ happens to exist from an earlier build in a
// different module but gets hit by a process with the same (ppid,
// starttime) for some unexpected reason, the module root mismatch
// still invalidates the cache.
type cacheHeader struct {
	ParentPID       int    `json:"parentPid"`
	ParentStartTime int64  `json:"parentStartTime"`
	ModuleRoot      string `json:"moduleRoot"`
}

// currentHeader computes the identity tuple for the current rewire
// invocation. Returns ok=false if the parent process's start time
// can't be determined — the caller should then bypass the cache
// entirely (scan on every invocation) rather than write an
// unverifiable identity to disk.
//
// ParentStartTime values are opaque and only meaningful within one
// boot on one platform. Linux returns jiffies since boot, Darwin
// returns Unix seconds, Windows returns Unix nanoseconds — the only
// comparison performed is equality against a value produced by the
// same function on the same machine, so the encoding difference is
// irrelevant.
func currentHeader(moduleRoot string) (cacheHeader, bool) {
	ppid := os.Getppid()
	st, err := parentStartTime(ppid)
	if err != nil {
		return cacheHeader{}, false
	}
	return cacheHeader{
		ParentPID:       ppid,
		ParentStartTime: st,
		ModuleRoot:      moduleRoot,
	}, true
}
