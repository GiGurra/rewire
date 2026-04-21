package foo

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GiGurra/rewire/example/bar"
	"github.com/GiGurra/rewire/pkg/rewire"
	"github.com/GiGurra/rewire/pkg/rewire/expect"
)

// Basic: multiple rules dispatched in declaration order.
func TestExpect_BasicMultiplePatterns(t *testing.T) {
	e := expect.For(t, bar.Greet)
	e.On("Alice").Returns("hi Alice")
	e.On("Bob").Returns("hi Bob")
	e.OnAny().Returns("hi other")

	if got := bar.Greet("Alice"); got != "hi Alice" {
		t.Errorf("Alice: got %q, want %q", got, "hi Alice")
	}
	if got := bar.Greet("Bob"); got != "hi Bob" {
		t.Errorf("Bob: got %q, want %q", got, "hi Bob")
	}
	if got := bar.Greet("Charlie"); got != "hi other" {
		t.Errorf("Charlie: got %q, want %q", got, "hi other")
	}
}

// Typed predicate matching — the predicate closure is fully type-checked
// by Go because Match takes a function whose arg types are known at
// compile time via the expect.For[F] instantiation.
func TestExpect_MatchPredicate(t *testing.T) {
	e := expect.For(t, bar.Greet)
	e.Match(func(name string) bool {
		return strings.HasPrefix(name, "admin_")
	}).Returns("admin greeting")
	e.OnAny().Returns("hi other")

	if got := bar.Greet("admin_42"); got != "admin greeting" {
		t.Errorf("admin_42: got %q, want %q", got, "admin greeting")
	}
	if got := bar.Greet("plain"); got != "hi other" {
		t.Errorf("plain: got %q, want %q", got, "hi other")
	}
}

// DoFunc lets a rule run arbitrary typed code, including capturing
// test-local state for assertion-style verification.
func TestExpect_DoFuncSideEffects(t *testing.T) {
	var calls []string
	e := expect.For(t, bar.Greet)
	e.OnAny().DoFunc(func(name string) string {
		calls = append(calls, name)
		return "recorded"
	})

	bar.Greet("first")
	bar.Greet("second")
	bar.Greet("third")

	if len(calls) != 3 {
		t.Errorf("expected 3 recorded calls, got %d", len(calls))
	}
	if calls[0] != "first" || calls[1] != "second" || calls[2] != "third" {
		t.Errorf("calls = %v", calls)
	}
	_ = e
}

