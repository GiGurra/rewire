package foo

import (
	"testing"

	"github.com/GiGurra/rewire/example/bar"
	"github.com/GiGurra/rewire/pkg/rewire"
)

func TestWelcome_WithMock(t *testing.T) {
	rewire.Replace(t, &bar.Mock_Greet, func(name string) string {
		return "Howdy, " + name
	})

	got := Welcome("Alice")
	want := "Welcome! Howdy, Alice"
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
