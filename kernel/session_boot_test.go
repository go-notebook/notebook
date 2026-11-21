package kernel

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/go-notebook/notebook/runtime"
)

// A worker whose boot is slower than CellTimeout does not charge its boot to
// the input: the session's first worker and a replacement both run a quick
// input under the limit.
func TestSessionSlowBootIsNotTheInputs(t *testing.T) {
	opt := sessionOptions(t)
	opt.CellTimeout = 2 * time.Second
	t.Setenv(runtime.BootDelayEnv, "3s")
	s, err := NewSession(opt, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if ev, _ := eval(s, "1 + 1", 1); ev.Inspect != "2" || ev.Lost {
		t.Errorf("first worker: %+v", ev)
	}
	// The worker is gone (as after a timeout): Eval boots a new one.
	s.stop()
	if ev, _ := eval(s, "2 + 2", 2); ev.Inspect != "4" || ev.Lost {
		t.Errorf("replacement worker: %+v", ev)
	}
}

// A worker that does not boot within BootTimeout is killed, and the error
// names its boot.
func TestSessionBootTimeout(t *testing.T) {
	opt := sessionOptions(t)
	seam(t, &BootTimeout, time.Second)
	t.Setenv(runtime.BootDelayEnv, "1m")
	_, err := NewSession(opt, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "did not boot within 1s") {
		t.Errorf("boot timeout: %v", err)
	}
}

// brokenReader fails after its first line.
type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, errors.New("pipe broke") }

func TestSessionAwaitReady(t *testing.T) {
	for _, tc := range []struct {
		name, first, want string
		r                 io.Reader
	}{
		{name: "ready", r: strings.NewReader(`{"ready":true}` + "\n")},
		{name: "stops", r: strings.NewReader(""), want: "boot failed: EOF"},
		{name: "unreadable", r: brokenReader{}, want: "boot failed: pipe broke"},
		{name: "not json", r: strings.NewReader("garbage\n"), want: "boot failed: "},
		{name: "not ready", r: strings.NewReader(`{"inspect":"1"}` + "\n"), want: `it said "{\"inspect\":\"1\"}" first`},
	} {
		s := &Session{k: &Kernel{}}
		s.out = bufio.NewScanner(tc.r)
		err := s.awaitReady()
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}
