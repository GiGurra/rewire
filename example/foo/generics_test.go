package foo

import (
	"strings"
	"testing"

	"github.com/GiGurra/rewire/example/bar"
	"github.com/GiGurra/rewire/pkg/rewire"
)

// Per-instantiation mocking: replace only the [int, string] instantiation
// of bar.Map. Other instantiations in the same test must keep running
// the real implementation.
func TestGeneric_MockOnlyOneInstantiation(t *testing.T) {
	rewire.Func(t, bar.Map[int, string], func(in []int, f func(int) string) []string {
		return []string{"mocked"}
	})

	gotInt := bar.Map([]int{1, 2, 3}, func(x int) string { return "real" })
	if len(gotInt) != 1 || gotInt[0] != "mocked" {
		t.Errorf("Map[int,string]: got %v, want [mocked]", gotInt)
	}

	// A different instantiation must NOT be affected.
	gotFloat := bar.Map([]float64{1, 2}, func(x float64) bool { return x > 0 })
	if len(gotFloat) != 2 || !gotFloat[0] || !gotFloat[1] {
		t.Errorf("Map[float64,bool]: got %v, want [true true]", gotFloat)
	}
}

// Two different instantiations mocked simultaneously with different
// replacements. Each must dispatch to its own mock.
func TestGeneric_MockTwoInstantiationsIndependently(t *testing.T) {
	rewire.Func(t, bar.Map[int, string], func(in []int, f func(int) string) []string {
		return []string{"int-mock"}
	})
	rewire.Func(t, bar.Map[float64, bool], func(in []float64, f func(float64) bool) []bool {
		return []bool{false, false, false}
	})

	gotInt := bar.Map([]int{42}, func(x int) string { return "real" })
	if len(gotInt) != 1 || gotInt[0] != "int-mock" {
		t.Errorf("Map[int,string]: got %v, want [int-mock]", gotInt)
	}

	gotFloat := bar.Map([]float64{1.0}, func(x float64) bool { return true })
	want := []bool{false, false, false}
	if len(gotFloat) != len(want) {
		t.Errorf("Map[float64,bool]: got %v, want %v", gotFloat, want)
	}

	// A third, unmocked instantiation still runs real.
	gotString := bar.Map([]string{"a", "b"}, strings.ToUpper)
	if len(gotString) != 2 || gotString[0] != "A" || gotString[1] != "B" {
		t.Errorf("Map[string,string]: got %v, want [A B]", gotString)
	}
}

// Restore must clear only the specific instantiation.
func TestGeneric_RestoreSpecificInstantiation(t *testing.T) {
	rewire.Func(t, bar.Map[int, string], func(in []int, f func(int) string) []string {
		return []string{"mocked"}
	})
	rewire.Func(t, bar.Map[float64, bool], func(in []float64, f func(float64) bool) []bool {
		return []bool{true}
	})

	rewire.RestoreFunc(t, bar.Map[int, string])

	// [int,string] is back to real
	got := bar.Map([]int{1}, func(x int) string { return "real" })
	if len(got) != 1 || got[0] != "real" {
		t.Errorf("after Restore, Map[int,string]: got %v, want [real]", got)
	}

	// [float64,bool] mock still applies
	gotFloat := bar.Map([]float64{1.0}, func(x float64) bool { return false })
	if len(gotFloat) != 1 || !gotFloat[0] {
		t.Errorf("Map[float64,bool]: got %v, want [true]", gotFloat)
	}
}

// Spy pattern via rewire.Real on a generic instantiation — same API
// as non-generic, no IDE complaints about synthetic identifiers.
func TestGeneric_SpyViaRewireReal(t *testing.T) {
	realMap := rewire.Real(t, bar.Map[int, string])

	rewire.Func(t, bar.Map[int, string], func(in []int, f func(int) string) []string {
		out := realMap(in, f)
		for i := range out {
			out[i] += "!"
		}
		return out
	})

	got := bar.Map([]int{1, 2, 3}, func(x int) string {
		switch x {
		case 1:
			return "one"
		case 2:
			return "two"
		default:
			return "many"
		}
	})
	want := []string{"one!", "two!", "many!"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("at %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

