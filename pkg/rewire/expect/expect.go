// Package expect provides an opt-in expectation DSL layered on top of
// rewire.Func. It lets tests declare multiple rules with first-fit
// dispatch, call-count bounds, and automatic verification at test end —
// without forcing the closure-and-counter style that plain rewire.Func
// uses.
//
// Example:
//
//	e := expect.For(t, bar.Greet)
//	e.On("Alice").Returns("hi Alice")
//	e.On("Bob").Returns("hi Bob")
//	e.OnAny().Returns("hi other")
//
// From the moment For returns, bar.Greet is mocked. Each rule is
// appended to the expectation's state and the dispatcher walks them in
// first-fit order on every call. t.Cleanup automatically verifies
// call-count bounds (strict by default for .On and .Match, lenient for
// .OnAny) and fails the test if any rule's bound was violated.
//
// Users who prefer the plain closure style should use rewire.Func
// directly — this package is strictly additive.
package expect

import (
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GiGurra/rewire/pkg/rewire"
)

// testingT is the subset of *testing.T that the DSL uses. It's an
// interface rather than *testing.T so tests of the DSL itself can
// substitute a recording fake and verify that error paths fire without
// the fake's errors propagating to the outer test. Production callers
// always pass *testing.T via For.
type testingT interface {
	Errorf(format string, args ...any)
	Fatal(args ...any)
	Fatalf(format string, args ...any)
	Helper()
	Cleanup(func())
}

// Expectation holds the state for a mocked function: the list of rules,
// configuration flags, and the dispatcher's mutex. It is created by For
// and configured via its On / Match / OnAny methods.
type Expectation[F any] struct {
	t        testingT
	name     string       // canonical function name (FuncForPC, with [...] stripped)
	fnType   reflect.Type // reflect.TypeOf(original)
	realFn   F            // captured at construction for AllowUnmatched passthrough

	// elideReceiver is set by ForInstance. When true, .On(args...) and
	// .Match(predicate) treat the target signature as if the receiver
	// parameter didn't exist: callers pass args / predicate-params for
	// positions 1..N only. Internally the rule matcher still sees the
	// full arg list the dispatcher hands it, so .On prepends an implicit
	// Any() for the receiver slot and .Match wraps the predicate so the
	// receiver arg is dropped before the user's predicate runs. The
	// receiver is already pinned to a specific instance at install time
	// (via rewire.InstanceFunc), so requiring callers to restate it at
	// every rule would be noise.
	elideReceiver bool

	mu             sync.Mutex
	rules          []*Rule[F]
	allowUnmatched bool
}

// Rule is a single expectation entry: a matcher (argument predicate), a
// response (return values or a callback), and a call-count bound.
// Create via Expectation.On / Match / OnAny, configure via Returns /
// DoFunc / Times / AtLeast / Never / Maybe.
type Rule[F any] struct {
	parent   *Expectation[F]
	matcher  matcher
	response response
	bound    bound
	count    int
	site     string // caller file:line captured at creation, for diagnostics
}

// For installs an expectation-driven mock on target and returns the
// *Expectation[F] so the caller can declare rules. From the moment For
// returns, target is mocked: every call is routed through the
// expectation's dispatcher, which walks the rule list in first-fit
// order.
//
// For is the moment the mock is installed — there is no separate
// rewire.Func call. Installing both For and rewire.Func on the same
// target will clobber whichever ran second. The call registers a
// t.Cleanup that verifies call-count bounds at test end.
func For[F any](t *testing.T, target F) *Expectation[F] {
	t.Helper()

	e := newExpectation[F](t, target)
	if e == nil {
		return nil
	}

	// Capture the real implementation up-front so .AllowUnmatched() can
	// pass through to it without a per-call registry lookup. This works
	// because rewire.Real reads the realRegistry (populated at init via
	// generated code) and is independent of the current Mock_ state.
	e.realFn = rewire.Real(t, target)

	// Build a reflect.MakeFunc dispatcher with the same signature as F
	// and install it via rewire.Func. The conversion via Interface().(F)
	// works because reflect.MakeFunc produces a function of exactly
	// fnType, which is the same type as F by construction.
	dispatcher := reflect.MakeFunc(e.fnType, e.dispatch).Interface().(F)
	rewire.Func(t, target, dispatcher)

	return e
}

