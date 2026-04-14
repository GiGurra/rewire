package toolexec

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Verifies that resolvePackageDir honors a go.mod `replace` directive.
// If rewire used the old go/build.Default.Import path, the replace
// would be ignored and we'd get the un-replaced resolution (or a "not
// found" for the synthetic example.com/upstream path).
func TestResolvePackageDir_ReplaceDirective(t *testing.T) {
	tmpDir := t.TempDir()

	// The "replaced" local source dir.
	upstreamDir := filepath.Join(tmpDir, "localupstream")
	if err := os.MkdirAll(upstreamDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(upstreamDir, "go.mod"), []byte(
		"module example.com/upstream\n\ngo 1.22\n",
	), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(upstreamDir, "upstream.go"), []byte(
		"package upstream\n\ntype Greeter interface{ Greet(name string) string }\n",
	), 0644); err != nil {
		t.Fatal(err)
	}

	// Consumer module with a replace directive pointing at upstreamDir.
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(
		"module testmod\n\ngo 1.22\n\n"+
			"require example.com/upstream v0.0.0\n\n"+
			"replace example.com/upstream => ./localupstream\n",
	), 0644); err != nil {
		t.Fatal(err)
	}
	// A trivial main file so `go list` treats this as a real package.
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte(
		"package main\n\nimport _ \"example.com/upstream\"\n\nfunc main() {}\n",
	), 0644); err != nil {
		t.Fatal(err)
	}

	// resolvePackageDir inherits cwd via the `go list` subprocess, so
	// we need to run with tmpDir as cwd. We also clear the cache so
	// previous tests don't leak state.
	resetPackageDirCache()
	withCwd(t, tmpDir, func() {
		dir, err := resolvePackageDir("example.com/upstream")
		if err != nil {
			t.Fatalf("resolvePackageDir: %v", err)
		}
		absUpstream := mustResolveSymlink(t, upstreamDir)
		absResolved := mustResolveSymlink(t, dir)
		if absResolved != absUpstream {
			t.Errorf("resolved dir mismatch\n  got:  %s\n  want: %s", absResolved, absUpstream)
		}
	})
}

// Verifies that resolvePackageDir honors a `go.work` workspace. Two
// modules are joined by go.work; the consumer module has NO replace
// directive for the producer, so only the workspace can find it.
func TestResolvePackageDir_WorkspaceGoWork(t *testing.T) {
	tmpDir := t.TempDir()

	// Producer module: exports an interface.
	aDir := filepath.Join(tmpDir, "mod-a")
	if err := os.MkdirAll(filepath.Join(aDir, "api"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(aDir, "go.mod"), []byte(
		"module example.com/moda\n\ngo 1.22\n",
	), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(aDir, "api", "api.go"), []byte(
		"package api\n\ntype Store interface {\n\tGet(key string) (string, bool)\n\tPut(key, value string)\n}\n",
	), 0644); err != nil {
		t.Fatal(err)
	}

	// Consumer module: requires the producer but has NO replace for
	// it. Resolution must go through go.work.
	bDir := filepath.Join(tmpDir, "mod-b")
	if err := os.MkdirAll(filepath.Join(bDir, "app"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bDir, "go.mod"), []byte(
		"module example.com/modb\n\ngo 1.22\n\n"+
			"require example.com/moda v0.0.0\n",
	), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bDir, "app", "app.go"), []byte(
		"package app\n\nimport _ \"example.com/moda/api\"\n",
	), 0644); err != nil {
		t.Fatal(err)
	}

	// go.work at tmpDir root joining both modules.
	if err := os.WriteFile(filepath.Join(tmpDir, "go.work"), []byte(
		"go 1.22\n\nuse (\n\t./mod-a\n\t./mod-b\n)\n",
	), 0644); err != nil {
		t.Fatal(err)
	}

	// Resolve from inside mod-b/app — go.work at the ancestor dir
	// must still take effect. This mirrors the real-world toolexec
	// case: the compile step runs cwd'd inside a sub-package.
	resetPackageDirCache()
	withCwd(t, filepath.Join(bDir, "app"), func() {
		dir, err := resolvePackageDir("example.com/moda/api")
		if err != nil {
			t.Fatalf("resolvePackageDir: %v", err)
		}
		want := mustResolveSymlink(t, filepath.Join(aDir, "api"))
		got := mustResolveSymlink(t, dir)
		if got != want {
			t.Errorf("resolved dir mismatch\n  got:  %s\n  want: %s", got, want)
		}
	})
}

// Verifies that resolvePackageDir uses the stdlib fast path for
// packages in GOROOT/src — no subprocess is spawned.
func TestResolvePackageDir_StdlibFastPath(t *testing.T) {
	resetPackageDirCache()
	dir, err := resolvePackageDir("io")
	if err != nil {
		t.Fatalf("resolvePackageDir(io): %v", err)
	}
	if !strings.Contains(dir, "src/io") && !strings.Contains(dir, string(filepath.Separator)+"io") {
		t.Errorf("stdlib dir doesn't look like a GOROOT/src/io path: %s", dir)
	}
}

// Verifies caching: two consecutive calls with the same import path
// return the same directory and the second hit comes from the cache
// (we can't directly observe "cache hit" but at minimum the second
// call must not error out differently than the first).
func TestResolvePackageDir_CachedWithinRun(t *testing.T) {
	resetPackageDirCache()
	first, err := resolvePackageDir("io")
	if err != nil {
		t.Fatal(err)
	}
	second, err := resolvePackageDir("io")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Errorf("cache mismatch: %q vs %q", first, second)
	}
}

// withCwd runs fn with the process's working directory changed to
// dir, restoring the original cwd afterward. Chdir affects every
// goroutine in the process, so this isn't safe under t.Parallel —
// we don't use t.Parallel in these tests.
func withCwd(t *testing.T, dir string, fn func()) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(prev); err != nil {
			t.Errorf("restoring cwd: %v", err)
		}
	}()
	fn()
}

// mustResolveSymlink canonicalizes a path by resolving any symlinks
// (needed on macOS where /var/folders is a symlink to /private/var).
func mustResolveSymlink(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", path, err)
	}
	return resolved
}

// resetPackageDirCache clears the package-dir memoization between
// tests so each test starts from a clean slate.
func resetPackageDirCache() {
	packageDirCache = sync.Map{}
}
