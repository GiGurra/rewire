package foo

import (
	"testing"

	"github.com/GiGurra/rewire/example/bar"
	"github.com/GiGurra/rewire/pkg/rewire"
)

// Two instances, per-instance mock on one — the other runs the real body.
func TestInstanceMethod_ScopedToOneInstance(t *testing.T) {
	g1 := &bar.Greeter{Prefix: "Hi"}
	g2 := &bar.Greeter{Prefix: "Hello"}

	rewire.InstanceMethod(t, g1, (*bar.Greeter).Greet, func(g *bar.Greeter, name string) string {
		return "g1-mock: " + name
	})

	if got := g1.Greet("Alice"); got != "g1-mock: Alice" {
		t.Errorf("g1: got %q, want per-instance mock", got)
	}
	if got := g2.Greet("Bob"); got != "Hello, Bob!" {
		t.Errorf("g2: got %q, want real body", got)
	}
}

// Per-instance and global mocks combined: per-instance wins, then global, then real.
func TestInstanceMethod_OverridesGlobal(t *testing.T) {
	g1 := &bar.Greeter{Prefix: "Hi"}
	g2 := &bar.Greeter{Prefix: "Hello"}
	g3 := &bar.Greeter{Prefix: "Hej"}

	rewire.Func(t, (*bar.Greeter).Greet, func(g *bar.Greeter, name string) string {
		return "global: " + name
	})
	rewire.InstanceMethod(t, g1, (*bar.Greeter).Greet, func(g *bar.Greeter, name string) string {
		return "g1-mock: " + name
	})

	if got := g1.Greet("Alice"); got != "g1-mock: Alice" {
		t.Errorf("g1: expected per-instance to override global, got %q", got)
	}
	if got := g2.Greet("Bob"); got != "global: Bob" {
		t.Errorf("g2: expected global mock, got %q", got)
	}
	if got := g3.Greet("Carol"); got != "global: Carol" {
		t.Errorf("g3: expected global mock, got %q", got)
	}
}

// Multiple methods on the same instance via per-instance mocks.
func TestInstanceMethod_MultipleMethodsOneInstance(t *testing.T) {
	g := &bar.Greeter{Prefix: "Hi"}
	other := &bar.Greeter{Prefix: "Hello"}

	rewire.InstanceMethod(t, g, (*bar.Greeter).Greet, func(g *bar.Greeter, name string) string {
		return "g-greet-mock: " + name
	})
	rewire.InstanceMethod(t, g, (*bar.Greeter).Farewell, func(g *bar.Greeter, name string) string {
		return "g-farewell-mock: " + name
	})

	if got := g.Greet("Alice"); got != "g-greet-mock: Alice" {
		t.Errorf("g.Greet: got %q", got)
	}
	if got := g.Farewell("Alice"); got != "g-farewell-mock: Alice" {
		t.Errorf("g.Farewell: got %q", got)
	}
	// Other instance untouched on both methods.
	if got := other.Greet("Bob"); got != "Hello, Bob!" {
		t.Errorf("other.Greet: expected real body, got %q", got)
	}
	if got := other.Farewell("Bob"); got != "Bye Bob from Hello" {
		t.Errorf("other.Farewell: expected real body, got %q", got)
	}
}

