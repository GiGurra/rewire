package rewire

import (
	"testing"
	"time"
)

// The target function used by these tests. Its fully-qualified
// name is hardcoded in fakeWrapper below to avoid pulling in
// reflect on the hot path of each test.
func stubGreet(name string) string {
	return "real:" + name
}

// stubGreetName is what runtime.FuncForPC produces for stubGreet —
// the package's import path plus the function's unqualified name.
const stubGreetName = "github.com/GiGurra/rewire/pkg/rewire.stubGreet"

// fakeWrapper stands in for what a rewriter-generated wrapper
// would look like on a FuncParallel-enabled target: check the
// per-goroutine table first, fall back to the real implementation
// if no mock is installed for this goroutine (or any of its
// ancestors — child goroutines inherit the parent's pprof labels).
//
// In a wired implementation the existing Mock_Foo nil check would
// sit between these two branches. The prototype omits it because
// no FuncParallel-enabled target would also be set via Func — and
// including it here would complicate the test story without
// adding signal about the goroutine-local design.
func fakeWrapper(name string) string {
	if m, ok := lookupParallelMock(stubGreetName); ok {
		return m.(func(string) string)(name)
	}
	return stubGreet(name)
}

func TestFuncParallel_InstallsMockForCurrentGoroutine(t *testing.T) {
	FuncParallel(t, stubGreet, func(name string) string {
		return "mocked:" + name
	})
	if got := fakeWrapper("alice"); got != "mocked:alice" {
		t.Errorf("got %q, want %q", got, "mocked:alice")
	}
}

func TestFuncParallel_DifferentGoroutinesDoNotInterfere(t *testing.T) {
	// Two parallel subtests each install their own mock for the
	// same target. Each subtest runs in its own goroutine (Go's
	// testing runtime guarantees this for t.Run + Parallel), so
	// their FuncParallel installations get distinct pprof labels
	// pointers and don't clobber each other. A small sleep inside
	// each subtest ensures the windows overlap.

	t.Run("A", func(subT *testing.T) {
		subT.Parallel()
		FuncParallel(subT, stubGreet, func(name string) string {
			return "A:" + name
		})
		time.Sleep(20 * time.Millisecond)
		if got := fakeWrapper("x"); got != "A:x" {
			subT.Errorf("got %q, want A:x", got)
		}
	})

	t.Run("B", func(subT *testing.T) {
		subT.Parallel()
		FuncParallel(subT, stubGreet, func(name string) string {
			return "B:" + name
		})
		time.Sleep(20 * time.Millisecond)
		if got := fakeWrapper("x"); got != "B:x" {
			subT.Errorf("got %q, want B:x", got)
		}
	})
}

// Key property of the pprof-labels approach: child goroutines
// spawned with raw `go` inherit the parent's labels pointer
// automatically (Go runtime behavior). That means they see the
// parent's FuncParallel mocks without any explicit handoff helper.
// This is the major ergonomic win over a goid-keyed approach.
func TestFuncParallel_RawGoroutineInheritsMock(t *testing.T) {
	FuncParallel(t, stubGreet, func(name string) string {
		return "parent-mock:" + name
	})

	var childGot string
	done := make(chan struct{})
	go func() {
		defer close(done)
		childGot = fakeWrapper("child")
	}()
	<-done

	if childGot != "parent-mock:child" {
		t.Errorf("child goroutine got %q, want %q (pprof label inheritance broken)", childGot, "parent-mock:child")
	}
}

// Nested child goroutines should inherit transitively — a
// grandchild spawned from a child spawned from the test should
// still see the test's mock, because pprof labels propagate down
// the full goroutine spawn tree.
func TestFuncParallel_NestedChildrenInherit(t *testing.T) {
	FuncParallel(t, stubGreet, func(name string) string {
		return "root-mock:" + name
	})

	var grandchildGot string
	done := make(chan struct{})
	go func() {
		nested := make(chan struct{})
		go func() {
			defer close(nested)
			grandchildGot = fakeWrapper("grandchild")
		}()
		<-nested
		close(done)
	}()
	<-done

	if grandchildGot != "root-mock:grandchild" {
		t.Errorf("grandchild got %q, want root-mock:grandchild", grandchildGot)
	}
}

func TestFuncParallel_CleanupRestoresAfterSubtest(t *testing.T) {
	t.Run("inner", func(inner *testing.T) {
		FuncParallel(inner, stubGreet, func(name string) string {
			return "inner-mock:" + name
		})
		if got := fakeWrapper("bob"); got != "inner-mock:bob" {
			inner.Errorf("got %q, want inner-mock:bob", got)
		}
	})

	// After the subtest's t.Cleanup runs, the mock entry should be
	// removed from the per-goroutine table. Because t.Run re-uses
	// the parent's goroutine (unlike Parallel subtests), the
	// cleanup must have actually removed the entry rather than
	// relying on goroutine termination.
	if got := fakeWrapper("carol"); got != "real:carol" {
		t.Errorf("after subtest got %q, want real:carol", got)
	}
}

// Sanity: ensureGoroutineLabeled should return the same value
// across calls within one goroutine (after the first call) and
// different values across goroutines. This is the underlying
// invariant that FuncParallel depends on.
func TestEnsureGoroutineLabeled_StableWithinGoroutine(t *testing.T) {
	first := ensureGoroutineLabeled()
	second := ensureGoroutineLabeled()
	if first != second {
		t.Errorf("ensureGoroutineLabeled not idempotent within one goroutine: %#x then %#x", first, second)
	}
	if first == 0 {
		t.Errorf("ensureGoroutineLabeled returned 0 — label install broken")
	}
}
