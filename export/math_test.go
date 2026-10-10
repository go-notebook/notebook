package export

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/go-notebook/notebook/format"
)

// prose renders Markdown prose and replaces each formula by a marker:
// [i:tex] inline, [d:tex] display, [e:tex] a formula go-tex refused.
func prose(t *testing.T, src string) string {
	t.Helper()
	var b bytes.Buffer
	if err := HTML(&b, "t", format.Parse(src), nil); err != nil {
		t.Fatal(err)
	}
	s := b.String()
	s = s[strings.Index(s, `<section class="prose">`)+len(`<section class="prose">`):]
	s = s[:strings.Index(s, "</section>")]
	s = regexp.MustCompile(`(?s)<span class="math" style="vertical-align:-[0-9.]+px"><svg.*?</svg></span>`).ReplaceAllString(s, "[i]")
	s = regexp.MustCompile(`(?s)<span class="math display"><svg.*?</svg></span>`).ReplaceAllString(s, "[d]")
	s = regexp.MustCompile(`<code class="math-error" title="[^"]*">([^<]*)</code>`).ReplaceAllString(s, "[e:$1]")
	return strings.TrimSpace(s)
}

func TestMathInline(t *testing.T) {
	for src, want := range map[string]string{
		"a $x^2$ b":                 "<p>a [i] b</p>",
		"$x$":                       "<p>[i]</p>",
		"costs $20 and $30.":        "<p>costs $20 and $30.</p>",
		"$ x$ is not math":          "<p>$ x$ is not math</p>",
		"$x $ is not math":          "<p>$x $ is not math</p>",
		"$x$5 is not math":          "<p>$x$5 is not math</p>",
		`$a\$b$ escapes`:            "<p>[i] escapes</p>",
		"$x":                        "<p>$x</p>",
		"$$":                        "<p>$$</p>",
		"a $x\ny$ b":                "<p>a $x\ny$ b</p>",
		"`$x$` in code":             "<p><code>$x$</code> in code</p>",
		"bad $\\nosuch$ here":       `<p>bad [e:\nosuch] here</p>`,
		"two $a$ and $b$":           "<p>two [i] and [i]</p>",
	} {
		if got := prose(t, src); got != want {
			t.Errorf("%q:\n got %q\nwant %q", src, got, want)
		}
	}
}

func TestMathDisplay(t *testing.T) {
	for src, want := range map[string]string{
		"$$x^2$$":                       "<p>[d]</p>",
		"$$\n\\int_0^1 x\\,dx\n$$":       "<p>[d]</p>",
		"$$\na\n\nb\n$$":                "<p>$$\na</p>\n<p>b\n$$</p>",
		"$$ never closed\nstill open":   "<p>$$ never closed\nstill open</p>",
		"before $$x$$ after":            "<p>before [d] after</p>",
		"$$\nx\n$$ tail":                "<p>[d] tail</p>",
		"$$\\nosuch$$":                  `<p>[e:\nosuch]</p>`,
	} {
		if got := prose(t, src); got != want {
			t.Errorf("%q:\n got %q\nwant %q", src, got, want)
		}
	}
}

func TestMathNode(t *testing.T) {
	n := &mathNode{tex: "x"}
	if n.Kind() != kindMath {
		t.Error("kind")
	}
	n.Dump([]byte("x"), 0) // goldmark's debugging dump: must not panic
}