// ForInstance installs an expectation-driven mock scoped to a single
// receiver, using rewire.InstanceFunc under the hood instead of
// rewire.Func. Works for two related cases:
//
//  1. A pointer-receiver method on a concrete type:
//
//	s1 := &bar.Server{Name: "primary"}
//	e := expect.ForInstance(t, s1, (*bar.Server).Handle)
//	e.On("ping").Returns("pong from primary")
//	// Other *bar.Server instances still run the real Handle.
//
//  2. An interface method on a rewire.NewMock instance:
//
//	greeter := rewire.NewMock[bar.GreeterIface](t)
//	e := expect.ForInstance(t, greeter, bar.GreeterIface.Greet)
//	e.On("Alice").Returns("hi Alice")
//	e.OnAny().Returns("hi other")
//
// The returned *Expectation[F] supports the same rule-builder API as
// For (.On / .Match / .OnAny / .Returns / .DoFunc / .Times / .AtLeast
// / .Never / .Maybe / .Wait), and the same verification at test end.
//
// Differences from For:
//
//   - Installation goes through rewire.InstanceFunc, so only calls
//     whose receiver is `instance` are routed through the dispatcher.
//     Calls from other instances still see the global mock (if any)
//     or the real implementation.
//
//   - The receiver parameter is elided from the rule-builder API.
//     .On(args...) and .Match(predicate) operate on positions 1..N
//     of the method expression's signature; the receiver is implicitly
//     pinned to `instance` at install time. So the example above uses
//     .On("Alice") instead of .On(greeter, "Alice"). The dispatcher
//     still sees the full arg list internally.
//
//   - The real implementation is NOT captured. Interface method
//     expressions have no registered real, and for per-instance
//     concrete method mocks the "fallthrough to real" semantic is
//     already handled at a lower level by the wrapper's dispatch
//     order (per-instance → global → real). As a result,
//     .AllowUnmatched() is not supported on ForInstance — use an
//     explicit .OnAny().Returns(...) catch-all rule instead.
func ForInstance[I any, F any](t *testing.T, instance I, target F) *Expectation[F] {
	t.Helper()

	e := newExpectation[F](t, target)
	if e == nil {
		return nil
	}

	// Elide the receiver slot from the rule-builder API: .On / .Match
	// callers provide args for the method's non-receiver parameters only.
	// The receiver is already pinned to `instance` via InstanceFunc
	// below, so re-stating it at every rule would be noise.
	e.elideReceiver = true

	// Intentionally do NOT capture rewire.Real(t, target) here:
	// interface method expressions have no registered real, and for
	// per-instance concrete mocks unmatched calls should surface as
	// test failures (or be caught explicitly via .OnAny()).
	// Leaving realFn nil is fine because the dispatcher's unmatched
	// path checks validity before calling it.

	dispatcher := reflect.MakeFunc(e.fnType, e.dispatch).Interface().(F)
	rewire.InstanceFunc(t, instance, target, dispatcher)

	return e
}

// newExpectation builds an Expectation[F] and registers its verifier
// cleanup, but does NOT call rewire.Func or rewire.Real. Used by For
// for the normal path and by the DSL's own unit tests to exercise the
// state machine with a fake testingT recorder.
func newExpectation[F any](t testingT, target F) *Expectation[F] {
	t.Helper()

	fnType := reflect.TypeOf(target)
	if fnType == nil || fnType.Kind() != reflect.Func {
		t.Fatal("rewire/expect: For target must be a function")
		return nil
	}

	name := runtime.FuncForPC(reflect.ValueOf(target).Pointer()).Name()
	name = strings.ReplaceAll(name, "[...]", "")

	e := &Expectation[F]{
		t:      t,
		name:   name,
		fnType: fnType,
	}
	t.Cleanup(e.verify)
	return e
}

