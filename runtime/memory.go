package runtime

import (
	"fmt"
	"os"
	"runtime/debug"
	"runtime/metrics"
	"strconv"
	"time"
)

// MemoryEnv is the environment variable through which the kernel gives a
// worker its memory limit, in bytes (Options.CellMemory).
const MemoryEnv = "NOTEBOOK_MEMORY_LIMIT"

// ExitMemory is the exit status of a worker that went over its limit: the
// kernel reports it as the cell's failure.
const ExitMemory = 70

// heapBytes is what the limit counts: the memory the Go runtime holds for
// the heap's objects, live or not yet swept. A seam for the tests.
var heapBytes = func() uint64 {
	s := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
	metrics.Read(s)
	return s[0].Value.Uint64()
}

// watchMemory enforces a worker's memory limit. The garbage collector is
// asked to keep under it first (debug.SetMemoryLimit); a cell that still
// holds more is stopped by ending the process, which the kernel survives —
// the only way to stop a cell in the middle of whatever it is doing.
func watchMemory(env string, every time.Duration, exit func(int)) {
	limit, err := strconv.ParseUint(env, 10, 64)
	if err != nil || limit == 0 {
		return
	}
	debug.SetMemoryLimit(int64(limit))
	go func() {
		for range time.Tick(every) {
			if used := heapBytes(); used > limit {
				fmt.Fprintf(os.Stderr, "notebook: the cell holds %d bytes, over its limit of %d\n", used, limit)
				exit(ExitMemory)
				return
			}
		}
	}()
}
