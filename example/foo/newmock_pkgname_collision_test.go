package foo

import (
	"testing"

	a "github.com/GiGurra/rewire/example/namedup_a"
	b "github.com/GiGurra/rewire/example/namedup_b"
	"github.com/GiGurra/rewire/pkg/rewire"
)

// Two different import paths (namedup_a, namedup_b) both declare
// `package caller`, and both expose a type named Doer. A single test
// package that mocks both interfaces must get two distinct backing
// structs in its generated code — the per-package mangled struct name
// previously combined only alias + interfaceName, which collides when
// the alias is identical for two different packages.
//
// The test passes only if rewire disambiguates the generated struct
// names by import path.
func TestNewMock_TwoPackagesSameDeclaredName(t *testing.T) {
	aMock := rewire.NewMock[a.Doer](t)
	bMock := rewire.NewMock[b.Doer](t)

	rewire.InstanceFunc(t, aMock, a.Doer.Do, func(_ a.Doer, x int) int {
		return x * 2
	})
	rewire.InstanceFunc(t, bMock, b.Doer.Do, func(_ b.Doer, s string) string {
		return s + "!"
	})

	if got := aMock.Do(5); got != 10 {
		t.Errorf("aMock.Do(5) = %d, want 10", got)
	}
	if got := bMock.Do("hi"); got != "hi!" {
		t.Errorf(`bMock.Do("hi") = %q, want "hi!"`, got)
	}
}
