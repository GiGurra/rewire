# Review follow-ups (2026-04-17)

Follow-ups from an architectural review pass on the whole rewire
project. Ordered roughly by structural concern. Per current
implementation plan: tackle smaller items first, then the main list
from the top.

---

## Main items

### 1. Scan cache keyed on parent PID

`internal/toolexec/scan.go` caches scan results in
`/tmp/rewire-<ppid>/mock_targets.json`. The TODO in
`plans/TODO_next_session.md` calls this out-of-scope, but it's the one
place the design feels structurally shaky rather than cosmetic: PID
reuse on long-running CI hosts will hand a build a stale cache at
some point, and the failure mode is silent — wrong functions get
rewritten (or worse, none do).

**Direction** (per 2026-04-17 user guidance): keep PID as the cache
key, but pair it with an identity signal so a reused PID can be
detected and the stale cache rejected. Investigate what's reliably
available on Darwin + Linux:
- Process start time (from `/proc/<pid>/stat` on Linux, `ps -o lstart`
  or `sysctl kern.proc` on Darwin).
- `ppid` chain (is our parent still the expected `go` process?).
- `cwd` or `GOCACHE` of the caller baked into the cache header.
Whichever we pick, write it into the cache file and verify on read;
mismatch → re-scan + rewrite.

**Tradeoff**: keeps the current lock-free fast path intact, only
adds a cheap syscall or two on cache hit. Cheaper than re-hashing the
scanned file set.

### 2. Parallel-test limitation — investigated, working prototype in draft PR #6

**Status**: end-to-end working implementation exists on
`proto/parallel-safety-experimental` (draft PR #6). Full test
suite passes including `-race`. Not merged: the mechanism leans
on a linkname loophole and needs API-shape decisions before it
could ship.

**Mechanism that actually worked** (different from the original
"goroutine-local map keyed on test *T pointer" direction in this
doc): **pprof labels as goroutine identity.**
`pprof.SetGoroutineLabels` is public API, the labels pointer is
per-goroutine, and crucially it's **inherited automatically by
child goroutines** at spawn time. Read via
`//go:linkname runtime/pprof.runtime_getProfLabel` — a single
linkname through pprof that's reachable under Go 1.23+ rules
because runtime pushes the symbol out to pprof.

See `plans/parallel_test_safety_findings.md` for the full write-up
of lessons learned: why goroutine-ID keying doesn't work in
Go 1.23+, why cross-package linkname into rewire doesn't work for
test binaries that don't transitively import rewire, why a
per-package state architecture (each rewritten function has its
own `Mock_Foo_ByGoroutine sync.Map`) beats a central map, and
what still needs resolving before this could merge.

### 3. AST-as-text generation — evaluated 2026-04-17, no action

**Verdict**: close, don't migrate. Original critique was overstated.

The concern was that `clearNodePositions` in
`internal/rewriter/rewriter.go:764–789` walks AST nodes via
reflection and would "break silently when `go/ast` grows a field."
On closer inspection, the reflection keys on
`reflect.TypeOf(token.NoPos)` — a stable sentinel type — so any
future `token.Pos` field on any AST node type is automatically
handled. The realistic failure mode would be Go changing
`token.Pos` to a non-int64 type, which would break every `go/ast`
consumer in the ecosystem, not specifically rewire.

Migration cost (full rewrite of both `internal/rewriter` and
`internal/mockgen` to build `ast.Node` trees directly, or adopt
`github.com/dave/dst`) is substantial. Gain is ~22 lines of
well-commented reflection removed, a cleaner mental model if you
already know `dst`, and a negligible speedup. Not worth it at
current scope.

The text-based approach is also **easier to extend** for the cases
that currently add generation logic (new wrapper features extend a
`fmt.Sprintf` template; test assertions are coarse substring
matches), so the working conventions match what the codebase
actually does.

**If this is ever revisited**, the trigger would be one of:
- `clearNodePositions` actually breaking on a future Go release
- A wrapper feature that's genuinely awkward to express as text
- Adopting `dst` for a different reason (e.g., gopls overlay work
  in the roadmap)

Until then, status quo is the correct choice.

### 4. Generic dispatch cost

`reflect.TypeOf(fn).String()` on every call into a mocked generic
function is fine for tests but means the machinery can't be reused
for, e.g., production-time feature-flag rewiring without rethinking
the dispatch path.

**Direction**: not a code change — a doc change. Make it explicit in
`docs/design.md` that this is a deliberate scope choice (test-only),
not an oversight. Discourages future contributors from trying to
"optimize" by removing the reflection.

---

## Smaller items

### 5a. README duplication — minimal pass done 2026-04-17

**Done**: dropped the triplicate `os.Getwd` / `filepath.Abs` "Quick
example" from `docs/index.md`. It now lives canonically in
`docs/function-mocking.md` (where the page is actually about function
mocking); `docs/index.md` still links to it via "Next steps".

**Left as-is (justified)**:
- Teaser snippets in README (lines 11–29) and `docs/index.md` (lines
  8–26) are byte-identical, but serve different entry points (GitHub
  vs hosted docs site). Visitors to one haven't necessarily seen the
  other.
- Install/cache/run quick-start in README and
  `docs/getting-started.md` differ in audience: README's is a
  30-second pitch, getting-started is the hand-held walkthrough.

**Open question**: is the byte-identical teaser duplication worth
further dedup? Could trim `docs/index.md`'s teaser to a shorter form
that complements rather than mirrors README. Left for user call.

### 5b. benchtool/ status

`benchtool/` sits alongside `cmd/` and isn't mentioned in `CLAUDE.md`.
Check whether it's still load-bearing or cruft from an earlier
iteration. If still used: document it. If not: remove.

### 5c. expect.Wait placement — resolved, no change

On re-read, the critique doesn't hold. `Wait` is 25 lines, operates
purely on `Rule`'s own state (`r.count`, `r.parent.mu`,
`r.matcher.describe()`), and is a natural extension of the counting
DSL rather than async-coordination reaching into a different domain.
Moving it would just expose `Rule` internals or duplicate them.
Dropped.

---

## Implementation order

Per user direction (2026-04-17):
1. Smaller items first: 5b (benchtool) → 5c (expect.Wait) → 5a (README)
2. Then main list from the top: 1 → 2 → 3 → 4
