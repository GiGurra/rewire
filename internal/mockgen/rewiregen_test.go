package mockgen

import (
	"fmt"
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
	out, err := GenerateRewireMock(src, "GreeterIface", "github.com/example/bar", "bar", "footest", nil, nil, nil)
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
	out, err := GenerateRewireMock(src, "Logger", "example/logpkg", "logpkg", "footest", nil, nil, nil)
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

// Same-file, same-package embed — the method set flattens: Read and
// Close are promoted from the embedded interface, Name is own.
func TestGenerateRewireMock_EmbedSameFile(t *testing.T) {
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
	out, err := GenerateRewireMock(src, "Bigger", "example/bar", "bar", "footest", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	result := string(out)
	t.Log("Generated:\n" + result)

	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, "", result, parser.ParseComments); err != nil {
		t.Fatalf("generated source does not parse: %v\n%s", err, result)
	}

	// All three methods must be present — the receiver is always the
	// ROOT interface (Bigger), even for promoted methods.
	mustContain := []string{
		`func (m *_rewire_mock_bar_Bigger) Read(p []byte) (_r0 int, _r1 error)`,
		`func (m *_rewire_mock_bar_Bigger) Close() (_r0 error)`,
		`func (m *_rewire_mock_bar_Bigger) Name() (_r0 string)`,
		// Registration uses Bigger, not ReaderCloser — runtime.FuncForPC
		// reports method expressions as `pkg.Outer.Method` even for
		// promoted methods.
		`rewire.RegisterByInstance("example/bar.Bigger.Read"`,
		`rewire.RegisterByInstance("example/bar.Bigger.Close"`,
		`rewire.RegisterByInstance("example/bar.Bigger.Name"`,
	}
	for _, s := range mustContain {
		if !strings.Contains(result, s) {
			t.Errorf("expected output to contain %q\n---\n%s", s, result)
		}
	}
}

// Cross-file / cross-package embed via a stub resolver. Simulates
// embedding io.Reader without actually reading the stdlib — mockgen
// asks the resolver for (pkgPath, ifaceName) and gets back synthetic
// source.
func TestGenerateRewireMock_EmbedCrossPackage(t *testing.T) {
	rootSrc := []byte(`package bar

import "extio"

type Closeable interface {
	extio.Reader
	Close() error
}
`)
	resolver := func(importPath, ifaceName string) ([]byte, error) {
		if importPath != "extio" || ifaceName != "Reader" {
			return nil, fmt.Errorf("unexpected resolver call: %s.%s", importPath, ifaceName)
		}
		return []byte(`package extio

type Reader interface {
	Read(p []byte) (n int, err error)
}
`), nil
	}
	out, err := GenerateRewireMock(rootSrc, "Closeable", "example/bar", "bar", "footest", nil, nil, resolver)
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
		`func (m *_rewire_mock_bar_Closeable) Read(p []byte) (_r0 int, _r1 error)`,
		`func (m *_rewire_mock_bar_Closeable) Close() (_r0 error)`,
		// The registration keys use the OUTER interface's pkgPath and
		// name, not extio.Reader's.
		`rewire.RegisterByInstance("example/bar.Closeable.Read"`,
		`rewire.RegisterByInstance("example/bar.Closeable.Close"`,
	}
	for _, s := range mustContain {
		if !strings.Contains(result, s) {
			t.Errorf("expected output to contain %q\n---\n%s", s, result)
		}
	}
}

// Generic embed with type-parameter flow: Outer[U] embeds Base[U], so
// the promoted method's type arg propagates from Outer to Base.
func TestGenerateRewireMock_EmbedGenericFlow(t *testing.T) {
	src := []byte(`package bar

type Base[T any] interface {
	Get(id int) T
}

type Outer[U any] interface {
	Base[U]
	List() []U
}
`)
	out, err := GenerateRewireMock(src, "Outer", "example/bar", "bar", "footest", []string{"int"}, nil, nil)
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
		// The promoted Get method has U → int flowing through via
		// Base[U] → Base[int]. The generated method returns int.
		`func (m *_rewire_mock_bar_Outer_int) Get(id int) (_r0 int)`,
		`func (m *_rewire_mock_bar_Outer_int) List() (_r0 []int)`,
		// Receiver type in the mockFnType uses the ROOT Outer[int], not Base[int].
		`_rewire_raw.(func(bar.Outer[int], int) int)`,
	}
	for _, s := range mustContain {
		if !strings.Contains(result, s) {
			t.Errorf("expected output to contain %q\n---\n%s", s, result)
		}
	}
}

