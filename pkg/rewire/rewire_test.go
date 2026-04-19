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

type validationTestStruct struct{ n int }

func TestValidateFuncArgument_RejectsIntLiteral(t *testing.T) {
	msg := validateFuncArgument(42)
	if msg == "" {
		t.Fatal("expected error for int literal, got empty")
	}
	for _, want := range []string{"expected a function", "int"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error missing %q; full message:\n%s", want, msg)
		}
	}
}

func TestValidateFuncArgument_RejectsString(t *testing.T) {
	msg := validateFuncArgument("hello")
	if msg == "" {
		t.Fatal("expected error for string, got empty")
	}
	if !strings.Contains(msg, "string") {
		t.Errorf("error should mention 'string'; got:\n%s", msg)
	}
}

func TestValidateFuncArgument_RejectsStruct(t *testing.T) {
	msg := validateFuncArgument(validationTestStruct{n: 7})
	if msg == "" {
		t.Fatal("expected error for struct, got empty")
	}
	if !strings.Contains(msg, "validationTestStruct") {
		t.Errorf("error should mention the struct type name; got:\n%s", msg)
	}
}

func TestValidateFuncArgument_RejectsNilFunctionVariable(t *testing.T) {
	var f func(int) int
	msg := validateFuncArgument(f)
	if msg == "" {
		t.Fatal("expected error for nil function variable, got empty")
	}
	if !strings.Contains(msg, "nil") {
		t.Errorf("error should mention 'nil'; got:\n%s", msg)
	}
}

func TestValidateFuncArgument_AcceptsPlainFunction(t *testing.T) {
	if msg := validateFuncArgument(plainTestFunc); msg != "" {
		t.Errorf("expected empty error for plain function, got:\n%s", msg)
	}
}

func TestValidateFuncArgument_AcceptsMethodExpression(t *testing.T) {
	if msg := validateFuncArgument((*testGreeter).Greet); msg != "" {
		t.Errorf("expected empty error for method expression, got:\n%s", msg)
	}
}

// tryClaimOwnership is the pure side of the parallel-conflict detector —
// unit tests live here so they can exercise conflicts without a subtest
// propagating the failure up to the parent.

func TestTryClaimOwnership_FreshKeyIsClaimed(t *testing.T) {
	key := "TestTryClaimOwnership_FreshKeyIsClaimed|target-A"
	t.Cleanup(func() { ownerRegistry.Delete(key) })

	prior, claimed := tryClaimOwnership(key, t)
	if !claimed {
		t.Fatalf("fresh key should be claimed, got claimed=false priorOwner=%v", prior)
	}
	if prior != nil {
		t.Errorf("no prior owner expected, got %v", prior)
	}
}

func TestTryClaimOwnership_SameOwnerReturnsNotClaimedNoConflict(t *testing.T) {
	key := "TestTryClaimOwnership_SameOwnerReturnsNotClaimedNoConflict|target-A"
	t.Cleanup(func() { ownerRegistry.Delete(key) })

	if _, claimed := tryClaimOwnership(key, t); !claimed {
		t.Fatal("first claim should succeed")
	}

	// Second attempt by the same test is not a fresh claim (already owned
	// by us) but also not a conflict — priorOwner is nil.
	prior, claimed := tryClaimOwnership(key, t)
	if claimed {
		t.Error("second claim by same owner should not report claimed=true (already ours)")
	}
	if prior != nil {
		t.Errorf("priorOwner should be nil for same-owner case, got %v", prior)
	}
}

func TestTryClaimOwnership_DifferentOwnerReturnsPrior(t *testing.T) {
	key := "TestTryClaimOwnership_DifferentOwnerReturnsPrior|target-A"
	t.Cleanup(func() { ownerRegistry.Delete(key) })

	// Seed with a sentinel *testing.T (the outer t) as the "prior" owner.
	ownerRegistry.Store(key, t)

	other := &testing.T{}
	prior, claimed := tryClaimOwnership(key, other)
	if claimed {
		t.Error("should not claim when another owner holds the key")
	}
	if prior != t {
		t.Errorf("priorOwner should be the outer t, got %v", prior)
	}
}

func TestOwnershipConflictMsg_MentionsKeyContextAndFixes(t *testing.T) {
	msg := ownershipConflictMsg("pkg.Foo", "TestOther")
	for _, want := range []string{
		"pkg.Foo",
		"TestOther",
		"not parallel-safe",
		"t.Parallel()",
		"InstanceFunc",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("conflict message missing %q; full:\n%s", want, msg)
		}
	}
}
