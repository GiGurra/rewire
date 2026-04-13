package rewire

import (
	"strings"
	"testing"
)

type testGreeter struct{ prefix string }

func (g *testGreeter) Greet(name string) string { return g.prefix + ", " + name }

func plainTestFunc(x int) int { return x + 1 }

func TestMethodValueError_DetectsBoundMethodValue(t *testing.T) {
	g := &testGreeter{prefix: "hi"}
	name := funcName(g.Greet)

	if !strings.HasSuffix(name, "-fm") {
		t.Fatalf("precondition failed: expected FuncForPC name to end in -fm, got %q", name)
	}

	msg := methodValueError(name)
	if msg == "" {
		t.Fatalf("expected a method-value error message, got empty string")
	}

	mustContain := []string{
		"method value",
		"method expression",
		"(*pkg.Type).Method",
		"instance.Method",
		name,
	}
	for _, want := range mustContain {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q; full message:\n%s", want, msg)
		}
	}
}

func TestMethodValueError_EmptyForPlainFunction(t *testing.T) {
	name := funcName(plainTestFunc)
	if strings.HasSuffix(name, "-fm") {
		t.Fatalf("precondition failed: plain function should not produce -fm, got %q", name)
	}
	if msg := methodValueError(name); msg != "" {
		t.Errorf("expected empty error for plain function, got:\n%s", msg)
	}
}

func TestMethodValueError_EmptyForMethodExpression(t *testing.T) {
	name := funcName((*testGreeter).Greet)
	if strings.HasSuffix(name, "-fm") {
		t.Fatalf("precondition failed: method expression should not produce -fm, got %q", name)
	}
	if msg := methodValueError(name); msg != "" {
		t.Errorf("expected empty error for method expression, got:\n%s", msg)
	}
}
