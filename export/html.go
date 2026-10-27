// Package export renders a notebook and its results as a standalone HTML
// page: the prose as Markdown, each code cell with what it printed, showed
// or failed with. The page has no scripts and no external resources, and raw
// HTML in the prose is left out (goldmark's safe default), so a page made
// from someone else's notebook runs nothing of theirs but what its cells
// chose to display.
package export

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"html"
	"io"
	"strings"
	"time"

	"github.com/go-rouge/rouge"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"

	"github.com/go-notebook/notebook/format"
	"github.com/go-notebook/notebook/kernel"
)

var md = goldmark.New(goldmark.WithExtensions(extension.GFM, extension.Footnote, mathExtension{}))

// HTML writes the page. results holds one entry per code cell, in order; a
// nil report renders the notebook without results.
func HTML(w io.Writer, title string, nb *format.Notebook, rep *kernel.Report) error {
	var b bytes.Buffer
	fmt.Fprintf(&b, page, html.EscapeString(title))
	code := 0
	for i, c := range nb.Cells {
		if c.Kind == format.Prose {
			fmt.Fprintf(&b, `<section class="prose" data-i="%d">`, i)
			_ = md.Convert([]byte(c.Source), &b) // fails only on a write error; b is a buffer
			b.WriteString("</section>\n")
			continue
		}
		fmt.Fprintf(&b, `<section class="cell" id="cell-%d" data-i="%d"><pre class="code"><code>%s</code></pre>`,
			code, i, highlight(c.Lang, strings.TrimRight(c.Source, "\n")))
		if rep != nil && code < len(rep.Results) {
			writeResult(&b, rep.Results[code])
		}
		b.WriteString("</section>\n")
		code++
	}
	if rep != nil {
		ran := 0
		for _, r := range rep.Results {
			if r.Ran {
				ran++
			}
		}
		fmt.Fprintf(&b, `<footer>%d of %d cells ran; build %s.</footer>`+"\n",
			ran, len(rep.Results), rep.Build.Round(time.Millisecond))
	}
	b.WriteString("</main></body></html>\n")
	_, err := w.Write(b.Bytes())
	return err
}

// writeResult writes a cell's output, displays and error.
func writeResult(b *bytes.Buffer, r kernel.Result) {
	if r.Output != "" {
		fmt.Fprintf(b, `<pre class="out">%s</pre>`, html.EscapeString(r.Output))
	}
	for _, d := range r.Display {
		b.WriteString(`<div class="display">`)
		writeBundle(b, d)
		b.WriteString("</div>")
	}
	if r.Error != "" {
		fmt.Fprintf(b, `<pre class="err">%s</pre>`, html.EscapeString(r.Error))
	}
	if r.Ran && r.Took > 0 {
		fmt.Fprintf(b, `<div class="took">%s</div>`, r.Took.Round(time.Microsecond))
	}
}

// writeBundle writes the richest representation of a MIME bundle the page
// can show.
func writeBundle(b *bytes.Buffer, d map[string][]byte) {
	switch {
	case d["image/svg+xml"] != nil:
		b.Write(inlineSVG(d["image/svg+xml"]))
	case d["image/png"] != nil:
		fmt.Fprintf(b, `<img alt="" src="data:image/png;base64,%s">`, base64.StdEncoding.EncodeToString(d["image/png"]))
	case d["text/html"] != nil:
		b.Write(d["text/html"])
	default:
		fmt.Fprintf(b, `<pre>%s</pre>`, html.EscapeString(string(d["text/plain"])))
	}
}

// highlight colours a cell's source with go-rouge's lexer for its language,
// Go or Ruby (Rouge's short classes: k keyword, s string, ss symbol, m
// number, c comment, n name ...), escaped as HTML.
//
// go-rouge has no Julia lexer yet: a Julia cell is shown escaped, uncoloured.
func highlight(lang, src string) string {
	switch lang {
	case "julia":
		return html.EscapeString(src)
	case "ruby":
	default:
		lang = "go"
	}
	h, _ := rouge.Highlight(src, lang, "html") // fails only for an unknown lexer or formatter
	return h
}

// inlineSVG drops what a standalone SVG file starts with but an SVG inside
// HTML must not: the XML declaration and a DOCTYPE.
func inlineSVG(svg []byte) []byte {
	if i := bytes.Index(svg, []byte("<svg")); i > 0 {
		return svg[i:]
	}
	return svg
}

const page = `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>%s</title>
<style>
:root { --bg: #fdfdfc; --fg: #1d1f21; --muted: #6a6f75; --code: #f3f4f1; --line: #e2e3df; --err: #b3261e; --accent: #00758f;
  --hl-key: #8f2f8a; --hl-str: #2f7d32; --hl-num: #a3541a; --hl-com: #7a7f86; --hl-fn: #1f5fa8; --hl-type: #00758f; }
@media (prefers-color-scheme: dark) { :root { --bg: #16181a; --fg: #e6e6e3; --muted: #9a9fa5; --code: #202326; --line: #2e3236; --err: #f2b8b5; --accent: #5fc1d6;
  --hl-key: #d49bd0; --hl-str: #9ccc8f; --hl-num: #e0a671; --hl-com: #8a9098; --hl-fn: #8fb8ec; --hl-type: #5fc1d6; } }
body { margin: 0; background: var(--bg); color: var(--fg); font: 16px/1.6 system-ui, sans-serif; }
main { max-width: 52rem; margin: 0 auto; padding: 2rem 1rem 4rem; }
a { color: var(--accent); }
pre { overflow-x: auto; padding: .75rem 1rem; border-radius: 6px; font: 14px/1.5 ui-monospace, SFMono-Regular, Menlo, monospace; }
.cell { margin: 1.25rem 0; }
.code { background: var(--code); border: 1px solid var(--line); margin: 0; }
.code .k, .code .kd, .code .kn, .code .kr, .code .kc { color: var(--hl-key); }
.code .kt, .code .nc { color: var(--hl-type); }
.code .s, .code .s1, .code .s2, .code .sb, .code .sc, .code .se, .code .dl, .code .ss, .code .si, .code .sr { color: var(--hl-str); }
.code .no, .code .vi, .code .vg, .code .vc { color: var(--hl-type); }
.code .m, .code .mi, .code .mf, .code .mh, .code .mo, .code .mb { color: var(--hl-num); }
.code .c, .code .c1, .code .cm, .code .cp { color: var(--hl-com); font-style: italic; }
.code .nf, .code .nb, .code .bp { color: var(--hl-fn); }
.out { margin: .25rem 0 0; border-left: 3px solid var(--line); border-radius: 0; }
.err { margin: .25rem 0 0; color: var(--err); border-left: 3px solid var(--err); border-radius: 0; }
.display { margin: .5rem 0; overflow-x: auto; }
.display svg, .display img { max-width: 100%%; height: auto; }
.took, footer { color: var(--muted); font-size: 13px; }
table { border-collapse: collapse; font: 13px/1.4 ui-monospace, SFMono-Regular, Menlo, monospace; }
th, td { padding: .2rem .6rem; border-bottom: 1px solid var(--line); text-align: right; }
thead th, tbody th { color: var(--muted); font-weight: normal; }
caption { caption-side: top; text-align: left; color: var(--muted); padding-bottom: .25rem; }
.math svg { display: inline; fill: currentColor; }
.math.display { display: block; text-align: center; margin: 1rem 0; overflow-x: auto; }
.math-error { color: var(--err); }
</style></head><body><main>
`
