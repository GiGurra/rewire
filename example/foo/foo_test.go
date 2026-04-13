package foo

import (
	"math"
	"testing"

	"github.com/GiGurra/rewire/example/bar"
	"github.com/GiGurra/rewire/pkg/rewire"
)

func TestWelcome_WithMock(t *testing.T) {
	rewire.Func(t, bar.Greet, func(name string) string {
		return "Howdy, " + name
	})

	got := Welcome("Alice")
	want := "Welcome! Howdy, Alice"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSquareRoot_WithMockedMathPow(t *testing.T) {
	rewire.Func(t, math.Pow, func(x, y float64) float64 {
		return 42
	})

	got := SquareRoot(9)
	want := 42.0
	if got != want {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSquareRoot_RealMathPow(t *testing.T) {
	t.Parallel()
	got := SquareRoot(9)
	want := 3.0
	if got != want {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestGreetWith_MockedMethod(t *testing.T) {
	rewire.Func(t, (*bar.Greeter).Greet, func(g *bar.Greeter, name string) string {
		return "Mocked, " + name
	})

	g := &bar.Greeter{Prefix: "Hi"}
	got := GreetWith(g, "Alice")
	want := "Mocked, Alice"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestGreetWith_RealMethod(t *testing.T) {
	g := &bar.Greeter{Prefix: "Hi"}
	got := GreetWith(g, "Bob")
	want := "Hi, Bob!"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestGreet_CallCounter(t *testing.T) {
	callCount := 0
	var lastArg string

	rewire.Func(t, bar.Greet, func(name string) string {
		callCount++
		lastArg = name
		return "counted"
	})

	Welcome("Alice")
	Welcome("Bob")
	Welcome("Charlie")

	if callCount != 3 {
		t.Errorf("expected 3 calls, got %d", callCount)
	}
	if lastArg != "Charlie" {
		t.Errorf("expected last arg %q, got %q", "Charlie", lastArg)
	}
}

func TestWelcome_RealImplementation(t *testing.T) {
	got := Welcome("Bob")
	want := "Welcome! Hello, Bob!"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Spy pattern around a free function: wrap bar.Greet so every greeting
// is shouted (uppercased) while still delegating to the real greeting logic.
func TestReal_SpyWrapsBarGreet(t *testing.T) {
	realGreet := rewire.Real(t, bar.Greet)

	rewire.Func(t, bar.Greet, func(name string) string {
		return realGreet(name) + " [wrapped]"
	})

	got := Welcome("Alice")
	want := "Welcome! Hello, Alice! [wrapped]"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Spy pattern around a method: delegate to the real (*Greeter).Greet
// while adding audit behavior.
func TestReal_SpyWrapsGreeterMethod(t *testing.T) {
	realMethod := rewire.Real(t, (*bar.Greeter).Greet)

	var seen []string
	rewire.Func(t, (*bar.Greeter).Greet, func(g *bar.Greeter, name string) string {
		seen = append(seen, name)
		return realMethod(g, name)
	})

	g := &bar.Greeter{Prefix: "Hi"}
	got := GreetWith(g, "Alice")
	want := "Hi, Alice!"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if len(seen) != 1 || seen[0] != "Alice" {
		t.Errorf("expected audit log [Alice], got %v", seen)
	}
}
