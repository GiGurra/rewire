package mockgen

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestGenerateRewireMock_Simple(t *testing.T) {
	src := []byte(`package bar

type GreeterIface interface {
	Greet(name string) string
}
`)
	out, err := GenerateRewireMock(src, "GreeterIface", "github.com/example/bar", "bar", "footest", nil)
	if err != nil {
		t.Fatal(err)
	}
	result := string(out)
	t.Log("Generated:\n" + result)

	// Must parse as valid Go.
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, "", result, parser.ParseComments); err != nil {
		t.Fatalf("generated source does not parse: %v\n%s", err, result)
	}

	mustContain := []string{
		`package footest`,
		`"sync"`,
		`"github.com/GiGurra/rewire/pkg/rewire"`,
		`"github.com/example/bar"`,
		`type _rewire_mock_bar_GreeterIface struct{ _ [1]byte }`,
		`var Mock__rewire_mock_bar_GreeterIface_Greet_ByInstance sync.Map`,
		`func (m *_rewire_mock_bar_GreeterIface) Greet(name string) (_r0 string)`,
		`Mock__rewire_mock_bar_GreeterIface_Greet_ByInstance.Load(m)`,
		`_rewire_raw.(func(bar.GreeterIface, string) string)`,
		`_rewire_fn(m, name)`,
		`rewire.RegisterMockFactory[bar.GreeterIface](func() any { return &_rewire_mock_bar_GreeterIface{} })`,
		`rewire.RegisterByInstance("github.com/example/bar.GreeterIface.Greet"`,
	}
	for _, s := range mustContain {
		if !strings.Contains(result, s) {
			t.Errorf("expected output to contain %q\n---\n%s", s, result)
		}
	}
}

func TestGenerateRewireMock_MultipleMethods_NoReturn(t *testing.T) {
	src := []byte(`package logpkg

type Logger interface {
	Log(msg string)
	Logf(format string, args ...any)
}
`)
	out, err := GenerateRewireMock(src, "Logger", "example/logpkg", "logpkg", "footest", nil)
	if err != nil {
		t.Fatal(err)
	}
	result := string(out)
	t.Log("Generated:\n" + result)

	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, "", result, parser.ParseComments); err != nil {
		t.Fatalf("generated source does not parse: %v\n%s", err, result)
	}

	// Void methods must use fn(...); return — not "return fn(...)".
	if !strings.Contains(result, "_rewire_fn(m, msg)") {
		t.Error("expected call to _rewire_fn for Log")
	}
	if strings.Contains(result, "return _rewire_fn(m, msg)") {
		t.Error("void methods should not use return _rewire_fn(...)")
	}
	// Variadic spread preserved.
	if !strings.Contains(result, "Logf(format string, args ...any)") {
		t.Error("variadic param decl missing")
	}
	if !strings.Contains(result, "_rewire_fn(m, format, args...)") {
		t.Error("variadic spread on call missing")
	}
}

func TestGenerateRewireMock_RejectsEmbedded(t *testing.T) {
	src := []byte(`package bar

type ReaderCloser interface {
	Read(p []byte) (int, error)
	Close() error
}

type Bigger interface {
	ReaderCloser
	Name() string
}
`)
	_, err := GenerateRewireMock(src, "Bigger", "example/bar", "bar", "footest", nil)
	if err == nil {
		t.Fatal("expected error for embedded interface")
	}
	if !strings.Contains(err.Error(), "embedded") {
		t.Errorf("expected error mentioning embedded, got: %v", err)
	}
}

// Generic interface with no type arguments → arity error pointing at
// the missing type args.
func TestGenerateRewireMock_GenericArityErrorMissing(t *testing.T) {
	src := []byte(`package bar

type Store[V any] interface {
	Get(key string) V
	Set(key string, v V)
}
`)
	_, err := GenerateRewireMock(src, "Store", "example/bar", "bar", "footest", nil)
	if err == nil {
		t.Fatal("expected arity error")
	}
	if !strings.Contains(err.Error(), "expects 1 type argument") {
		t.Errorf("expected arity error mentioning '1 type argument', got: %v", err)
	}
}