// AllowUnmatched configures the expectation so that calls not matching
// any rule fall through to the real implementation instead of failing
// the test. Returns the same expectation for chaining.
func (e *Expectation[F]) AllowUnmatched() *Expectation[F] {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.allowUnmatched = true
	return e
}

// On begins a new rule that matches calls whose arguments are deeply
// equal to the provided values. The number and types of args must
// match the target's signature, checked at registration time.
//
// Per-position matcher sentinels are accepted alongside literal values:
// pass Any() to skip a position, Eq(v) for explicit literal equality
// (identical in effect to passing v directly), or ArgThat(pred) to use
// a user predicate for a single argument.
//
//	e.On("alice").Returns(...)                         // literal
//	e.On(Any(), Any(), "vat-1").Returns(...)           // any receiver+ctx
//	e.On(Any(), ArgThat(func(s string) bool {...}))    // predicate on one arg
//
// Returns a *Rule so the caller can specify .Returns(...) / .DoFunc(...)
// and optional bounds like .Times(n).
//
// Defaults to strict: the rule must match at least one call, or
// verification fails at t.Cleanup. Override with .Maybe() for optional.
func (e *Expectation[F]) On(args ...any) *Rule[F] {
	e.t.Helper()
	userArgs := args
	effectiveArgs := args
	if e.elideReceiver {
		// Expectations from ForInstance take args for the non-receiver
		// positions only. Prepend an implicit Any() for the receiver
		// so the validator + matcher see the full fnType arity.
		effectiveArgs = make([]any, 0, len(args)+1)
		effectiveArgs = append(effectiveArgs, Any())
		effectiveArgs = append(effectiveArgs, args...)
	}
	if err := validateLiteralArgs(e.fnType, effectiveArgs); err != nil {
		e.t.Fatalf("rewire/expect: %s: %s", e.name, err)
		return nil
	}
	entries := make([]argEntry, len(effectiveArgs))
	for i, a := range effectiveArgs {
		if m, ok := a.(ArgMatcher); ok {
			entries[i] = argEntry{matcher: m}
			continue
		}
		if a == nil {
			entries[i] = argEntry{literal: reflect.Zero(e.fnType.In(i))}
			continue
		}
		entries[i] = argEntry{literal: reflect.ValueOf(a)}
	}
	// Describe using the user-visible args so the rendered rule doesn't
	// expose the auto-prepended Any() when elision is active.
	descr := ".On(" + formatArgsInterface(userArgs) + ")"
	r := &Rule[F]{
		parent:  e,
		matcher: &literalMatcher{entries: entries, descr: descr},
		bound:   bound{kind: boundAtLeast, n: 1}, // strict default
		site:    callerSite(2),
	}
	e.appendRule(r)
	return r
}

// Match begins a new rule that matches calls for which the provided
// predicate returns true. The predicate must be a function with the
// same argument types as the target and a single bool return — checked
// at registration time. Typed naturally via Go's normal type inference.
//
// Defaults to strict: the rule must match at least one call.
func (e *Expectation[F]) Match(predicate any) *Rule[F] {
	e.t.Helper()
	// The predicate is validated against the effective signature —
	// fnType minus the receiver when this expectation came from
	// ForInstance, fnType unchanged otherwise.
	expectedType := e.fnType
	if e.elideReceiver {
		expectedType = elidedReceiverType(e.fnType)
	}
	predType, err := validatePredicate(expectedType, predicate)
	if err != nil {
		e.t.Fatalf("rewire/expect: %s: %s", e.name, err)
		return nil
	}
	// The dispatcher invokes matchers with the full arg list (receiver
	// included). When the user supplied a predicate without a receiver
	// parameter, wrap it so the receiver is dropped before the user's
	// predicate runs. The wrapper has type matching fnType's In()s with
	// a bool return, which is exactly what predicateMatcher expects.
	matcherFn := reflect.ValueOf(predicate)
	if e.elideReceiver {
		matcherFn = wrapPredicateElideReceiver(e.fnType, reflect.ValueOf(predicate))
	}
	r := &Rule[F]{
		parent: e,
		matcher: &predicateMatcher{
			fn:    matcherFn,
			descr: ".Match(" + predType.String() + ")",
		},
		bound: bound{kind: boundAtLeast, n: 1}, // strict default
		site:  callerSite(2),
	}
	e.appendRule(r)
	return r
}

