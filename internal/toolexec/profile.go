package toolexec

import (
	"fmt"
	"os"
	"sync/atomic"
	"time"
)

// profileEnabled is set once at package init based on REWIRE_PROFILE.
// Kept in an atomic so hot-path reads don't allocate or synchronize
// beyond a single load.
var profileEnabled atomic.Bool

func init() {
	if os.Getenv("REWIRE_PROFILE") != "" {
		profileEnabled.Store(true)
	}
}

// profileStage records the wall-clock duration of a stage to stderr
// when REWIRE_PROFILE is set in the environment. Intended for
// drill-down analysis after benchtool gives us overall numbers.
//
// Typical use:
//
//	defer profileStage("scan", "")()
//	defer profileStage("rewrite", "github.com/foo/bar")()
//
// The returned function captures the end timestamp when deferred.
// When REWIRE_PROFILE is unset the returned function is a no-op
// closure with no allocations on the hot path beyond the closure
// itself, which is negligible compared to the work being measured.
func profileStage(stage, detail string) func() {
	if !profileEnabled.Load() {
		return func() {}
	}
	start := time.Now()
	return func() {
		elapsed := time.Since(start)
		if detail != "" {
			fmt.Fprintf(os.Stderr, "rewire-profile stage=%s pkg=%s duration_ms=%.2f pid=%d\n",
				stage, detail, float64(elapsed.Microseconds())/1000, os.Getpid())
		} else {
			fmt.Fprintf(os.Stderr, "rewire-profile stage=%s duration_ms=%.2f pid=%d\n",
				stage, float64(elapsed.Microseconds())/1000, os.Getpid())
		}
	}
}
