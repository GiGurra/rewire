package expect

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// These unit tests exercise the DSL's internal validation and state
// machine — the pieces that don't require toolexec or actual function
// rewriting. End-to-end tests that exercise the full mocking flow live
// in example/foo/expectations_test.go.

// --- matcher tests -----------------------------------------------------------

func TestLiteralMatcher_EqualValues(t *testing.T) {
	m := &literalMatcher{
		entries: []argEntry{{literal: reflect.ValueOf("alice")}},
	}
	if !m.match([]reflect.Value{reflect.ValueOf("alice")}) {
		t.Error("expected literal matcher to match equal values")
	}
	if m.match([]reflect.Value{reflect.ValueOf("bob")}) {
		t.Error("expected literal matcher to reject unequal values")
	}
}

func TestLiteralMatcher_DifferentArgCount(t *testing.T) {
	m := &literalMatcher{
		entries: []argEntry{{literal: reflect.ValueOf("alice")}},
	}
	if m.match([]reflect.Value{reflect.ValueOf("alice"), reflect.ValueOf(42)}) {
		t.Error("expected literal matcher to reject mismatched arg count")
	}
}

func TestLiteralMatcher_AnySentinel(t *testing.T) {
	m := &literalMatcher{
		entries: []argEntry{
			{matcher: Any()},
			{literal: reflect.ValueOf("bob")},
		},
	}
	if !m.match([]reflect.Value{reflect.ValueOf("alice"), reflect.ValueOf("bob")}) {
		t.Error("expected Any() to accept any value at position 0 while position 1 matches literally")
	}
	if m.match([]reflect.Value{reflect.ValueOf("alice"), reflect.ValueOf("other")}) {
		t.Error("expected position 1 literal mismatch to reject the call")
	}
}

func TestLiteralMatcher_EqSentinel(t *testing.T) {
	m := &literalMatcher{
		entries: []argEntry{{matcher: Eq("alice")}},
	}
	if !m.match([]reflect.Value{reflect.ValueOf("alice")}) {
		t.Error("expected Eq to match equal value")
	}
	if m.match([]reflect.Value{reflect.ValueOf("bob")}) {
		t.Error("expected Eq to reject unequal value")
	}
}

func TestLiteralMatcher_ArgThatSentinel(t *testing.T) {
	m := &literalMatcher{
		entries: []argEntry{
			{matcher: ArgThat(func(s string) bool { return strings.HasPrefix(s, "a") })},
		},
	}
	if !m.match([]reflect.Value{reflect.ValueOf("alice")}) {
		t.Error("expected ArgThat to accept 'alice'")
	}
	if m.match([]reflect.Value{reflect.ValueOf("bob")}) {
		t.Error("expected ArgThat to reject 'bob'")
	}
}

func TestArgMatchers_ParamType(t *testing.T) {
	if Any().paramType() != nil {
		t.Error("Any() should have no paramType constraint")
	}
	if got, want := Eq("x").paramType(), reflect.TypeOf(""); got != want {
		t.Errorf("Eq(string).paramType() = %v, want %v", got, want)
	}
	if got, want := ArgThat(func(i int) bool { return true }).paramType(), reflect.TypeOf(0); got != want {
		t.Errorf("ArgThat(int).paramType() = %v, want %v", got, want)
	}
}

func TestArgMatchers_Describe(t *testing.T) {
	if Any().describeArg() != "Any()" {
		t.Errorf("Any() describe = %q", Any().describeArg())
	}
	if got := Eq("hi").describeArg(); got != `Eq("hi")` {
		t.Errorf("Eq describe = %q", got)
	}
	if got := ArgThat(func(s string) bool { return true }).describeArg(); got != "ArgThat(func(string) bool)" {
		t.Errorf("ArgThat describe = %q", got)
	}
}

func TestPredicateMatcher_CallsPredicate(t *testing.T) {
	called := false
	pred := func(s string) bool {
		called = true
		return strings.HasPrefix(s, "a")
	}
	m := &predicateMatcher{fn: reflect.ValueOf(pred), descr: ".Match(...)"}
	if !m.match([]reflect.Value{reflect.ValueOf("apple")}) {
		t.Error("expected predicate matcher to accept apple")
	}
	if !called {
		t.Error("expected predicate to have been called")
	}
	if m.match([]reflect.Value{reflect.ValueOf("banana")}) {
		t.Error("expected predicate matcher to reject banana")
	}
}

