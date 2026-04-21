package expect

import (
	"fmt"
	"reflect"
	"strings"
)

// matcher decides whether a rule applies to a given call's arguments.
type matcher interface {
	match(args []reflect.Value) bool
	describe() string
}

// literalMatcher matches calls whose arguments are reflect.DeepEqual
// to the stored literal values, or (per position) accepted by an
// ArgMatcher sentinel passed to .On(...). Entries are pre-built at
// registration time so the match path doesn't re-interpret the args.
type literalMatcher struct {
	entries []argEntry
	descr   string
}

// argEntry holds the per-position matching state. Exactly one of
// `matcher` or the literal fields is used: if matcher != nil the
// position uses per-arg matcher logic (Any / Eq / ArgThat), otherwise
// the position compares via reflect.DeepEqual against literal.
type argEntry struct {
	matcher ArgMatcher
	literal reflect.Value
}

func (m *literalMatcher) match(args []reflect.Value) bool {
	if len(args) != len(m.entries) {
		return false
	}
	for i := range args {
		e := m.entries[i]
		if e.matcher != nil {
			if !e.matcher.matchArg(args[i]) {
				return false
			}
			continue
		}
		var expected any
		if e.literal.IsValid() {
			expected = e.literal.Interface()
		}
		var actual any
		if args[i].IsValid() {
			actual = args[i].Interface()
		}
		if !reflect.DeepEqual(actual, expected) {
			return false
		}
	}
	return true
}

func (m *literalMatcher) describe() string { return m.descr }

// predicateMatcher calls a user-supplied function with the call's
// arguments and uses its bool return as the match decision. The
// predicate's type is validated against the target's argument types
// at registration time.
type predicateMatcher struct {
	fn    reflect.Value
	descr string
}

func (m *predicateMatcher) match(args []reflect.Value) bool {
	return m.fn.Call(args)[0].Bool()
}

func (m *predicateMatcher) describe() string { return m.descr }

// anyMatcher always matches. Used by OnAny.
type anyMatcher struct{}

func (m *anyMatcher) match(args []reflect.Value) bool { return true }
func (m *anyMatcher) describe() string                { return ".OnAny()" }

// response produces the return values for a matching call. A response
// may be a fixed list of values (valuesResponse), a callback function
// (funcResponse), or nil (meaning the rule was declared without a
// response — typically paired with .Never() to assert non-call).
type response interface {
	produce(args []reflect.Value, fnType reflect.Type) []reflect.Value
}

// valuesResponse returns a pre-computed fixed list of values. Values
// are converted to the target's return types at registration time so
// the dispatcher path is allocation-free.
type valuesResponse struct {
	values []reflect.Value
}

func (r *valuesResponse) produce(args []reflect.Value, fnType reflect.Type) []reflect.Value {
	return r.values
}

// funcResponse invokes a callback with the call's arguments. The
// callback's type is the same as the target's signature, so the
// reflect call always matches the dispatcher's expected shape.
type funcResponse struct {
	fn reflect.Value
}

func (r *funcResponse) produce(args []reflect.Value, fnType reflect.Type) []reflect.Value {
	return r.fn.Call(args)
}

// --- validation helpers ---

// validateLiteralArgs checks that the supplied args match the target's
// argument list in count and types, and that nil is only used for
// nilable parameter types. ArgMatcher sentinels (Any, Eq, ArgThat) are
// accepted at any position — their paramType() is checked against the
// parameter type when it declares one.
func validateLiteralArgs(fnType reflect.Type, args []any) error {
	want := fnType.NumIn()
	if fnType.IsVariadic() {
		// For variadic, allow at least (want-1) args — the variadic
		// slot can be absent entirely. If supplied as a single slice of
		// the variadic element type, accept that.
		if len(args) < want-1 {
			return fmt.Errorf("On() got %d args, expected at least %d", len(args), want-1)
		}
	} else if len(args) != want {
		return fmt.Errorf("On() got %d args, expected %d", len(args), want)
	}
	for i, a := range args {
		paramType := paramTypeAt(fnType, i)
		if m, ok := a.(ArgMatcher); ok {
			if at, ok := m.(argThatArg); ok && !at.pred.IsValid() {
				return fmt.Errorf("On() arg %d: ArgThat predicate is nil", i)
			}
			mt := m.paramType()
			if mt == nil {
				continue
			}
			if !mt.AssignableTo(paramType) {
				return fmt.Errorf("On() arg %d %s: type %s is not assignable to parameter %d type %s",
					i, m.describeArg(), mt, i, paramType)
			}
			continue
		}
		if a == nil {
			if !isNilable(paramType) {
				return fmt.Errorf("On() arg %d is nil, but parameter %d type %s is not nilable",
					i, i, paramType)
			}
			continue
		}
		argType := reflect.TypeOf(a)
		if !argType.AssignableTo(paramType) {
			return fmt.Errorf("On() arg %d type %s is not assignable to parameter %d type %s",
				i, argType, i, paramType)
		}
	}
	return nil
}

