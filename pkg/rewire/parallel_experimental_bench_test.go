package rewire

// Benchmarks for the pprof-labels goroutine-identity mechanism.
// Compare against:
//   - baseline nil check (what the current non-generic wrapper does)
//   - runtime.Stack() parsing (what we'd fall back to if the linkname
//     loophole closes in a future Go release)
//
// Run with:
//   go test ./pkg/rewire/ -bench 'ParallelProto|Reference' -benchmem -run=^$

import (
	"bytes"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"unsafe"
)

// A package-level sink prevents the compiler from eliminating
// the work we're trying to measure. Without it, the nil-check
// wrapper benchmark was being dead-code-eliminated to 0 B/op
// while the labels-lookup wrapper couldn't be, which made the
// allocation columns useless as a comparison.
var (
	benchStringSink string
	benchPtrSink    unsafe.Pointer
	benchAnySink    any
	benchBoolSink   bool
	benchIntSink    int64
)

// ---------- Baselines (absolute floor) ----------

// Raw direct call — the actual best case for "mocked function with
// no instrumentation."
//
//go:noinline
func benchTargetDirect(name string) string {
	return "direct:" + name
}

func BenchmarkParallelProto_DirectCall(b *testing.B) {
	for b.Loop() {
		benchStringSink = benchTargetDirect("x")
	}
}

// Nil-check wrapper — what the current rewire non-generic wrapper
// does. This is the cost we're trying to keep for non-parallel
// targets.
var benchNilMock func(string) string

//go:noinline
func benchNilCheckWrapper(name string) string {
	if benchNilMock != nil {
		return benchNilMock(name)
	}
	return benchTargetDirect(name)
}

func BenchmarkParallelProto_NilCheckWrapper(b *testing.B) {
	for b.Loop() {
		benchStringSink = benchNilCheckWrapper("x")
	}
}

// ---------- pprof-labels mechanism components ----------

// Just the linkname call in isolation.
func BenchmarkParallelProto_RuntimeGetProfLabel(b *testing.B) {
	for b.Loop() {
		benchPtrSink = runtimeGetProfLabel()
	}
}

// lookupParallelMock with no labels set on this goroutine. The
// nil-check at the top should bail immediately.
//
// Note: the testing framework may set labels on test goroutines,
// so this benchmark runs inside a fresh goroutine that has never
// had SetGoroutineLabels called on it.
func BenchmarkParallelProto_LookupNoLabels(b *testing.B) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for b.Loop() {
			benchAnySink, benchBoolSink = lookupParallelMock("bench.target")
		}
	}()
	<-done
}

// lookupParallelMock with labels set but no matching mock — the
// realistic "parallel-mode active, this target isn't mocked"
// scenario.
func BenchmarkParallelProto_LookupMissAfterLabel(b *testing.B) {
	ensureGoroutineLabeled()
	for b.Loop() {
		benchAnySink, benchBoolSink = lookupParallelMock("bench.nonexistent")
	}
}

// lookupParallelMock with a mock installed and matching — the
// "this goroutine's mock is active for this target" path.
func BenchmarkParallelProto_LookupHit(b *testing.B) {
	key := ensureGoroutineLabeled()
	rawTable, _ := goroutineMocks.LoadOrStore(key, &sync.Map{})
	table := rawTable.(*sync.Map)
	table.Store("bench.target", func(string) string { return "mocked" })
	b.Cleanup(func() { table.Delete("bench.target") })

	for b.Loop() {
		benchAnySink, benchBoolSink = lookupParallelMock("bench.target")
	}
}

// ---------- Full wrapper shape (what a rewriter-emitted wrapper would do) ----------

// fakeWrapperBench is the same shape as fakeWrapper in the main
// tests, but isolated here so the benchmark name makes it obvious
// we're measuring the full composed dispatch.

//
//go:noinline
func fakeWrapperBench(name string) string {
	if m, ok := lookupParallelMock("bench.target"); ok {
		return m.(func(string) string)(name)
	}
	return benchTargetDirect(name)
}

// Full wrapper, no mock installed — the common test path
// (parallel mocking enabled for this target in this package, but
// this specific test doesn't mock it).
func BenchmarkParallelProto_WrapperMiss(b *testing.B) {
	ensureGoroutineLabeled()
	for b.Loop() {
		benchStringSink = fakeWrapperBench("x")
	}
}

// Full wrapper, mock installed — the active-mock path.
func BenchmarkParallelProto_WrapperHit(b *testing.B) {
	key := ensureGoroutineLabeled()
	rawTable, _ := goroutineMocks.LoadOrStore(key, &sync.Map{})
	table := rawTable.(*sync.Map)
	table.Store("bench.target", func(name string) string { return "mocked:" + name })
	b.Cleanup(func() { table.Delete("bench.target") })

	for b.Loop() {
		benchStringSink = fakeWrapperBench("x")
	}
}

// ---------- Reference: runtime.Stack() parsing (the fallback) ----------

// If the linkname loophole ever closes, parsing runtime.Stack() is
// the portable fallback. This benchmark establishes how much the
// pprof-labels mechanism saves us vs that worst case.
func BenchmarkReference_StackParseGoid(b *testing.B) {
	for b.Loop() {
		var buf [64]byte
		n := runtime.Stack(buf[:], false)
		text := bytes.TrimPrefix(buf[:n], []byte("goroutine "))
		if before, _, ok := bytes.Cut(text, []byte(" ")); ok {
			id, _ := strconv.ParseInt(string(before), 10, 64)
			benchIntSink = id
		}
	}
}
