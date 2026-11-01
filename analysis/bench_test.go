package analysis

import (
	"fmt"
	"testing"
)

// hundredCells is a 100-cell notebook: Go, Ruby and Julia cells that read
// each other's variables.
func hundredCells() []Source {
	srcs := make([]Source, 100)
	for i := range srcs {
		switch i % 4 {
		case 0, 1:
			srcs[i] = Source{Lang: Go, Source: fmt.Sprintf("import \"math\"\nx%d := math.Sqrt(float64(%d))\nx%d * 2\n", i, i, i)}
		case 2:
			srcs[i] = Source{Lang: Ruby, Source: fmt.Sprintf("r%d = %d * 2\nr%d + 1\n", i, i, i)}
		default:
			srcs[i] = Source{Lang: Julia, Source: fmt.Sprintf("j%d = %d + 1\n", i, i)}
		}
		if i >= 4 && srcs[i].Lang == Ruby {
			srcs[i].Source = fmt.Sprintf("r%d = r%d + 1\n", i, i-4)
		}
	}
	return srcs
}

func BenchmarkAnalyzeSources100(b *testing.B) {
	srcs := hundredCells()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		AnalyzeSources(srcs)
	}
}