func TestAnyMatcher_AlwaysMatches(t *testing.T) {
	m := &anyMatcher{}
	if !m.match(nil) {
		t.Error("expected anyMatcher to match nil args")
	}
	if !m.match([]reflect.Value{reflect.ValueOf(1), reflect.ValueOf("x")}) {
		t.Error("expected anyMatcher to match any args")
	}
}

// --- validation tests --------------------------------------------------------

func TestValidateLiteralArgs_CountMismatch(t *testing.T) {
	fnType := reflect.TypeOf(func(a int, b string) {})
	err := validateLiteralArgs(fnType, []any{1})
	if err == nil || !strings.Contains(err.Error(), "got 1 args") {
		t.Errorf("expected count mismatch error, got %v", err)
	}
}

func TestValidateLiteralArgs_WrongType(t *testing.T) {
	fnType := reflect.TypeOf(func(a int) {})
	err := validateLiteralArgs(fnType, []any{"not an int"})
	if err == nil || !strings.Contains(err.Error(), "not assignable") {
		t.Errorf("expected type mismatch error, got %v", err)
	}
}

func TestValidateLiteralArgs_NilForNilable(t *testing.T) {
	fnType := reflect.TypeOf(func(p *int) {})
	if err := validateLiteralArgs(fnType, []any{nil}); err != nil {
		t.Errorf("expected nil to be accepted for *int, got %v", err)
	}
}

func TestValidateLiteralArgs_NilForNonNilable(t *testing.T) {
	fnType := reflect.TypeOf(func(a int) {})
	err := validateLiteralArgs(fnType, []any{nil})
	if err == nil || !strings.Contains(err.Error(), "not nilable") {
		t.Errorf("expected non-nilable error, got %v", err)
	}
}

func TestValidateLiteralArgs_AnyAccepted(t *testing.T) {
	fnType := reflect.TypeOf(func(a int, b string) {})
	if err := validateLiteralArgs(fnType, []any{Any(), Any()}); err != nil {
		t.Errorf("expected Any() to be accepted for any position, got %v", err)
	}
}

func TestValidateLiteralArgs_EqTypeMatches(t *testing.T) {
	fnType := reflect.TypeOf(func(a int) {})
	if err := validateLiteralArgs(fnType, []any{Eq(42)}); err != nil {
		t.Errorf("expected Eq(int) to pass int-param check, got %v", err)
	}
}

func TestValidateLiteralArgs_EqTypeMismatch(t *testing.T) {
	fnType := reflect.TypeOf(func(a int) {})
	err := validateLiteralArgs(fnType, []any{Eq("not an int")})
	if err == nil || !strings.Contains(err.Error(), "not assignable") {
		t.Errorf("expected Eq type mismatch error, got %v", err)
	}
}

func TestValidateLiteralArgs_ArgThatTypeMatches(t *testing.T) {
	fnType := reflect.TypeOf(func(a string) {})
	if err := validateLiteralArgs(fnType, []any{ArgThat(func(s string) bool { return true })}); err != nil {
		t.Errorf("expected ArgThat(string) to pass string-param check, got %v", err)
	}
}

func TestValidateLiteralArgs_ArgThatTypeMismatch(t *testing.T) {
	fnType := reflect.TypeOf(func(a string) {})
	err := validateLiteralArgs(fnType, []any{ArgThat(func(i int) bool { return true })})
	if err == nil || !strings.Contains(err.Error(), "not assignable") {
		t.Errorf("expected ArgThat type mismatch error, got %v", err)
	}
}

func TestValidateLiteralArgs_ArgThatNilPredicate(t *testing.T) {
	fnType := reflect.TypeOf(func(a string) {})
	err := validateLiteralArgs(fnType, []any{ArgThat[string](nil)})
	if err == nil || !strings.Contains(err.Error(), "ArgThat predicate is nil") {
		t.Errorf("expected nil-predicate error, got %v", err)
	}
}

func TestValidateLiteralArgs_MixedLiteralAndMatcher(t *testing.T) {
	fnType := reflect.TypeOf(func(a int, b string, c int) {})
	// Literal at 0, Any at 1, Eq at 2.
	if err := validateLiteralArgs(fnType, []any{1, Any(), Eq(3)}); err != nil {
		t.Errorf("expected mixed literal+matcher to validate, got %v", err)
	}
}

func TestValidatePredicate_RightShape(t *testing.T) {
	fnType := reflect.TypeOf(func(a int, b string) string { return "" })
	pred := func(a int, b string) bool { return true }
	if _, err := validatePredicate(fnType, pred); err != nil {
		t.Errorf("expected valid predicate, got %v", err)
	}
}

