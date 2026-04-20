// Package caller — second, intentionally identical-name package (see
// example/namedup_a for the rationale).
package caller

// Doer is a separate interface from example/namedup_a.Doer — different
// signature so the test can confirm rewire generated the correct
// backing struct against each target.
type Doer interface {
	Do(x string) string
}
