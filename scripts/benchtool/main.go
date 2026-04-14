// benchtool is rewire's integrated benchmarking binary. It replaces
// the previous bash harness + separate genbench program with a
// single Go CLI that can:
//
//   - generate synthetic benchmark modules at arbitrary sizes
//   - time a single-module benchmark (baseline vs -toolexec=rewire)
//   - run a scaling sweep across multiple module sizes
//
// Everything is in the same binary so there's no shell parsing,
// no stddev regression from awk field shifts, no quoting tax, and
// stats come from the standard library.
//
// Usage:
//
//	go run ./scripts/benchtool gen   -n 50 -o /tmp/bench-50
//	go run ./scripts/benchtool bench -target /tmp/bench-50
//	go run ./scripts/benchtool scale -sizes 10,25,50,100
//
// Run from the rewire repo root.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "gen":
		runGen(os.Args[2:])
	case "bench":
		runBench(os.Args[2:])
	case "scale":
		runScale(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `benchtool — rewire benchmark harness

Subcommands:
  gen    Generate a synthetic benchmark module
  bench  Run a single-module benchmark
  scale  Run a scaling sweep across multiple module sizes

Run "benchtool <subcommand> -h" for subcommand flags.`)
}

// ───────────────────────── gen ─────────────────────────

func runGen(args []string) {
	fs := flag.NewFlagSet("gen", flag.ExitOnError)
	n := fs.Int("n", 50, "number of packages to generate")
	outDir := fs.String("o", "", "output directory (required)")
	rewireRoot := fs.String("rewire", "", "absolute path to the rewire checkout (default: cwd)")
	_ = fs.Parse(args)

	if *outDir == "" {
		fmt.Fprintln(os.Stderr, "gen: -o is required")
		os.Exit(2)
	}
	root := *rewireRoot
	if root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			die("getwd", err)
		}
		root = cwd
	}
	if err := generateModule(*outDir, *n, root); err != nil {
		die("gen", err)
	}
	fmt.Printf("gen: wrote %d packages to %s (10%% mock density, tidied)\n", *n, *outDir)
}

// generateModule writes a synthetic Go module to outDir with n
// packages: every 10th uses rewire.Func / rewire.NewMock, the rest
// are plain. The module go.mod has a replace directive pointing at
// the rewire checkout at rewireRoot so the generated tests compile
// against the local working copy.
func generateModule(outDir string, n int, rewireRoot string) error {
	if err := os.RemoveAll(outDir); err != nil {
		return fmt.Errorf("clean outDir: %w", err)
	}
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	gomod := fmt.Sprintf(`module bench

go 1.22

require github.com/GiGurra/rewire v0.0.0

replace github.com/GiGurra/rewire => %s
`, rewireRoot)
	if err := writeFile(filepath.Join(outDir, "go.mod"), gomod); err != nil {
		return err
	}

	// Shared helper package: declares the function + interface that
	// the mocked subset targets.
	sharedDir := filepath.Join(outDir, "shared")
	if err := os.MkdirAll(sharedDir, 0755); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(sharedDir, "shared.go"), `package shared

// Ping is mocked via rewire.Func from the benchmark packages.
func Ping(msg string) string {
	return "pong:" + msg
}

// Store is mocked via rewire.NewMock from the benchmark packages.
type Store interface {
	Get(key string) string
	Put(key, value string)
}
`); err != nil {
		return err
	}

	// N numbered packages, identical shape, 10% mock density.
	for i := range n {
		pkgName := fmt.Sprintf("pkg%03d", i)
		pkgDir := filepath.Join(outDir, pkgName)
		if err := os.MkdirAll(pkgDir, 0755); err != nil {
			return err
		}
		if err := writeFile(filepath.Join(pkgDir, pkgName+".go"), fmt.Sprintf(`package %s

import "bench/shared"

type Value struct {
	ID   int
	Name string
}

func New(id int, name string) *Value {
	return &Value{ID: id, Name: name}
}

func (v *Value) Describe() string {
	return shared.Ping(v.Name)
}

func (v *Value) Tag() string {
	return "pkg%03d:" + v.Name
}
`, pkgName, i)); err != nil {
			return err
		}

		if i%10 == 0 {
			// Mocked test package.
			if err := writeFile(filepath.Join(pkgDir, pkgName+"_test.go"), fmt.Sprintf(`package %s

import (
	"testing"

	"bench/shared"
	"github.com/GiGurra/rewire/pkg/rewire"
)

func TestDescribe_MockedPing(t *testing.T) {
	rewire.Func(t, shared.Ping, func(msg string) string {
		return "mocked:" + msg
	})
	v := New(1, "alice")
	if got := v.Describe(); got != "mocked:alice" {
		t.Errorf("got %%q", got)
	}
}

func TestStore_Mocked(t *testing.T) {
	m := rewire.NewMock[shared.Store](t)
	rewire.InstanceMethod(t, m, shared.Store.Get, func(s shared.Store, key string) string {
		return "mocked-" + key
	})
	if got := m.Get("x"); got != "mocked-x" {
		t.Errorf("got %%q", got)
	}
}
`, pkgName)); err != nil {
				return err
			}
		} else {
			// Plain test package (no rewire references at all).
			if err := writeFile(filepath.Join(pkgDir, pkgName+"_test.go"), fmt.Sprintf(`package %s

import "testing"

func TestNew(t *testing.T) {
	v := New(1, "alice")
	if v.ID != 1 || v.Name != "alice" {
		t.Errorf("got %%+v", v)
	}
}

func TestTag(t *testing.T) {
	v := New(1, "alice")
	if v.Tag() == "" {
		t.Error("empty tag")
	}
}
`, pkgName)); err != nil {
				return err
			}
		}
	}

	// `go mod tidy` without GOFLAGS so a recursive toolexec doesn't
	// fire during the tidy.
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = outDir
	tidy.Env = envWithoutGOFLAGS()
	if out, err := tidy.CombinedOutput(); err != nil {
		return fmt.Errorf("go mod tidy: %w\n%s", err, out)
	}
	return nil
}

