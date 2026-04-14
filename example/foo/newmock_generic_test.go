package foo

import (
	"testing"
	"time"

	"github.com/GiGurra/rewire/example/bar"
	"github.com/GiGurra/rewire/pkg/rewire"
)

// rewire.NewMock should support generic interfaces. Phase 2a: single
// type parameter, builtin type argument. The toolexec wrapper sees
// rewire.NewMock[bar.ContainerIface[int]] in this file, locates the
// ContainerIface declaration in example/bar, substitutes T → int in
// every method signature, and synthesizes a backing struct that
// satisfies bar.ContainerIface[int].
func TestNewMock_Generic_SingleTypeParam_Int(t *testing.T) {
	c := rewire.NewMock[bar.ContainerIface[int]](t)

	// Stub Add via the interface method expression.
	var added []int
	rewire.InstanceMethod(t, c, bar.ContainerIface[int].Add, func(c bar.ContainerIface[int], v int) {
		added = append(added, v)
	})

	c.Add(1)
	c.Add(2)
	c.Add(3)

	if len(added) != 3 || added[0] != 1 || added[1] != 2 || added[2] != 3 {
		t.Errorf("Add stub did not receive the expected calls: %v", added)
	}
}

// Two instantiations of the same generic interface with different type
// arguments must produce independent mocks. The factory key is derived
// per instantiation, so Container[int] and Container[string] do not
// collide.
func TestNewMock_Generic_DistinctInstantiations(t *testing.T) {
	ci := rewire.NewMock[bar.ContainerIface[int]](t)
	cs := rewire.NewMock[bar.ContainerIface[string]](t)

	rewire.InstanceMethod(t, ci, bar.ContainerIface[int].Get, func(c bar.ContainerIface[int], i int) int {
		return 42
	})
	rewire.InstanceMethod(t, cs, bar.ContainerIface[string].Get, func(c bar.ContainerIface[string], i int) string {
		return "answer"
	})

	if got := ci.Get(0); got != 42 {
		t.Errorf("Container[int].Get: got %d, want 42", got)
	}
	if got := cs.Get(0); got != "answer" {
		t.Errorf("Container[string].Get: got %q, want %q", got, "answer")
	}
}

// Methods that don't reference the type parameter (here: Len) should
// still be generated correctly and dispatch via the per-instance
// table just like methods that do reference T.
func TestNewMock_Generic_NonGenericMethod(t *testing.T) {
	c := rewire.NewMock[bar.ContainerIface[int]](t)

	rewire.InstanceMethod(t, c, bar.ContainerIface[int].Len, func(c bar.ContainerIface[int]) int {
		return 99
	})

	if got := c.Len(); got != 99 {
		t.Errorf("Len stub: got %d, want 99", got)
	}
}

// Nested generics: a generic interface whose type argument is itself
// a generic instantiation. Container[Container[int]] should produce
// methods like Add(v Container[int]) and Get(i int) Container[int].
//
// Both inner and outer instantiations come from the same package
// (bar), which is already imported via the interfacePkgAlias. The
// substituted method signatures reference bar.ContainerIface[int]
// and the generated mock satisfies it.
func TestNewMock_Generic_NestedSamePackage(t *testing.T) {
	// Build an inner mock to use as a stub return value.
	inner := rewire.NewMock[bar.ContainerIface[int]](t)
	rewire.InstanceMethod(t, inner, bar.ContainerIface[int].Len, func(c bar.ContainerIface[int]) int {
		return 7
	})

	// Build an outer mock keyed on Container[Container[int]].
	outer := rewire.NewMock[bar.ContainerIface[bar.ContainerIface[int]]](t)
	rewire.InstanceMethod(t, outer, bar.ContainerIface[bar.ContainerIface[int]].Get,
		func(c bar.ContainerIface[bar.ContainerIface[int]], i int) bar.ContainerIface[int] {
			return inner
		})

	got := outer.Get(0)
	if got == nil {
		t.Fatal("outer.Get returned nil")
	}
	if got.Len() != 7 {
		t.Errorf("inner.Len through outer.Get: got %d, want 7", got.Len())
	}
}

