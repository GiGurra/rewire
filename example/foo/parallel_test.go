package foo

import (
	"sync"
	"testing"
	"time"

	"github.com/GiGurra/rewire/example/bar"
	"github.com/GiGurra/rewire/pkg/rewire"
)

// These tests exercise the parallel-safe dispatch path that
// rewire.Func now uses: two t.Parallel() subtests each install
// their own mock for the same target, and each sees its own
// replacement rather than racing on a shared Mock_Foo variable.
//
// Before the per-goroutine dispatch path was added, these tests
// would non-deterministically fail depending on scheduling: both
// subtests write to the same package-level Mock_bar_Greet, and
// the reader doesn't know which one it's seeing. With the
// goroutine-keyed table, each subtest's goroutine has its own
// pprof labels pointer and therefore its own map entry.

func TestParallel_TwoSubtestsMockSameTarget(t *testing.T) {
	t.Run("A", func(subT *testing.T) {
		subT.Parallel()
		rewire.Func(subT, bar.Greet, func(name string) string {
			return "A:" + name
		})
		// Sleep to ensure the other subtest has time to install
		// its own mock before we call bar.Greet. Without overlap,
		// the test would pass trivially (subtests running in
		// sequence don't exercise the race).
		time.Sleep(20 * time.Millisecond)
		if got := bar.Greet("x"); got != "A:x" {
			subT.Errorf("subtest A got %q, want %q", got, "A:x")
		}
	})

	t.Run("B", func(subT *testing.T) {
		subT.Parallel()
		rewire.Func(subT, bar.Greet, func(name string) string {
			return "B:" + name
		})
		time.Sleep(20 * time.Millisecond)
		if got := bar.Greet("x"); got != "B:x" {
			subT.Errorf("subtest B got %q, want %q", got, "B:x")
		}
	})
}

// Child goroutines spawned from a test that mocked the target
// should inherit the mock automatically via pprof label
// inheritance. This is the property that distinguishes the
// pprof-labels-pointer approach from a pure goid-keyed scheme —
// no explicit handoff helper needed.
func TestParallel_ChildGoroutineInheritsMock(t *testing.T) {
	rewire.Func(t, bar.Greet, func(name string) string {
		return "parent:" + name
	})

	var childGot string
	done := make(chan struct{})
	go func() {
		defer close(done)
		childGot = bar.Greet("child")
	}()
	<-done

	if childGot != "parent:child" {
		t.Errorf("child goroutine got %q, want %q (pprof label inheritance broken?)", childGot, "parent:child")
	}
}

// Two parallel subtests each spawn their own goroutines that call
// the mocked function. Each spawned goroutine should see its own
// parent test's mock, not the other test's. Stress version of
// the above — verifies isolation holds under concurrent child
// goroutine activity.
func TestParallel_SubtestsWithChildGoroutines(t *testing.T) {
	run := func(subT *testing.T, label string) {
		subT.Parallel()
		rewire.Func(subT, bar.Greet, func(name string) string {
			return label + ":" + name
		})
		var wg sync.WaitGroup
		results := make([]string, 10)
		for i := range results {
			wg.Go(func() {
				results[i] = bar.Greet("x")
			})
		}
		wg.Wait()
		want := label + ":x"
		for i, got := range results {
			if got != want {
				subT.Errorf("child %d got %q, want %q", i, got, want)
			}
		}
	}

	t.Run("A", func(subT *testing.T) { run(subT, "A") })
	t.Run("B", func(subT *testing.T) { run(subT, "B") })
}
