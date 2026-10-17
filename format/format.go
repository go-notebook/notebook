// Package format reads and writes notebooks.
//
// A notebook is a Markdown document. Its fenced code blocks whose info
// string's first word is "go" or "ruby" are code cells, in Go or in Ruby;
// everything between them is
// prose, kept byte for byte. GitHub and any Markdown viewer show a notebook as
// a document, diffs are diffs of text, and no output is ever stored in the
// file: results live in the kernel's value store and in exports.
//
//	# Spectrum
//
//	```go
//	x := ndarray.Linspace(0, 1, 1024)
//	```
//
// Fences follow CommonMark: three or more backticks or tildes, indented by at
// most three spaces, closed by a fence of the same character at least as
// long. A code block in another language is prose.
package format

import (
	"strings"
)

// Kind tells prose from code.
type Kind uint8

const (
	// Prose is Markdown text.
	Prose Kind = iota
	// Code is a code cell, in Go or in Ruby (see Lang).
	Code
)

// Cell is one cell of a notebook.
type Cell struct {
	Kind Kind

	// Source is the cell's text: the Markdown of a prose cell, the Go code of
	// a code cell (without its fences, ending in a newline unless empty).
	Source string

	// Line is the 1-based line of the document where Source starts.
	Line int

	// Info is a code cell's info string after its language (its options),
	// trimmed.
	Info string

	// Lang is a code cell's language, the first word of its info string:
	// "go" or "ruby".
	Lang string

	// open and close are a code cell's fence lines, newline included, kept
	// so that writing the notebook back reproduces it exactly.
	open, close string
}

// Notebook is a parsed notebook.
type Notebook struct {
	Cells []Cell
}

// Parse splits a Markdown document into cells. It never fails: any text is
// a notebook, perhaps of prose only. An unclosed go fence runs to the end of
// the document, as CommonMark says.
func Parse(src string) *Notebook {
	nb := &Notebook{}
	lines := splitLines(src)
	var prose strings.Builder
	proseLine := 1
	flush := func(next int) {
		if prose.Len() > 0 {
			nb.Cells = append(nb.Cells, Cell{Kind: Prose, Source: prose.String(), Line: proseLine})
			prose.Reset()
		}
		proseLine = next
	}
	for i := 0; i < len(lines); i++ {
		char, n, info, ok := openFence(lines[i])
		if !ok {
			prose.WriteString(lines[i])
			continue
		}
		// Find the closing fence; a block in another language stays prose.
		end := len(lines)
		for j := i + 1; j < len(lines); j++ {
			if closesFence(lines[j], char, n) {
				end = j
				break
			}
		}
		word, rest, _ := strings.Cut(strings.TrimSpace(info), " ")
		if word != "go" && word != "ruby" {
			for k := i; k <= end && k < len(lines); k++ {
				prose.WriteString(lines[k])
			}
			i = end
			continue
		}
		flush(i + 1)
		c := Cell{Kind: Code, Lang: word, Line: i + 2, Info: strings.TrimSpace(rest), open: lines[i]}
		c.Source = strings.Join(lines[i+1:end], "")
		if end < len(lines) {
			c.close = lines[end]
		}
		nb.Cells = append(nb.Cells, c)
		i = end
		proseLine = end + 2
	}
	flush(0)
	return nb
}

// String writes the notebook back as Markdown. For a parsed notebook it is
// the original text, byte for byte. A code cell made by NewCode gets a
// backtick fence long enough for its source.
func (nb *Notebook) String() string {
	var b strings.Builder
	for _, c := range nb.Cells {
		if c.Kind == Prose {
			b.WriteString(c.Source)
			continue
		}
		open, close := c.open, c.close
		if open == "" {
			fence := strings.Repeat("`", max(3, longestRun(c.Source, '`')+1))
			lang := c.Lang
			if lang == "" {
				lang = "go"
			}
			open = fence + strings.TrimRight(lang+" "+c.Info, " ") + "\n"
			close = fence + "\n"
		}
		b.WriteString(open)
		b.WriteString(c.Source)
		if c.Source != "" && !strings.HasSuffix(c.Source, "\n") && close != "" {
			b.WriteByte('\n')
		}
		b.WriteString(close)
	}
	return b.String()
}

// NewCode returns a Go code cell holding src, for building a notebook in
// code.
func NewCode(src string) Cell { return NewCodeIn("go", src) }

// NewCodeIn returns a code cell in the language lang ("go" or "ruby")
// holding src.
func NewCodeIn(lang, src string) Cell {
	if src != "" && !strings.HasSuffix(src, "\n") {
		src += "\n"
	}
	return Cell{Kind: Code, Lang: lang, Source: src}
}

// splitLines splits s after every newline, keeping the newlines; the last
// line has none if s does not end in one.
func splitLines(s string) []string {
	var out []string
	for s != "" {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			out = append(out, s)
			break
		}
		out = append(out, s[:i+1])
		s = s[i+1:]
	}
	return out
}

// openFence recognises an opening code fence: up to three spaces, then three
// or more backticks or tildes, then the info string (which may not contain a
// backtick after a backtick fence).
func openFence(line string) (char byte, n int, info string, ok bool) {
	s := strings.TrimRight(line, "\r\n")
	indent := len(s) - len(strings.TrimLeft(s, " "))
	if indent > 3 {
		return 0, 0, "", false
	}
	s = s[indent:]
	if s == "" || (s[0] != '`' && s[0] != '~') {
		return 0, 0, "", false
	}
	char = s[0]
	n = len(s) - len(strings.TrimLeft(s, string(char)))
	if n < 3 {
		return 0, 0, "", false
	}
	info = s[n:]
	if char == '`' && strings.IndexByte(info, '`') >= 0 {
		return 0, 0, "", false
	}
	return char, n, info, true
}

// closesFence reports whether line closes a fence of n chars: up to three
// spaces, at least n of them, then only spaces.
func closesFence(line string, char byte, n int) bool {
	s := strings.TrimRight(line, "\r\n")
	indent := len(s) - len(strings.TrimLeft(s, " "))
	if indent > 3 {
		return false
	}
	s = s[indent:]
	run := len(s) - len(strings.TrimLeft(s, string(char)))
	return run >= n && strings.TrimSpace(s[run:]) == ""
}

// longestRun returns the length of the longest run of c in s.
func longestRun(s string, c byte) int {
	best, cur := 0, 0
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			cur++
			best = max(best, cur)
		} else {
			cur = 0
		}
	}
	return best
}

// SetSource replaces a cell's source. When a line of the new source would
// close a code cell's fence, the fence is rebuilt when the notebook is
// written — backticks, longer than any run in the source, info kept — so the
// cell still reads back whole.
func (c *Cell) SetSource(src string) {
	c.Source = src
	if c.Kind != Code || c.open == "" {
		return
	}
	char, n, _, _ := openFence(c.open)
	for _, line := range splitLines(src) {
		if closesFence(line, char, n) {
			c.open, c.close = "", ""
			return
		}
	}
}
