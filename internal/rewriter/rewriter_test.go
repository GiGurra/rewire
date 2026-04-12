package rewriter

import (
	"testing"
)

func TestRewriteSource_SimpleFunc(t *testing.T) {
	src := []byte(`package bar

func Hello(name string) string {
	return "hello " + name
}
`)
	out, err := RewriteSource(src, "Hello")
	if err != nil {
		t.Fatal(err)
	}

	result := string(out)
	t.Log("Rewritten source:\n" + result)

	// Should contain the mock var
	assertContains(t, result, "var Mock_Hello func(name string) string")
	// Should contain the wrapper calling the mock
	assertContains(t, result, "Mock_Hello")
	assertContains(t, result, "_real_Hello")
	// The original body should be in _real_Hello
	assertContains(t, result, `"hello " + name`)
}

func TestRewriteSource_NoReturn(t *testing.T) {
	src := []byte(`package bar

func Log(msg string) {
	println(msg)
}
`)
	out, err := RewriteSource(src, "Log")
	if err != nil {
		t.Fatal(err)
	}

	result := string(out)
	t.Log("Rewritten source:\n" + result)

	assertContains(t, result, "var Mock_Log func(msg string)")
	assertContains(t, result, "_real_Log")
}

func TestRewriteSource_MultipleReturns(t *testing.T) {
	src := []byte(`package bar

func Fetch(url string) ([]byte, error) {
	return nil, nil
}
`)
	out, err := RewriteSource(src, "Fetch")
	if err != nil {
		t.Fatal(err)
	}

	result := string(out)
	t.Log("Rewritten source:\n" + result)

	assertContains(t, result, "Mock_Fetch")
	assertContains(t, result, "_real_Fetch")
}

func TestRewriteSource_UnnamedParams(t *testing.T) {
	src := []byte(`package bar

func Add(int, int) int {
	return 0
}
`)
	out, err := RewriteSource(src, "Add")
	if err != nil {
		t.Fatal(err)
	}

	result := string(out)
	t.Log("Rewritten source:\n" + result)

	// Should have generated param names
	assertContains(t, result, "p0")
	assertContains(t, result, "p1")
}

func TestRewriteSource_Variadic(t *testing.T) {
	src := []byte(`package bar

func Printf(format string, args ...any) {
	println(format)
}
`)
	out, err := RewriteSource(src, "Printf")
	if err != nil {
		t.Fatal(err)
	}

	result := string(out)
	t.Log("Rewritten source:\n" + result)

	assertContains(t, result, "args...")
}

func TestRewriteSource_MethodRejected(t *testing.T) {
	src := []byte(`package bar

type S struct{}

func (s *S) Hello() string {
	return "hello"
}
`)
	_, err := RewriteSource(src, "Hello")
	if err == nil {
		t.Fatal("expected error for method, got nil")
	}
	assertContains(t, err.Error(), "method")
}

func TestRewriteSource_NotFound(t *testing.T) {
	src := []byte(`package bar

func Hello() {}
`)
	_, err := RewriteSource(src, "Goodbye")
	if err == nil {
		t.Fatal("expected error for missing function, got nil")
	}
	assertContains(t, err.Error(), "not found")
}

func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !contains(s, substr) {
		t.Errorf("expected output to contain %q, but it didn't.\nFull output:\n%s", substr, s)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
