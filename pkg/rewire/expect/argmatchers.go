package expect

import (
	"fmt"
	"reflect"
)

// ArgMatcher is a per-argument matcher sentinel. Passing an ArgMatcher
// to .On(args...) declares per-position matching logic for that argument
// instead of the default reflect.DeepEqual literal comparison.
//
// Mix freely with plain literal values — each position is evaluated
// independently. Example:
//
//	// Match any receiver + any context, specific VAT, specific partner.
//	e.On(Any(), Any(), "SE1234", "partner-42").Returns(...)
//
// The interface is deliberately sealed (matchArg / describeArg /
// paramType are unexported) so implementations must come from this
// package. Use Any, Eq, or ArgThat.
type ArgMatcher interface {
	matchArg(arg reflect.Value) bool
	describeArg() string
	// paramType returns the parameter type this matcher constrains
	// itself to. A nil return means "no constraint" — i.e. the matcher
	// accepts any parameter type at its position (used by Any).
	paramType() reflect.Type
}

// Any returns an ArgMatcher that matches any value at its argument
// position. Equivalent in spirit to gomock.Any() or mockito's
// anyString() / anyObject() family.
func Any() ArgMatcher { return anyArg{} }

// Eq returns an ArgMatcher that matches when the call's argument is
// reflect.DeepEqual to v. Passing v directly to .On(...) has the same
// effect; Eq is useful for symmetry when other positions use matchers,
// or to disambiguate when v itself happens to be an ArgMatcher value.
func Eq[T any](v T) ArgMatcher {
	// Capture the declared T — this is what the registration-time
	// assignability check uses. For interface T (e.g. any), that may
	// be wider than the runtime concrete type of v, but that's what
	// the user asked for.
	var zero T
	t := reflect.TypeOf(&zero).Elem()
	return eqArg{v: v, declaredType: t}
}

// ArgThat returns an ArgMatcher that accepts the argument when the
// supplied predicate returns true. T must be assignable to the target's
// parameter type at the position where the matcher appears — checked
// at .On() registration time.
//
//	e.On(Any(), ArgThat(func(s string) bool { return len(s) > 0 })).Returns(...)
func ArgThat[T any](pred func(T) bool) ArgMatcher {
	if pred == nil {
		// Returning a matcher that always panics on use would hide the
		// bug; record the nil and let the registration validator catch
		// it (paramType nil + nil fn is unusual enough to reject there).
		return argThatArg{pred: reflect.Value{}, in: nil}
	}
	fnType := reflect.TypeOf(pred)
	return argThatArg{
		pred:  reflect.ValueOf(pred),
		in:    fnType.In(0),
		descr: fmt.Sprintf("ArgThat(func(%s) bool)", fnType.In(0)),
	}
}

// --- implementations -------------------------------------------------

type anyArg struct{}

func (anyArg) matchArg(reflect.Value) bool { return true }
func (anyArg) describeArg() string         { return "Any()" }
func (anyArg) paramType() reflect.Type     { return nil }

type eqArg struct {
	v            any
	declaredType reflect.Type
}

func (m eqArg) matchArg(arg reflect.Value) bool {
	var actual any
	if arg.IsValid() {
		actual = arg.Interface()
	}
	return reflect.DeepEqual(actual, m.v)
}
func (m eqArg) describeArg() string     { return fmt.Sprintf("Eq(%#v)", m.v) }
func (m eqArg) paramType() reflect.Type { return m.declaredType }

type argThatArg struct {
	pred  reflect.Value
	in    reflect.Type
	descr string
}

func (m argThatArg) matchArg(arg reflect.Value) bool {
	if !arg.IsValid() {
		// Zero reflect.Value — nothing to hand to the predicate.
		return false
	}
	in := arg
	// When the target parameter is an interface type (e.g. `any` or
	// `io.Reader`) the reflect.Value received here carries the
	// interface wrapper. Unwrap so the predicate sees the dynamic
	// value, not the boxed interface. For concrete-typed parameters
	// this branch is a no-op.
	if in.Kind() == reflect.Interface {
		in = in.Elem()
		if !in.IsValid() {
			// nil interface value — no concrete value to test.
			return false
		}
	}
	// Registration only guarantees `m.in.AssignableTo(paramType)`
	// (matcher valid at this position). At runtime, the call's
	// dynamic type may be a sibling that's NOT assignable to m.in
	// (e.g. target is `func(any)`, predicate is `func(string) bool`,
	// and the actual call passed an int). In that case the matcher
	// legitimately doesn't apply — report no-match rather than
	// coercing via Convert (which either panics or produces
	// semantically wrong data for numeric/int-to-string cases).
	if !in.Type().AssignableTo(m.in) {
		return false
	}
	return m.pred.Call([]reflect.Value{in})[0].Bool()
}
func (m argThatArg) describeArg() string     { return m.descr }
func (m argThatArg) paramType() reflect.Type { return m.in }
