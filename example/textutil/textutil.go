// Package textutil is a small helper package with narrow exports
// used by the dot-import example in example/bar. It exists so the
// dotimport example can demonstrate `import . "textutil"` without
// colliding with example/bar's existing package-level declarations.
package textutil

// Tag is a named string type that the dot-import example references
// as a bare ident in interface method signatures.
type Tag string

// Fragment is a struct type referenced bare by the dot-import
// example's interface.
type Fragment struct {
	Text string
	Pri  int
}