// elidedReceiverType returns fnType with its first input parameter
// removed, preserving returns and variadic-ness. Used by ForInstance
// expectations so .On / .Match validate against the user-visible
// (non-receiver) signature.
func elidedReceiverType(fnType reflect.Type) reflect.Type {
	n := fnType.NumIn()
	if n == 0 {
		return fnType
	}
	ins := make([]reflect.Type, n-1)
	for i := 1; i < n; i++ {
		ins[i-1] = fnType.In(i)
	}
	outs := make([]reflect.Type, fnType.NumOut())
	for i := 0; i < fnType.NumOut(); i++ {
		outs[i] = fnType.Out(i)
	}
	// Only propagate variadic-ness if the variadic slot wasn't the
	// receiver (which it can't be in practice; receivers are always
	// concrete types) AND there's at least one remaining param for
	// reflect.FuncOf to treat as variadic.
	variadic := fnType.IsVariadic() && n > 1
	return reflect.FuncOf(ins, outs, variadic)
}

// wrapPredicateElideReceiver builds a synthetic predicate whose type
// matches fnType's full In() list but returns bool. At call time it
// discards the first argument (the receiver) and invokes the user's
// predicate with the remaining args. The result is a reflect.Value
// that predicateMatcher can store and call uniformly, with the
// dispatcher's full-arg list.
func wrapPredicateElideReceiver(fnType reflect.Type, userPred reflect.Value) reflect.Value {
	ins := make([]reflect.Type, fnType.NumIn())
	for i := 0; i < fnType.NumIn(); i++ {
		ins[i] = fnType.In(i)
	}
	synthType := reflect.FuncOf(ins, []reflect.Type{reflect.TypeOf(true)}, fnType.IsVariadic())
	return reflect.MakeFunc(synthType, func(args []reflect.Value) []reflect.Value {
		return userPred.Call(args[1:])
	})
}

// OnAny begins a new catch-all rule that matches every call. Useful as
// a fallback after more specific rules, or as a simple stubbing
// shortcut when you just want any call to produce a canned return.
//
// Defaults to lenient: zero matches is fine (unlike .On / .Match which
// default to requiring at least one match). Override with .Times(n) for
// strict counts.
func (e *Expectation[F]) OnAny() *Rule[F] {
	e.t.Helper()
	r := &Rule[F]{
		parent:  e,
		matcher: &anyMatcher{},
		bound:   bound{kind: boundAny}, // lenient default
		site:    callerSite(2),
	}
	e.appendRule(r)
	return r
}

func (e *Expectation[F]) appendRule(r *Rule[F]) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rules = append(e.rules, r)
}

// Returns sets the rule's response to a fixed set of return values.
// The number and types of values must match the target's return
// signature, checked at registration time. Returns the rule for
// chaining (e.g. .Times(n)).
func (r *Rule[F]) Returns(values ...any) *Rule[F] {
	r.parent.t.Helper()
	converted, err := convertReturnValues(r.parent.fnType, values)
	if err != nil {
		r.parent.t.Fatalf("rewire/expect: %s %s: %s", r.parent.name, r.matcher.describe(), err)
		return r
	}
	r.response = &valuesResponse{values: converted}
	return r
}

