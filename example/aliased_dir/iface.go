// Package aliasedpkg is deliberately placed in a directory whose
// basename (aliased_dir) differs from its declared package name
// (aliasedpkg). Exercises mock generation for interfaces whose
// declaring package name can't be inferred from the import path's
// last segment — previously rewire guessed wrong and emitted an
// unresolvable "aliased_dir." qualifier into the generated backing
// struct.
package aliasedpkg

// Greeter is a trivial interface exercised by the mismatched-dir test.
type Greeter interface {
	Greet(name string) string
}