// Nil resolver with a cross-package embed → clear error referencing
// the embed.
func TestGenerateRewireMock_EmbedNilResolverError(t *testing.T) {
	src := []byte(`package bar

import "io"

type WithEmbed interface {
	io.Reader
}
`)
	_, err := GenerateRewireMock(src, "WithEmbed", "example/bar", "bar", "footest", nil, nil, nil)
	if err == nil {
		t.Fatal("expected error when resolver is nil and embed crosses packages")
	}
	if !strings.Contains(err.Error(), "io.Reader") && !strings.Contains(err.Error(), "InterfaceResolver") {
		t.Errorf("expected error mentioning io.Reader or InterfaceResolver, got: %v", err)
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
	_, err := GenerateRewireMock(src, "Store", "example/bar", "bar", "footest", nil, nil, nil)
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
	_, err := GenerateRewireMock(src, "Greeter", "example/bar", "bar", "footest", []string{"int"}, nil, nil)
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
	out, err := GenerateRewireMock(src, "Container", "github.com/example/bar", "bar", "footest", []string{"int"}, nil, nil)
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

// Type-arg packages overlapping with the interface declaring file's
// imports must dedupe — the generator should emit one import line
// per package, not two. This case: the interface uses
// context.Context internally, AND the test instantiates it with
// context.Context as a type argument. typeArgImports has "context",
// the declaring file's imports also have "context", both should
// resolve to a single import in the generated source.
func TestGenerateRewireMock_TypeArgImportDedupedAgainstDeclaringFile(t *testing.T) {
	src := []byte(`package bar

import "context"

type Holder[T any] interface {
	Wrap(ctx context.Context, v T) (T, error)
}
`)
	out, err := GenerateRewireMock(src,
		"Holder", "github.com/example/bar", "bar", "footest",
		[]string{"context.Context"},
		map[string]string{"context": "context"},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	result := string(out)
	t.Log("Generated:\n" + result)

	// "context" must appear exactly once in the import block.
	count := strings.Count(result, `"context"`)
	if count != 1 {
		t.Errorf(`expected exactly one "context" import, got %d\n---\n%s`, count, result)
	}
}

// typeArgImports providing a package the test references (which the
// interface's declaring file does NOT import). The generator must
// emit the import in the generated mock so the substituted methods
// compile.
func TestGenerateRewireMock_TypeArgImportFromTestFile(t *testing.T) {
	src := []byte(`package bar

type Holder[T any] interface {
	Get() T
}
`)
	out, err := GenerateRewireMock(src,
		"Holder", "github.com/example/bar", "bar", "footest",
		[]string{"time.Duration"},
		map[string]string{"time": "time"},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	result := string(out)
	t.Log("Generated:\n" + result)

	if !strings.Contains(result, `"time"`) {
		t.Errorf(`expected "time" import in generated source\n---\n%s`, result)
	}
	if !strings.Contains(result, `time.Duration`) {
		t.Errorf("expected time.Duration in generated source\n---\n%s", result)
	}
}

// Same-package type qualification: an interface whose methods use
// bare identifiers for types declared in the same package gets those
// identifiers wrapped with the declaring package alias. Previously
// rejected; now the generator qualifies them on the fly.
func TestGenerateRewireMock_SamePackageBareType(t *testing.T) {
	src := []byte(`package bar

type Widget struct {
	Name string
}

type Service interface {
	MakeWidget() *Widget
	Rename(w *Widget, name string) *Widget
	List() []Widget
}
`)
	out, err := GenerateRewireMock(src, "Service", "example/bar", "bar", "footest", nil, nil, nil)
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
		// Bare `*Widget` became `*bar.Widget`, including inside the
		// slice type and as a parameter.
		`func (m *_rewire_mock_bar_Service) MakeWidget() (_r0 *bar.Widget)`,
		`func (m *_rewire_mock_bar_Service) Rename(w *bar.Widget, name string) (_r0 *bar.Widget)`,
		`func (m *_rewire_mock_bar_Service) List() (_r0 []bar.Widget)`,
		// The mockFnType signatures likewise use the qualified form.
		`_rewire_raw.(func(bar.Service) *bar.Widget)`,
		`_rewire_raw.(func(bar.Service, *bar.Widget, string) *bar.Widget)`,
		`_rewire_raw.(func(bar.Service) []bar.Widget)`,
	}
	for _, s := range mustContain {
		if !strings.Contains(result, s) {
			t.Errorf("expected output to contain %q\n---\n%s", s, result)
		}
	}
}

// When the same-package qualifier wants to add an import for the
// interface's declaring package, the import must dedupe against the
// entry already added at the top of the import block — exactly one
// import line for the interface's own package, even though it's
// referenced by both the interface receiver and the bare-type
// qualification pass.
func TestGenerateRewireMock_SamePackageQualification_ImportDedup(t *testing.T) {
	src := []byte(`package bar

type Widget struct{}

type Service interface {
	Get() *Widget
}
`)
	out, err := GenerateRewireMock(src, "Service", "example/bar", "bar", "footest", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	result := string(out)
	// "example/bar" must appear exactly once in the import block.
	if got := strings.Count(result, `"example/bar"`); got != 1 {
		t.Errorf(`expected exactly one "example/bar" import, got %d\n---\n%s`, got, result)
	}
}

// Predeclared types (int, string, error, any) must NOT be qualified —
// they aren't package-local.
func TestGenerateRewireMock_PredeclaredTypesNotQualified(t *testing.T) {
	src := []byte(`package bar

type Basic interface {
	Count() int
	Message() string
	Done() error
	Raw() any
}
`)
	out, err := GenerateRewireMock(src, "Basic", "example/bar", "bar", "footest", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	result := string(out)

	shouldNot := []string{
		"bar.int", "bar.string", "bar.error", "bar.any",
	}
	for _, s := range shouldNot {
		if strings.Contains(result, s) {
			t.Errorf("predeclared type was incorrectly qualified: found %q\n---\n%s", s, result)
		}
	}
}

// Qualification interacts correctly with generic type-param
// substitution. Key invariant: bare same-package type refs in method
// signatures get qualified with the interface's pkg alias, but
// type-arg expressions that came from the test file stay as-is
// (they're in the test pkg's scope, which IS the generated output
// package). This test exercises both in one interface.
func TestGenerateRewireMock_SamePackageQualificationWithGenerics(t *testing.T) {
	src := []byte(`package bar

type Gadget struct{ N int }

type Holder[T any] interface {
	Get() T                   // T stays bare (substituted later)
	MakeGadget() *Gadget      // same-pkg bare ident — must become *bar.Gadget
	Store(v T, g *Gadget)     // mix: T → substituted, Gadget → qualified
}
`)
	// The test file would have written e.g.
	//   rewire.NewMock[bar.Holder[*Widget]]
	// where Widget lives in the test package, so the scanner passes
	// "*Widget" as the type-arg string. In the generated mock
	// (which IS the test package), *Widget stays unqualified.
	out, err := GenerateRewireMock(src, "Holder", "example/bar", "bar", "footest", []string{"*Widget"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	result := string(out)
	t.Log("Generated:\n" + result)

	mustContain := []string{
		// T substituted with test-pkg *Widget — stays bare.
		`func (m *_rewire_mock_bar_Holder_ptr_Widget) Get() (_r0 *Widget)`,
		// Same-pkg Gadget → bar.Gadget.
		`func (m *_rewire_mock_bar_Holder_ptr_Widget) MakeGadget() (_r0 *bar.Gadget)`,
		// Mix: test-pkg *Widget + qualified *bar.Gadget.
		`func (m *_rewire_mock_bar_Holder_ptr_Widget) Store(v *Widget, g *bar.Gadget)`,
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
	out, err := GenerateRewireMock(src, "Cache", "github.com/example/bar", "bar", "footest", []string{"string", "int"}, nil, nil)
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
