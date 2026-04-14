package foo

import (
	"testing"

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
