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

### 2. Parallel-test limitation

Single package-global `Mock_Foo` variable per target is what makes
registration and dispatch cheap, but it's also what prevents
`t.Parallel()` on same-target tests (documented as a limitation in
`docs/limitations.md`).

**Direction**: look up mocks via a goroutine-local map (test `*T`
pointer → per-test mock table) instead of reading a package global.
Wrapper becomes:
```go
if m := lookupMock(testID, "pkg.Foo"); m != nil { return m(args...) }
```

**Tradeoff**: real redesign, not a tweak. Affects the rewriter's
wrapper shape, the registry API, and the dispatch hot path. Worth a
design doc before code.

### 3. AST-as-text generation

Both `internal/rewriter/` and `internal/mockgen/` build code via
`fmt.Sprintf` + re-parse. The comments defend this well (parse+print
validates syntax, diffs are readable), but `clearNodePositions`
walking AST nodes via reflection is exactly the kind of code that
breaks silently when `go/ast` grows a field.

**Direction**: switch to direct `ast.Node` construction (possibly via
`dst.NewPackage`/`dst` for easier decoration), which removes the need
for position clearing entirely.

**Tradeoff**: more code, and the text approach has never actually
broken in practice. Not urgent. Fine to defer indefinitely if no
concrete pain.

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
