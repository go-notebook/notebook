package format

import (
	"strings"
	"testing"
)

const sample = "# Title\n\nSome prose.\n\n```go\nx := 1\n```\n\nMore.\n\n~~~~go  name=load\ny := x + 1\n~~~~\n```python\nnot go\n```\n   ```go\nz := 3\n   ```\n"

func TestParse(t *testing.T) {
	nb := Parse(sample)
	type want struct {
		kind   Kind
		source string
		line   int
		info   string
	}
	wants := []want{
		{Prose, "# Title\n\nSome prose.\n\n", 1, ""},
		{Code, "x := 1\n", 6, ""},
		{Prose, "\nMore.\n\n", 8, ""},
		{Code, "y := x + 1\n", 12, "name=load"},
		{Prose, "```python\nnot go\n```\n", 14, ""},
		{Code, "z := 3\n", 18, ""},
	}
	if len(nb.Cells) != len(wants) {
		t.Fatalf("got %d cells: %+v", len(nb.Cells), nb.Cells)
	}
	for i, w := range wants {
		c := nb.Cells[i]
		if c.Kind != w.kind || c.Source != w.source || c.Line != w.line || c.Info != w.info {
			t.Errorf("cell %d = {%v %q line %d info %q}, want %+v", i, c.Kind, c.Source, c.Line, c.Info, w)
		}
	}
	if got := nb.String(); got != sample {
		t.Errorf("round trip:\n%q\nwant\n%q", got, sample)
	}
}

func TestParseEdges(t *testing.T) {
	for _, src := range []string{
		"",
		"no fences at all",
		"```go\nunclosed\nstill code",
		"```go\nunclosed with newline\n",
		"````go\n```\nstill inside\n````\n",
		"    ```go\nindented four is not a fence\n```\n",
		"``go\ntwo backticks are not a fence\n``\n",
		"```go`\nbacktick in a backtick info string\n```\n",
		"```go\n```\n",
		"~~~go\n~~~ trailing text does not close\n~~~\n",
		"```gopher\nanother word\n```\n",
		"```\nno info\n```\n",
		"```go\r\nx := 1\r\n```\r\n",
		"```go\nx\n    ```\n```\n",
	} {
		if got := Parse(src).String(); got != src {
			t.Errorf("round trip of %q gave %q", src, got)
		}
	}
	nb := Parse("````go\n```\nstill inside\n````\n")
	if len(nb.Cells) != 1 || nb.Cells[0].Source != "```\nstill inside\n" {
		t.Errorf("long fence: %+v", nb.Cells)
	}
	nb = Parse("```gopher\nx\n```\n")
	if len(nb.Cells) != 1 || nb.Cells[0].Kind != Prose {
		t.Errorf("gopher block is not go: %+v", nb.Cells)
	}
	nb = Parse("~~~go\n~~~ trailing text does not close\n~~~\n")
	if len(nb.Cells) != 1 || !strings.Contains(nb.Cells[0].Source, "trailing") {
		t.Errorf("fence with trailing text closed the block: %+v", nb.Cells)
	}
}

func TestNewCode(t *testing.T) {
	nb := &Notebook{Cells: []Cell{
		{Kind: Prose, Source: "# Built\n"},
		NewCode("s := \"```\""),
		NewCode(""),
	}}
	want := "# Built\n````go\ns := \"```\"\n````\n```go\n```\n"
	if got := nb.String(); got != want {
		t.Errorf("String = %q, want %q", got, want)
	}
	back := Parse(want)
	if len(back.Cells) != 3 || back.Cells[1].Source != "s := \"```\"\n" {
		t.Errorf("reparse = %+v", back.Cells)
	}
	nb = &Notebook{Cells: []Cell{{Kind: Code, Source: "no newline"}}}
	if got := nb.String(); got != "```go\nno newline\n```\n" {
		t.Errorf("literal cell = %q", got)
	}
	c := NewCode("x := 1")
	c.Info = "name=x"
	nb = &Notebook{Cells: []Cell{c}}
	if got := nb.String(); got != "```go name=x\nx := 1\n```\n" {
		t.Errorf("info = %q", got)
	}
}

func FuzzRoundTrip(f *testing.F) {
	f.Add(sample)
	f.Add("```go\nx\n")
	f.Fuzz(func(t *testing.T, src string) {
		if got := Parse(src).String(); got != src {
			t.Errorf("round trip of %q gave %q", src, got)
		}
	})
}
