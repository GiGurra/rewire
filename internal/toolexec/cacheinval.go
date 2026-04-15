package toolexec

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// cacheFilePattern matches Go build cache data files: 64 hex chars + "-d".
var cacheFilePattern = regexp.MustCompile(`^[0-9a-f]{64}-d$`)

// isSafeCacheDelete validates that path is safe to delete: it must be
// a Go build cache data file inside $GOCACHE and nothing else.
//
// Guards:
//  1. path is non-empty
//  2. path is absolute
//  3. GOCACHE is non-empty
//  4. GOCACHE is absolute
//  5. GOCACHE is not "/" or $HOME or "."
//  6. path is strictly under GOCACHE (not equal to it)
//  7. filename matches the <64-hex-chars>-d pattern
func isSafeCacheDelete(path string) bool {
	if path == "" {
		return false
	}
	if !filepath.IsAbs(path) {
		return false
	}

	goCache := os.Getenv("GOCACHE")
	if goCache == "" {
		// GOCACHE not set — resolve via `go env` would be expensive,
		// and if it's not set we can't validate. Bail out.
		return false
	}
	if !filepath.IsAbs(goCache) {
		return false
	}

	// Reject dangerous GOCACHE values.
	cleanCache := filepath.Clean(goCache)
	if cleanCache == "/" || cleanCache == "." {
		return false
	}
	home := os.Getenv("HOME")
	if home != "" && cleanCache == filepath.Clean(home) {
		return false
	}

	// path must be strictly inside GOCACHE.
	cleanPath := filepath.Clean(path)
	rel, err := filepath.Rel(cleanCache, cleanPath)
	if err != nil {
		return false
	}
	// Rel returns ".." prefixed paths if cleanPath is outside cleanCache.
	if rel == "." || strings.HasPrefix(rel, "..") {
		return false
	}

	// Filename must match the Go cache data file pattern.
	if !cacheFilePattern.MatchString(filepath.Base(cleanPath)) {
		return false
	}

	return true
}

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
// targets hash across builds. Unlike the per-PID scan cache, this
// file persists across `go test` invocations so we can detect when
// the target set changes.
func persistentHashPath(moduleRoot string) string {
	// Use the module root to scope the hash — different modules
	// shouldn't share a hash. Hash the module root to get a safe
	// directory name.
	h := sha256.Sum256([]byte(moduleRoot))
	dirName := fmt.Sprintf("rewire-targets-%x", h[:8])
	return filepath.Join(os.TempDir(), dirName, "targets_hash")
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

// invalidateAndRebuildStaleTargets compares the current scan targets
// against the persisted hash. If the target set changed, it deletes
// stale cache entries for affected packages and rebuilds them through
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
	// anything destructive.
	goCache := os.Getenv("GOCACHE")
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

// resolveExportPath runs `go list -export` to find the cached .a path
// for a package. Returns "" on failure.
func resolveExportPath(importPath string, env []string) string {
	cmd := exec.Command("go", "list", "-export", "-f", "{{.Export}}", importPath)
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

