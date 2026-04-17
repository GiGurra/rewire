package toolexec

import (
	"os"
	"testing"
)

// TestParentStartTime_CurrentParent exercises the per-platform
// implementation against the test runner's actual parent. The value
// is opaque — we only assert it's non-zero and stable across calls.
// On platforms where parentStartTime isn't implemented, the test
// skips rather than failing.
func TestParentStartTime_CurrentParent(t *testing.T) {
	pid := os.Getppid()
	st, err := parentStartTime(pid)
	if err != nil {
		t.Skipf("parentStartTime not supported on this platform: %v", err)
	}
	if st == 0 {
		t.Errorf("expected non-zero start time for live parent, got 0")
	}
}

func TestParentStartTime_StableAcrossCalls(t *testing.T) {
	pid := os.Getppid()
	st1, err := parentStartTime(pid)
	if err != nil {
		t.Skipf("parentStartTime not supported on this platform: %v", err)
	}
	st2, err := parentStartTime(pid)
	if err != nil {
		t.Fatalf("second parentStartTime call failed: %v", err)
	}
	if st1 != st2 {
		t.Errorf("parentStartTime unstable across calls: %d vs %d", st1, st2)
	}
}

// Non-existent PIDs must yield an error — otherwise a stale cache
// could be accepted when our recorded parent has exited (e.g. a
// rewire process somehow outliving its `go` parent). We use a PID
// well beyond any realistic PID_MAX on Linux (4194304), Darwin
// (99998), or Windows (dynamic but well below 2^30) as the sentinel
// "no such process" case.
func TestParentStartTime_NonExistentPIDReturnsError(t *testing.T) {
	const definitelyDeadPID = 1 << 30
	if _, err := parentStartTime(definitelyDeadPID); err == nil {
		t.Errorf("expected error for non-existent PID %d, got nil", definitelyDeadPID)
	}
}

func TestCurrentHeader_PopulatesPIDAndModuleRoot(t *testing.T) {
	moduleRoot := "/some/module/root"
	h, ok := currentHeader(moduleRoot)
	if !ok {
		t.Skip("parentStartTime unavailable on this platform")
	}
	if h.ParentPID != os.Getppid() {
		t.Errorf("ParentPID = %d, want %d", h.ParentPID, os.Getppid())
	}
	if h.ParentStartTime == 0 {
		t.Errorf("ParentStartTime = 0, want non-zero")
	}
	if h.ModuleRoot != moduleRoot {
		t.Errorf("ModuleRoot = %q, want %q", h.ModuleRoot, moduleRoot)
	}
}
