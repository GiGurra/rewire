package expect

import (
	"fmt"
	"reflect"
	"strings"
)

// dispatch is the reflect.MakeFunc body that For installs as the
// target's replacement. It walks the rule list in first-fit order,
// increments the matched rule's count, and produces the rule's
// response. If nothing matches, it either delegates to the real
// implementation (if AllowUnmatched was called) or fails the test.
func (e *Expectation[F]) dispatch(args []reflect.Value) []reflect.Value {
	e.mu.Lock()
	var matched *Rule[F]
	for _, r := range e.rules {
		if r.matcher.match(args) {
			matched = r
			r.count++
			break
		}
	}
	allowUnmatched := e.allowUnmatched
	e.mu.Unlock()

	if matched != nil {
		if matched.response == nil {
			// Rule has no configured response (typical for .Never()
			// rules). Returning zero values is safe in principle, but
			// hitting a Never rule is a test failure.
			if matched.bound.kind == boundNever {
				e.t.Errorf("rewire/expect: %s rule %s (%s) matched but was declared .Never()",
					e.name, matched.matcher.describe(), matched.site)
			}
			return zeroValues(e.fnType)
		}
		return matched.response.produce(args, e.fnType)
	}

	// Unmatched path.
	if allowUnmatched {
		return reflect.ValueOf(e.realFn).Call(args)
	}
	e.t.Errorf("rewire/expect: unexpected call to %s(%s) — no rule matched",
		e.name, formatCallArgs(args))
	return zeroValues(e.fnType)
}

// zeroValues returns a zero-valued []reflect.Value slice for fnType's
// return signature. Used on unmatched or no-response paths so the
// dispatcher always returns something reflect.MakeFunc can forward.
func zeroValues(fnType reflect.Type) []reflect.Value {
	n := fnType.NumOut()
	out := make([]reflect.Value, n)
	for i := 0; i < n; i++ {
		out[i] = reflect.Zero(fnType.Out(i))
	}
	return out
}

// formatCallArgs renders a []reflect.Value as a human-readable comma
// list for error diagnostics.
func formatCallArgs(args []reflect.Value) string {
	parts := make([]string, len(args))
	for i, a := range args {
		if !a.IsValid() {
			parts[i] = "<invalid>"
			continue
		}
		parts[i] = fmt.Sprintf("%#v", a.Interface())
	}
	return strings.Join(parts, ", ")
}
