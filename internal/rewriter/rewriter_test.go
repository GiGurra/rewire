package rewriter

import (
	"go/parser"
	"go/token"
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

// When ImportPath is set, the wrapper gets a goroutine-local
// dispatch head ahead of the Mock_Foo nil check. The head reads
// the pprof labels pointer for the current goroutine and does a
// lookup in a per-function sync.Map (Mock_Foo_ByGoroutine) also
// emitted by the rewriter. The linkname stub for
// _rewire_getProfLabel lives in a sidecar file written by the
// toolexec layer, not by the rewriter itself.
func TestRewriteSourceOpts_WithImportPath_EmitsGoroutineLocalHead(t *testing.T) {
	src := []byte(`package bar

func Hello(name string) string {
	return "hello " + name
}
`)
	out, err := RewriteSourceOpts(src, "Hello", RewriteOptions{
		ImportPath: "github.com/example/bar",
	})
	if err != nil {
		t.Fatal(err)
	}

	result := string(out)
	t.Log("Rewritten source:\n" + result)

	assertParsesAsGo(t, result)
	// Per-function goroutine-keyed sync.Map is declared.
	assertContains(t, result, "var Mock_Hello_ByGoroutine sync.Map")
	// Wrapper reads the labels pointer and looks up in that map.
	assertContains(t, result, `_rewire_labels := _rewire_getProfLabel()`)
	assertContains(t, result, `Mock_Hello_ByGoroutine.Load(uintptr(_rewire_labels))`)
	assertContains(t, result, `_rewire_raw.(func(name string) string)`)
	// Rewriter adds sync import (not unsafe — unsafe lives in the sidecar).
	assertContains(t, result, `"sync"`)
	assertNotContains(t, result, `"unsafe"`)
	// Mock_Foo fallback remains for the legacy path.
	assertContains(t, result, `Mock_Hello`)
	assertContains(t, result, `_real_Hello`)
}

// Pointer-receiver method: Mock_Type_Method_ByGoroutine is emitted.
func TestRewriteSourceOpts_WithImportPath_PointerMethod(t *testing.T) {
	src := []byte(`package bar

type Server struct{}

func (s *Server) Handle(req string) string {
	return "handled " + req
}
`)
	out, err := RewriteSourceOpts(src, "(*Server).Handle", RewriteOptions{
		ImportPath: "github.com/example/bar",
	})
	if err != nil {
		t.Fatal(err)
	}
	result := string(out)
	assertParsesAsGo(t, result)
	assertContains(t, result, "var Mock_Server_Handle_ByGoroutine sync.Map")
	assertContains(t, result, `Mock_Server_Handle_ByGoroutine.Load(uintptr(_rewire_labels))`)
}

// Value-receiver method.
func TestRewriteSourceOpts_WithImportPath_ValueMethod(t *testing.T) {
	src := []byte(`package bar

type Greeter struct{}

func (g Greeter) Greet(name string) string {
	return "hi " + name
}
`)
	out, err := RewriteSourceOpts(src, "Greeter.Greet", RewriteOptions{
		ImportPath: "github.com/example/bar",
	})
	if err != nil {
		t.Fatal(err)
	}
	result := string(out)
	assertParsesAsGo(t, result)
	assertContains(t, result, "var Mock_Greeter_Greet_ByGoroutine sync.Map")
	assertContains(t, result, `Mock_Greeter_Greet_ByGoroutine.Load(uintptr(_rewire_labels))`)
}

// Without ImportPath, the wrapper keeps the old Mock_Foo-only shape.
func TestRewriteSourceOpts_NoImportPath_KeepsLegacyShape(t *testing.T) {
	src := []byte(`package bar

func Hello(name string) string {
	return "hello " + name
}
`)
	out, err := RewriteSourceOpts(src, "Hello", RewriteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result := string(out)
	assertParsesAsGo(t, result)
	assertNotContains(t, result, "_rewire_getProfLabel")
	assertNotContains(t, result, "_ByGoroutine")
	assertContains(t, result, "Mock_Hello")
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

// ---------------------------------------------------------------------------
// Generic function rewriting
// ---------------------------------------------------------------------------

func TestRewriteSource_GenericFunction_Basic(t *testing.T) {
	src := []byte(`package bar

func Map[T, U any](in []T, f func(T) U) []U {
	out := make([]U, len(in))
	for i, v := range in {
		out[i] = f(v)
	}
	return out
}
`)
	out, err := RewriteSource(src, "Map")
	if err != nil {
		t.Fatal(err)
	}
	result := string(out)
	t.Log("Rewritten source:\n" + result)

	assertParsesAsGo(t, result)
	assertContains(t, result, `"sync"`)
	assertContains(t, result, `"reflect"`)
	assertContains(t, result, "var Mock_Map sync.Map")
	assertContains(t, result, "func Real_Map[T, U any](in []T, f func(T) U) []U")
	assertContains(t, result, "func Map[T, U any](in []T, f func(T) U) []U")
	assertContains(t, result, "Mock_Map.Load(reflect.TypeOf(Map[T, U]).String())")
	assertContains(t, result, "_rewire_raw.(func([]T, func(T) U) []U)")
	assertContains(t, result, "func _real_Map[T, U any](in []T, f func(T) U) []U")
}

func TestRewriteSource_GenericFunction_SingleTypeParam(t *testing.T) {
	src := []byte(`package bar

func First[T any](xs []T) T {
	return xs[0]
}
`)
	out, err := RewriteSource(src, "First")
	if err != nil {
		t.Fatal(err)
	}
	result := string(out)
	t.Log("Rewritten source:\n" + result)

	assertParsesAsGo(t, result)
	assertContains(t, result, "var Mock_First sync.Map")
	assertContains(t, result, "Mock_First.Load(reflect.TypeOf(First[T]).String())")
	assertContains(t, result, "func Real_First[T any](xs []T) T")
}

func TestRewriteSource_GenericFunction_NoResults(t *testing.T) {
	src := []byte(`package bar

func Consume[T any](x T) {
	_ = x
}
`)
	out, err := RewriteSource(src, "Consume")
	if err != nil {
		t.Fatal(err)
	}
	result := string(out)
	t.Log("Rewritten source:\n" + result)

	assertParsesAsGo(t, result)
	assertContains(t, result, "var Mock_Consume sync.Map")
	// Void wrapper form: no `return` before the mock call.
	assertContains(t, result, "_rewire_typed(x)")
	assertNotContains(t, result, "return _rewire_typed(x)")
}

func TestRewriteSource_GenericFunction_Variadic(t *testing.T) {
	src := []byte(`package bar

func Concat[T any](prefix T, rest ...T) []T {
	out := []T{prefix}
	out = append(out, rest...)
	return out
}
`)
	out, err := RewriteSource(src, "Concat")
	if err != nil {
		t.Fatal(err)
	}
	result := string(out)
	t.Log("Rewritten source:\n" + result)

	assertParsesAsGo(t, result)
	// Variadic spread must survive on the forwarding mock call and the real call.
	assertContains(t, result, "_rewire_typed(prefix, rest...)")
	assertContains(t, result, "_real_Concat(prefix, rest...)")
}

func TestRewriteSource_GenericFunction_Constraint(t *testing.T) {
	src := []byte(`package bar

func Max[T int | float64](a, b T) T {
	if a > b {
		return a
	}
	return b
}
`)
	out, err := RewriteSource(src, "Max")
	if err != nil {
		t.Fatal(err)
	}
	result := string(out)
	t.Log("Rewritten source:\n" + result)

	assertParsesAsGo(t, result)
	// The constraint should be preserved on the wrapper, alias, and real.
	assertContains(t, result, "func Max[T int | float64](a, b T) T")
	assertContains(t, result, "func Real_Max[T int | float64](a, b T) T")
	assertContains(t, result, "func _real_Max[T int | float64](a, b T) T")
}

// ---------------------------------------------------------------------------
// Generic method rewriting (methods on generic types)
// ---------------------------------------------------------------------------

func TestRewriteSource_GenericMethod_PointerReceiver(t *testing.T) {
	src := []byte(`package bar

type Container[T any] struct {
	items []T
}

func (c *Container[T]) Add(v T) {
	c.items = append(c.items, v)
}
`)
	out, err := RewriteSource(src, "(*Container).Add")
	if err != nil {
		t.Fatal(err)
	}
	result := string(out)
	t.Log("Rewritten source:\n" + result)

	assertParsesAsGo(t, result)
	assertContains(t, result, "type Container[T any]") // type decl preserved
	assertContains(t, result, "var Mock_Container_Add sync.Map")
	assertContains(t, result, "func Real_Container_Add[T any](c *Container[T], v T)")
	assertContains(t, result, "func (c *Container[T]) Add(v T)")
	assertContains(t, result, "Mock_Container_Add.Load(reflect.TypeOf((*Container[T]).Add).String())")
	assertContains(t, result, "_rewire_raw.(func(*Container[T], T))")
	assertContains(t, result, "_rewire_typed(c, v)")
	assertContains(t, result, "func (c *Container[T]) _real_Container_Add(v T)")
}

func TestRewriteSource_GenericMethod_ValueReceiver(t *testing.T) {
	src := []byte(`package bar

type Pair[T any] struct {
	A, B T
}

func (p Pair[T]) First() T {
	return p.A
}
`)
	out, err := RewriteSource(src, "Pair.First")
	if err != nil {
		t.Fatal(err)
	}
	result := string(out)
	t.Log("Rewritten source:\n" + result)

	assertParsesAsGo(t, result)
	assertContains(t, result, "var Mock_Pair_First sync.Map")
	assertContains(t, result, "func Real_Pair_First[T any](p Pair[T]) T")
	assertContains(t, result, "func (p Pair[T]) First() T")
	// Value receiver self-reference uses the method expression Pair[T].First.
	assertContains(t, result, "Mock_Pair_First.Load(reflect.TypeOf(Pair[T].First).String())")
	assertContains(t, result, "_rewire_raw.(func(Pair[T]) T)")
	assertContains(t, result, "func (p Pair[T]) _real_Pair_First() T")
}

func TestRewriteSource_GenericMethod_MultipleTypeParams(t *testing.T) {
	src := []byte(`package bar

type Map2[K comparable, V any] struct {
	data map[K]V
}

func (m *Map2[K, V]) Get(k K) (V, bool) {
	v, ok := m.data[k]
	return v, ok
}
`)
	out, err := RewriteSource(src, "(*Map2).Get")
	if err != nil {
		t.Fatal(err)
	}
	result := string(out)
	t.Log("Rewritten source:\n" + result)

	assertParsesAsGo(t, result)
	assertContains(t, result, "var Mock_Map2_Get sync.Map")
	// Constraint `comparable` must survive on the Real_ alias.
	assertContains(t, result, "func Real_Map2_Get[K comparable, V any](m *Map2[K, V], k K) (V, bool)")
	assertContains(t, result, "func (m *Map2[K, V]) Get(k K) (V, bool)")
	assertContains(t, result, "Mock_Map2_Get.Load(reflect.TypeOf((*Map2[K, V]).Get).String())")
	assertContains(t, result, "_rewire_raw.(func(*Map2[K, V], K) (V, bool))")
}

func TestRewriteSource_GenericMethod_NoResults(t *testing.T) {
	src := []byte(`package bar

type Logger[T any] struct{}

func (l *Logger[T]) Log(v T) {
	_ = v
}
`)
	out, err := RewriteSource(src, "(*Logger).Log")
	if err != nil {
		t.Fatal(err)
	}
	result := string(out)
	t.Log("Rewritten source:\n" + result)

	assertParsesAsGo(t, result)
	assertContains(t, result, "_rewire_typed(l, v)")
	// No `return` on the void wrapper body.
	assertNotContains(t, result, "return _rewire_typed(l, v)")
}

func TestRewriteSource_GenericMethod_Variadic(t *testing.T) {
	src := []byte(`package bar

type Batch[T any] struct {
	items []T
}

func (b *Batch[T]) Push(vs ...T) {
	b.items = append(b.items, vs...)
}
`)
	out, err := RewriteSource(src, "(*Batch).Push")
	if err != nil {
		t.Fatal(err)
	}
	result := string(out)
	t.Log("Rewritten source:\n" + result)

	assertParsesAsGo(t, result)
	// Variadic spread preserved in both the mock forward and the real call.
	assertContains(t, result, "_rewire_typed(b, vs...)")
	assertContains(t, result, "b._real_Batch_Push(vs...)")
}

func TestRewriteSource_GenericMethod_DistinctFromNonGenericSameName(t *testing.T) {
	// Two types in the same file — one generic, one not — both with an
	// Add method. Rewrite only the generic one and verify the other is
	// left alone.
	src := []byte(`package bar

type Container[T any] struct{ items []T }

func (c *Container[T]) Add(v T) {
	c.items = append(c.items, v)
}

type PlainBag struct{ items []int }

func (p *PlainBag) Add(v int) {
	p.items = append(p.items, v)
}
`)
	out, err := RewriteSource(src, "(*Container).Add")
	if err != nil {
		t.Fatal(err)
	}
	result := string(out)
	t.Log("Rewritten source:\n" + result)

	assertParsesAsGo(t, result)
	assertContains(t, result, "var Mock_Container_Add sync.Map")
	// PlainBag.Add must be untouched.
	assertNotContains(t, result, "Mock_PlainBag_Add")
	assertContains(t, result, "func (p *PlainBag) Add(v int)")
}

// --- ByInstance rewrites ---

func TestRewriteSourceOpts_ByInstance_PointerMethod(t *testing.T) {
	src := []byte(`package bar

type Server struct {
	Name string
}

func (s *Server) Handle(req string) string {
	return s.Name + ": " + req
}
`)
	out, err := RewriteSourceOpts(src, "(*Server).Handle", RewriteOptions{ByInstance: true})
	if err != nil {
		t.Fatal(err)
	}
	result := string(out)
	t.Log("Rewritten source:\n" + result)

	assertParsesAsGo(t, result)
	// Per-instance table emitted.
	assertContains(t, result, "var Mock_Server_Handle_ByInstance sync.Map")
	// Wrapper still emits the global mock var.
	assertContains(t, result, "var Mock_Server_Handle func(*Server, string) string")
	// Wrapper body prepends a per-instance Load on the receiver name.
	assertContains(t, result, "Mock_Server_Handle_ByInstance.Load(s)")
	// The type assertion uses the global mock var type.
	assertContains(t, result, "_rewire_raw.(func(*Server, string) string)")
	// Dispatch order: per-instance → global → real.
	assertContains(t, result, "_real_Server_Handle")
	// sync import was injected.
	assertContains(t, result, `"sync"`)
}

func TestRewriteSourceOpts_ByInstance_VoidPointerMethod(t *testing.T) {
	src := []byte(`package bar

type Bus struct{}

func (b *Bus) Publish(msg string) {
	_ = msg
}
`)
	out, err := RewriteSourceOpts(src, "(*Bus).Publish", RewriteOptions{ByInstance: true})
	if err != nil {
		t.Fatal(err)
	}
	result := string(out)
	t.Log("Rewritten source:\n" + result)

	assertParsesAsGo(t, result)
	assertContains(t, result, "var Mock_Bus_Publish_ByInstance sync.Map")
	// Void wrapper uses `return` after the typed callback, not `return _rewire_typed(...)`.
	assertContains(t, result, "_rewire_typed(b, msg)")
	assertNotContains(t, result, "return _rewire_typed(b, msg)")
}

func TestRewriteSourceOpts_ByInstance_RejectsFreeFunction(t *testing.T) {
	src := []byte(`package bar

func Greet(name string) string {
	return "hi " + name
}
`)
	_, err := RewriteSourceOpts(src, "Greet", RewriteOptions{ByInstance: true})
	if err == nil {
		t.Fatal("expected error for free-function rewrite with ByInstance, got nil")
	}
	if !contains(err.Error(), "free function") {
		t.Errorf("expected error mentioning free function, got: %v", err)
	}
}

func TestRewriteSourceOpts_ByInstance_RejectsValueReceiver(t *testing.T) {
	src := []byte(`package bar

type Point struct {
	X, Y int
}

func (p Point) String() string {
	return "point"
}
`)
	_, err := RewriteSourceOpts(src, "Point.String", RewriteOptions{ByInstance: true})
	if err == nil {
		t.Fatal("expected error for value-receiver rewrite with ByInstance, got nil")
	}
	if !contains(err.Error(), "value-receiver") {
		t.Errorf("expected error mentioning value-receiver, got: %v", err)
	}
}

func TestRewriteSourceOpts_ByInstance_GenericMethod(t *testing.T) {
	src := []byte(`package bar

type Container[T any] struct {
	items []T
}

func (c *Container[T]) Add(v T) {
	c.items = append(c.items, v)
}
`)
	out, err := RewriteSourceOpts(src, "(*Container).Add", RewriteOptions{ByInstance: true})
	if err != nil {
		t.Fatal(err)
	}
	result := string(out)
	t.Log("Rewritten source:\n" + result)

	assertParsesAsGo(t, result)
	// Both the per-instance table AND the per-instantiation table exist.
	assertContains(t, result, "var Mock_Container_Add_ByInstance sync.Map")
	assertContains(t, result, "var Mock_Container_Add sync.Map")
	// The per-instance head loads on the receiver name.
	assertContains(t, result, "Mock_Container_Add_ByInstance.Load(c)")
	// The per-instance head type-asserts to the generic-method mock fn type.
	assertContains(t, result, "_rewire_raw.(func(*Container[T], T))")
	// Per-instantiation dispatch (on reflect.TypeOf(...).String()) is still there after the per-instance head.
	assertContains(t, result, "Mock_Container_Add.Load(reflect.TypeOf((*Container[T]).Add).String())")
}

func TestRewriteSourceOpts_ByInstanceOff_NoByInstanceEmission(t *testing.T) {
	src := []byte(`package bar

type Server struct{}

func (s *Server) Handle(req string) string {
	return req
}
`)
	out, err := RewriteSourceOpts(src, "(*Server).Handle", RewriteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result := string(out)

	assertParsesAsGo(t, result)
	// Default path: no _ByInstance table, no sync import.
	assertNotContains(t, result, "Mock_Server_Handle_ByInstance")
	assertNotContains(t, result, `"sync"`)
}

// --- Helpers ---

func assertParsesAsGo(t *testing.T, src string) {
	t.Helper()
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, "", src, parser.ParseComments); err != nil {
		t.Fatalf("rewritten source is not valid Go: %v\n---\n%s", err, src)
	}
}

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
