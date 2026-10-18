package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-notebook/notebook/format"
)

const doc = "# T\n\n```go\nx := 1\n```\n"

// editServer serves an editor on a fresh notebook.
func editServer(t *testing.T) (*editor, *httptest.Server, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nb.md")
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	e := newEditor(path)
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	return e, srv, path
}

func do(t *testing.T, method, url, token, host, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	if token != "" {
		req.Header.Set("X-Notebook-Token", token)
	}
	if host != "" {
		req.Host = host
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestEditor(t *testing.T) {
	e, srv, path := editServer(t)
	file := func() string { b, _ := os.ReadFile(path); return string(b) }

	if code, body := do(t, "GET", srv.URL+"/source/1", "", "", ""); code != 200 || body != "x := 1\n" {
		t.Errorf("GET source = %d %q", code, body)
	}
	for name, c := range map[string]struct {
		method, path, token, host string
		want                      int
	}{
		"no token":      {"POST", "/source/1", "", "", http.StatusForbidden},
		"wrong token":   {"POST", "/source/1", "nope", "", http.StatusForbidden},
		"foreign host":  {"GET", "/source/1", "", "evil.example:8888", http.StatusForbidden},
		"method":        {"PUT", "/source/1", e.token, "", http.StatusMethodNotAllowed},
		"bad index":     {"GET", "/source/x", "", "", http.StatusNotFound},
		"no such cell":  {"GET", "/source/9", "", "", http.StatusNotFound},
		"deep path":     {"GET", "/source/1/2", "", "", http.StatusNotFound},
		"unknown verb":  {"POST", "/frobnicate/1", e.token, "", http.StatusNotFound},
		"edit missing":  {"POST", "/source/9", e.token, "", http.StatusBadRequest},
		"insert range":  {"POST", "/insert/9", e.token, "", http.StatusBadRequest},
		"delete range":  {"POST", "/delete/9", e.token, "", http.StatusBadRequest},
		"localhost ok":  {"GET", "/source/1", "", "localhost:1", http.StatusOK},
		"ipv6 loopback": {"GET", "/source/1", "", "[::1]:1", http.StatusOK},
		"bare host":     {"GET", "/source/1", "", "localhost", http.StatusOK},
	} {
		if code, _ := do(t, c.method, srv.URL+c.path, c.token, c.host, ""); code != c.want {
			t.Errorf("%s: %d, want %d", name, code, c.want)
		}
	}
	if file() != doc {
		t.Fatalf("a refused request changed the notebook:\n%s", file())
	}

	// Edit a code cell: the newline is added, the fences stay.
	if code, _ := do(t, "POST", srv.URL+"/source/1", e.token, "", "x := 2"); code != http.StatusNoContent {
		t.Fatal(code)
	}
	if file() != "# T\n\n```go\nx := 2\n```\n" {
		t.Errorf("after edit:\n%q", file())
	}
	// Edit prose: it keeps a blank line before what follows.
	do(t, "POST", srv.URL+"/source/0", e.token, "", "# Title")
	if !strings.HasPrefix(file(), "# Title\n\n```go") {
		t.Errorf("after prose edit:\n%q", file())
	}
	// Insert a code cell after the first code cell and a text cell after
	// it, then delete the code cell inserted.
	do(t, "POST", srv.URL+"/insert/1", e.token, "", "")
	do(t, "POST", srv.URL+"/insert/2?kind=prose", e.token, "", "")
	nb := format.Parse(file())
	if len(nb.Cells) != 4 || nb.Cells[2].Kind != format.Code || nb.Cells[2].Source != "" || nb.Cells[3].Kind != format.Prose {
		t.Fatalf("after inserts: %+v", nb.Cells)
	}
	do(t, "POST", srv.URL+"/delete/2", e.token, "", "")
	// A Ruby cell, written with its own fence; then gone again.
	do(t, "POST", srv.URL+"/insert/1?kind=ruby", e.token, "", "")
	if nb := format.Parse(file()); len(nb.Cells) != 4 || nb.Cells[2].Lang != "ruby" || !strings.Contains(file(), "```ruby\n```\n") {
		t.Fatalf("after a Ruby insert: %+v\n%s", nb.Cells, file())
	}
	do(t, "POST", srv.URL+"/delete/2", e.token, "", "")
	if nb := format.Parse(file()); len(nb.Cells) != 3 || nb.Cells[1].Source != "x := 2\n" || nb.Cells[2].Kind != format.Prose {
		t.Errorf("after delete: %+v", nb.Cells)
	}
	// A text cell inserted next to text joins it when the file is read
	// back: only a code cell separates two of them.
	do(t, "POST", srv.URL+"/insert/-1?kind=prose", e.token, "", "")
	if nb := format.Parse(file()); len(nb.Cells) != 3 || nb.Cells[0].Kind != format.Prose {
		t.Errorf("text next to text: %+v", nb.Cells)
	}
}

func TestEditorFileErrors(t *testing.T) {
	e := newEditor(filepath.Join(t.TempDir(), "missing.md"))
	if _, err := e.source(0); err == nil {
		t.Error("source of a missing file")
	}
	if err := e.edit(func(*format.Notebook) error { return nil }); err == nil {
		t.Error("edit of a missing file")
	}
	if err := writeAtomic(filepath.Join(t.TempDir(), "no", "dir", "f"), nil); err == nil {
		t.Error("write into a missing directory")
	}
	if err := deleteCell(&format.Notebook{}, -1); err == nil {
		t.Error("delete of cell -1")
	}
	if loopback("example.com") || !loopback("127.0.0.1:80") {
		t.Error("loopback")
	}
}
