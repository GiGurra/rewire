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
	assertContains(t, err.Error(), "not found")
}

func TestRewriteSource_PointerReceiverMethod(t *testing.T) {
	src := []byte(`package bar

type Server struct{}

func (s *Server) Handle(req string) string {
	return "handled " + req
}
`)
	out, err := RewriteSource(src, "(*Server).Handle")
	if err != nil {
		t.Fatal(err)
	}

	result := string(out)
	t.Log("Rewritten source:\n" + result)

	// Mock var includes receiver as first param
	assertContains(t, result, "var Mock_Server_Handle func")
	assertContains(t, result, "*Server")
	// Wrapper is still a method on *Server
	assertContains(t, result, "func (s *Server) Handle(")
	// Real impl preserved as method
	assertContains(t, result, "_real_Server_Handle")
	// Original body preserved
	assertContains(t, result, `"handled " + req`)
}

func TestRewriteSource_ValueReceiverMethod(t *testing.T) {
	src := []byte(`package bar

type Point struct{ X, Y int }

func (p Point) String() string {
	return "point"
}
`)
	out, err := RewriteSource(src, "Point.String")
	if err != nil {
		t.Fatal(err)
	}

	result := string(out)
	t.Log("Rewritten source:\n" + result)

	assertContains(t, result, "var Mock_Point_String func")
	assertContains(t, result, "func (p Point) String(")
	assertContains(t, result, "_real_Point_String")
}

func TestRewriteSource_MethodMultiParamsAndReturns(t *testing.T) {
	src := []byte(`package bar

type DB struct{}

func (db *DB) Query(ctx string, query string, args ...any) ([]string, error) {
	return nil, nil
}
`)
	out, err := RewriteSource(src, "(*DB).Query")
	if err != nil {
		t.Fatal(err)
	}

	result := string(out)
	t.Log("Rewritten source:\n" + result)

	assertContains(t, result, "var Mock_DB_Query func")
	assertContains(t, result, "*DB")
	assertContains(t, result, "func (db *DB) Query(")
	assertContains(t, result, "_real_DB_Query")
	// Verify receiver forwarded to mock
	assertContains(t, result, "Mock_DB_Query; _rewire_mock != nil")
	assertContains(t, result, "_rewire_mock(db, ctx, query, args...)")
	// Verify real call uses receiver
	assertContains(t, result, "db._real_DB_Query(ctx, query, args...)")
}

// --- Functions: edge cases ---

func TestRewriteSource_NoParamsNoReturn(t *testing.T) {
	src := []byte(`package bar

func Ping() {
	println("pong")
}
`)
	out, err := RewriteSource(src, "Ping")
	if err != nil {
		t.Fatal(err)
	}

	result := string(out)
	t.Log("Rewritten source:\n" + result)

	assertContains(t, result, "var Mock_Ping func()")
	assertContains(t, result, "_real_Ping()")
	assertContains(t, result, `println("pong")`)
}

func TestRewriteSource_MultipleParamsSameType(t *testing.T) {
	src := []byte(`package bar

func Add(a, b int) int {
	return a + b
}
`)
	out, err := RewriteSource(src, "Add")
	if err != nil {
		t.Fatal(err)
	}

	result := string(out)
	t.Log("Rewritten source:\n" + result)

	assertContains(t, result, "var Mock_Add func(a, b int) int")
	assertContains(t, result, "_rewire_mock(a, b)")
	assertContains(t, result, "_real_Add(a, b)")
}

func TestRewriteSource_NamedReturns(t *testing.T) {
	src := []byte(`package bar

func Divide(a, b float64) (result float64, err error) {
	return a / b, nil
}
`)
	out, err := RewriteSource(src, "Divide")
	if err != nil {
		t.Fatal(err)
	}

	result := string(out)
	t.Log("Rewritten source:\n" + result)

	assertContains(t, result, "Mock_Divide")
	assertContains(t, result, "_real_Divide")
	assertContains(t, result, "a / b")
}

func TestRewriteSource_ComplexParamTypes(t *testing.T) {
	src := []byte(`package bar

func Process(data []byte, opts map[string]any, callback func(int) error) error {
	return nil
}
`)
	out, err := RewriteSource(src, "Process")
	if err != nil {
		t.Fatal(err)
	}

	result := string(out)
	t.Log("Rewritten source:\n" + result)

	assertContains(t, result, "Mock_Process")
	assertContains(t, result, "[]byte")
	assertContains(t, result, "map[string]any")
	assertContains(t, result, "func(int) error")
	assertContains(t, result, "_rewire_mock(data, opts, callback)")
}

