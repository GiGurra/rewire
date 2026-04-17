package rewire

// Gating probe for the pprof-labels-as-goroutine-identity approach.
// See plans/parallel_test_safety_design.md. This file answers one
// narrow question before any real work happens: can we read the
// current goroutine's pprof labels pointer via go:linkname on this
// Go version?
//
// If yes, we have a path to goroutine identification that:
// - Uses mostly public API (runtime/pprof.SetGoroutineLabels)
// - Gives child-goroutine inheritance for free
// - Needs only one linkname (for reading the raw pointer without
//   threading a context through every call site)
//
// If no, this test file can be deleted and we fall back to option
// #2 (stack-walk for owning test PC).

import (
	"context"
	"runtime/pprof"
	"sync"
	"testing"
	"unsafe"
)

// runtimeGetProfLabel is declared in parallel_experimental.go; these
// probes were the gating tests that validated it works on Go 1.26.2
// via the runtime/pprof.runtime_getProfLabel linkname path. Kept as
// regression guards: if Go ever closes the loophole or pprof's
// label-storage layout changes, these fail loudly rather than the
// FuncParallel tests silently degrading.

// TestPprofProbe_BasicLinkname is the minimum test: can we call the
// linkname'd function at all? If this test builds and runs, Go's
// linker is letting us access runtime.getProfLabel, and the rest
// of the probe is worth taking seriously.
func TestPprofProbe_BasicLinkname(t *testing.T) {
	// Fresh test goroutine: the testing package sets profile labels
	// automatically (with test name etc.), so it may or may not be
	// nil here. Either result proves the call itself works.
	_ = runtimeGetProfLabel()
	t.Log("runtime.getProfLabel callable — linkname not blocked")
}

// TestPprofProbe_PointerChangesAfterSetLabels confirms that
// SetGoroutineLabels does in fact update what getProfLabel returns
// (otherwise the identity we'd use is a lie).
func TestPprofProbe_PointerChangesAfterSetLabels(t *testing.T) {
	before := runtimeGetProfLabel()

	ctx := pprof.WithLabels(context.Background(), pprof.Labels("rewire.probe", "x"))
	pprof.SetGoroutineLabels(ctx)

	after := runtimeGetProfLabel()

	if after == before {
		t.Errorf("labels pointer did not change after SetGoroutineLabels — before=%p after=%p", before, after)
	}
	if after == nil {
		t.Errorf("labels pointer is nil after SetGoroutineLabels with a non-empty labels set")
	}
}

// TestPprofProbe_ChildInheritsParentLabels is the critical test for
// the design. If child goroutines inherit the parent's labels
// pointer automatically (as runtime/pprof docs claim), the whole
// "child goroutines see parent's mocks without any rewire.Go
// helper" property falls out for free.
func TestPprofProbe_ChildInheritsParentLabels(t *testing.T) {
	ctx := pprof.WithLabels(context.Background(), pprof.Labels("rewire.probe", "parent"))
	pprof.SetGoroutineLabels(ctx)
	parent := runtimeGetProfLabel()
	if parent == nil {
		t.Fatalf("parent labels pointer is nil after SetGoroutineLabels")
	}

	var childPtr unsafe.Pointer
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		childPtr = runtimeGetProfLabel()
	}()
	wg.Wait()

	if childPtr != parent {
		t.Errorf("child labels pointer does not match parent — parent=%p child=%p (child inheritance broken)", parent, childPtr)
	}
}

// TestPprofProbe_TwoGoroutinesGetDistinctPointers is the
// per-goroutine-isolation property. Two goroutines that each install
// their own labels must end up with DIFFERENT labels pointers —
// otherwise they'd collide on the same mock table. Not using t.Run
// with Parallel because Go pauses Parallel subtests until the parent
// function returns, which makes it awkward to assert across both.
func TestPprofProbe_TwoGoroutinesGetDistinctPointers(t *testing.T) {
	resultA := make(chan unsafe.Pointer, 1)
	resultB := make(chan unsafe.Pointer, 1)

	go func() {
		ctx := pprof.WithLabels(context.Background(), pprof.Labels("rewire.probe", "A"))
		pprof.SetGoroutineLabels(ctx)
		resultA <- runtimeGetProfLabel()
	}()
	go func() {
		ctx := pprof.WithLabels(context.Background(), pprof.Labels("rewire.probe", "B"))
		pprof.SetGoroutineLabels(ctx)
		resultB <- runtimeGetProfLabel()
	}()

	aPtr := <-resultA
	bPtr := <-resultB

	if aPtr == nil || bPtr == nil {
		t.Errorf("one of the goroutine label pointers is nil: A=%p B=%p", aPtr, bPtr)
	}
	if aPtr == bPtr {
		t.Errorf("two goroutines ended up with the same labels pointer: %p — per-goroutine isolation broken", aPtr)
	}
}
