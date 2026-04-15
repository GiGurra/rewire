package foo

import (
	"testing"

	"github.com/GiGurra/rewire/example/bar"
	"github.com/GiGurra/rewire/example/textutil"
	"github.com/GiGurra/rewire/pkg/rewire"
)

// bar.TextProcessor is declared in a file that uses
// `import . "textutil"`. Its method signatures reference `Tag` and
// `Fragment` as bare identifiers. The toolexec mock generator must
// resolve those to textutil.Tag / textutil.Fragment (from the
// dot-imported package), not to bar.Tag / bar.Fragment (which don't
// exist).
func TestNewMock_DotImport(t *testing.T) {
	mock := rewire.NewMock[bar.TextProcessor](t)

	rewire.InstanceFunc(t, mock, bar.TextProcessor.Tag, func(p bar.TextProcessor, f textutil.Fragment) textutil.Tag {
		return textutil.Tag("tag-" + f.Text)
	})
	rewire.InstanceFunc(t, mock, bar.TextProcessor.Highlight, func(p bar.TextProcessor, ts []textutil.Tag) []textutil.Fragment {
		out := make([]textutil.Fragment, len(ts))
		for i, t := range ts {
			out[i] = textutil.Fragment{Text: string(t), Pri: i}
		}
		return out
	})

	if got := mock.Tag(textutil.Fragment{Text: "hello"}); got != "tag-hello" {
		t.Errorf("Tag: got %q, want tag-hello", got)
	}

	frags := mock.Highlight([]textutil.Tag{"a", "b"})
	if len(frags) != 2 || frags[0].Text != "a" || frags[1].Pri != 1 {
		t.Errorf("Highlight: got %v", frags)
	}
}