// ───────────────────────── bench (single) ─────────────────────────

func runBench(args []string) {
	fs := flag.NewFlagSet("bench", flag.ExitOnError)
	target := fs.String("target", "", "module to benchmark (default: current dir)")
	pkgs := fs.String("pkgs", "./...", "package spec to compile")
	iters := fs.Int("iters", 5, "timed iterations per mode")
	warm := fs.Bool("warm", false, "warm-cache mode: don't clean the cache between iterations")
	jsonOut := fs.Bool("json", false, "emit JSON instead of the human-readable summary")
	_ = fs.Parse(args)

	rewireRoot, err := os.Getwd()
	if err != nil {
		die("getwd", err)
	}
	if *target == "" {
		*target = rewireRoot
	}

	res, err := runSingleBenchmark(*target, *pkgs, *iters, *warm, rewireRoot)
	if err != nil {
		die("bench", err)
	}

	if *jsonOut {
		if err := json.NewEncoder(os.Stdout).Encode(res); err != nil {
			die("encode", err)
		}
		return
	}
	printBenchResult(os.Stdout, *res)
}

// runSingleBenchmark runs `go test -run ^$ -count=1 <pkgs>` in two
// modes: baseline (no toolexec) and rewire (-toolexec=rewire). Each
// mode gets its own throw-away build cache, pre-warmed once before
// the timing loop so iteration 1 isn't a cold-metadata outlier. In
// non-warm mode we `go clean -cache` between iterations so every
// measured run is a cold compile.
func runSingleBenchmark(target, pkgs string, iters int, warm bool, rewireRoot string) (*BenchResult, error) {
	if err := installRewire(rewireRoot); err != nil {
		return nil, fmt.Errorf("install rewire: %w", err)
	}

	baselineCache, err := os.MkdirTemp("", "rewire-bench-baseline-*")
	if err != nil {
		return nil, err
	}
	rewireCache, err := os.MkdirTemp("", "rewire-bench-rewire-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(baselineCache) }()
	defer func() { _ = os.RemoveAll(rewireCache) }()

	baselineEnv := envOverride(envWithoutGOFLAGS(),
		"GOCACHE="+baselineCache,
		"GOFLAGS=",
	)
	rewireEnv := envOverride(envWithoutGOFLAGS(),
		"GOCACHE="+rewireCache,
		"GOFLAGS=-toolexec=rewire",
	)

	fmt.Fprintf(os.Stderr, "    pre-warming caches...\n")
	if _, err := runGoTestCompile(target, pkgs, baselineEnv); err != nil {
		return nil, fmt.Errorf("baseline pre-warm: %w", err)
	}
	if _, err := runGoTestCompile(target, pkgs, rewireEnv); err != nil {
		return nil, fmt.Errorf("rewire pre-warm: %w", err)
	}

	baseline := make([]time.Duration, iters)
	rewire := make([]time.Duration, iters)

	for i := range iters {
		_, _ = fmt.Fprintf(os.Stderr, "    iter %d/%d  baseline...", i+1, iters)
		if !warm {
			if err := cleanBuildCache(baselineCache); err != nil {
				return nil, fmt.Errorf("clean baseline cache: %w", err)
			}
		}
		d, err := runGoTestCompile(target, pkgs, baselineEnv)
		if err != nil {
			return nil, fmt.Errorf("baseline iter %d: %w", i+1, err)
		}
		baseline[i] = d
		fmt.Fprintf(os.Stderr, " %s", fmtDur(d))

		fmt.Fprintf(os.Stderr, "   rewire...")
		if !warm {
			if err := cleanBuildCache(rewireCache); err != nil {
				return nil, fmt.Errorf("clean rewire cache: %w", err)
			}
		}
		d, err = runGoTestCompile(target, pkgs, rewireEnv)
		if err != nil {
			return nil, fmt.Errorf("rewire iter %d: %w", i+1, err)
		}
		rewire[i] = d
		fmt.Fprintf(os.Stderr, " %s\n", fmtDur(d))
	}

	return &BenchResult{
		Target:   target,
		Pkgs:     pkgs,
		Warm:     warm,
		Baseline: stats(baseline),
		Rewire:   stats(rewire),
	}, nil
}

// BenchResult is the structured result of a single-module benchmark
// run. It's the JSON shape emitted by `bench -json`.
type BenchResult struct {
	Target   string  `json:"target"`
	Pkgs     string  `json:"pkgs"`
	Warm     bool    `json:"warm"`
	Baseline Stats   `json:"baseline"`
	Rewire   Stats   `json:"rewire"`
}

// Stats is a sample distribution summary.
type Stats struct {
	Samples []float64 `json:"samples_seconds"`
	Mean    float64   `json:"mean_seconds"`
	Stddev  float64   `json:"stddev_seconds"`
	Min     float64   `json:"min_seconds"`
	Max     float64   `json:"max_seconds"`
	N       int       `json:"n"`
}

// Ratio returns mean(rewire) / mean(baseline), or 0 if baseline is 0.
func (r *BenchResult) Ratio() float64 {
	if r.Baseline.Mean == 0 {
		return 0
	}
	return r.Rewire.Mean / r.Baseline.Mean
}

// OverheadPct returns (ratio - 1) * 100.
func (r *BenchResult) OverheadPct() float64 {
	return (r.Ratio() - 1) * 100
}

// printBenchResult writes a human-readable summary of a single-
// module benchmark run to stdout.
func printBenchResult(_ *os.File, r BenchResult) {
	p := func(format string, args ...any) {
		_, _ = fmt.Fprintf(os.Stdout, format, args...)
	}
	p("\n==> Results for %s [%s]\n", r.Target, r.Pkgs)
	p("    mode:     %s\n", ternary(r.Warm, "warm cache", "cold cache"))
	p("    baseline: %s  (samples: %s)\n",
		fmtStatsLine(r.Baseline), fmtSamples(r.Baseline.Samples))
	p("    rewire:   %s  (samples: %s)\n",
		fmtStatsLine(r.Rewire), fmtSamples(r.Rewire.Samples))
	p("    overhead: +%.3fs  %+.1f%%  (ratio %.2fx)\n",
		r.Rewire.Mean-r.Baseline.Mean, r.OverheadPct(), r.Ratio())
}

// ───────────────────────── scale ─────────────────────────

func runScale(args []string) {
	fs := flag.NewFlagSet("scale", flag.ExitOnError)
	sizesFlag := fs.String("sizes", "10,25,50,100", "comma-separated module sizes")
	iters := fs.Int("iters", 3, "timed iterations per size")
	jsonOut := fs.Bool("json", false, "emit JSON instead of the human-readable summary")
	_ = fs.Parse(args)

	sizes, err := parseSizes(*sizesFlag)
	if err != nil {
		die("scale", err)
	}
	rewireRoot, err := os.Getwd()
	if err != nil {
		die("getwd", err)
	}

	// One shared tmp dir for all generated modules. We keep them
	// around until the sweep finishes so a failure in one size
	// doesn't forfeit earlier measurements.
	workDir, err := os.MkdirTemp("", "rewire-bench-scale-*")
	if err != nil {
		die("mkdir", err)
	}
	defer func() { _ = os.RemoveAll(workDir) }()

	if err := installRewire(rewireRoot); err != nil {
		die("install rewire", err)
	}

	var all []ScaleRow
	for _, n := range sizes {
		fmt.Fprintf(os.Stderr, "\n==> N=%d: generating synthetic module\n", n)
		modDir := filepath.Join(workDir, fmt.Sprintf("bench-%d", n))
		if err := generateModule(modDir, n, rewireRoot); err != nil {
			die(fmt.Sprintf("gen N=%d", n), err)
		}

		fmt.Fprintf(os.Stderr, "==> N=%d: benchmarking × %d iters\n", n, *iters)
		res, err := runSingleBenchmark(modDir, "./...", *iters, false, rewireRoot)
		if err != nil {
			die(fmt.Sprintf("bench N=%d", n), err)
		}
		all = append(all, ScaleRow{N: n, Result: *res})
		fmt.Fprintf(os.Stderr, "    baseline %.3fs±%.3f  rewire %.3fs±%.3f  ratio %.2fx (%+.1f%%)\n",
			res.Baseline.Mean, res.Baseline.Stddev,
			res.Rewire.Mean, res.Rewire.Stddev,
			res.Ratio(), res.OverheadPct())
	}

	if *jsonOut {
		if err := json.NewEncoder(os.Stdout).Encode(all); err != nil {
			die("encode", err)
		}
		return
	}
	printScaleTable(os.Stdout, all)
}

// ScaleRow is one N sample in a scaling sweep.
type ScaleRow struct {
	N      int         `json:"n_packages"`
	Result BenchResult `json:"result"`
}

func printScaleTable(_ *os.File, rows []ScaleRow) {
	p := func(format string, args ...any) {
		_, _ = fmt.Fprintf(os.Stdout, format, args...)
	}
	pln := func(args ...any) {
		_, _ = fmt.Fprintln(os.Stdout, args...)
	}
	pln()
	pln("==> Scaling summary")
	p("%8s  %20s  %20s  %8s  %10s\n",
		"N pkgs", "baseline (s)", "rewire (s)", "ratio", "overhead")
	p("%8s  %20s  %20s  %8s  %10s\n",
		"------", "------------", "----------", "-----", "--------")
	for _, r := range rows {
		p("%8d  %8.3f ± %-8.3f  %8.3f ± %-8.3f  %7.2fx  %9.1f%%\n",
			r.N,
			r.Result.Baseline.Mean, r.Result.Baseline.Stddev,
			r.Result.Rewire.Mean, r.Result.Rewire.Stddev,
			r.Result.Ratio(), r.Result.OverheadPct())
	}
	pln()
	pln("==> Interpretation guide")
	pln("    Flat ratio across N → rewire scales O(N) with module size.")
	pln("    Ratio climbing with N → super-linear term worth investigating.")
}

// ───────────────────────── shared helpers ─────────────────────────

// runGoTestCompile runs `go test -run ^$ -count=1 <pkgs>` in the
// given module dir with the given env, timing the invocation on
// monotonic wall clock. `-run ^$` matches no tests so the test
// binary is compiled (exercising any toolexec) but nothing runs.
func runGoTestCompile(target, pkgs string, env []string) (time.Duration, error) {
	cmd := exec.Command("go", "test", "-run", "^$", "-count=1", pkgs)
	cmd.Dir = target
	cmd.Env = env
	start := time.Now()
	out, err := cmd.CombinedOutput()
	elapsed := time.Since(start)
	if err != nil {
		return elapsed, fmt.Errorf("%w\n%s", err, out)
	}
	return elapsed, nil
}

func cleanBuildCache(cache string) error {
	cmd := exec.Command("go", "clean", "-cache")
	cmd.Env = envOverride(envWithoutGOFLAGS(), "GOCACHE="+cache)
	return cmd.Run()
}

func installRewire(rewireRoot string) error {
	cmd := exec.Command("go", "install", "./cmd/rewire/")
	cmd.Dir = rewireRoot
	cmd.Env = envWithoutGOFLAGS()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	return nil
}

// envOverride returns env with the given KEY=VALUE entries
// appended, dropping any previous entries with the same key.
func envOverride(env []string, overrides ...string) []string {
	out := make([]string, 0, len(env)+len(overrides))
	overridden := make(map[string]bool, len(overrides))
	for _, o := range overrides {
		if eq := strings.Index(o, "="); eq > 0 {
			overridden[o[:eq+1]] = true
		}
	}
	for _, e := range env {
		skip := false
		for prefix := range overridden {
			if strings.HasPrefix(e, prefix) {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, e)
		}
	}
	return append(out, overrides...)
}

func envWithoutGOFLAGS() []string {
	env := os.Environ()
	filtered := env[:0]
	for _, e := range env {
		if !strings.HasPrefix(e, "GOFLAGS=") {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

// stats computes a Stats from a slice of durations, converting to
// seconds for the summary fields.
func stats(samples []time.Duration) Stats {
	if len(samples) == 0 {
		return Stats{}
	}
	secs := make([]float64, len(samples))
	for i, s := range samples {
		secs[i] = s.Seconds()
	}
	sorted := append([]float64(nil), secs...)
	sort.Float64s(sorted)
	var sum float64
	for _, v := range secs {
		sum += v
	}
	mean := sum / float64(len(secs))
	var sqdiff float64
	for _, v := range secs {
		d := v - mean
		sqdiff += d * d
	}
	stddev := 0.0
	if len(secs) > 1 {
		stddev = math.Sqrt(sqdiff / float64(len(secs)-1))
	}
	return Stats{
		Samples: secs,
		Mean:    mean,
		Stddev:  stddev,
		Min:     sorted[0],
		Max:     sorted[len(sorted)-1],
		N:       len(secs),
	}
}

func fmtDur(d time.Duration) string    { return fmt.Sprintf("%.3fs", d.Seconds()) }
func fmtStatsLine(s Stats) string {
	return fmt.Sprintf("mean=%.3fs stddev=%.3fs min=%.3fs max=%.3fs n=%d",
		s.Mean, s.Stddev, s.Min, s.Max, s.N)
}

func fmtSamples(samples []float64) string {
	parts := make([]string, len(samples))
	for i, v := range samples {
		parts[i] = fmt.Sprintf("%.3fs", v)
	}
	return strings.Join(parts, " ")
}

func parseSizes(s string) ([]int, error) {
	parts := strings.Split(s, ",")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("parse size %q: %w", p, err)
		}
		if n <= 0 {
			return nil, fmt.Errorf("size must be > 0, got %d", n)
		}
		out = append(out, n)
	}
	return out, nil
}

func ternary[T any](cond bool, a, b T) T {
	if cond {
		return a
	}
	return b
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0644)
}

func die(context string, err error) {
	fmt.Fprintf(os.Stderr, "benchtool: %s: %v\n", context, err)
	os.Exit(1)
}

// stamp the Go version into the result so future comparisons can
// account for compiler performance drift. Called in main() only
// when running `bench -json` to keep non-JSON output tidy.
func goVersion() string { return runtime.Version() }
var _ = goVersion
