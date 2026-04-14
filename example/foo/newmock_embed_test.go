package foo

import (
	"errors"
	"testing"

	"github.com/GiGurra/rewire/example/bar"
	"github.com/GiGurra/rewire/pkg/rewire"
)

// Embedded interface whose method set is a mix of cross-package
// (io.Reader) and same-file (bar.Named) promoted methods plus an own
// Close() method. All three are stubbed via method expressions on the
// OUTER interface (bar.ReadCloser), matching what runtime.FuncForPC
// reports for interface method expressions on types with embeds.
func TestNewMock_Embed_MixedPromotion(t *testing.T) {
	mock := rewire.NewMock[bar.ReadCloser](t)

	rewire.InstanceMethod(t, mock, bar.ReadCloser.Read, func(r bar.ReadCloser, p []byte) (int, error) {
		copy(p, "hi")
		return 2, nil
	})
	rewire.InstanceMethod(t, mock, bar.ReadCloser.Name, func(r bar.ReadCloser) string {
		return "mock-name"
	})
	rewire.InstanceMethod(t, mock, bar.ReadCloser.Close, func(r bar.ReadCloser) error {
		return errors.New("closed")
	})

	buf := make([]byte, 4)
	n, err := mock.Read(buf)
	if err != nil {
		t.Fatalf("Read err: %v", err)
	}
	if n != 2 || string(buf[:n]) != "hi" {
		t.Errorf("Read: got n=%d buf=%q, want n=2 buf=%q", n, string(buf[:n]), "hi")
	}

	if got := mock.Name(); got != "mock-name" {
		t.Errorf("Name: got %q, want %q", got, "mock-name")
	}

	if err := mock.Close(); err == nil || err.Error() != "closed" {
		t.Errorf("Close: got %v, want error 'closed'", err)
	}
}

// Unstubbed promoted methods return zero values just like unstubbed
// own methods — no special case for embeds.
func TestNewMock_Embed_UnstubbedReturnsZero(t *testing.T) {
	mock := rewire.NewMock[bar.ReadCloser](t)

	buf := make([]byte, 4)
	n, err := mock.Read(buf)
	if n != 0 || err != nil {
		t.Errorf("unstubbed Read: got n=%d err=%v, want n=0 err=nil", n, err)
	}
	if got := mock.Name(); got != "" {
		t.Errorf("unstubbed Name: got %q, want zero value", got)
	}
	if err := mock.Close(); err != nil {
		t.Errorf("unstubbed Close: got %v, want nil", err)
	}
}

// Generic embed with type-parameter flow. ListRepo[int] embeds Base[U]
// with U=int — the promoted Load method returns int. Stubbing the
// promoted method uses the outer ListRepo[int] as the receiver in the
// method expression.
func TestNewMock_Embed_GenericFlow(t *testing.T) {
	mock := rewire.NewMock[bar.ListRepo[int]](t)

	rewire.InstanceMethod(t, mock, bar.ListRepo[int].Load, func(r bar.ListRepo[int], id int) int {
		return id * 10
	})
	rewire.InstanceMethod(t, mock, bar.ListRepo[int].List, func(r bar.ListRepo[int]) []int {
		return []int{1, 2, 3}
	})

	if got := mock.Load(7); got != 70 {
		t.Errorf("Load(7): got %d, want 70", got)
	}
	if got := mock.List(); len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Errorf("List: got %v", got)
	}
}
