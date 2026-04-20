// Package caller is deliberately declared at two different import
// paths (example/namedup_a and example/namedup_b) so a single test
// package can mock interfaces from both. Exercises rewire's
// disambiguation when two different packages happen to share the same
// declared package name — previously the generated mock struct names
// collided because they were keyed only on the declared package name.
package caller

// Doer is the interface exposed by this `caller` package. Intentionally
// distinct from example/namedup_b's Doer so one is obviously not the
// other, but both types could plausibly be confused under a struct
// naming scheme keyed only on the package name.
type Doer interface {
	Do(x int) int
}
