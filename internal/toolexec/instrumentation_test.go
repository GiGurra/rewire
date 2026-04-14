package toolexec

import (
	"reflect"
	"testing"
)

// detectInstrumentationFlags must detect -race / -msan / -asan in
// compile-tool args and return them in order, and must NOT confuse
// values like `-race=0` or unrelated args that happen to contain
// the substring "race".
func TestDetectInstrumentationFlags(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "no flags",
			args: []string{"-p", "foo", "-o", "foo.a", "foo.go"},
			want: nil,
		},
		{
			name: "race",
			args: []string{"-p", "foo", "-race", "-o", "foo.a", "foo.go"},
			want: []string{"-race"},
		},
		{
			name: "msan",
			args: []string{"-msan", "-p", "foo"},
			want: []string{"-msan"},
		},
		{
			name: "asan",
			args: []string{"-asan", "-p", "foo"},
			want: []string{"-asan"},
		},
		{
			name: "race and asan",
			args: []string{"-p", "foo", "-race", "-asan"},
			want: []string{"-race", "-asan"},
		},
		{
			name: "no false positive on substring",
			args: []string{"-p", "github.com/example/race-detector"},
			want: nil,
		},
		{
			name: "no false positive on equals form",
			args: []string{"-p", "foo", "-D=race", "-race=0"},
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := detectInstrumentationFlags(tc.args)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
