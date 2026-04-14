package bar

// TextProcessor is declared in a file that uses a dot import to
// demonstrate rewire.NewMock's dot-import support. Method signatures
// reference `Tag` and `Fragment` as bare identifiers — the generator
// detects the dot import and qualifies them as `textutil.Tag` and
// `textutil.Fragment` when emitting the backing struct into the
// test package. The textutil package exports only Tag and Fragment,
// so the dot import doesn't collide with any existing bar-package
// declarations.
//
// Dot imports are generally discouraged in Go style guides; rewire
// supports them so users can mock interfaces in files that use them.

import . "github.com/GiGurra/rewire/example/textutil" //nolint:staticcheck // intentional: demonstrates rewire.NewMock's dot-import support

type TextProcessor interface {
	Tag(f Fragment) Tag
	Highlight(ts []Tag) []Fragment
}