// DoFunc sets the rule's response to a callback function invoked with
// the real arguments. The callback must have exactly the target's
// signature — typed by Go's type system because fn is of type F.
func (r *Rule[F]) DoFunc(fn F) *Rule[F] {
	r.parent.t.Helper()
	r.response = &funcResponse{fn: reflect.ValueOf(fn)}
	return r
}

// Times sets an exact call-count bound: the rule must match exactly n
// calls, or verification fails at t.Cleanup.
func (r *Rule[F]) Times(n int) *Rule[F] {
	r.parent.t.Helper()
	if n < 0 {
		r.parent.t.Fatalf("rewire/expect: Times(%d) — count must be non-negative", n)
		return r
	}
	r.bound = bound{kind: boundExact, n: n}
	return r
}

// AtLeast sets a minimum call-count bound: the rule must match at
// least n calls. This is the default for .On and .Match with n=1.
func (r *Rule[F]) AtLeast(n int) *Rule[F] {
	r.parent.t.Helper()
	if n < 0 {
		r.parent.t.Fatalf("rewire/expect: AtLeast(%d) — count must be non-negative", n)
		return r
	}
	r.bound = bound{kind: boundAtLeast, n: n}
	return r
}

// Never asserts the rule must NOT match any calls. Equivalent to
// Times(0) but produces a clearer diagnostic at verification time.
// Typically used without Returns / DoFunc: a Never rule with no
// response will fail the test at call time if it ever matches.
func (r *Rule[F]) Never() *Rule[F] {
	r.parent.t.Helper()
	r.bound = bound{kind: boundNever}
	return r
}

// Maybe opts the rule out of its strict default: zero matches is now
// acceptable. Useful when a rule should apply if reached but the test
// doesn't care whether it's reached at all.
func (r *Rule[F]) Maybe() *Rule[F] {
	r.parent.t.Helper()
	r.bound = bound{kind: boundAny}
	return r
}

// Wait blocks until the rule has matched at least n calls, or the
// timeout elapses. On timeout, the test is failed via t.Errorf with a
// diagnostic showing the rule, the expected count, and the actual
// count at deadline.
//
// Wait is useful for tests that kick off async work and need to
// synchronize before asserting — e.g. launching a goroutine that
// eventually calls the mocked function, then waiting for it to have
// happened before the test body continues.
//
// Implementation is a simple 10ms poll. The polling overhead is
// invisible in test timings, and keeping it polling-based avoids
// per-rule signaling state that would complicate the dispatcher.
//
// Wait on a .Never() rule is not meaningful — the rule's count is
// expected to stay 0, so Wait would always time out. Don't do that.
func (r *Rule[F]) Wait(n int, timeout time.Duration) *Rule[F] {
	r.parent.t.Helper()
	if n < 0 {
		r.parent.t.Fatalf("rewire/expect: Wait(%d, ...) — count must be non-negative", n)
		return r
	}
	deadline := time.Now().Add(timeout)
	const tick = 10 * time.Millisecond
	for {
		r.parent.mu.Lock()
		count := r.count
		r.parent.mu.Unlock()
		if count >= n {
			return r
		}
		if !time.Now().Before(deadline) {
			r.parent.t.Errorf(
				"rewire/expect: %s rule %s (declared at %s) did not reach %d match(es) within %s (got %d)",
				r.parent.name, r.matcher.describe(), r.site, n, timeout, count,
			)
			return r
		}
		time.Sleep(tick)
	}
}

// callerSite returns file:line of the caller skip frames up, for use
// in error messages. skip=1 is the direct caller of callerSite.
func callerSite(skip int) string {
	_, file, line, ok := runtime.Caller(skip + 1)
	if !ok {
		return "<unknown>"
	}
	// Strip the leading directory for brevity.
	if idx := strings.LastIndex(file, "/"); idx >= 0 {
		file = file[idx+1:]
	}
	return fmt.Sprintf("%s:%d", file, line)
}
