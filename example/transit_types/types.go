// Package transit_types defines a single public type used in an
// interface's method signature elsewhere. The test that exercises
// the missing-importcfg bug deliberately does NOT import this package
// directly — it shows up only as a type reference inside the
// interface being mocked. That means Go's build system, when it
// computes the test package's -importcfg from the set of source-file
// imports, doesn't include transit_types. Rewire's generated mock
// file then imports transit_types (because the interface method
// references it), and the compile used to fail with a confusing
// "open : no such file or directory" error.
package transit_types

// Payload is a trivial opaque type — the test cares only that it
// survives a round-trip through the mocked method signature.
type Payload struct {
	ID string
}
