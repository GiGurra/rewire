package foo

import (
	"testing"

	"github.com/GiGurra/rewire/example/bar"
	"github.com/GiGurra/rewire/pkg/rewire"
)

// bar.GreeterFactory uses bare same-package identifiers in its method
// signatures (returns *Greeter, not *bar.Greeter). The toolexec mock
// generator has to qualify those bare idents with the bar alias when
// synthesizing the backing struct in the test package; otherwise the
// generated file can't compile.
func TestNewMock_SamePackageBareType(t *testing.T) {
	mock := rewire.NewMock[bar.GreeterFactory](t)

	rewire.InstanceMethod(t, mock, bar.GreeterFactory.Make, func(f bar.GreeterFactory, prefix string) *bar.Greeter {
		return &bar.Greeter{Prefix: "mocked-" + prefix}
	})
	rewire.InstanceMethod(t, mock, bar.GreeterFactory.WrapAll, func(f bar.GreeterFactory, gs []*bar.Greeter) []*bar.Greeter {
		out := make([]*bar.Greeter, 0, len(gs)+1)
		out = append(out, &bar.Greeter{Prefix: "head"})
		out = append(out, gs...)
		return out
	})
	rewire.InstanceMethod(t, mock, bar.GreeterFactory.ByName, func(f bar.GreeterFactory) map[string]*bar.Greeter {
		return map[string]*bar.Greeter{
			"alice": {Prefix: "Hi Alice"},
			"bob":   {Prefix: "Hi Bob"},
		}
	})

	g := mock.Make("foo")
	if g.Prefix != "mocked-foo" {
		t.Errorf("Make: got prefix %q, want mocked-foo", g.Prefix)
	}

	wrapped := mock.WrapAll([]*bar.Greeter{{Prefix: "original"}})
	if len(wrapped) != 2 || wrapped[0].Prefix != "head" || wrapped[1].Prefix != "original" {
		t.Errorf("WrapAll: got %v", wrapped)
	}

	byName := mock.ByName()
	if byName["alice"].Prefix != "Hi Alice" || byName["bob"].Prefix != "Hi Bob" {
		t.Errorf("ByName: got %v", byName)
	}
}

// Unstubbed methods on a same-pkg-qualified interface return zero
// values — the qualification mechanism doesn't disturb the standard
// fallback behavior.
func TestNewMock_SamePackageBareType_UnstubbedReturnsZero(t *testing.T) {
	mock := rewire.NewMock[bar.GreeterFactory](t)

	if g := mock.Make("anything"); g != nil {
		t.Errorf("unstubbed Make: got %v, want nil", g)
	}
	if gs := mock.WrapAll(nil); gs != nil {
		t.Errorf("unstubbed WrapAll: got %v, want nil", gs)
	}
	if m := mock.ByName(); m != nil {
		t.Errorf("unstubbed ByName: got %v, want nil", m)
	}
}