func TestRewriteSource_ChannelParams(t *testing.T) {
	src := []byte(`package bar

func Send(ch chan<- string, msg string) {
	ch <- msg
}
`)
	out, err := RewriteSource(src, "Send")
	if err != nil {
		t.Fatal(err)
	}

	result := string(out)
	t.Log("Rewritten source:\n" + result)

	assertContains(t, result, "chan<- string")
	assertContains(t, result, "_rewire_mock(ch, msg)")
}

func TestRewriteSource_PointerParams(t *testing.T) {
	src := []byte(`package bar

type Config struct{ Debug bool }

func Apply(cfg *Config) error {
	return nil
}
`)
	out, err := RewriteSource(src, "Apply")
	if err != nil {
		t.Fatal(err)
	}

	result := string(out)
	t.Log("Rewritten source:\n" + result)

	assertContains(t, result, "var Mock_Apply func(cfg *Config) error")
	assertContains(t, result, "_rewire_mock(cfg)")
}

func TestRewriteSource_InterfaceParam(t *testing.T) {
	src := []byte(`package bar

import "io"

func ReadAll(r io.Reader) ([]byte, error) {
	return nil, nil
}
`)
	out, err := RewriteSource(src, "ReadAll")
	if err != nil {
		t.Fatal(err)
	}

	result := string(out)
	t.Log("Rewritten source:\n" + result)

	assertContains(t, result, "io.Reader")
	assertContains(t, result, "_rewire_mock(r)")
}

func TestRewriteSource_OnlyTargetFunctionRewritten(t *testing.T) {
	src := []byte(`package bar

func Keep(x int) int {
	return x
}

func Rewrite(x int) int {
	return x * 2
}

func AlsoKeep(x int) int {
	return x + 1
}
`)
	out, err := RewriteSource(src, "Rewrite")
	if err != nil {
		t.Fatal(err)
	}

	result := string(out)
	t.Log("Rewritten source:\n" + result)

	// Rewrite should be mocked
	assertContains(t, result, "Mock_Rewrite")
	assertContains(t, result, "_real_Rewrite")
	// Keep and AlsoKeep should be untouched
	assertContains(t, result, "func Keep(x int) int")
	assertContains(t, result, "func AlsoKeep(x int) int")
	assertNotContains(t, result, "Mock_Keep")
	assertNotContains(t, result, "Mock_AlsoKeep")
}

func TestRewriteSource_UnexportedFunctionByName(t *testing.T) {
	src := []byte(`package bar

func helper(x int) int {
	return x
}
`)
	out, err := RewriteSource(src, "helper")
	if err != nil {
		t.Fatal(err)
	}

	result := string(out)
	t.Log("Rewritten source:\n" + result)

	assertContains(t, result, "var Mock_helper func(x int) int")
	assertContains(t, result, "_real_helper")
}

func TestRewriteSource_ManyParams(t *testing.T) {
	src := []byte(`package bar

func BigFunc(a int, b string, c float64, d bool, e []int, f map[string]string) (int, error) {
	return 0, nil
}
`)
	out, err := RewriteSource(src, "BigFunc")
	if err != nil {
		t.Fatal(err)
	}

	result := string(out)
	t.Log("Rewritten source:\n" + result)

	assertContains(t, result, "_rewire_mock(a, b, c, d, e, f)")
	assertContains(t, result, "_real_BigFunc(a, b, c, d, e, f)")
}

// --- Methods: edge cases ---

func TestRewriteSource_MethodNoReturn(t *testing.T) {
	src := []byte(`package bar

type Logger struct{}

func (l *Logger) Log(msg string) {
	println(msg)
}
`)
	out, err := RewriteSource(src, "(*Logger).Log")
	if err != nil {
		t.Fatal(err)
	}

	result := string(out)
	t.Log("Rewritten source:\n" + result)

	assertContains(t, result, "var Mock_Logger_Log func(*Logger, string)")
	assertContains(t, result, "_rewire_mock(l, msg)")
	assertContains(t, result, "l._real_Logger_Log(msg)")
	// No-return wrapper should have explicit return
	assertContains(t, result, "return\n")
}

func TestRewriteSource_MethodUnnamedReceiver(t *testing.T) {
	src := []byte(`package bar

type Counter struct{ N int }

func (*Counter) Reset() {
	// receiver unnamed
}
`)
	out, err := RewriteSource(src, "(*Counter).Reset")
	if err != nil {
		t.Fatal(err)
	}

	result := string(out)
	t.Log("Rewritten source:\n" + result)

	assertContains(t, result, "var Mock_Counter_Reset func(*Counter)")
	// Should generate a receiver name for forwarding
	assertContains(t, result, "_rewire_recv")
	assertContains(t, result, "_rewire_mock(_rewire_recv)")
	assertContains(t, result, "_rewire_recv._real_Counter_Reset()")
}