func TestValidatePredicate_WrongArgCount(t *testing.T) {
	fnType := reflect.TypeOf(func(a int) {})
	pred := func(a int, b int) bool { return true }
	_, err := validatePredicate(fnType, pred)
	if err == nil || !strings.Contains(err.Error(), "takes 2 args") {
		t.Errorf("expected arg count error, got %v", err)
	}
}

func TestValidatePredicate_WrongArgType(t *testing.T) {
	fnType := reflect.TypeOf(func(a int) {})
	pred := func(a string) bool { return true }
	_, err := validatePredicate(fnType, pred)
	if err == nil || !strings.Contains(err.Error(), "does not match target") {
		t.Errorf("expected arg type error, got %v", err)
	}
}

func TestValidatePredicate_NonFunction(t *testing.T) {
	fnType := reflect.TypeOf(func(a int) {})
	_, err := validatePredicate(fnType, 42)
	if err == nil || !strings.Contains(err.Error(), "must be a function") {
		t.Errorf("expected function error, got %v", err)
	}
}

func TestValidatePredicate_WrongReturn(t *testing.T) {
	fnType := reflect.TypeOf(func(a int) {})
	pred := func(a int) string { return "" }
	_, err := validatePredicate(fnType, pred)
	if err == nil || !strings.Contains(err.Error(), "single bool") {
		t.Errorf("expected return error, got %v", err)
	}
}

func TestConvertReturnValues_CountMismatch(t *testing.T) {
	fnType := reflect.TypeOf(func() (string, error) { return "", nil })
	_, err := convertReturnValues(fnType, []any{"only one"})
	if err == nil || !strings.Contains(err.Error(), "got 1 values") {
		t.Errorf("expected count mismatch, got %v", err)
	}
}

func TestConvertReturnValues_CorrectTypes(t *testing.T) {
	fnType := reflect.TypeOf(func() (string, int) { return "", 0 })
	values, err := convertReturnValues(fnType, []any{"hi", 42})
	if err != nil {
		t.Fatal(err)
	}
	if values[0].Interface() != "hi" || values[1].Interface() != 42 {
		t.Errorf("got %v", values)
	}
}

func TestConvertReturnValues_NilForError(t *testing.T) {
	fnType := reflect.TypeOf(func() error { return nil })
	values, err := convertReturnValues(fnType, []any{nil})
	if err != nil {
		t.Fatal(err)
	}
	if !values[0].IsZero() {
		t.Errorf("expected zero error value, got %v", values[0])
	}
}

// --- bound check tests -------------------------------------------------------

func TestBound_Any(t *testing.T) {
	b := bound{kind: boundAny}
	for _, n := range []int{0, 1, 100} {
		if msg := b.check(n); msg != "" {
			t.Errorf("boundAny should accept count=%d, got: %s", n, msg)
		}
	}
}

func TestBound_AtLeast(t *testing.T) {
	b := bound{kind: boundAtLeast, n: 2}
	if b.check(2) != "" {
		t.Error("boundAtLeast(2) should accept count=2")
	}
	if b.check(5) != "" {
		t.Error("boundAtLeast(2) should accept count=5")
	}
	if msg := b.check(1); !strings.Contains(msg, "at least 2") {
		t.Errorf("boundAtLeast(2) count=1 should fail with 'at least 2' message, got %q", msg)
	}
}

func TestBound_Exact(t *testing.T) {
	b := bound{kind: boundExact, n: 3}
	if b.check(3) != "" {
		t.Error("boundExact(3) should accept count=3")
	}
	if msg := b.check(2); !strings.Contains(msg, "exactly 3") {
		t.Errorf("boundExact(3) count=2 should fail, got %q", msg)
	}
	if msg := b.check(4); !strings.Contains(msg, "exactly 3") {
		t.Errorf("boundExact(3) count=4 should fail, got %q", msg)
	}
}

func TestBound_Never(t *testing.T) {
	b := bound{kind: boundNever}
	if b.check(0) != "" {
		t.Error("boundNever should accept count=0")
	}
	if msg := b.check(1); !strings.Contains(msg, "never") {
		t.Errorf("boundNever count=1 should fail, got %q", msg)
	}
}

// --- recording testingT for DSL self-tests ----------------------------------

// recordingT is a fake testingT used to exercise the DSL's error paths
// without propagating the errors to the enclosing real test. It records
// Errorf/Fatal/Fatalf invocations into a slice so assertions can check
// that the DSL reported the expected diagnostics.
type recordingT struct {
	errors   []string
	fatals   []string
	cleanups []func()
}

