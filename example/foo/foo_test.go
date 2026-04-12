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

func TestWelcome_RealImplementation(t *testing.T) {
	got := Welcome("Bob")
	want := "Welcome! Hello, Bob!"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
