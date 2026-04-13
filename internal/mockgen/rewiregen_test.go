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
	out, err := GenerateRewireMock(src, "GreeterIface", "github.com/example/bar", "bar", "footest")
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
		`rewire.RegisterMockFactory("github.com/example/bar.GreeterIface"`,
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
	out, err := GenerateRewireMock(src, "Logger", "example/logpkg", "logpkg", "footest")
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
	_, err := GenerateRewireMock(src, "Bigger", "example/bar", "bar", "footest")
	if err == nil {
		t.Fatal("expected error for embedded interface")
	}
	if !strings.Contains(err.Error(), "embedded") {
		t.Errorf("expected error mentioning embedded, got: %v", err)
	}
}

func TestGenerateRewireMock_RejectsGeneric(t *testing.T) {
	src := []byte(`package bar

type Store[V any] interface {
	Get(key string) V
	Set(key string, v V)
}
`)
	_, err := GenerateRewireMock(src, "Store", "example/bar", "bar", "footest")
	if err == nil {
		t.Fatal("expected error for generic interface")
	}
	if !strings.Contains(err.Error(), "generic") {
		t.Errorf("expected error mentioning generic, got: %v", err)
	}
}