func (r *recordingT) Helper()                                    {}
func (r *recordingT) Errorf(format string, args ...any)          { r.errors = append(r.errors, formatMsg(format, args...)) }
func (r *recordingT) Fatal(args ...any)                          { r.fatals = append(r.fatals, formatArgsPlain(args)) }
func (r *recordingT) Fatalf(format string, args ...any)          { r.fatals = append(r.fatals, formatMsg(format, args...)) }
func (r *recordingT) Cleanup(fn func())                          { r.cleanups = append(r.cleanups, fn) }

// runCleanups invokes the registered cleanup functions in LIFO order,
// matching how *testing.T runs them.
func (r *recordingT) runCleanups() {
	for i := len(r.cleanups) - 1; i >= 0; i-- {
		r.cleanups[i]()
	}
}

func formatMsg(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}

func formatArgsPlain(args []any) string {
	return fmt.Sprintf("%v", args)
}

// --- dispatch / verify state-machine tests ----------------------------------

func TestDispatch_FirstFitOrdering(t *testing.T) {
	r := &recordingT{}
	target := func(name string) string { return "real " + name }
	e := newExpectation[func(string) string](r, target)
	e.On("Alice").Returns("first-alice")
	e.On("Alice").Returns("second-alice") // second .On("Alice") — should never match
	e.OnAny().Returns("other")

	out := e.dispatch([]reflect.Value{reflect.ValueOf("Alice")})
	if got := out[0].String(); got != "first-alice" {
		t.Errorf("first-fit expected %q, got %q", "first-alice", got)
	}
	// And a non-Alice call hits OnAny.
	out = e.dispatch([]reflect.Value{reflect.ValueOf("Bob")})
	if got := out[0].String(); got != "other" {
		t.Errorf("fallback expected %q, got %q", "other", got)
	}
}

func TestDispatch_StrictDefaultFailsAtVerify(t *testing.T) {
	r := &recordingT{}
	target := func(s string) string { return "" }
	e := newExpectation[func(string) string](r, target)
	e.On("Alice").Returns("hi") // strict default — requires ≥1 call

	// No dispatch happens; run cleanups.
	r.runCleanups()

	if len(r.errors) == 0 {
		t.Fatal("expected verify to report an error for uncalled strict rule")
	}
	if !strings.Contains(r.errors[0], "at least 1") {
		t.Errorf("expected 'at least 1' diagnostic, got: %s", r.errors[0])
	}
}

func TestDispatch_OnAnyLenientNoErrorAtVerify(t *testing.T) {
	r := &recordingT{}
	target := func(s string) string { return "" }
	e := newExpectation[func(string) string](r, target)
	e.OnAny().Returns("hi") // lenient default — zero calls is fine

	r.runCleanups()

	if len(r.errors) > 0 {
		t.Errorf("expected no errors for unmatched OnAny, got: %v", r.errors)
	}
}

func TestDispatch_UnmatchedCallErrors(t *testing.T) {
	r := &recordingT{}
	target := func(s string) string { return "" }
	e := newExpectation[func(string) string](r, target)
	e.On("Alice").Returns("hi")

	// Call with something no rule matches.
	_ = e.dispatch([]reflect.Value{reflect.ValueOf("Bob")})

	if len(r.errors) == 0 {
		t.Fatal("expected unmatched call to produce an error")
	}
	if !strings.Contains(r.errors[0], "unexpected call") {
		t.Errorf("expected 'unexpected call' diagnostic, got: %s", r.errors[0])
	}
}

func TestDispatch_NeverRuleMatchedErrors(t *testing.T) {
	r := &recordingT{}
	target := func(s string) string { return "" }
	e := newExpectation[func(string) string](r, target)
	e.On("forbidden").Never()
	e.OnAny().Returns("ok")

	// Match the Never rule — should error at call time.
	_ = e.dispatch([]reflect.Value{reflect.ValueOf("forbidden")})

	if len(r.errors) == 0 {
		t.Fatal("expected Never rule match to produce an error")
	}
	if !strings.Contains(r.errors[0], ".Never()") {
		t.Errorf("expected '.Never()' diagnostic, got: %s", r.errors[0])
	}
}

func TestDispatch_TimesBoundFailsWhenExceeded(t *testing.T) {
	r := &recordingT{}
	target := func(s string) string { return "" }
	e := newExpectation[func(string) string](r, target)
	e.On("Alice").Returns("hi").Times(2)

	// Call three times — Times(2) wants exactly 2.
	_ = e.dispatch([]reflect.Value{reflect.ValueOf("Alice")})
	_ = e.dispatch([]reflect.Value{reflect.ValueOf("Alice")})
	_ = e.dispatch([]reflect.Value{reflect.ValueOf("Alice")})

	r.runCleanups()

	if len(r.errors) == 0 {
		t.Fatal("expected Times(2) to fail at verify when called 3 times")
	}
	if !strings.Contains(r.errors[0], "exactly 2") {
		t.Errorf("expected 'exactly 2' diagnostic, got: %s", r.errors[0])
	}
}