// Non-generic interface called with type arguments → arity error.
func TestGenerateRewireMock_GenericArityErrorExtra(t *testing.T) {
	src := []byte(`package bar

type Greeter interface {
	Greet(name string) string
}
`)
	_, err := GenerateRewireMock(src, "Greeter", "example/bar", "bar", "footest", []string{"int"})
	if err == nil {
		t.Fatal("expected arity error for non-generic interface with type args")
	}
	if !strings.Contains(err.Error(), "not generic") {
		t.Errorf("expected error mentioning 'not generic', got: %v", err)
	}
}

// Generic interface with one type parameter, instantiated with int.
// Verifies type-parameter substitution in the method signatures and
// distinct struct naming per instantiation.
func TestGenerateRewireMock_GenericSingleParam(t *testing.T) {
	src := []byte(`package bar

type Container[T any] interface {
	Add(v T)
	Get(i int) T
	Len() int
}
`)
	out, err := GenerateRewireMock(src, "Container", "github.com/example/bar", "bar", "footest", []string{"int"})
	if err != nil {
		t.Fatal(err)
	}
	result := string(out)
	t.Log("Generated:\n" + result)

	// Must parse as valid Go.
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, "", result, parser.ParseComments); err != nil {
		t.Fatalf("generated source does not parse: %v\n%s", err, result)
	}

	mustContain := []string{
		// Mangled struct name carries the instantiation suffix.
		`type _rewire_mock_bar_Container_int struct{ _ [1]byte }`,
		// Method signatures have T → int substituted.
		`func (m *_rewire_mock_bar_Container_int) Add(v int)`,
		`func (m *_rewire_mock_bar_Container_int) Get(i int) (_r0 int)`,
		`func (m *_rewire_mock_bar_Container_int) Len() (_r0 int)`,
		// mockFnType uses the instantiated interface.
		`_rewire_raw.(func(bar.Container[int], int))`,
		// Factory registration uses the instantiated interface as type param.
		`rewire.RegisterMockFactory[bar.Container[int]](func() any { return &_rewire_mock_bar_Container_int{} })`,
		// RegisterByInstance uses the witness pattern with typed nil.
		`rewire.RegisterByInstance("github.com/example/bar.Container.Add", &Mock__rewire_mock_bar_Container_int_Add_ByInstance, (func(bar.Container[int], int))(nil))`,
	}
	for _, s := range mustContain {
		if !strings.Contains(result, s) {
			t.Errorf("expected output to contain %q\n---\n%s", s, result)
		}
	}
}

// Multiple type parameters, e.g. Cache[K comparable, V any]. Verifies
// that arity > 1 substitution works and produces a struct name
// disambiguated by both type args.
func TestGenerateRewireMock_GenericMultipleParams(t *testing.T) {
	src := []byte(`package bar

type Cache[K comparable, V any] interface {
	Set(k K, v V)
	Get(k K) (V, bool)
}
`)
	out, err := GenerateRewireMock(src, "Cache", "github.com/example/bar", "bar", "footest", []string{"string", "int"})
	if err != nil {
		t.Fatal(err)
	}
	result := string(out)
	t.Log("Generated:\n" + result)

	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, "", result, parser.ParseComments); err != nil {
		t.Fatalf("generated source does not parse: %v\n%s", err, result)
	}

	mustContain := []string{
		`type _rewire_mock_bar_Cache_string_int struct{ _ [1]byte }`,
		// K → string, V → int substituted in both methods.
		`func (m *_rewire_mock_bar_Cache_string_int) Set(k string, v int)`,
		`func (m *_rewire_mock_bar_Cache_string_int) Get(k string) (_r0 int, _r1 bool)`,
		// Instantiated factory.
		`rewire.RegisterMockFactory[bar.Cache[string, int]](func() any { return &_rewire_mock_bar_Cache_string_int{} })`,
		// mockFnType has both type args substituted.
		`_rewire_raw.(func(bar.Cache[string, int], string, int))`,
		`_rewire_raw.(func(bar.Cache[string, int], string) (int, bool))`,
	}
	for _, s := range mustContain {
		if !strings.Contains(result, s) {
			t.Errorf("expected output to contain %q\n---\n%s", s, result)
		}
	}
}
