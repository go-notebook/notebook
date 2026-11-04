package export

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-notebook/notebook/analysis"
	"github.com/go-notebook/notebook/format"
	"github.com/go-notebook/notebook/kernel"
)

// An unknown language is highlighted as Go, a language with no lexer is
// shown escaped, and a hostile word does not panic.
func TestHighlightDispatch(t *testing.T) {
	const src = "x := 1 < 2"
	goHTML := highlight("go", src)
	if !strings.Contains(goHTML, "&lt;") || highlight("nonesuch", src) != goHTML || highlight("\x00../", src) != goHTML {
		t.Errorf("unknown language: %q", highlight("nonesuch", src))
	}
	analysis.Register(plainLang{})
	defer analysis.Unregister("plain")
	if got := highlight("plain", src); got != "x := 1 &lt; 2" {
		t.Errorf("a language with no lexer: %q", got)
	}
	if highlight("ruby", src) == goHTML && highlight("ruby", "def f; end") == highlight("go", "def f; end") {
		t.Error("ruby uses the ruby lexer")
	}
}

func TestHTML(t *testing.T) {
	nb := format.Parse("# Title <x>\n\nSome *prose*.\n\n```go\nx := 1 < 2\n```\n\n```go\ny\n```\n\n```go\nz\n```\n")
	rep := &kernel.Report{Build: 1500 * time.Millisecond, Results: []kernel.Result{
		{Cell: 0, Output: "out <b>\n", Ran: true, Took: time.Millisecond, Display: []map[string][]byte{
			{"image/svg+xml": []byte("<?xml version=\"1.0\"?>\n<!DOCTYPE svg>\n<svg id=s></svg>")},
			{"image/svg+xml": []byte("<svg id=bare></svg>")},
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
		`<span class="n">x</span> <span class="o">:=</span> <span class="m">1</span> <span class="o">&lt;</span> <span class="m">2</span>`,
		`<pre class="out">out &lt;b&gt;`,
		`<div class="display"><svg id=s></svg>`,
		`<div class="display"><svg id=bare></svg>`,
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

// A Ruby cell is highlighted with the Ruby lexer.
func TestRubyHighlight(t *testing.T) {
	nb := format.Parse("```ruby\nputs :sym\n```\n")
	var b strings.Builder
	if err := HTML(&b, "t", nb, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), `<span class="ss">:sym</span>`) {
		t.Fatalf("not highlighted as Ruby:\n%s", b.String())
	}
}

// plainLang is a language with no go-rouge lexer, so it is not a Highlighter.
type plainLang struct{}

func (plainLang) Name() string { return "plain" }
func (plainLang) Analyze(string, analysis.Limits) (analysis.Names, []analysis.Diagnostic, error) {
	return analysis.Names{}, nil, nil
}
func (plainLang) Crosses(_, _ *analysis.Cell, _ string) bool { return false }
func (plainLang) Run() analysis.Run                          { return analysis.Run{Runs: true} }

// A Julia cell and an R cell are highlighted with go-rouge's julia and r
// lexers (go-rouge v0.8.0): the token classes differ from the Go lexer's, and
// the source is still escaped.
func TestJuliaAndRHighlight(t *testing.T) {
	for _, tc := range []struct{ lang, src string }{
		{"julia", "function f(x)\n    x < 2 ? \"a\" : :b\nend\n"},
		{"r", "f <- function(x) {\n  if (x < 2) \"a\" else NULL\n}\n"},
	} {
		nb := format.Parse("```" + tc.lang + "\n" + tc.src + "```\n")
		var b strings.Builder
		if err := HTML(&b, "t", nb, nil); err != nil {
			t.Fatal(err)
		}
		page := b.String()
		if !strings.Contains(page, `<span class="k">`) || !strings.Contains(page, "&lt;") || strings.Contains(page, "x < 2") {
			t.Errorf("%s: not highlighted and escaped:\n%s", tc.lang, page)
		}
		if got, plain := highlight(tc.lang, tc.src), highlight("plain-none", tc.src); got == plain {
			t.Errorf("%s is shown as Go", tc.lang)
		}
	}
}
