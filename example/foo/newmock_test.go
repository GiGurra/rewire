package foo

import (
	"testing"

	"github.com/GiGurra/rewire/example/bar"
	"github.com/GiGurra/rewire/pkg/rewire"
)

// Fresh mock of an interface, with no go:generate and no committed
// mock file. The toolexec wrapper generates the backing struct at
// compile time, triggered by the rewire.NewMock[bar.GreeterIface]
// reference below.
func TestNewMock_Phase1_StubAndCall(t *testing.T) {
	greeter := rewire.NewMock[bar.GreeterIface](t)

	// Stub via the same per-instance API we already have, passing the
	// interface method expression as the target.
	rewire.InstanceMethod(t, greeter, bar.GreeterIface.Greet, func(g bar.GreeterIface, name string) string {
		return "newmock: " + name
	})

	got := greeter.Greet("Alice")
	if got != "newmock: Alice" {
		t.Errorf("got %q, want %q", got, "newmock: Alice")
	}
}

// Unstubbed methods return zero values. Here, Greet isn't stubbed, so
// calling it returns "" (the zero value for string).
func TestNewMock_Phase1_UnstubbedReturnsZero(t *testing.T) {
	greeter := rewire.NewMock[bar.GreeterIface](t)

	if got := greeter.Greet("Alice"); got != "" {
		t.Errorf("unstubbed Greet: got %q, want zero value", got)
	}
}

// Two separate mocks of the same interface — stubs on one don't affect
// the other. This exercises the per-instance dispatch keyed on the
// receiver pointer, same mechanism that backs rewire.InstanceMethod
// for rewritten concrete methods.
func TestNewMock_Phase1_TwoInstancesScopedIndependently(t *testing.T) {
	g1 := rewire.NewMock[bar.GreeterIface](t)
	g2 := rewire.NewMock[bar.GreeterIface](t)

	rewire.InstanceMethod(t, g1, bar.GreeterIface.Greet, func(g bar.GreeterIface, name string) string {
		return "g1: " + name
	})
	rewire.InstanceMethod(t, g2, bar.GreeterIface.Greet, func(g bar.GreeterIface, name string) string {
		return "g2: " + name
	})

	if got := g1.Greet("Alice"); got != "g1: Alice" {
		t.Errorf("g1: got %q", got)
	}
	if got := g2.Greet("Bob"); got != "g2: Bob" {
		t.Errorf("g2: got %q", got)
	}
}

// rewire.Restore(t, mock) clears every per-instance mock bound to the
// mock. Calls revert to zero-value returns.
func TestNewMock_Phase1_RestoreClearsAllMethods(t *testing.T) {
	greeter := rewire.NewMock[bar.GreeterIface](t)

	rewire.InstanceMethod(t, greeter, bar.GreeterIface.Greet, func(g bar.GreeterIface, name string) string {
		return "stubbed: " + name
	})

	if got := greeter.Greet("Alice"); got != "stubbed: Alice" {
		t.Errorf("pre-restore: got %q", got)
	}

	rewire.Restore(t, greeter)

	if got := greeter.Greet("Alice"); got != "" {
		t.Errorf("post-restore: got %q, want zero", got)
	}
}

// Hand the mock into production code through an interface parameter.
// Verifies that the generated backing struct satisfies bar.GreeterIface
// at compile time AND at runtime.
func TestNewMock_Phase1_PassedToProductionCode(t *testing.T) {
	greeter := rewire.NewMock[bar.GreeterIface](t)

	rewire.InstanceMethod(t, greeter, bar.GreeterIface.Greet, func(g bar.GreeterIface, name string) string {
		return "production-call: " + name
	})

	got := callViaInterface(greeter, "World")
	if got != "production-call: World" {
		t.Errorf("got %q", got)
	}
}

// Minimal helper simulating production code that takes a GreeterIface.
func callViaInterface(g bar.GreeterIface, name string) string {
	return g.Greet(name)
}