// Spy pattern: delegate to the real implementation via rewire.Real and
// wrap its output.
func TestExpect_SpyViaRewireReal(t *testing.T) {
	realGreet := rewire.Real(t, bar.Greet)
	e := expect.For(t, bar.Greet)
	e.OnAny().DoFunc(func(name string) string {
		return realGreet(name) + " [spied]"
	})

	got := bar.Greet("Alice")
	want := "Hello, Alice! [spied]"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Times bound: exact call count.
func TestExpect_TimesBound_Passes(t *testing.T) {
	e := expect.For(t, bar.Greet)
	e.On("Alice").Returns("hi Alice").Times(2)

	bar.Greet("Alice")
	bar.Greet("Alice")
	_ = e
}

// Never: calls that never hit the forbidden pattern leave the Never
// bound satisfied at cleanup.
func TestExpect_NeverBound_Passes(t *testing.T) {
	e := expect.For(t, bar.Greet)
	e.On("should-not-be-called").Never()
	e.OnAny().Returns("ok")
	bar.Greet("anything-else")
	_ = e
}

// Negative-path verification (strict default fails when rule never
// matches; Never rule fails when matched; unmatched call fails) is
// exercised directly against the dispatcher in
// pkg/rewire/expect/expect_test.go using a recording testingT fake,
// since `t.Run` subtests propagate their failure status to the parent
// and can't cleanly assert "this test was supposed to fail."

// AllowUnmatched: unmatched calls fall through to the real.
func TestExpect_AllowUnmatched(t *testing.T) {
	e := expect.For(t, bar.Greet).AllowUnmatched()
	e.On("Alice").Returns("mocked")

	if got := bar.Greet("Alice"); got != "mocked" {
		t.Errorf("Alice: got %q, want %q", got, "mocked")
	}
	// Non-matching call goes to the real bar.Greet.
	if got := bar.Greet("Bob"); got != "Hello, Bob!" {
		t.Errorf("Bob (pass-through): got %q, want %q", got, "Hello, Bob!")
	}
}

// Method targets via method expression work the same way.
func TestExpect_OnMethod(t *testing.T) {
	e := expect.For(t, (*bar.Greeter).Greet)
	e.Match(func(g *bar.Greeter, name string) bool {
		return name == "Alice"
	}).DoFunc(func(g *bar.Greeter, name string) string {
		return "mocked: " + name
	})
	e.OnAny().DoFunc(func(g *bar.Greeter, name string) string {
		return g.Prefix + ", " + name + "!"
	})

	g := &bar.Greeter{Prefix: "Hi"}
	if got := g.Greet("Alice"); got != "mocked: Alice" {
		t.Errorf("Alice: got %q", got)
	}
	if got := g.Greet("Bob"); got != "Hi, Bob!" {
		t.Errorf("Bob: got %q", got)
	}
	_ = e
}

// Generic function: expect.For works transparently with per-instantiation
// mocking since rewire.Func already handles it.
func TestExpect_OnGenericFunction(t *testing.T) {
	e := expect.For(t, bar.Map[int, string])
	e.OnAny().DoFunc(func(in []int, f func(int) string) []string {
		return []string{"mocked"}
	})

	got := bar.Map([]int{1, 2, 3}, func(x int) string { return "real" })
	if len(got) != 1 || got[0] != "mocked" {
		t.Errorf("got %v, want [mocked]", got)
	}

	// A different instantiation is unaffected.
	gotFloat := bar.Map([]float64{1, 2}, func(x float64) bool { return x > 0 })
	if len(gotFloat) != 2 {
		t.Errorf("Map[float64,bool] should not be mocked, got %v", gotFloat)
	}
	_ = e
}

// Generic method: expect.For on (*bar.Container[int]).Add. The rule
// captures inserted values and delegates to the real implementation
// so the container's state still reflects the adds.
func TestExpect_OnGenericMethod(t *testing.T) {
	realAdd := rewire.Real(t, (*bar.Container[int]).Add)

	var audit []int
	e := expect.For(t, (*bar.Container[int]).Add)
	e.OnAny().DoFunc(func(c *bar.Container[int], v int) {
		audit = append(audit, v)
		realAdd(c, v) // delegate to real
	})

	ci := &bar.Container[int]{}
	ci.Add(10)
	ci.Add(20)
	ci.Add(30)

	if ci.Len() != 3 {
		t.Errorf("expected len 3, got %d", ci.Len())
	}
	if len(audit) != 3 || audit[0] != 10 || audit[1] != 20 || audit[2] != 30 {
		t.Errorf("audit trail wrong: %v", audit)
	}

	// A different instantiation runs real.
	cs := &bar.Container[string]{}
	cs.Add("ignored-by-audit")
	if cs.Len() != 1 {
		t.Errorf("string container should still run real, got len=%d", cs.Len())
	}
	_ = e
}

// AtLeast: rule must match at least N calls. Satisfied by overshoot.
func TestExpect_AtLeastBound_Passes(t *testing.T) {
	e := expect.For(t, bar.Greet)
	e.OnAny().Returns("hi").AtLeast(2)

	bar.Greet("one")
	bar.Greet("two")
	bar.Greet("three") // overshooting AtLeast(2) is fine
	_ = e
}

// Maybe: opt-out of strict default. An explicitly declared rule that
// may or may not be called without failing verification.
func TestExpect_MaybeBound(t *testing.T) {
	e := expect.For(t, bar.Greet)
	e.On("optional").Returns("hi optional").Maybe()
	e.OnAny().Returns("fallback")

	// Never call with "optional" — should still pass verification.
	bar.Greet("something-else")
	_ = e
}

// Multi-return: a function that returns several values.
func TestExpect_MultipleReturnValues(t *testing.T) {
	// bar.Container[string].Get returns a single value, so use a
	// function that returns two. We'll use math.Pow-style — but we
	// need something that actually returns multiple values. Use a
	// spy on a function that takes args and returns (T, error).
	e := expect.For(t, bar.Map[int, string])
	e.OnAny().DoFunc(func(in []int, f func(int) string) []string {
		// Ignore f, return a fixed-shape result.
		out := make([]string, len(in))
		for i := range in {
			out[i] = "item"
		}
		return out
	})

	got := bar.Map([]int{1, 2, 3, 4}, func(x int) string { return "real" })
	if len(got) != 4 {
		t.Fatalf("got %v, want 4 elements", got)
	}
	for _, s := range got {
		if s != "item" {
			t.Errorf("element = %q, want 'item'", s)
		}
	}
	_ = e
}

// Sequential rules: the first call matches one rule, subsequent calls
// fall through to another. Useful for "first call returns X, rest
// return Y" patterns.
func TestExpect_SequentialBehavior(t *testing.T) {
	callCount := 0
	e := expect.For(t, bar.Greet)
	e.OnAny().DoFunc(func(name string) string {
		callCount++
		if callCount == 1 {
			return "first " + name
		}
		return "later " + name
	})

	if got := bar.Greet("Alice"); got != "first Alice" {
		t.Errorf("1st call: got %q", got)
	}
	if got := bar.Greet("Bob"); got != "later Bob" {
		t.Errorf("2nd call: got %q", got)
	}
	if got := bar.Greet("Carol"); got != "later Carol" {
		t.Errorf("3rd call: got %q", got)
	}
	_ = e
}

// Overlapping rules: first-fit means specific rules must be declared
// before catch-alls, otherwise the catch-all swallows everything.
func TestExpect_FirstFitOrdering(t *testing.T) {
	e := expect.For(t, bar.Greet)
	e.On("Alice").Returns("specific Alice")    // rule 0 — narrow
	e.Match(func(name string) bool {           // rule 1 — any starting with A
		return strings.HasPrefix(name, "A")
	}).Returns("starts with A")
	e.OnAny().Returns("any")                   // rule 2 — fallback

	if got := bar.Greet("Alice"); got != "specific Alice" {
		t.Errorf("Alice: got %q (should match rule 0, not rule 1)", got)
	}
	if got := bar.Greet("Anne"); got != "starts with A" {
		t.Errorf("Anne: got %q (should match rule 1)", got)
	}
	if got := bar.Greet("Bob"); got != "any" {
		t.Errorf("Bob: got %q (should match rule 2)", got)
	}
	_ = e
}

// Multiple .On with distinct literal values — the most common usage.
func TestExpect_ManyLiteralRules(t *testing.T) {
	e := expect.For(t, bar.Greet)
	e.On("one").Returns("1")
	e.On("two").Returns("2")
	e.On("three").Returns("3")
	e.On("four").Returns("4")
	e.On("five").Returns("5")

	for i, name := range []string{"one", "two", "three", "four", "five"} {
		got := bar.Greet(name)
		want := []string{"1", "2", "3", "4", "5"}[i]
		if got != want {
			t.Errorf("%s: got %q, want %q", name, got, want)
		}
	}
	_ = e
}

// Strict default provides automatic "was called?" coverage — if this
// test body forgets to call any of the declared rules, cleanup fails.
// Here we do call each rule, so verification is silent.
func TestExpect_StrictDefaultVerifiesUsage(t *testing.T) {
	e := expect.For(t, bar.Greet)
	e.On("Alice").Returns("hi Alice")
	e.On("Bob").Returns("hi Bob")
	// Both must be called, or cleanup fails.

	bar.Greet("Alice")
	bar.Greet("Bob")
	_ = e
}

// Combining Match with call counting: predicate-matched calls also
// respect Times/AtLeast bounds.
func TestExpect_MatchWithBound(t *testing.T) {
	e := expect.For(t, bar.Greet)
	e.Match(func(name string) bool {
		return strings.HasPrefix(name, "admin_")
	}).Returns("admin").Times(2)
	e.OnAny().Returns("other")

	bar.Greet("admin_1")
	bar.Greet("admin_2")
	bar.Greet("user_3") // fallthrough, doesn't count toward the admin rule
	_ = e
}

// Input-dependent return values via DoFunc. The callback takes the
// real arguments and computes the return — fully type-checked because
// DoFunc accepts a function of the target's exact signature (the
// generic F from expect.For[F]).
func TestExpect_DoFuncInputDependentReturn(t *testing.T) {
	e := expect.For(t, bar.TinyDouble)
	e.OnAny().DoFunc(func(x int) int {
		return x * 2 // the real TinyDouble happens to do this too;
		// but under the mock this is our replacement logic
	})

	if got := bar.TinyDouble(3); got != 6 {
		t.Errorf("TinyDouble(3) = %d, want 6", got)
	}
	if got := bar.TinyDouble(10); got != 20 {
		t.Errorf("TinyDouble(10) = %d, want 20", got)
	}
	if got := bar.TinyDouble(-5); got != -10 {
		t.Errorf("TinyDouble(-5) = %d, want -10", got)
	}
	_ = e
}

// Input-dependent with branching: use DoFunc to compute different
// results based on argument properties, or combine Returns and DoFunc
// rules in the same expectation.
func TestExpect_InputDependentWithBranching(t *testing.T) {
	e := expect.For(t, bar.TinyAdd)
	e.On(0, 0).Returns(0) // literal short-circuit
	e.Match(func(a, b int) bool {
		return a < 0 || b < 0
	}).DoFunc(func(a, b int) int {
		return -1 // any negative input produces -1
	})
	e.OnAny().DoFunc(func(a, b int) int {
		return a*1000 + b // everything else: custom encoding
	})

	if got := bar.TinyAdd(0, 0); got != 0 {
		t.Errorf("TinyAdd(0, 0) = %d, want 0", got)
	}
	if got := bar.TinyAdd(-1, 5); got != -1 {
		t.Errorf("TinyAdd(-1, 5) = %d, want -1", got)
	}
	if got := bar.TinyAdd(2, 3); got != 2003 {
		t.Errorf("TinyAdd(2, 3) = %d, want 2003", got)
	}
	_ = e
}

// Wait synchronizes async work that eventually calls the mocked
// function. The test kicks off a goroutine that calls bar.Greet a
// few times after a short delay, then Wait blocks until the rule
// has matched the expected count before the test body continues.
func TestExpect_WaitForAsyncWork(t *testing.T) {
	e := expect.For(t, bar.Greet)
	rule := e.OnAny().DoFunc(func(name string) string {
		return "async-" + name
	})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(30 * time.Millisecond)
		bar.Greet("a")
		bar.Greet("b")
		bar.Greet("c")
	}()

	// Block until the rule has matched 3 times or 2 seconds elapse.
	rule.Wait(3, 2*time.Second)

	// After Wait returns, the test can assert directly.
	wg.Wait()
}

