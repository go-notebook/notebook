package main

import (
	"testing"

	"github.com/go-notebook/notebook/format"
)

// insertAfter makes a cell of any registered language, a text cell for
// "prose", and a Go cell for anything else, hostile words included.
func TestInsertAfterLanguages(t *testing.T) {
	for kind, lang := range map[string]string{"go": "go", "ruby": "ruby", "julia": "julia", "nonesuch": "go", "": "go", "\x00```": "go"} {
		nb := &format.Notebook{}
		if err := insertAfter(nb, -1, kind); err != nil {
			t.Fatal(err)
		}
		if c := nb.Cells[0]; c.Kind != format.Code || c.Lang != lang {
			t.Errorf("kind %q: %+v, want a %s cell", kind, c, lang)
		}
	}
}
