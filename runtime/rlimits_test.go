package runtime

import (
	"testing"
	"time"
)

func TestRLimitsEnv(t *testing.T) {
	for _, tc := range []struct {
		in   RLimits
		want string
	}{
		{RLimits{}, "NOTEBOOK_R_LIMITS=536870912,50000000,30000000000"},
		{RLimits{MaxAlloc: 1 << 20, MaxSteps: 5, Timeout: time.Second}, "NOTEBOOK_R_LIMITS=1048576,5,1000000000"},
		{RLimits{MaxAlloc: -1, MaxSteps: -1, Timeout: -1}, "NOTEBOOK_R_LIMITS=0,0,0"},
	} {
		if got := tc.in.Env(); got != tc.want {
			t.Errorf("%+v: %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseRLimits(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want RLimits
	}{
		{"1048576,5,1000000000", RLimits{MaxAlloc: 1 << 20, MaxSteps: 5, Timeout: time.Second}},
		{"0,0,0", RLimits{}}, // no bound
		{"", DefaultRLimits},
		{"1,2", DefaultRLimits},
		{"1,2,3,4", DefaultRLimits},
		{"1,x,3", DefaultRLimits},
		{"1,-2,3", DefaultRLimits},
	} {
		if got := ParseRLimits(tc.env); got != tc.want {
			t.Errorf("%q: %+v, want %+v", tc.env, got, tc.want)
		}
	}
	// What the kernel writes, the worker reads back.
	for _, l := range []RLimits{{}, {MaxAlloc: 7, MaxSteps: 8, Timeout: 9}, {MaxAlloc: -1}} {
		env := l.Env()[len(RLimitsEnv)+1:]
		if got, want := ParseRLimits(env), l.resolve(); got != want {
			t.Errorf("%+v: %+v, want %+v", l, got, want)
		}
	}
}