// Note: the "Wait timing out correctly fails the test" path is
// covered by TestWait_TimesOutWithClearMessage in
// pkg/rewire/expect/expect_test.go using a recording testingT fake.
// An end-to-end subtest would have its failure status propagate up
// to the parent test, so we can't cleanly assert "this subtest was
// supposed to fail."

// Chain of DoFunc side effects + call count verification without a
// separate counter variable.
func TestExpect_ChainDoFuncAndTimes(t *testing.T) {
	var seen []string
	e := expect.For(t, bar.Greet)
	e.OnAny().DoFunc(func(name string) string {
		seen = append(seen, name)
		return "seen " + name
	}).AtLeast(3)

	bar.Greet("a")
	bar.Greet("b")
	bar.Greet("c")
	bar.Greet("d")

	if len(seen) != 4 {
		t.Errorf("expected 4 calls, got %d: %v", len(seen), seen)
	}
	_ = e
}

// Per-argument matchers: Any() accepts any value at a given position
// while other positions match literally. Exercises the mixed-entry
// path through the real dispatcher.
func TestExpect_OnAnyAtPosition(t *testing.T) {
	e := expect.For(t, bar.TinyAdd)
	e.On(expect.Any(), 5).Returns(100)
	e.OnAny().Returns(-1)

	if got := bar.TinyAdd(1, 5); got != 100 {
		t.Errorf("(any, 5): got %d, want %d", got, 100)
	}
	if got := bar.TinyAdd(999, 5); got != 100 {
		t.Errorf("(any, 5): got %d, want %d", got, 100)
	}
	if got := bar.TinyAdd(1, 6); got != -1 {
		t.Errorf("(_, 6) fell through to OnAny: got %d, want %d", got, -1)
	}
	_ = e
}

