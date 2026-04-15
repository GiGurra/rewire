package foo

import (
	"testing"

	"github.com/GiGurra/rewire/example/bar"
	"github.com/GiGurra/rewire/pkg/rewire"
)

// Per-instantiation mocking on a method of a generic type. Mocking
// (*Container[int]).Add must only affect the int instantiation —
// Container[string] and any other instantiation still run the real body.
func TestGenericMethod_MockOnlyOneInstantiation(t *testing.T) {
	var intAddCalls int
	rewire.Func(t, (*bar.Container[int]).Add, func(c *bar.Container[int], v int) {
		intAddCalls++
		// Deliberately do NOT delegate to the real — we want to verify
		// the real wasn't called for int.
	})

	ci := &bar.Container[int]{}
	ci.Add(1)
	ci.Add(2)
	ci.Add(3)
	if ci.Len() != 0 {
		t.Errorf("int container should be empty (mock swallowed adds), got len=%d", ci.Len())
	}
	if intAddCalls != 3 {
		t.Errorf("expected 3 mock calls, got %d", intAddCalls)
	}

	// A different instantiation must still run the real Add.
	cs := &bar.Container[string]{}
	cs.Add("a")
	cs.Add("b")
	if cs.Len() != 2 {
		t.Errorf("string container Len = %d, want 2 (mock should not leak)", cs.Len())
	}
	if cs.Get(0) != "a" || cs.Get(1) != "b" {
		t.Errorf("string container contents wrong: %q, %q", cs.Get(0), cs.Get(1))
	}
}

// Two different instantiations mocked simultaneously with distinct
// replacements — each dispatches to its own mock.
func TestGenericMethod_TwoInstantiationsIndependently(t *testing.T) {
	var intCalls, stringCalls int
	rewire.Func(t, (*bar.Container[int]).Add, func(c *bar.Container[int], v int) {
		intCalls++
	})
	rewire.Func(t, (*bar.Container[string]).Add, func(c *bar.Container[string], v string) {
		stringCalls++
	})

	ci := &bar.Container[int]{}
	ci.Add(42)
	ci.Add(43)

	cs := &bar.Container[string]{}
	cs.Add("hello")

	if intCalls != 2 {
		t.Errorf("intCalls = %d, want 2", intCalls)
	}
	if stringCalls != 1 {
		t.Errorf("stringCalls = %d, want 1", stringCalls)
	}
}

// Spy pattern on a generic method via rewire.Real. The real function
// value has the free-function-with-receiver shape:
//
//	func(*bar.Container[int], int)
//
// since Real_<Name> is emitted as an exported generic free function.
func TestGenericMethod_SpyViaRewireReal(t *testing.T) {
	realAdd := rewire.Real(t, (*bar.Container[int]).Add)

	var audit []int
	rewire.Func(t, (*bar.Container[int]).Add, func(c *bar.Container[int], v int) {
		audit = append(audit, v)
		realAdd(c, v) // delegate to the real body
	})

	ci := &bar.Container[int]{}
	ci.Add(10)
	ci.Add(20)
	ci.Add(30)

	if ci.Len() != 3 {
		t.Errorf("real body should have run: Len=%d, want 3", ci.Len())
	}
	if ci.Get(0) != 10 || ci.Get(1) != 20 || ci.Get(2) != 30 {
		t.Errorf("real body produced wrong contents: %v", []int{ci.Get(0), ci.Get(1), ci.Get(2)})
	}
	wantAudit := []int{10, 20, 30}
	if len(audit) != len(wantAudit) {
		t.Fatalf("audit = %v, want %v", audit, wantAudit)
	}
	for i := range audit {
		if audit[i] != wantAudit[i] {
			t.Errorf("audit[%d] = %d, want %d", i, audit[i], wantAudit[i])
		}
	}
}

// Restore clears the mock for the specific instantiation only.
func TestGenericMethod_RestoreSpecificInstantiation(t *testing.T) {
	rewire.Func(t, (*bar.Container[int]).Add, func(c *bar.Container[int], v int) {
		// no-op
	})
	rewire.Func(t, (*bar.Container[string]).Add, func(c *bar.Container[string], v string) {
		// no-op
	})

	rewire.RestoreFunc(t, (*bar.Container[int]).Add)

	// After restore, int Add runs real again.
	ci := &bar.Container[int]{}
	ci.Add(1)
	if ci.Len() != 1 {
		t.Errorf("after Restore on int: Len=%d, want 1", ci.Len())
	}

	// String mock is still in effect.
	cs := &bar.Container[string]{}
	cs.Add("hi")
	if cs.Len() != 0 {
		t.Errorf("string mock should still apply: Len=%d, want 0", cs.Len())
	}
}
