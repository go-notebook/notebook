package parsers

import (
	"strings"
	"testing"
)

// fortranCorpus are Fortran cells of the shapes notebooks hold.
var fortranCorpus = []string{
	"integer :: a = 1\nreal(8) :: b",
	"integer, parameter :: n = 3",
	"a = 1\nprint *, a",
	"subroutine f(x)\n  integer :: x\n  x = x + 1\nend subroutine",
	"function g(x) result(r)\n  integer :: x, r\n  r = x\nend function",
	"type :: point\n  real :: x, y\nend type",
	"module m\n  integer :: v = 1\ncontains\n  subroutine s()\n  end subroutine\nend module",
	"do i = 1, 3\n  print *, i\nend do",
	"if (a > 1) then\n  b = 2\nelse\n  b = 3\nend if",
	"real, allocatable :: xs(:)\nallocate(xs(10))",
	"character(len=5) :: s = 'hello'",
	"! a comment\ninteger :: z",
	"integer :: a = 1 &\n  + 2",
	"INTEGER :: Mixed\nmixed = 1",
}

// FuzzAnalyzeFortranCell: any text is analysed, alone and after a cell, without
// a panic, the front end's or ours, and an error's offset is inside the cell.
func FuzzAnalyzeFortranCell(f *testing.F) {
	for _, s := range fortranCorpus {
		f.Add(s)
	}
	for _, s := range []string{"integer :: a = (1,", "type :: t", "module", "'unterminated", "include 'x'", "integer :: a\ninteger :: b = ;", "é é", "if (", "do\n", "x = " + strings.Repeat("(", 300), "&\n&", "subroutine s\nsubroutine t", "use nothere", "implicit none\nimplicit real (a-z)"} {
		f.Add(s)
	}
	_, prev := analyzeFortranCell(0, "integer :: n = 3\ntype :: p\n  integer :: i\nend type\nsubroutine sub()\nend subroutine", nil)
	f.Fuzz(func(t *testing.T, src string) {
		for _, state := range []any{nil, prev} {
			c, next := analyzeFortranCell(1, src, state)
			for _, e := range c.Errors {
				if strings.Contains(e.Msg, "Fortran parser failed") {
					t.Fatalf("%q: the front end panicked", src)
				}
				if e.Pos < 0 || e.Pos > len(src) {
					t.Fatalf("%q: offset %d outside the cell", src, e.Pos)
				}
			}
			if len(c.Errors) > 0 && (len(c.Defines) != 0 || next != state) {
				t.Fatalf("%q: a cell with errors defines %v or moves the state", src, c.Defines)
			}
		}
	})
}