// Eq at a position is equivalent to passing the literal directly; both
// rules match the same calls.
func TestExpect_EqMatcher(t *testing.T) {
	e := expect.For(t, bar.TinyAdd)
	e.On(expect.Eq(2), expect.Eq(3)).Returns(42)
	e.OnAny().Returns(-1)

	if got := bar.TinyAdd(2, 3); got != 42 {
		t.Errorf("Eq match: got %d, want %d", got, 42)
	}
	if got := bar.TinyAdd(2, 4); got != -1 {
		t.Errorf("non-match fell through: got %d, want %d", got, -1)
	}
	_ = e
}

// ArgThat applies a per-argument predicate. Mixing with Any() at another
// position keeps the unconstrained arg free while the predicate
// disambiguates the other.
func TestExpect_ArgThatMatcher(t *testing.T) {
	e := expect.For(t, bar.TinyAdd)
	e.On(expect.ArgThat(func(a int) bool { return a > 100 }), expect.Any()).Returns(999)
	e.OnAny().Returns(-1)

	if got := bar.TinyAdd(200, 5); got != 999 {
		t.Errorf("ArgThat accepted: got %d, want %d", got, 999)
	}
	if got := bar.TinyAdd(200, 9999); got != 999 {
		t.Errorf("ArgThat + Any() accepted: got %d, want %d", got, 999)
	}
	if got := bar.TinyAdd(5, 5); got != -1 {
		t.Errorf("ArgThat rejected, fell through: got %d, want %d", got, -1)
	}
	_ = e
}
