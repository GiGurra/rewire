package expect

import "fmt"

// boundKind enumerates the kinds of call-count constraints a rule can
// carry. Each rule holds exactly one bound, checked at t.Cleanup.
type boundKind int

const (
	// boundAny accepts any call count including zero. It's the default
	// for OnAny rules and is what Maybe() sets.
	boundAny boundKind = iota

	// boundAtLeast requires count >= n. It's the default for On and
	// Match rules with n=1 (the strict default). Users can set other
	// values via AtLeast(n).
	boundAtLeast

	// boundExact requires count == n. Set by Times(n).
	boundExact

	// boundNever requires count == 0. Equivalent to Exact(0) but
	// produces clearer diagnostics. Set by Never().
	boundNever
)

// bound is a single call-count constraint. n is only meaningful when
// kind is boundAtLeast or boundExact.
type bound struct {
	kind boundKind
	n    int
}

// check compares a recorded call count against the bound and returns
// a human-readable error message, or "" if the bound is satisfied.
func (b bound) check(count int) string {
	switch b.kind {
	case boundAny:
		return ""
	case boundAtLeast:
		if count < b.n {
			return fmt.Sprintf("was called %d time(s), expected at least %d", count, b.n)
		}
	case boundExact:
		if count != b.n {
			return fmt.Sprintf("was called %d time(s), expected exactly %d", count, b.n)
		}
	case boundNever:
		if count != 0 {
			return fmt.Sprintf("was called %d time(s), expected never", count)
		}
	}
	return ""
}

// verify is registered with t.Cleanup by For and checks every rule's
// call count against its bound. Failures are reported via t.Errorf —
// using Errorf (not Fatalf) so that multiple rule violations can all
// be reported in a single test run.
func (e *Expectation[F]) verify() {
	e.t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, r := range e.rules {
		if msg := r.bound.check(r.count); msg != "" {
			e.t.Errorf("rewire/expect: %s rule #%d %s (declared at %s) %s",
				e.name, i, r.matcher.describe(), r.site, msg)
		}
	}
}
