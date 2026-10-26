package runtime

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
)

// fakeInterp is an Interpreter whose inputs are Go-side commands.
type fakeInterp struct {
	out, errOut io.Writer
	last        string
}

type classErr struct{ msg, class string }

func (e classErr) Error() string { return e.msg }
func (e classErr) Class() string { return e.class }

func (f *fakeInterp) Eval(src, file string, line int) (string, map[string][]byte, error) {
	switch src {
	case "print":
		fmt.Fprint(f.out, "hello ")
		fmt.Fprint(f.errOut, "world\n")
		return "nil", nil, nil
	case "figure":
		return "#<Plot::Figure>", map[string][]byte{"image/svg+xml": []byte("<svg/>")}, nil
	case "raise":
		return "", nil, classErr{fmt.Sprintf("boom (RuntimeError)\n\tfrom %s:%d", file, line), "RuntimeError"}
	case "plain":
		return "", nil, errors.New("plain failure")
	case "panic":
		panic("binding bug")
	}
	f.last = src
	return fmt.Sprintf("%q", src), nil, nil
}

func (f *fakeInterp) Complete(target string) []string {
	if target == "panic" {
		panic("completion bug")
	}
	return []string{target + "1", target + "2"}
}

// converse runs serveInterpreter over the requests and returns its replies.
func converse(t *testing.T, reqs ...string) ([]Reply, bool) {
	t.Helper()
	var out strings.Builder
	var mu sync.Mutex
	exited := false
	done := make(chan struct{})
	open := func(o, e io.Writer) Interpreter { return &fakeInterp{out: o, errOut: e} }
	exit := func(int) {
		mu.Lock()
		exited = true
		mu.Unlock()
	}
	go func() {
		serveInterpreter(open, strings.NewReader(strings.Join(reqs, "\n")+"\n"), &out, exit)
		close(done)
	}()
	<-done
	var reps []Reply
	sc := bufio.NewScanner(strings.NewReader(out.String()))
	for sc.Scan() {
		var r Reply
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		reps = append(reps, r)
	}
	mu.Lock()
	defer mu.Unlock()
	return reps, exited
}

func TestServeInterpreter(t *testing.T) {
	reps, _ := converse(t,
		`{"op":"eval","source":"1 + 1","line":1}`,
		`{"op":"eval","source":"print","line":2}`,
		`{"op":"eval","source":"figure","line":3}`,
		`{"op":"eval","source":"raise","line":4}`,
		`{"op":"eval","source":"plain","line":5}`,
		`{"op":"eval","source":"panic","line":6}`,
		`{"op":"complete","source":"x"}`,
		`{"op":"complete","source":"panic"}`,
		`{"op":"frobnicate"}`,
		`not json`,
		`{"op":"exit"}`,
		`{"op":"eval","source":"after exit"}`,
	)
	if len(reps) != 12 {
		t.Fatalf("%d replies: %+v", len(reps), reps)
	}
	if reps[0].Inspect != `"1 + 1"` || reps[0].Error != "" || reps[0].More {
		t.Errorf("value: %+v", reps[0])
	}
	// What the input prints comes first, in order, as More replies.
	if !reps[1].More || reps[1].Output != "hello " || !reps[2].More || reps[2].Output != "world\n" || reps[3].Inspect != "nil" || reps[3].More {
		t.Errorf("output: %+v", reps[1:4])
	}
	if len(reps[4].Display) != 1 || string(reps[4].Display[0]["image/svg+xml"]) != "<svg/>" {
		t.Errorf("figure: %+v", reps[4])
	}
	if reps[5].Error != "boom (RuntimeError)\n\tfrom (irb):4" || reps[5].Class != "RuntimeError" {
		t.Errorf("raise: %+v", reps[5])
	}
	if reps[6].Error != "plain failure" || reps[6].Class != "" {
		t.Errorf("plain: %+v", reps[6])
	}
	if !strings.HasPrefix(reps[7].Error, "panic: binding bug") {
		t.Errorf("panic: %+v", reps[7])
	}
	if strings.Join(reps[8].Candidates, ",") != "x1,x2" {
		t.Errorf("complete: %+v", reps[8])
	}
	if len(reps[9].Candidates) != 0 || reps[9].Error != "" {
		t.Errorf("complete panic: %+v", reps[9])
	}
	if reps[10].Error != "unknown op frobnicate" {
		t.Errorf("unknown: %+v", reps[10])
	}
	if !strings.HasPrefix(reps[11].Error, "unknown op bad request: ") {
		t.Errorf("bad: %+v", reps[11])
	}
}

func TestServeInterpreterInputEnds(t *testing.T) {
	// The kernel closes the input: the worker ends, without an exit request.
	reps, exited := converse(t, `{"op":"eval","source":"x","line":1}`)
	if len(reps) != 1 || !exited {
		t.Errorf("replies %+v, exited %v", reps, exited)
	}
}