// Composite type as the type argument: Container[*User] where User is
// in the test package. The substituted method signatures should
// reference *User correctly.
func TestNewMock_Generic_PointerTypeArg(t *testing.T) {
	c := rewire.NewMock[bar.ContainerIface[*User]](t)

	stored := []*User{}
	rewire.InstanceMethod(t, c, bar.ContainerIface[*User].Add, func(c bar.ContainerIface[*User], v *User) {
		stored = append(stored, v)
	})

	c.Add(&User{ID: 1, Name: "Alice"})
	c.Add(&User{ID: 2, Name: "Bob"})

	if len(stored) != 2 {
		t.Fatalf("got %d stored users, want 2", len(stored))
	}
	if stored[0].Name != "Alice" || stored[1].Name != "Bob" {
		t.Errorf("unexpected stored users: %+v", stored)
	}
}

// Slice type as the type argument: Container[[]int].
func TestNewMock_Generic_SliceTypeArg(t *testing.T) {
	c := rewire.NewMock[bar.ContainerIface[[]int]](t)

	rewire.InstanceMethod(t, c, bar.ContainerIface[[]int].Get, func(c bar.ContainerIface[[]int], i int) []int {
		return []int{i, i * 2, i * 3}
	})

	got := c.Get(5)
	if len(got) != 3 || got[0] != 5 || got[1] != 10 || got[2] != 15 {
		t.Errorf("got %v, want [5 10 15]", got)
	}
}

// External-package type as the type argument: Container[time.Duration].
// The `time` package is imported by THIS test file, but NOT by
// example/bar/interfaces.go (where ContainerIface is declared). This
// exercises the case where the toolexec generator needs to discover
// the import path of a package mentioned only in the user's
// type-argument expression.
func TestNewMock_Generic_ExternalPackageTypeArg(t *testing.T) {
	c := rewire.NewMock[bar.ContainerIface[time.Duration]](t)

	rewire.InstanceMethod(t, c, bar.ContainerIface[time.Duration].Get,
		func(c bar.ContainerIface[time.Duration], i int) time.Duration {
			return time.Duration(i) * time.Second
		})

	if got := c.Get(5); got != 5*time.Second {
		t.Errorf("got %v, want %v", got, 5*time.Second)
	}
}

// Map type as the type argument: Container[map[string]int].
func TestNewMock_Generic_MapTypeArg(t *testing.T) {
	c := rewire.NewMock[bar.ContainerIface[map[string]int]](t)

	rewire.InstanceMethod(t, c, bar.ContainerIface[map[string]int].Get,
		func(c bar.ContainerIface[map[string]int], i int) map[string]int {
			return map[string]int{"key": i}
		})

	got := c.Get(42)
	if got["key"] != 42 {
		t.Errorf("got[key] = %d, want 42", got["key"])
	}
}

// Multi-type-parameter generic interface. The mock for
// CacheIface[string, int] must substitute K → string and V → int
// throughout the method set.
func TestNewMock_Generic_MultipleTypeParams(t *testing.T) {
	c := rewire.NewMock[bar.CacheIface[string, int]](t)

	store := map[string]int{}
	rewire.InstanceMethod(t, c, bar.CacheIface[string, int].Set, func(c bar.CacheIface[string, int], k string, v int) {
		store[k] = v
	})
	rewire.InstanceMethod(t, c, bar.CacheIface[string, int].Get, func(c bar.CacheIface[string, int], k string) (int, bool) {
		v, ok := store[k]
		return v, ok
	})

	c.Set("foo", 1)
	c.Set("bar", 2)
	if v, ok := c.Get("foo"); !ok || v != 1 {
		t.Errorf("Get(foo): got (%d, %v), want (1, true)", v, ok)
	}
	if v, ok := c.Get("missing"); ok || v != 0 {
		t.Errorf("Get(missing): got (%d, %v), want (0, false)", v, ok)
	}
}
