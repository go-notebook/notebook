package export

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-notebook/notebook/format"
	"github.com/go-notebook/notebook/kernel"
)

func TestHTML(t *testing.T) {
	nb := format.Parse("# Title <x>\n\nSome *prose*.\n\n```go\nx := 1 < 2\n```\n\n```go\ny\n```\n\n```go\nz\n```\n")
	rep := &kernel.Report{Build: 1500 * time.Millisecond, Results: []kernel.Result{
		{Cell: 0, Output: "out <b>\n", Ran: true, Took: time.Millisecond, Display: []map[string][]byte{
			{"image/svg+xml": []byte("<svg id=s></svg>")},
			{"image/png": []byte{1, 2, 3}},
			{"text/html": []byte("<table></table>")},
			{"text/plain": []byte("a < b")},
		}},
		{Cell: 1, Error: "undefined: y"},
	}}
	var b bytes.Buffer
	if err := HTML(&b, "nb <1>", nb, rep); err != nil {
		t.Fatal(err)
	}
	page := b.String()
	for _, want := range []string{
		"<title>nb &lt;1&gt;</title>",
		"<h1>Title <!-- raw HTML omitted --></h1>",
		"<em>prose</em>",
		"x := 1 &lt; 2",
		`<pre class="out">out &lt;b&gt;`,
		"<svg id=s></svg>",
		"data:image/png;base64,AQID",
		"<table></table>",
		"<pre>a &lt; b</pre>",
		`<pre class="err">undefined: y</pre>`,
		`<div class="took">1ms</div>`,
		"1 of 2 cells ran; build 1.5s.",
		`id="cell-2"`,
		"</html>",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	b.Reset()
	if err := HTML(&b, "t", nb, nil); err != nil || strings.Contains(b.String(), "<footer>") {
		t.Errorf("without results: %v", err)
	}
	if err := HTML(failing{}, "t", nb, nil); err == nil {
		t.Error("write error not returned")
	}
}

type failing struct{}

func (failing) Write([]byte) (int, error) { return 0, errors.New("disk full") }
