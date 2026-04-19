package foo

import (
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GiGurra/rewire/example/bar"
	"github.com/GiGurra/rewire/pkg/rewire"
)

// Reinstalling a mock on a target the same test already mocks must not
// trip the conflict check — it's a legitimate (if uncommon) pattern.
func TestParallelConflict_Func_SameTestReinstallOK(t *testing.T) {
	rewire.Func(t, bar.Greet, func(name string) string { return "first" })
	if got := bar.Greet("x"); got != "first" {
		t.Fatalf("first install: got %q, want %q", got, "first")
	}
	rewire.Func(t, bar.Greet, func(name string) string { return "second" })
	if got := bar.Greet("x"); got != "second" {
		t.Fatalf("reinstall: got %q, want %q", got, "second")
	}
}

// Sequential subtests that each mock the same target must both succeed —
// the first subtest's cleanup runs before the second starts, releasing
// ownership cleanly.
func TestParallelConflict_Func_SequentialSubtestsOK(t *testing.T) {
	p1 := t.Run("first", func(st *testing.T) {
		rewire.Func(st, bar.TinyAdd, func(a, b int) int { return 100 })
		if got := bar.TinyAdd(1, 2); got != 100 {
			st.Errorf("first: got %d, want 100", got)
		}
	})
	p2 := t.Run("second", func(st *testing.T) {
		rewire.Func(st, bar.TinyAdd, func(a, b int) int { return 200 })
		if got := bar.TinyAdd(1, 2); got != 200 {
			st.Errorf("second: got %d, want 200", got)
		}
	})
	if !p1 || !p2 {
		t.Fatalf("sequential subtests should both pass, got p1=%v p2=%v", p1, p2)
	}
}

// Two parallel subtests mocking *different* targets must coexist without
// tripping the conflict check.
func TestParallelConflict_Func_DifferentTargetsInParallelOK(t *testing.T) {
	t.Run("mocks-TinyAdd", func(st *testing.T) {
		st.Parallel()
		rewire.Func(st, bar.TinyAdd, func(a, b int) int { return 42 })
		if got := bar.TinyAdd(1, 2); got != 42 {
			st.Errorf("got %d, want 42", got)
		}
	})
	t.Run("mocks-TinyDouble", func(st *testing.T) {
		st.Parallel()
		rewire.Func(st, bar.TinyDouble, func(x int) int { return x * 10 })
		if got := bar.TinyDouble(3); got != 30 {
			st.Errorf("got %d, want 30", got)
		}
	})
}

// rewire.RestoreFunc releases ownership so a subsequent subtest can claim
// the same target without tripping the conflict check.
func TestParallelConflict_Func_RestoreReleasesOwnership(t *testing.T) {
	rewire.Func(t, bar.TinyDouble, func(x int) int { return -1 })
	rewire.RestoreFunc(t, bar.TinyDouble)

	// With ownership released, a different *testing.T (subtest) can claim.
	passed := t.Run("claim-after-restore", func(st *testing.T) {
		rewire.Func(st, bar.TinyDouble, func(x int) int { return x + 1000 })
		if got := bar.TinyDouble(5); got != 1005 {
			st.Errorf("got %d, want 1005", got)
		}
	})
	if !passed {
		t.Fatal("subtest should have succeeded after RestoreFunc released ownership")
	}
}

// Per-instance ownership is scoped to the receiver address. Two different
// instances may be mocked concurrently — the ownership key includes the
// instance address, so no conflict is raised.
func TestParallelConflict_InstanceFunc_DifferentInstancesOK(t *testing.T) {
	g1 := &bar.Greeter{Prefix: "A"}
	g2 := &bar.Greeter{Prefix: "B"}

	rewire.InstanceFunc(t, g1, (*bar.Greeter).Greet, func(g *bar.Greeter, n string) string {
		return "g1-mock"
	})

	passed := t.Run("other-instance", func(st *testing.T) {
		rewire.InstanceFunc(st, g2, (*bar.Greeter).Greet, func(g *bar.Greeter, n string) string {
			return "g2-mock"
		})
	})
	if !passed {
		t.Fatal("per-instance ownership on a different receiver must not conflict")
	}
}