func TestRewriteSource_TwoTypesWithSameMethodName(t *testing.T) {
	src := []byte(`package bar

type Cat struct{}
type Dog struct{}

func (c *Cat) Speak() string {
	return "meow"
}

func (d *Dog) Speak() string {
	return "woof"
}
`)
	// Mock only Cat.Speak
	out, err := RewriteSource(src, "(*Cat).Speak")
	if err != nil {
		t.Fatal(err)
	}

	result := string(out)
	t.Log("Rewritten source:\n" + result)

	assertContains(t, result, "Mock_Cat_Speak")
	assertContains(t, result, "_real_Cat_Speak")
	// Dog.Speak should be untouched
	assertNotContains(t, result, "Mock_Dog_Speak")
	assertContains(t, result, "func (d *Dog) Speak() string")
}

func TestRewriteSource_MethodWithVariadic(t *testing.T) {
	src := []byte(`package bar

type Formatter struct{}

func (f *Formatter) Format(pattern string, args ...any) string {
	return pattern
}
`)
	out, err := RewriteSource(src, "(*Formatter).Format")
	if err != nil {
		t.Fatal(err)
	}

	result := string(out)
	t.Log("Rewritten source:\n" + result)

	assertContains(t, result, "_rewire_mock(f, pattern, args...)")
	assertContains(t, result, "f._real_Formatter_Format(pattern, args...)")
}

func TestRewriteSource_ValueReceiverWithFields(t *testing.T) {
	src := []byte(`package bar

type Rect struct{ W, H float64 }

func (r Rect) Area() float64 {
	return r.W * r.H
}
`)
	out, err := RewriteSource(src, "Rect.Area")
	if err != nil {
		t.Fatal(err)
	}

	result := string(out)
	t.Log("Rewritten source:\n" + result)

	assertContains(t, result, "var Mock_Rect_Area func(Rect) float64")
	assertContains(t, result, "_rewire_mock(r)")
	assertContains(t, result, "r._real_Rect_Area()")
	assertContains(t, result, "r.W * r.H")
}

// --- Error cases ---

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

func TestRewriteSource_BodylessFunction(t *testing.T) {
	// Simulate an assembly-backed function (no body)
	src := []byte(`package bar

func AsmFunc()
`)
	_, err := RewriteSource(src, "AsmFunc")
	if err == nil {
		t.Fatal("expected error for bodyless function, got nil")
	}
	assertContains(t, err.Error(), "no body")
}

func TestRewriteSource_MethodWrongReceiverType(t *testing.T) {
	src := []byte(`package bar

type A struct{}
type B struct{}

func (a *A) Do() {}
`)
	_, err := RewriteSource(src, "(*B).Do")
	if err == nil {
		t.Fatal("expected error for wrong receiver type, got nil")
	}
	assertContains(t, err.Error(), "not found")
}

func TestRewriteSource_MethodPointerVsValue(t *testing.T) {
	src := []byte(`package bar

type T struct{}

func (t *T) PtrMethod() string { return "ptr" }
func (t T) ValMethod() string { return "val" }
`)
	// Ask for value receiver but function has pointer receiver — should fail
	_, err := RewriteSource(src, "T.PtrMethod")
	if err == nil {
		t.Fatal("expected error for pointer/value mismatch, got nil")
	}
	assertContains(t, err.Error(), "not found")

	// Correct pointer syntax should work
	out, err := RewriteSource(src, "(*T).PtrMethod")
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, string(out), "Mock_T_PtrMethod")
}

func TestRewriteSource_PlainNameDoesNotMatchMethod(t *testing.T) {
	// Using plain "Hello" should NOT find a method named Hello
	src := []byte(`package bar

type S struct{}

func (s *S) Hello() string { return "hi" }
`)
	_, err := RewriteSource(src, "Hello")
	if err == nil {
		t.Fatal("expected error — plain name should not match method")
	}
	assertContains(t, err.Error(), "not found")
}

// --- Helpers ---

func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !contains(s, substr) {
		t.Errorf("expected output to contain %q, but it didn't.\nFull output:\n%s", substr, s)
	}
}

func assertNotContains(t *testing.T, s, substr string) {
	t.Helper()
	if contains(s, substr) {
		t.Errorf("expected output NOT to contain %q, but it did.\nFull output:\n%s", substr, s)
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