// rewire.Restore(t, instance) clears every per-instance mock on the instance
// at once, leaving other instances untouched.
func TestInstanceMethod_RestoreInstanceClearsAll(t *testing.T) {
	g := &bar.Greeter{Prefix: "Hi"}

	rewire.InstanceMethod(t, g, (*bar.Greeter).Greet, func(g *bar.Greeter, name string) string {
		return "g-greet-mock: " + name
	})
	rewire.InstanceMethod(t, g, (*bar.Greeter).Farewell, func(g *bar.Greeter, name string) string {
		return "g-farewell-mock: " + name
	})

	// Confirm both mocks are live.
	if got := g.Greet("Alice"); got != "g-greet-mock: Alice" {
		t.Errorf("pre-restore g.Greet: got %q", got)
	}
	if got := g.Farewell("Alice"); got != "g-farewell-mock: Alice" {
		t.Errorf("pre-restore g.Farewell: got %q", got)
	}

	// Restore(t, instance) clears per-instance scope for ALL methods on g.
	rewire.Restore(t, g)

	if got := g.Greet("Alice"); got != "Hi, Alice!" {
		t.Errorf("post-restore g.Greet: expected real body, got %q", got)
	}
	if got := g.Farewell("Alice"); got != "Bye Alice from Hi" {
		t.Errorf("post-restore g.Farewell: expected real body, got %q", got)
	}
}

// rewire.RestoreInstanceMethod clears one specific per-instance entry,
// leaving other methods on the same instance intact.
func TestInstanceMethod_RestoreInstanceMethodClearsOne(t *testing.T) {
	g := &bar.Greeter{Prefix: "Hi"}

	rewire.InstanceMethod(t, g, (*bar.Greeter).Greet, func(g *bar.Greeter, name string) string {
		return "g-greet-mock: " + name
	})
	rewire.InstanceMethod(t, g, (*bar.Greeter).Farewell, func(g *bar.Greeter, name string) string {
		return "g-farewell-mock: " + name
	})

	rewire.RestoreInstanceMethod(t, g, (*bar.Greeter).Greet)

	if got := g.Greet("Alice"); got != "Hi, Alice!" {
		t.Errorf("g.Greet should be restored: got %q", got)
	}
	if got := g.Farewell("Alice"); got != "g-farewell-mock: Alice" {
		t.Errorf("g.Farewell should still be mocked: got %q", got)
	}
}

// Per-instance mocking on a generic method. (*bar.Container[int]).Add is
// scoped to one instance — a different *Container[int] still runs the real
// append, and a *Container[string] is entirely untouched.
func TestInstanceMethod_GenericMethod(t *testing.T) {
	c1 := &bar.Container[int]{}
	c2 := &bar.Container[int]{}
	cs := &bar.Container[string]{}

	var c1Added []int
	rewire.InstanceMethod(t, c1, (*bar.Container[int]).Add, func(c *bar.Container[int], v int) {
		c1Added = append(c1Added, v)
	})

	c1.Add(1)
	c1.Add(2)
	c2.Add(3) // real body — appends to c2.items
	cs.Add("hello")

	if len(c1Added) != 2 || c1Added[0] != 1 || c1Added[1] != 2 {
		t.Errorf("c1 mock captured %v, want [1 2]", c1Added)
	}
	if c1.Len() != 0 {
		t.Errorf("c1 mock should have swallowed real appends, Len=%d", c1.Len())
	}
	if c2.Len() != 1 || c2.Get(0) != 3 {
		t.Errorf("c2 should run real body, got Len=%d", c2.Len())
	}
	if cs.Len() != 1 || cs.Get(0) != "hello" {
		t.Errorf("cs should run real body, got Len=%d", cs.Len())
	}
}

// Different generic instantiations at the same address don't collide as
// per-instance keys — interface equality compares both dynamic type and
// value. This test is mostly illustrative; in practice two allocations
// never share an address.
func TestInstanceMethod_GenericInstantiationsScopedIndependently(t *testing.T) {
	ci := &bar.Container[int]{}
	cs := &bar.Container[string]{}

	rewire.InstanceMethod(t, ci, (*bar.Container[int]).Add, func(c *bar.Container[int], v int) {
		// swallow
	})
	rewire.InstanceMethod(t, cs, (*bar.Container[string]).Add, func(c *bar.Container[string], v string) {
		// swallow
	})

	ci.Add(42)
	cs.Add("x")
	if ci.Len() != 0 || cs.Len() != 0 {
		t.Errorf("both instances should be mocked: ci.Len=%d cs.Len=%d", ci.Len(), cs.Len())
	}
}
