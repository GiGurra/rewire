# benchtool

Rewire's benchmark harness. One Go binary, three subcommands.

## Quick start

```bash
# Time rewire's compile overhead against the current module
go run ./scripts/benchtool bench

# Scaling sweep: 10, 25, 50, 100 packages
go run ./scripts/benchtool scale

# Scaling sweep with incremental output (safe to kill mid-run)
go run ./scripts/benchtool scale -incremental /tmp/scale.jsonl

# Just generate a synthetic module to poke at manually
go run ./scripts/benchtool gen -n 50 -o /tmp/bench-50
```

## Subcommands

### `gen`

Generates a synthetic Go module at a chosen size. 10% of the packages
use `rewire.Func` + `rewire.NewMock`, the rest are plain Go. Every
package has identical shape so the only dimension that varies is `N`.

```
Usage: benchtool gen -n <count> -o <dir> [-rewire <path>]

  -n       number of packages (default 50)
  -o       output directory (required)
  -rewire  absolute path to the rewire checkout (default: cwd)
```

The generated `go.mod` contains a `replace` directive pointing at the
rewire checkout, so the synthetic tests compile against the local
working copy — whatever you have on disk is what gets benchmarked.
`go mod tidy` runs automatically after generation so deps are ready.

### `bench`

Runs `go test -run '^$' -count=1 <pkgs>` in two modes against a
target module. `-run '^$'` matches no tests so we measure compile +
link time, not test runtime.

```
Usage: benchtool bench [flags]

  -target  module to benchmark (default: current dir)
  -pkgs    package spec to compile (default: ./...)
  -iters   timed iterations per mode (default 5)
  -warm    warm-cache mode: don't clean the cache between iterations
  -json    emit JSON instead of the human-readable summary
```

What `bench` does:

1. `go install ./cmd/rewire/` — so the measurement reflects your current source.
2. Pre-warm baseline and rewire caches once — populates the module
   metadata cache so iteration 1 isn't an outlier.
3. For each iteration: `go clean -cache` the throw-away cache (unless
   `-warm`), time `go test -run '^$'`.
4. Report mean ± stddev for each mode plus the ratio.

### `scale`

Runs `gen` and `bench` across a range of module sizes. Each completed
row streams to stdout as it finishes — no `| tail` buffering trap —
and is optionally appended to a JSON Lines file so killing mid-run
keeps partial results.

```
Usage: benchtool scale [flags]

  -sizes        comma-separated module sizes (default "10,25,50,100")
  -iters        timed iterations per size (default 3)
  -json         emit JSON instead of the human-readable summary
  -incremental  write each completed row to this JSONL file
```

## Profiling rewire internals

Set `REWIRE_PROFILE=1` when running a build to get per-stage timing
from rewire's toolexec wrapper:

```bash
REWIRE_PROFILE=1 GOFLAGS='-toolexec=rewire' GOCACHE=/tmp/cache \
  go test -run '^$' ./...
```

Produces lines like:

```
rewire-profile stage=scan duration_ms=42.31 pid=91508
rewire-profile stage=resolve-pkg-dir pkg=github.com/example/bar duration_ms=31.88 pid=91508
rewire-profile stage=rewrite-compile-args pkg=example/foo duration_ms=12.44 pid=91508
rewire-profile stage=compile-wrap pkg=example/foo duration_ms=187.66 pid=91508
```

Stages:

- `scan` — test-file scan pass (cached on disk per build, so you see it once per build)
- `resolve-pkg-dir` — `go list -find` subprocess for package resolution (cached per process)
- `rewrite-compile-args` — source rewriting for targeted functions
- `iface-mock-gen` — interface backing-struct synthesis
- `compile-wrap` — total wall time spent in the toolexec wrapper per compile invocation

## Results archive

The `results/` directory holds JSONL files from reference benchmark
runs. Each filename is dated and each line is a `ScaleRow` record.
Use them as a baseline when measuring the impact of a change:

```bash
# Before and after a change, diff the new results against a saved run.
go run ./scripts/benchtool scale -json > /tmp/after.json
```
