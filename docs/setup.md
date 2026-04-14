# Setup

All of rewire's mock generation — function/method mocking via `rewire.Func` / `rewire.InstanceMethod`, and interface mocking via `rewire.NewMock[T]` — runs through the same `-toolexec=rewire` pipeline. So this one setup step covers everything.

## Recommended: test-specific environment

Keep test builds in a separate cache so `go build` and `go test` never interfere.

### Terminal (alias in shell profile)

```bash
alias gotest='GOFLAGS="-toolexec=rewire" GOCACHE="$HOME/.cache/rewire-test" go test'
```

Then run tests with:

```bash
gotest ./...
```

### GoLand

Run > Edit Configurations > Templates > Go Test > Environment variables:

```
GOFLAGS=-toolexec=rewire
GOCACHE=/Users/<you>/.cache/rewire-test
```

This applies to all Go Test run configurations, including click-to-run from the gutter.

### VS Code

Add to `.vscode/settings.json` or user settings:

```json
"go.testEnvVars": {
    "GOFLAGS": "-toolexec=rewire",
    "GOCACHE": "${env:HOME}/.cache/rewire-test"
}
```

### What this gives you

- `go build` uses the default cache — clean production binaries, no rewire artifacts
- `go test` (via alias or IDE) uses a separate cache — rewire active, no cache conflicts

## Alternative: global GOFLAGS

If you don't mind the minimal overhead (a nil check per mocked function in production builds):

```bash
export GOFLAGS="-toolexec=rewire"
```

This is simpler but means `go build` also rewrites targeted functions. The overhead is probably negligible in most situations — only functions you explicitly mock are affected, and it's just a nil check.

## First-time cache clean

After installing rewire for the first time (or after changing rewire versions), clean the build cache so packages get recompiled through rewire:

```bash
go clean -cache
```

This only needs to happen once. After that, Go's build cache handles incremental rebuilds correctly.

**If you're using a separate test cache** (recommended above), `go clean -cache` wipes whichever cache `$GOCACHE` currently points at — which is *not* your test cache unless you set it explicitly:

```bash
GOCACHE="$HOME/.cache/rewire-test" go clean -cache
```

Worth knowing if you ever find yourself chasing stale test results after editing the rewire source or changing versions.
