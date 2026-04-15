package toolexec

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// targetsHash computes a deterministic hash of the mock target set.
// Only the Targets map (importPath → []funcName) is hashed, since
// that's what determines which packages need rewriting. Instantiations,
// byInstance, and mockedInterfaces don't affect which packages need
// cache invalidation — they only affect how the rewriting is done
// within an already-targeted package.
func targetsHash(targets mockTargets) string {
	// Sort for determinism.
	type entry struct {
		Pkg   string
		Funcs []string
	}
	var sorted []entry
	for pkg, funcs := range targets {
		cp := make([]string, len(funcs))
		copy(cp, funcs)
		sort.Strings(cp)
		sorted = append(sorted, entry{pkg, cp})
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Pkg < sorted[j].Pkg
	})
	data, _ := json.Marshal(sorted)
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h)
}

// persistentHashPath returns the path to the file that stores the
// targets hash across builds. The file lives inside GOCACHE so that
// its lifetime is coupled to the build cache's lifetime: if the
// cache is cleared (or a fresh GOCACHE is used), the hash disappears
// with it, and we never get into a state where the hash claims the
// cache is in sync with a target set that doesn't match the cached
// .a files. Storing it under $TMPDIR was unsafe on macOS because
// /var/folders gets purged on reboot while ~/Library/Caches/go-build
// survives, leaving the two out of sync.
//
// Returns "" when GOCACHE can't be resolved to a usable absolute
// path; the caller then skips hash tracking.
func persistentHashPath(moduleRoot string) string {
	goCache := resolveGoCache()
	if goCache == "" || !filepath.IsAbs(goCache) {
		return ""
	}
	// Scope by module root so different modules don't share a hash.
	h := sha256.Sum256([]byte(moduleRoot))
	return filepath.Join(goCache, "rewire", fmt.Sprintf("targets-%x.hash", h[:8]))
}

// resolveGoCache returns the effective GOCACHE. The result is
// cached for the lifetime of the process via sync.Once, since the
// value can't meaningfully change between calls inside one toolexec
// invocation.
//
// Lookup order:
//  1. $GOCACHE in the environment. The `go` driver sets this before
//     spawning toolexec, so the common path ends here.
//  2. $XDG_CACHE_HOME/go-build or the platform equivalent via
//     os.UserCacheDir() — this matches cmd/go's default when the
//     user hasn't overridden GOCACHE at all.
//  3. `go env GOCACHE` as a last-resort subprocess, used only when
//     the user has set GOCACHE via `go env -w` but is invoking rewire
//     from a context where that override hasn't been propagated into
//     the environment.
var (
	goCacheOnce  sync.Once
	goCacheValue string
)

func resolveGoCache() string {
	goCacheOnce.Do(func() {
		if v := os.Getenv("GOCACHE"); v != "" {
			goCacheValue = v
			return
		}
		if dir, err := os.UserCacheDir(); err == nil && dir != "" {
			goCacheValue = filepath.Join(dir, "go-build")
			return
		}
		cmd := exec.Command("go", "env", "GOCACHE")
		cmd.Env = envWithoutGOFLAGS()
		if out, err := cmd.Output(); err == nil {
			goCacheValue = strings.TrimSpace(string(out))
		}
	})
	return goCacheValue
}

// readPersistentHash reads the stored targets hash. Returns "" if
// the file doesn't exist or can't be read.
func readPersistentHash(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// writePersistentHash atomically writes the targets hash.
func writePersistentHash(path, hash string) {
	dir := filepath.Dir(path)
	_ = os.MkdirAll(dir, 0755)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(hash+"\n"), 0644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// checkAndInvalidateStaleTargets compares the current scan targets
// against the persisted hash and detects when mock
// targets changed since the last build. Rebuilding individual packages
// isn't enough because the linker verifies fingerprints — rewriting `os`
// changes its fingerprint, which mismatches with `testing` (compiled
// against the old `os`). The entire transitive closure would need
// rebuilding, which is equivalent to clearing the cache.
//
// When a change is detected, this function clears the build cache and
// returns a non-empty error message for the caller to surface. The
// current build will fail, but the next `go test` invocation rebuilds
// everything correctly through toolexec with the updated target set.
//
// Returns "" when no action was needed; returns a user-facing message
// when the cache was cleared and a re-run is required.
func checkAndInvalidateStaleTargets(moduleRoot string, targets mockTargets) string {
	hashPath := persistentHashPath(moduleRoot)
	if hashPath == "" {
		// No usable GOCACHE — we can't track a persistent hash here,
		// and without it we also can't meaningfully clear a cache.
		// Skip silently; the separate GOCACHE check below would fire
		// anyway if a real mismatch surfaced.
		return ""
	}
	currentHash := targetsHash(targets)
	storedHash := readPersistentHash(hashPath)

	if currentHash == storedHash {
		return "" // nothing changed
	}

	// First build ever (no stored hash): just record the hash.
	// The packages are being built fresh through toolexec right now,
	// so no stale entries exist to invalidate.
	if storedHash == "" {
		writePersistentHash(hashPath, currentHash)
		return ""
	}

	// Identify which packages changed targets.
	var changed []string
	for importPath, funcs := range targets {
		if len(funcs) > 0 {
			changed = append(changed, importPath)
		}
	}
	sort.Strings(changed)

	// Clear the build cache. We validate GOCACHE before doing
	// anything destructive. persistentHashPath succeeded above, so
	// resolveGoCache() is guaranteed to return a non-empty value —
	// but keep the guard in case either check evolves.
	goCache := resolveGoCache()
	if goCache == "" || !filepath.IsAbs(goCache) {
		return "rewire: mock target set changed but GOCACHE is not set or not absolute — run `go clean -cache` manually and re-run."
	}
	cleanCache := filepath.Clean(goCache)
	if cleanCache == "/" || cleanCache == "." {
		return "rewire: mock target set changed but GOCACHE looks dangerous — run `go clean -cache` manually and re-run."
	}
	home := os.Getenv("HOME")
	if home != "" && cleanCache == filepath.Clean(home) {
		return "rewire: mock target set changed but GOCACHE equals HOME — run `go clean -cache` manually and re-run."
	}

	// Use `go clean -cache` rather than rm -rf, so Go handles its
	// own cache structure safely.
	cmd := exec.Command("go", "clean", "-cache")
	cmd.Env = envWithoutGOFLAGS()
	_ = cmd.Run()

	// Update the stored hash so the next run doesn't clear again.
	writePersistentHash(hashPath, currentHash)

	return fmt.Sprintf(
		"rewire: mock target set changed (affected packages: %s).\n"+
			"    The build cache has been cleared automatically.\n"+
			"    Please re-run your test command — the next run will succeed.",
		strings.Join(changed, ", "))
}


