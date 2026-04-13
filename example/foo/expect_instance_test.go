package foo

import (
	"strings"
	"testing"

	"github.com/GiGurra/rewire/example/bar"
	"github.com/GiGurra/rewire/pkg/rewire"
	"github.com/GiGurra/rewire/pkg/rewire/expect"
)

// Per-instance concrete method: two *Greeter instances, expectation
// scoped to one. The other runs the real method body.
func TestExpectForInstance_ConcreteMethod_Scoped(t *testing.T) {
	g1 := &bar.Greeter{Prefix: "Hi"}
	g2 := &bar.Greeter{Prefix: "Hello"}

	e := expect.ForInstance(t, g1, (*bar.Greeter).Greet)
	e.On(g1, "Alice").Returns("g1-expect: Alice")
	e.OnAny().Returns("g1-catchall")

	if got := g1.Greet("Alice"); got != "g1-expect: Alice" {
		t.Errorf("g1.Greet(Alice): got %q", got)
	}
	if got := g1.Greet("Bob"); got != "g1-catchall" {
		t.Errorf("g1.Greet(Bob) catch-all: got %q", got)
	}
	if got := g2.Greet("Carol"); got != "Hello, Carol!" {
		t.Errorf("g2 should run real body, got %q", got)
	}
}

// Expectation on an interface method via NewMock. No go:generate, no
// committed mock files — the test just references the interface.
func TestExpectForInstance_InterfaceMethod_ViaNewMock(t *testing.T) {
	greeter := rewire.NewMock[bar.GreeterIface](t)

	e := expect.ForInstance(t, greeter, bar.GreeterIface.Greet)
	e.On(greeter, "Alice").Returns("mock: Alice")
	e.Match(func(g bar.GreeterIface, name string) bool {
		return strings.HasPrefix(name, "admin_")
	}).Returns("mock: admin")
	e.OnAny().Returns("mock: other")

	if got := greeter.Greet("Alice"); got != "mock: Alice" {
		t.Errorf("literal rule: got %q", got)
	}
	if got := greeter.Greet("admin_root"); got != "mock: admin" {
		t.Errorf("predicate rule: got %q", got)
	}
	if got := greeter.Greet("Bob"); got != "mock: other" {
		t.Errorf("catch-all rule: got %q", got)
	}
}

// Two NewMock instances of the same interface, scoped independently.
// Rule cleanup at test end verifies bounds for both expectations.
func TestExpectForInstance_TwoMocksIndependent(t *testing.T) {
	g1 := rewire.NewMock[bar.GreeterIface](t)
	g2 := rewire.NewMock[bar.GreeterIface](t)

	e1 := expect.ForInstance(t, g1, bar.GreeterIface.Greet)
	e1.OnAny().Returns("from g1")

	e2 := expect.ForInstance(t, g2, bar.GreeterIface.Greet)
	e2.OnAny().Returns("from g2")

	if got := g1.Greet("x"); got != "from g1" {
		t.Errorf("g1: got %q", got)
	}
	if got := g2.Greet("x"); got != "from g2" {
		t.Errorf("g2: got %q", got)
	}
}

// DoFunc captures arguments into the test body — a common spy pattern
// that also exercises the typed-callback response path through
// InstanceMethod dispatch.
func TestExpectForInstance_DoFuncSpy(t *testing.T) {
	greeter := rewire.NewMock[bar.GreeterIface](t)

	var seen []string
	e := expect.ForInstance(t, greeter, bar.GreeterIface.Greet)
	e.OnAny().DoFunc(func(g bar.GreeterIface, name string) string {
		seen = append(seen, name)
		return "seen: " + name
	})

	greeter.Greet("Alice")
	greeter.Greet("Bob")
	greeter.Greet("Carol")

	if len(seen) != 3 || seen[0] != "Alice" || seen[2] != "Carol" {
		t.Errorf("spy captured: %v", seen)
	}
}

// Call-count bounds on per-instance expectations. .Times(n) is
// verified by the expectation's t.Cleanup just like in expect.For.
func TestExpectForInstance_CallCountBounds(t *testing.T) {
	greeter := rewire.NewMock[bar.GreeterIface](t)

	e := expect.ForInstance(t, greeter, bar.GreeterIface.Greet)
	e.On(greeter, "Alice").Returns("hi").Times(2)

	greeter.Greet("Alice")
	greeter.Greet("Alice")
	// Two calls — matches the Times(2) bound.
}
