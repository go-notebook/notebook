package parsers

import (
	"strings"
	"testing"
)

// cCorpus are C cells of the shapes notebooks hold.
var cCorpus = []string{
	"int a = 1;\nint b;",
	"typedef struct { int x, y; } point;",
	"struct s { int a; };\nenum e { A, B = 3 };",
	"#define N 3\n#define SQ(x) ((x)*(x))",
	"#include <stdio.h>\nint main(void) { printf(\"%d\\n\", 1); return 0; }",
	"static int f(int a, int b) { return a < b ? a : b; }",
	"int xs[] = {1, 2, 3};\nint n = sizeof xs / sizeof xs[0];",
	"for (int i = 0; i < 3; i++) { }",
	"if (a) b = 1; else b = 2;",
	"do { a++; } while (a < 3);",
	"const char *s = \"a\" \"b\";",
	"union u { int i; float f; } v;",
	"int (*fp)(int) = 0;",
	"// a comment\nint z;",
}

// BenchmarkAnalyzeCCell is the whole analysis of a cell of ordinary shape.
func BenchmarkAnalyzeCCell(b *testing.B) {
	src := strings.Join(cCorpus[:7], "\n")
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for b.Loop() {
		analyzeAlone(0, src)
	}
}

// FuzzAnalyzeCCell: any text is analysed without a panic, the front end's or
// ours, and an error's offset is inside the cell.
func FuzzAnalyzeCCell(f *testing.F) {
	for _, s := range cCorpus {
		f.Add(s)
	}
	for _, s := range []string{"int a = (1,", "struct {", "#define", "#include <", "\"unterminated", "'\\q'", "/* open", "int a\nint b;", "\t\tx y", "é é", "#define A(x) A(A(x))\nint a = A(1);"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		c := analyzeAlone(0, src)
		for _, e := range c.Errors {
			if strings.Contains(e.Msg, "C parser failed") {
				t.Fatalf("%q: the front end panicked", src)
			}
			if e.Pos < 0 || e.Pos > len(src) {
				t.Fatalf("%q: offset %d outside the cell", src, e.Pos)
			}
		}
		if len(c.Errors) > 0 && len(c.Defines) != 0 {
			t.Fatalf("%q: a cell with errors defines %v", src, c.Defines)
		}
	})
}