// rewire.RestoreInstanceFunc releases per-instance ownership so a different
// test can install a fresh mock on the same (instance, method) pair.
func TestParallelConflict_RestoreInstanceFunc_ReleasesOwnership(t *testing.T) {
	g := &bar.Greeter{Prefix: "X"}

	rewire.InstanceFunc(t, g, (*bar.Greeter).Greet, func(g *bar.Greeter, n string) string {
		return "outer"
	})
	rewire.RestoreInstanceFunc(t, g, (*bar.Greeter).Greet)

	passed := t.Run("claim-after-restore", func(st *testing.T) {
		rewire.InstanceFunc(st, g, (*bar.Greeter).Greet, func(g *bar.Greeter, n string) string {
			return "inner"
		})
		if got := g.Greet("x"); got != "inner" {
			st.Errorf("got %q, want %q", got, "inner")
		}
	})
	if !passed {
		t.Fatal("subtest should succeed after RestoreInstanceFunc released ownership")
	}
}

// End-to-end test that two genuinely parallel subtests racing to mock the
// same target produce the expected failure in at least one of them. The
// child subprocess runs the parallel scenario under a distinct env flag
// and exits non-zero as soon as the conflict detector fires. The parent
// test scrapes the subprocess output to verify the diagnostic surfaced.
//
// This complements the unit tests that exercise the pure claimOwnership
// logic: here we actually hit the sync.Map with two concurrent goroutines
// and confirm the user-visible error path works end-to-end.
func TestParallelConflict_EndToEnd(t *testing.T) {
	if os.Getenv("REWIRE_CONFLICT_CHILD") == "1" {
		// Running as child. Spawn two parallel subtests, each claiming the
		// same target and sleeping long enough to overlap. An atomic counter
		// records any install that returned normally (i.e. succeeded); if
		// detection works, at most one should get through. The child always
		// exits non-zero when detection fires (one subtest Fatals), which
		// the parent relies on as the primary signal.
		var installsCompleted atomic.Int64
		tryInstall := func(st *testing.T, ret int) {
			rewire.Func(st, bar.TinyAdd, func(a, b int) int { return ret })
			// Reached only if claimOwnership didn't Fatal.
			installsCompleted.Add(1)
			time.Sleep(1 * time.Second)
		}
		t.Run("installer-A", func(st *testing.T) {
			st.Parallel()
			tryInstall(st, 111)
		})
		t.Run("installer-B", func(st *testing.T) {
			st.Parallel()
			tryInstall(st, 222)
		})
		// If both installs completed, detection failed — emit a marker the
		// parent can scrape for.
		if installsCompleted.Load() > 1 {
			t.Errorf("REWIRE_CONFLICT_CHILD: both parallel installs completed (%d); detection failed",
				installsCompleted.Load())
		}
		return
	}

	// Parent: re-exec the test binary on just this test with the child env
	// variable set, then verify the expected conflict diagnostic surfaced.
	cmd := exec.Command(os.Args[0], "-test.run=^TestParallelConflict_EndToEnd$", "-test.v")
	cmd.Env = append(os.Environ(), "REWIRE_CONFLICT_CHILD=1")
	out, err := cmd.CombinedOutput()
	outStr := string(out)

	if err == nil {
		t.Fatalf("child process should have failed (one parallel install must lose the race), but it passed.\nOutput:\n%s", outStr)
	}
	if strings.Contains(outStr, "detection failed") {
		t.Fatalf("child reported both installs completed — conflict detection did not fire.\nOutput:\n%s", outStr)
	}
	if !strings.Contains(outStr, "cannot install mock") {
		t.Fatalf("child output missing the parallel-mock diagnostic.\nOutput:\n%s", outStr)
	}
	if !strings.Contains(outStr, "not parallel-safe") {
		t.Fatalf("child output missing the parallel-safety explanation.\nOutput:\n%s", outStr)
	}
}

// rewire.RestoreInstance releases ownership for every per-instance entry
// scoped to the receiver, allowing other tests to claim any of them.
func TestParallelConflict_RestoreInstance_ReleasesAllOwnership(t *testing.T) {
	g := &bar.Greeter{Prefix: "X"}

	rewire.InstanceFunc(t, g, (*bar.Greeter).Greet, func(g *bar.Greeter, n string) string {
		return "greet-outer"
	})
	rewire.InstanceFunc(t, g, (*bar.Greeter).Farewell, func(g *bar.Greeter, n string) string {
		return "farewell-outer"
	})
	rewire.RestoreInstance(t, g)

	passed := t.Run("claim-after-restore-all", func(st *testing.T) {
		rewire.InstanceFunc(st, g, (*bar.Greeter).Greet, func(g *bar.Greeter, n string) string {
			return "greet-inner"
		})
		rewire.InstanceFunc(st, g, (*bar.Greeter).Farewell, func(g *bar.Greeter, n string) string {
			return "farewell-inner"
		})
	})
	if !passed {
		t.Fatal("subtest should succeed after RestoreInstance released ownership for both methods")
	}
}
