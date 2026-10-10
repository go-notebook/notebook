package export

import (
	"bytes"
	"fmt"
	"html"
	"sync"

	texmath "github.com/go-tex/math"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Math in prose: $...$ inline and $$...$$ on its own, typeset by go-tex
// into SVG glyph outlines — real TeX layout from the OpenType MATH table,
// no MathJax, no script, no network. An inline formula sits on the text's
// baseline. A formula go-tex cannot typeset is shown as its source, marked.
//
// Dollar signs follow pandoc's rule: an opening $ is not followed by a
// space, a closing $ is not preceded by one nor followed by a digit, so
// "costs $20 and $30" stays text.

// mathSize is the size, in pixels, formulas are set at: the page's text.
const mathSize = 17

var (
	mathOnce     sync.Once
	mathRenderer *texmath.Renderer
)

// typeset renders tex, inline or in display style, as an HTML fragment.
func typeset(tex string, display bool) string {
	mathOnce.Do(func() {
		// The embedded font always parses.
		mathRenderer, _ = texmath.New(texmath.DefaultFont())
	})
	render := mathRenderer.RenderSVGMetrics
	if display {
		render = mathRenderer.RenderDisplaySVGMetrics
	}
	svg, m, err := render(tex, mathSize)
	if err != nil {
		return fmt.Sprintf(`<code class="math-error" title="%s">%s</code>`,
			html.EscapeString(err.Error()), html.EscapeString(tex))
	}
	if display {
		// A span shown as a block: a div may not sit in the paragraph
		// goldmark puts the formula in.
		return `<span class="math display">` + svg + `</span>`
	}
	return fmt.Sprintf(`<span class="math" style="vertical-align:-%.2fpx">%s</span>`, m.Depth, svg)
}

// mathNode is a formula in the document tree.
type mathNode struct {
	ast.BaseInline
	tex     string
	display bool
}

var kindMath = ast.NewNodeKind("Math")

func (n *mathNode) Kind() ast.NodeKind { return kindMath }

func (n *mathNode) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, map[string]string{"tex": n.tex}, nil)
}

// mathParser finds formulas.
type mathParser struct{}

func (mathParser) Trigger() []byte { return []byte{'$'} }

func (mathParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	line, _ := block.PeekLine()
	if len(line) > 1 && line[1] == '$' {
		return parseDisplay(block)
	}
	// Inline: $x$, the opening $ not followed by a space.
	if len(line) < 3 || line[1] == ' ' || line[1] == '\t' || line[1] == '\n' {
		return nil
	}
	for i := 2; i < len(line); i++ {
		if line[i] == '\n' {
			return nil
		}
		if line[i] != '$' || line[i-1] == ' ' || line[i-1] == '\\' {
			continue
		}
		if i+1 < len(line) && line[i+1] >= '0' && line[i+1] <= '9' {
			continue
		}
		block.Advance(i + 1)
		return &mathNode{tex: string(line[1:i])}
	}
	return nil
}

// parseDisplay reads $$...$$, which may span lines.
func parseDisplay(block text.Reader) ast.Node {
	line, _ := block.PeekLine()
	startLine, startSeg := block.Position()
	var tex bytes.Buffer
	rest := line[2:]
	for {
		if i := bytes.Index(rest, []byte("$$")); i >= 0 {
			tex.Write(rest[:i])
			// Past the closing $$ on the current line, wherever the
			// formula started.
			block.Advance(len(line) - len(rest) + i + 2)
			return &mathNode{tex: string(bytes.TrimSpace(tex.Bytes())), display: true}
		}
		tex.Write(rest)
		block.AdvanceLine()
		if line, _ = block.PeekLine(); line == nil {
			block.SetPosition(startLine, startSeg)
			return nil
		}
		rest = line
	}
}

// mathHTML writes formulas.
type mathHTML struct{}

func (mathHTML) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(kindMath, func(w util.BufWriter, _ []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			m := n.(*mathNode)
			_, _ = w.WriteString(typeset(m.tex, m.display))
		}
		return ast.WalkSkipChildren, nil
	})
}

// mathExtension adds formulas to goldmark.
type mathExtension struct{}

func (mathExtension) Extend(m goldmark.Markdown) {
	m.Parser().AddOptions(parser.WithInlineParsers(util.Prioritized(mathParser{}, 500)))
	m.Renderer().AddOptions(renderer.WithNodeRenderers(util.Prioritized(mathHTML{}, 500)))
}
