package runtime

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// RLimitsEnv is the environment variable through which the kernel gives a
// worker the limits of its R session (Options.R): "alloc,steps,timeout", the
// bytes of vector storage and the calls one cell may use, and its time in
// nanoseconds; -1 (Unbounded) is no bound. Env always writes resolved values,
// so a worker reads 0 only from a hand-made environment, as the default.
const RLimitsEnv = "NOTEBOOK_R_LIMITS"

// Unbounded, as a field of RLimits, removes that limit. It is eval.Unbounded,
// go-embedded-r's own value. Zero is the default, never a bound: a notebook may
// come from anyone, so no bound is only ever asked for.
const Unbounded = -1

// RLimits bound what one R cell may use. A zero field is DefaultRLimits' value,
// Unbounded (-1) removes the limit, any other negative value is read as
// Unbounded.
type RLimits struct {
	// MaxAlloc is the bytes of vector storage one cell may allocate, in total
	// over its life, not at one time: a loop that grows a vector with c(x, i)
	// allocates n²/2 elements to hold n.
	MaxAlloc int
	// MaxSteps is the number of calls one cell may evaluate.
	MaxSteps int
	// Timeout is how long one cell may run.
	Timeout time.Duration
}

// DefaultRLimits are the limits a kernel gives its R cells when it is not told
// others. A cell that needs more says so: Options.R.
//
//   - 512 MiB of vectors is a 64-million-element numeric vector, the longest
//     the session allows (MaxVector), a matrix of 8000 x 8000, or 64 passes over
//     a 1-million-element vector that each make a new one. The total counts
//     garbage too, so the heap a cell holds at once is at most this.
//   - 50 million steps, calls and loop iterations together (go-embedded-r
//     v0.4.0), is 16,666,666 iterations of for (i in 1:n) s <- s + i (3 steps
//     each) and 12,499,999 of while (i < n) i <- i + 1 (4 each), measured, so a
//     runaway loop ends on the steps, the same on every machine, and a cell that
//     is merely slow (a simulation of 10 million iterations) still finishes.
//   - 30 seconds is a notebook cell a person is waiting for. It is also the
//     only bound on a loop that catches the step limit's own error
//     (repeat tryCatch(…, error = function(e) NULL)), which R code can.
var DefaultRLimits = RLimits{MaxAlloc: 512 << 20, MaxSteps: 50_000_000, Timeout: 30 * time.Second}

// Resolve gives a zero field its default and a negative one Unbounded.
func (l RLimits) Resolve() RLimits {
	pick := func(v, def int64) int64 {
		switch {
		case v == 0:
			return def
		case v < 0:
			return Unbounded
		}
		return v
	}
	return RLimits{
		MaxAlloc: int(pick(int64(l.MaxAlloc), int64(DefaultRLimits.MaxAlloc))),
		MaxSteps: int(pick(int64(l.MaxSteps), int64(DefaultRLimits.MaxSteps))),
		Timeout:  time.Duration(pick(int64(l.Timeout), int64(DefaultRLimits.Timeout))),
	}
}

// Env is the setting of RLimitsEnv for these limits, where a zero field takes
// its default and a negative one is written -1, no bound.
func (l RLimits) Env() string {
	r := l.Resolve()
	return fmt.Sprintf("%s=%d,%d,%d", RLimitsEnv, r.MaxAlloc, r.MaxSteps, int64(r.Timeout))
}

// ParseRLimits reads the value of RLimitsEnv, where a field is a count or
// exactly -1 (no bound). An empty or malformed value, or any other negative
// one, is the defaults: a worker is never left without bounds because its
// environment was wrong.
func ParseRLimits(env string) RLimits {
	parts := strings.Split(env, ",")
	if len(parts) != 3 {
		return DefaultRLimits
	}
	var v [3]int64
	for i, p := range parts {
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil || n < Unbounded {
			return DefaultRLimits
		}
		v[i] = n
	}
	return RLimits{MaxAlloc: int(v[0]), MaxSteps: int(v[1]), Timeout: time.Duration(v[2])}
}
