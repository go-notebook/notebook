package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

type shown struct{}

func (shown) Display() map[string][]byte {
	return map[string][]byte{"text/html": []byte("<b>x</b>")}
}

type full struct{}

func (full) Display() map[string][]byte {
	return map[string][]byte{"text/plain": []byte("own text")}
}

// point has a pointer-receiver binary encoding.
type point struct{ x, y int }

func (p *point) MarshalBinary() ([]byte, error) { return []byte(fmt.Sprintf("%d,%d", p.x, p.y)), nil }
func (p *point) UnmarshalBinary(b []byte) error {
	_, err := fmt.Sscanf(string(b), "%d,%d", &p.x, &p.y)
	return err
}

// vec has a value-receiver encoding and a pointer-receiver decoding.
type vec struct{ n int }

func (v vec) MarshalBinary() ([]byte, error) { return []byte{byte(v.n)}, nil }
func (v *vec) UnmarshalBinary(b []byte) error {
	if len(b) != 1 {
		return errors.New("bad vec")
	}
	v.n = int(b[0])
	return nil
}

type failing struct{}

func (failing) MarshalBinary() ([]byte, error) { return nil, errors.New("no") }

type failingN struct{ N int }

func (failingN) MarshalBinary() ([]byte, error) { return nil, errors.New("no") }

type exported struct {
	Name string
	Vals map[string][]float64
	Next *exported
}

func TestBundle(t *testing.T) {
	if b := Bundle(shown{}); string(b["text/html"]) != "<b>x</b>" || string(b["text/plain"]) != "{}" {
		t.Errorf("Bundle(shown) = %v", b)
	}
	if b := Bundle(full{}); string(b["text/plain"]) != "own text" {
		t.Errorf("Bundle(full) = %v", b)
	}
	if b := Bundle(3.5); string(b["text/plain"]) != "3.5" {
		t.Errorf("Bundle(3.5) = %v", b)
	}
	c := &Cell{}
	c.Result(2.5, nil)
	if len(c.shown) != 1 || string(c.shown[0]["text/plain"]) != "2.5" {
		t.Errorf("Result(2.5, nil) = %v", c.shown)
	}
	func() {
		defer func() {
			if p := recover(); p != "error: no" {
				t.Errorf("Result with an error panicked with %v", p)
			}
		}()
		c.Result(nil, errors.New("no"))
	}()
	c = &Cell{}
	c.Show(1, nil, "a")
	if len(c.shown) != 2 {
		t.Errorf("Show kept %d displays, want 2 (nil is skipped)", len(c.shown))
	}
}

