package toolexec

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// intrinsicsCache memoizes intrinsicFunctions() within a single
// toolexec process. The underlying work is expensive:
//
//   - `go env GOROOT` subprocess (~20ms)
//   - read + regex-parse the compiler's intrinsics.go (~5-15ms)
//
// Without caching, isIntrinsic calls this once per mocked function
// per compile invocation — in the loops at toolexec.go:59 and :668 —
// which scales linearly with the number of mocked functions and the
// number of compile steps. Cross-process caching isn't needed here;
// each toolexec invocation is short enough that one parse amortizes
// across all the isIntrinsic calls inside it, and intrinsics don't
// change within a build.
var (
	intrinsicsOnce sync.Once
	intrinsicsData map[string]map[string]bool
)

// intrinsicFunctions returns the set of functions that the Go compiler
// replaces with CPU intrinsics. These cannot be mocked because the compiler
// replaces calls to them at the call site with hardware instructions,
// bypassing any wrapper we generate.
//
// The list is parsed from $GOROOT/src/cmd/compile/internal/ssagen/intrinsics.go.
// Result is memoized for the lifetime of the toolexec process.
func intrinsicFunctions() map[string]map[string]bool {
	intrinsicsOnce.Do(func() {
		goroot := goroot()
		if goroot == "" {
			return
		}
		path := filepath.Join(goroot, "src", "cmd", "compile", "internal", "ssagen", "intrinsics.go")
		data, err := os.ReadFile(path)
		if err != nil {
			return
		}
		intrinsicsData = parseIntrinsics(string(data))
	})
	return intrinsicsData
}

// goroot returns the GOROOT path by running `go env GOROOT`.
func goroot() string {
	out, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// parseIntrinsics extracts addF("pkg", "func", ...) patterns from the
// compiler's intrinsics source file.
func parseIntrinsics(src string) map[string]map[string]bool {
	// Match addF("package", "FuncName", ...) patterns
	re := regexp.MustCompile(`addF\(\s*"([^"]+)"\s*,\s*"([^"]+)"`)

	result := map[string]map[string]bool{}
	for _, match := range re.FindAllStringSubmatch(src, -1) {
		pkg := match[1]
		fn := match[2]
		if result[pkg] == nil {
			result[pkg] = map[string]bool{}
		}
		result[pkg][fn] = true
	}
	return result
}

// isIntrinsic checks whether a function is a compiler intrinsic.
func isIntrinsic(pkgPath, funcName string) bool {
	intrinsics := intrinsicFunctions()
	if intrinsics == nil {
		return false
	}
	funcs, ok := intrinsics[pkgPath]
	if !ok {
		return false
	}
	return funcs[funcName]
}
