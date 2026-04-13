package foo

import (
	"testing"

	"github.com/GiGurra/rewire/example/bar"
	"github.com/GiGurra/rewire/pkg/rewire"
)

// These tests exercise rewire against functions that Go's inliner is
// aggressive about (one-line leaf functions). The goal is to prove that
// the rewritten wrapper still takes effect at inlined call sites, and
// that rewire.Real still returns the original body.
//
// We verify the inliner actually is inlining these functions in a
// separate script (see scripts/check-inlining.sh).

// Baseline: the real implementation works when the wrapper is present
// but no mock is installed. Exercises the fast path through the wrapper.
func TestInlining_BaselineNoMock(t *testing.T) {
	// bar.TinyDouble must still be referenced by at least one rewire call
	// in this file so the pre-scan rewrites it. A plain Real reference
	// (no Func) is enough — see the scanner.
	_ = rewire.Real(t, bar.TinyDouble)

	if got := QuadrupleViaTinyDouble(3); got != 12 {
		t.Errorf("unmocked QuadrupleViaTinyDouble(3) = %d, want 12", got)
	}
	if got := SumViaTinyAdd(1, 2, 3); got != 6 {
		t.Errorf("unmocked SumViaTinyAdd(1,2,3) = %d, want 6", got)
	}
}

// Mocking a tiny leaf function must take effect at every call site,
// including inlined ones. QuadrupleViaTinyDouble calls bar.TinyDouble
// twice — both calls must go through the mock.
func TestInlining_MockTinyDoubleAppliesAtEveryCallSite(t *testing.T) {
	calls := 0
	rewire.Func(t, bar.TinyDouble, func(x int) int {
		calls++
		return x + 100
	})

	got := QuadrupleViaTinyDouble(3)
	// First call:  TinyDouble(3)   → 3 + 100 = 103
	// Second call: TinyDouble(103) → 103 + 100 = 203
	if got != 203 {
		t.Errorf("QuadrupleViaTinyDouble(3) = %d, want 203 "+
			"(if inlining bypassed the mock, you'd see 12)", got)
	}
	if calls != 2 {
		t.Errorf("mock was called %d times, want 2 — inlined call site may have bypassed rewire", calls)
	}
}

// Same scenario for a two-arg function.
func TestInlining_MockTinyAddAppliesAtEveryCallSite(t *testing.T) {
	calls := 0
	rewire.Func(t, bar.TinyAdd, func(a, b int) int {
		calls++
		return a*1000 + b
	})

	got := SumViaTinyAdd(1, 2, 3)
	// First call:  TinyAdd(1, 2)    → 1*1000 + 2 = 1002
	// Second call: TinyAdd(1002, 3) → 1002*1000 + 3 = 1002003
	if got != 1002003 {
		t.Errorf("SumViaTinyAdd(1,2,3) = %d, want 1002003 "+
			"(if inlining bypassed the mock, you'd see 6)", got)
	}
	if calls != 2 {
		t.Errorf("mock was called %d times, want 2", calls)
	}
}

// Spy case: rewire.Real returns a reference to the pre-rewrite body.
// Even if the compiler inlines _real_TinyDouble into the wrapper, the
// exported Real_TinyDouble variable must still be a usable function value.
func TestInlining_RealReturnsUsableReference(t *testing.T) {
	realDouble := rewire.Real(t, bar.TinyDouble)

	if got := realDouble(7); got != 14 {
		t.Errorf("real TinyDouble(7) = %d, want 14", got)
	}

	// And the spy pattern composes correctly.
	rewire.Func(t, bar.TinyDouble, func(x int) int {
		return realDouble(x) + 1
	})
	if got := QuadrupleViaTinyDouble(3); got != 15 {
		// TinyDouble(3)  → 6 + 1 = 7
		// TinyDouble(7)  → 14 + 1 = 15
		t.Errorf("spy QuadrupleViaTinyDouble(3) = %d, want 15", got)
	}
}
