# TODO — Next session

## Docs review — end-to-end coherence pass

Read every file under `docs/` in nav order and sanity-check that
the whole thing flows as one document. The API-naming pass is done
(see below), so the remaining work is editorial, not mechanical.
Things to watch for:

- **Internal link integrity**: we've moved and merged sections a
  lot recently. Cross-references may have gone stale.
- **Duplication**: `docs/index.md`, `docs/getting-started.md`,
  `README.md`, and `docs/function-mocking.md` all have quick-start
  snippets. Are they consistent? Does each earn its place or is
  one redundant?
- **Design notes vs how-it-works**: now that `design.md` is in the
  nav, there's some overlap with `how-it-works.md`. Each should
  have a clear role (design = "why and alternatives considered",
  how-it-works = "what happens step by step").
- **Performance section tone**: after the recent rewrite, it's
  honest but longer than the other sections. Maybe trim.
- **Limitations**: should ONLY describe things that don't work.
  Verify no capability descriptions have crept back in.
- **Roadmap**: most Phase 2 items are shipped. The roadmap page
  should reflect that cleanly without a long trail of "shipped"
  markers.

## API naming pass — shipped

Done in the rename commit. Summary for historical context:

| Before | After |
|---|---|
| `InstanceMethod` | `InstanceFunc` — mirrors `Func`, honest about methods-as-funcs |
| `RestoreInstanceMethod` | `RestoreInstanceFunc` |
| `Restore` *(overloaded)* | split → `RestoreFunc` (global) / `RestoreInstance` (all-on-receiver) / `RestoreInstanceFunc` (one entry) |
| `Replace` | deleted — unused escape hatch |

Kept as-is after review: `Func`, `NewMock`, `Real`, `expect.For`,
`expect.ForInstance`. The `expect` DSL verbs read well as grammar
and don't benefit from mirroring the lower-level `Func` vocabulary.

The `pkg/rewire/replace.go` file was renamed to `rewire.go` at the
same time since it contains the whole package API.

## Out of scope (for now)

- Scan cache cleanup (disk leak + PID reuse edge case). Discussed
  earlier, deliberately skipped.
- Any further performance optimization. The cost is fundamental to
  compiling generated mock code; not worth chasing.
- Parallel test safety for the same target. Still documented as a
  limitation.
