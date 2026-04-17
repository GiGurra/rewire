package rewire

import (
	"sync"
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
// if no mock is installed in this goroutine.
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
	// their FuncParallel installations key on different goids and
	// don't clobber each other. A small sleep inside each subtest
	// ensures the windows overlap — without it, one subtest might
	// complete before the other even installs its mock, which
	// would trivially "pass" without testing the parallel case.

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

func TestGo_CarriesMockToChild(t *testing.T) {
	FuncParallel(t, stubGreet, func(name string) string {
		return "parent-mock:" + name
	})

	var childGot string
	done := make(chan struct{})
	Go(t, func() {
		defer close(done)
		childGot = fakeWrapper("child")
	})
	<-done

	if childGot != "parent-mock:child" {
		t.Errorf("child via rewire.Go got %q, want %q", childGot, "parent-mock:child")
	}
}

// Complements TestGo_CarriesMockToChild: documents that raw `go`
// deliberately does NOT inherit. A test author who forgets to use
// rewire.Go in a parallel test will see unmocked behavior in the
// child, which is the intended failure mode (visible, not silent).
func TestFuncParallel_RawGoroutineDoesNotInherit(t *testing.T) {
	FuncParallel(t, stubGreet, func(name string) string {
		return "parent-mock:" + name
	})

	var rawGot string
	done := make(chan struct{})
	go func() {
		defer close(done)
		rawGot = fakeWrapper("raw")
	}()
	<-done

	if rawGot != "real:raw" {
		t.Errorf("raw goroutine got %q, want %q (raw go must not inherit parent mocks)", rawGot, "real:raw")
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

	// After the subtest's t.Cleanup runs, the mock should be gone
	// from this goroutine's table. Because t.Run re-uses the
	// parent's goroutine (unlike Parallel subtests), the cleanup
	// must have actually removed the entry rather than relying on
	// goroutine termination.
	if got := fakeWrapper("carol"); got != "real:carol" {
		t.Errorf("after subtest got %q, want real:carol", got)
	}
}

// Sanity check: currentGoid returns the same value within one
// goroutine and different values across goroutines. If this ever
// fails on a new Go release, the linkname access has broken and
// the prototype can't distinguish goroutines.
func TestRuntimeGoid_StableWithinGoroutineAndDistinctAcross(t *testing.T) {
	first := currentGoid()
	second := currentGoid()
	if first != second {
		t.Errorf("currentGoid not stable within one goroutine: %d then %d", first, second)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	var childGid int64
	go func() {
		defer wg.Done()
		childGid = currentGoid()
	}()
	wg.Wait()

	if childGid == 0 {
		t.Errorf("childGid is zero — runtime.Stack or linkname likely broken")
	}
	if childGid == first {
		t.Errorf("childGid (%d) equals parent gid (%d) — goroutines not distinguished", childGid, first)
	}
}