func TestEncodeDecode(t *testing.T) {
	// gob for exported data, including recursion.
	e := exported{Name: "n", Vals: map[string][]float64{"a": {1, 2}}, Next: &exported{Name: "m"}}
	data, err := Encode(&e)
	if err != nil {
		t.Fatal(err)
	}
	var e2 exported
	if err := Decode(&e2, data); err != nil || e2.Name != "n" || e2.Next.Name != "m" || e2.Vals["a"][1] != 2 {
		t.Errorf("gob round trip: %+v %v", e2, err)
	}
	// A pointer variable whose type marshals itself.
	p := &point{3, 4}
	data, err = Encode(&p)
	if err != nil || string(data[1:]) != "3,4" {
		t.Fatalf("Encode(*point) = %q %v", data, err)
	}
	var p2 *point
	if err := Decode(&p2, data); err != nil || p2.x != 3 || p2.y != 4 {
		t.Errorf("Decode(*point) = %+v %v", p2, err)
	}
	if err := Decode(&p2, append([]byte{tagBinary}, "junk"...)); err == nil {
		t.Error("bad *point data decoded")
	}
	// Nil values are kept as nil.
	var np *point
	data, err = Encode(&np)
	p2 = &point{1, 1}
	if err != nil || Decode(&p2, data) != nil || p2 != nil {
		t.Errorf("nil *point: %q %v %v", data, err, p2)
	}
	for name, bad := range map[string][]byte{"empty": nil, "tag": {9}, "nodecoder": append([]byte{tagBinary}, 1)} {
		var x int
		if err := Decode(&x, bad); err == nil {
			t.Errorf("%s: decoded", name)
		}
	}
	// A value variable: value-receiver marshal, pointer-receiver unmarshal.
	v := vec{7}
	data, err = Encode(&v)
	if err != nil {
		t.Fatal(err)
	}
	var v2 vec
	if err := Decode(&v2, data); err != nil || v2.n != 7 {
		t.Errorf("Decode(vec) = %+v %v", v2, err)
	}
	// A value variable whose pointer marshals: point (value) through &point.
	pv := point{5, 6}
	if data, err := Encode(&pv); err != nil || string(data[1:]) != "5,6" {
		t.Errorf("Encode(point) = %q %v", data, err)
	}

	var iface any = 3
	deep := map[string][]struct{ F func() }{}
	key := map[chan int]int{}
	fn, ch := func() {}, make(chan int)
	for name, ptr := range map[string]any{
		"func":       &fn,
		"chan":       &ch,
		"interface":  &iface,
		"unexported": new(struct{ a int }),
		"deep":       &deep,
		"key":        &key,
		"uintptr":    new(uintptr),
	} {
		if _, err := Encode(ptr); err == nil || !strings.Contains(err.Error(), "cannot keep") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := Encode(new(failing)); err == nil {
		t.Error("a failing MarshalBinary encoded")
	}
}

// exchange runs serve on the given requests and returns the replies.
func exchange(t *testing.T, p Program, reqs ...string) []Reply {
	t.Helper()
	var out bytes.Buffer
	serve(p, strings.NewReader(strings.Join(reqs, "\n")+"\n"), &out)
	var reps []Reply
	dec := json.NewDecoder(&out)
	for dec.More() {
		var r Reply
		if err := dec.Decode(&r); err != nil {
			t.Fatal(err)
		}
		reps = append(reps, r)
	}
	return reps
}

func TestServe(t *testing.T) {
	x := 41
	p := Program{
		Cells: []func(*Cell){
			func(c *Cell) { fmt.Println("hello"); os.Stderr.WriteString("err\n"); c.Show(x + 1) },
			func(*Cell) { panic("boom") },
		},
		Vars: map[string]any{"x": &x, "f": new(func())},
	}
	reps := exchange(t, p,
		`{"op":"run","cell":0}`,
		`{"op":"run","cell":1}`,
		`{"op":"run","cell":9}`,
		`{"op":"get","name":"x"}`,
		`{"op":"get","name":"f"}`,
		`{"op":"get","name":"nope"}`,
		`not json`,
		`{"op":"dance"}`,
	)
	if len(reps) != 8 {
		t.Fatalf("got %d replies", len(reps))
	}
	if reps[0].Output != "hello\nerr\n" || string(reps[0].Display[0]["text/plain"]) != "42" {
		t.Errorf("run 0 = %+v", reps[0])
	}
	if !strings.Contains(reps[1].Error, "panic: boom") || !strings.Contains(reps[1].Error, "goroutine") {
		t.Errorf("panic = %q", reps[1].Error)
	}
	if reps[2].Error != "no cell 9" {
		t.Errorf("bad cell = %q", reps[2].Error)
	}
	data := reps[3].Data
	if len(data) == 0 || reps[3].Volatile {
		t.Errorf("get x = %+v", reps[3])
	}
	if !reps[4].Volatile {
		t.Errorf("get f = %+v", reps[4])
	}
	if reps[5].Error != "no variable nope" || !strings.HasPrefix(reps[6].Error, "bad request") || reps[7].Error != "unknown op dance" {
		t.Errorf("errors = %q %q %q", reps[5].Error, reps[6].Error, reps[7].Error)
	}

	// set, then exit stops serving.
	x = 0
	req, _ := json.Marshal(Request{Op: "set", Name: "x", Data: data})
	bad, _ := json.Marshal(Request{Op: "set", Name: "x", Data: []byte{tagGob, 0xff}})
	reps = exchange(t, p, string(req), string(bad), `{"op":"set","name":"nope"}`, `{"op":"exit"}`, `{"op":"run","cell":0}`)
	_ = bad
	if x != 41 || reps[0].Error != "" || reps[1].Error == "" || reps[2].Error != "no variable nope" || len(reps) != 3 {
		t.Errorf("set: x=%d %+v", x, reps)
	}
}

func TestRedirect(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	restore, err := redirect(w)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Print("captured")
	restore()
	_ = w.Close()
	var b bytes.Buffer
	_, _ = b.ReadFrom(r)
	if b.String() != "captured" {
		t.Errorf("captured %q", b.String())
	}
	if f := protocolOut(); f == nil {
		t.Error("no protocol stream")
	}
}

func TestServeStdin(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = stdin }()
	_, _ = w.WriteString(`{"op":"exit"}` + "\n")
	_ = w.Close()
	Serve(Program{}) // returns on exit
}

func TestRunFailures(t *testing.T) {
	p := Program{Cells: []func(*Cell){func(*Cell) {}}}
	pipe = func() (*os.File, *os.File, error) { return nil, nil, errors.New("no pipe") }
	rep := run(p, 0)
	pipe = os.Pipe
	if rep.Error != "no pipe" {
		t.Errorf("pipe failure: %q", rep.Error)
	}
	redirectOutput = func(*os.File) (func(), error) { return nil, errors.New("no redirect") }
	rep = run(p, 0)
	redirectOutput = redirect
	if rep.Error != "no redirect" {
		t.Errorf("redirect failure: %q", rep.Error)
	}
}

func TestEncodeNested(t *testing.T) {
	type holder struct{ P *point }
	h := holder{P: &point{1, 2}}
	data, err := Encode(&h)
	if err != nil {
		t.Fatal(err)
	}
	var h2 holder
	if err := Decode(&h2, data); err != nil || h2.P.y != 2 {
		t.Errorf("nested marshaler: %+v %v", h2, err)
	}
	// gob calls a nested field's MarshalBinary (when the field is not zero:
	// gob skips zero fields), and passes on its error.
	type wraps struct{ F failingN }
	if _, err := Encode(&wraps{F: failingN{1}}); err == nil {
		t.Error("a nested failing MarshalBinary encoded")
	}
}
