package toolexec

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// parentStartTime reads /proc/<pid>/stat and returns field 22
// (starttime — jiffies since boot when the process started). The
// value is opaque: callers only compare equality against another
// value produced by this same function on the same boot.
//
// The stat file format is:
//
//	<pid> (<comm>) <state> <ppid> <pgrp> ... <starttime> ...
//
// where <comm> is the process's short name wrapped in parens. The
// comm itself can contain whitespace and parens, so we locate the
// closing ')' and split the rest on whitespace — the fields after
// comm are well-defined and unambiguous. starttime is the 20th
// whitespace-separated token after the ')'.
func parentStartTime(pid int) (int64, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	// Everything up to and including the final ')' is pid + comm.
	// Everything after is the fixed-format field list.
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 || end+1 >= len(data) {
		return 0, fmt.Errorf("rewire: /proc/%d/stat: malformed (no comm terminator)", pid)
	}
	rest := strings.TrimSpace(string(data[end+1:]))
	fields := strings.Fields(rest)
	// rest starts at field 3 (state), so starttime (field 22) is
	// index 22 - 3 = 19.
	const starttimeIdx = 22 - 3
	if len(fields) <= starttimeIdx {
		return 0, fmt.Errorf("rewire: /proc/%d/stat: only %d post-comm fields, need %d", pid, len(fields), starttimeIdx+1)
	}
	return strconv.ParseInt(fields[starttimeIdx], 10, 64)
}