// paramTypeAt returns the parameter type at position i, handling the
// variadic tail: for a variadic func(a, b, ...c), positions 0 and 1
// give their declared types and position 2+ give the element type of
// the variadic slice.
func paramTypeAt(fnType reflect.Type, i int) reflect.Type {
	n := fnType.NumIn()
	if !fnType.IsVariadic() || i < n-1 {
		return fnType.In(i)
	}
	// Variadic position — return the element type of the slice.
	return fnType.In(n - 1).Elem()
}

func isNilable(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface,
		reflect.Map, reflect.Pointer, reflect.Slice:
		return true
	}
	return false
}

// validatePredicate checks that predicate is a function with the same
// argument types as fnType and a single bool return value. Returns the
// predicate's reflect.Type on success.
func validatePredicate(fnType reflect.Type, predicate any) (reflect.Type, error) {
	if predicate == nil {
		return nil, fmt.Errorf("Match() predicate must not be nil")
	}
	predType := reflect.TypeOf(predicate)
	if predType.Kind() != reflect.Func {
		return nil, fmt.Errorf("Match() predicate must be a function, got %s", predType)
	}
	if predType.NumIn() != fnType.NumIn() {
		return nil, fmt.Errorf("Match() predicate takes %d args, target takes %d",
			predType.NumIn(), fnType.NumIn())
	}
	for i := 0; i < predType.NumIn(); i++ {
		if predType.In(i) != fnType.In(i) {
			return nil, fmt.Errorf("Match() predicate arg %d type %s does not match target arg type %s",
				i, predType.In(i), fnType.In(i))
		}
	}
	if predType.IsVariadic() != fnType.IsVariadic() {
		return nil, fmt.Errorf("Match() predicate variadic-ness (%v) does not match target (%v)",
			predType.IsVariadic(), fnType.IsVariadic())
	}
	if predType.NumOut() != 1 || predType.Out(0).Kind() != reflect.Bool {
		return nil, fmt.Errorf("Match() predicate must return a single bool, got %s", predType)
	}
	return predType, nil
}

// convertReturnValues validates that values matches the target's
// return signature in count and types, and pre-converts them to
// reflect.Values with the correct concrete types (so the dispatcher
// can return them directly without allocations).
func convertReturnValues(fnType reflect.Type, values []any) ([]reflect.Value, error) {
	if fnType.NumOut() != len(values) {
		return nil, fmt.Errorf("Returns() got %d values, target returns %d",
			len(values), fnType.NumOut())
	}
	out := make([]reflect.Value, len(values))
	for i, v := range values {
		retType := fnType.Out(i)
		if v == nil {
			if !isNilable(retType) {
				return nil, fmt.Errorf("Returns() value %d is nil, but return %d type %s is not nilable",
					i, i, retType)
			}
			out[i] = reflect.Zero(retType)
			continue
		}
		vType := reflect.TypeOf(v)
		if !vType.AssignableTo(retType) {
			return nil, fmt.Errorf("Returns() value %d type %s is not assignable to return %d type %s",
				i, vType, i, retType)
		}
		rv := reflect.ValueOf(v)
		if vType != retType {
			rv = rv.Convert(retType)
		}
		out[i] = rv
	}
	return out, nil
}

// formatArgsInterface renders an []any as a human-readable comma list
// for error messages and rule descriptions. ArgMatcher sentinels are
// rendered via their describeArg() so descriptions read as e.g.
// `.On(Any(), "foo", ArgThat(func(int) bool))` rather than internal
// struct field dumps.
func formatArgsInterface(args []any) string {
	parts := make([]string, len(args))
	for i, a := range args {
		if m, ok := a.(ArgMatcher); ok {
			parts[i] = m.describeArg()
			continue
		}
		parts[i] = fmt.Sprintf("%#v", a)
	}
	return strings.Join(parts, ", ")
}