func TestDispatch_DoFuncInvokesCallback(t *testing.T) {
	r := &recordingT{}
	target := func(a, b int) int { return 0 }
	e := newExpectation[func(int, int) int](r, target)
	e.OnAny().DoFunc(func(a, b int) int { return a*1000 + b })

	out := e.dispatch([]reflect.Value{reflect.ValueOf(3), reflect.ValueOf(4)})
	if got := out[0].Int(); got != 3004 {
		t.Errorf("DoFunc should produce 3004, got %d", got)
	}
}

// --- Wait / async tests -----------------------------------------------------

func TestWait_ReturnsOnceCountReached(t *testing.T) {
	r := &recordingT{}
	target := func(name string) string { return "" }
	e := newExpectation[func(string) string](r, target)
	rule := e.OnAny().Returns("hi")

	// Dispatch twice from a goroutine after a small delay.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(20 * time.Millisecond)
		_ = e.dispatch([]reflect.Value{reflect.ValueOf("a")})
		_ = e.dispatch([]reflect.Value{reflect.ValueOf("b")})
	}()

	start := time.Now()
	rule.Wait(2, 2*time.Second)
	elapsed := time.Since(start)

	wg.Wait()

	if len(r.errors) > 0 {
		t.Errorf("Wait should not have errored, got: %v", r.errors)
	}
	// Sanity: we shouldn't have waited anywhere near the full timeout.
	if elapsed > 500*time.Millisecond {
		t.Errorf("Wait took %s, expected < 500ms since the goroutine dispatched after 20ms", elapsed)
	}
}

func TestWait_TimesOutWithClearMessage(t *testing.T) {
	r := &recordingT{}
	target := func(name string) string { return "" }
	e := newExpectation[func(string) string](r, target)
	rule := e.OnAny().Returns("hi")

	// Nobody dispatches. Wait should time out.
	rule.Wait(1, 50*time.Millisecond)

	if len(r.errors) == 0 {
		t.Fatal("expected Wait to report an error on timeout")
	}
	msg := r.errors[0]
	if !strings.Contains(msg, "did not reach 1") {
		t.Errorf("expected 'did not reach 1' in diagnostic, got: %s", msg)
	}
	if !strings.Contains(msg, "got 0") {
		t.Errorf("expected 'got 0' in diagnostic, got: %s", msg)
	}
}

func TestWait_ReturnsImmediatelyWhenAlreadyReached(t *testing.T) {
	r := &recordingT{}
	target := func(name string) string { return "" }
	e := newExpectation[func(string) string](r, target)
	rule := e.OnAny().Returns("hi")

	// Dispatch twice synchronously first.
	_ = e.dispatch([]reflect.Value{reflect.ValueOf("a")})
	_ = e.dispatch([]reflect.Value{reflect.ValueOf("b")})

	start := time.Now()
	rule.Wait(2, 2*time.Second)
	elapsed := time.Since(start)

	if len(r.errors) > 0 {
		t.Errorf("Wait should not have errored, got: %v", r.errors)
	}
	if elapsed > 50*time.Millisecond {
		t.Errorf("Wait took %s, expected near-instant return when count is already satisfied", elapsed)
	}
}

func TestWait_NegativeCountFails(t *testing.T) {
	r := &recordingT{}
	target := func(name string) string { return "" }
	e := newExpectation[func(string) string](r, target)
	rule := e.OnAny().Returns("hi")

	rule.Wait(-1, 10*time.Millisecond)
	if len(r.fatals) == 0 {
		t.Error("expected Wait(-1) to produce a fatal diagnostic")
	}
}

func TestDispatch_MatchPredicateTyped(t *testing.T) {
	r := &recordingT{}
	target := func(name string) bool { return false }
	e := newExpectation[func(string) bool](r, target)
	e.Match(func(name string) bool { return strings.HasPrefix(name, "admin_") }).Returns(true)
	e.OnAny().Returns(false)

	out := e.dispatch([]reflect.Value{reflect.ValueOf("admin_42")})
	if !out[0].Bool() {
		t.Error("expected admin_42 to match predicate rule (true)")
	}
	out = e.dispatch([]reflect.Value{reflect.ValueOf("plain")})
	if out[0].Bool() {
		t.Error("expected plain to fall through to OnAny (false)")
	}
}
