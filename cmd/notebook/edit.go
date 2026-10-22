package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/go-notebook/notebook/format"
)

// editor lets the page served by `notebook serve` change the notebook: read
// a cell's source, replace it, insert a cell after it, delete it. Each edit
// rewrites the Markdown file — the notebook stays the only source of truth,
// so an editor open on the same file keeps working — and the watch loop
// runs what changed.
//
// Writing the notebook is running code, so the endpoints are guarded twice:
// every change must carry the session's random token in a header, which only
// the page served here knows (another site can neither read it nor send a
// custom header without a CORS preflight this server never grants), and the
// Host must be a loopback name, which defeats DNS rebinding.
type editor struct {
	path  string
	token string
	mu    sync.Mutex
}

// newEditor returns an editor for the notebook at path with a fresh token.
func newEditor(path string) *editor {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // never fails (crypto/rand panics instead)
	return &editor{path: path, token: hex.EncodeToString(b)}
}

// errEdit is a request the editor refuses.
var errEdit = errors.New("bad edit")

// loopback reports whether a Host header names this machine.
func loopback(host string) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	h = strings.Trim(h, "[]")
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// ServeHTTP answers GET /source/i, POST /source/i (body: the new source),
// POST /insert/i?kind=code|prose and POST /delete/i, i being the cell's
// index among all the notebook's cells.
func (e *editor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !loopback(r.Host) {
		http.Error(w, "forbidden host", http.StatusForbidden)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	i, err := strconv.Atoi(parts[1])
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodGet && parts[0] == "source" {
		src, err := e.source(i)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, src)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("X-Notebook-Token") != e.token {
		http.Error(w, "missing or wrong token", http.StatusForbidden)
		return
	}
	switch parts[0] {
	case "source":
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<24))
		err = e.edit(func(nb *format.Notebook) error { return setSource(nb, i, string(body)) })
	case "insert":
		kind := r.URL.Query().Get("kind")
		err = e.edit(func(nb *format.Notebook) error { return insertAfter(nb, i, kind) })
	case "delete":
		err = e.edit(func(nb *format.Notebook) error { return deleteCell(nb, i) })
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// source returns cell i's source.
func (e *editor) source(i int) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	nb, err := e.read()
	if err != nil {
		return "", err
	}
	if i < 0 || i >= len(nb.Cells) {
		return "", fmt.Errorf("%w: no cell %d", errEdit, i)
	}
	return nb.Cells[i].Source, nil
}

// edit applies f to the notebook and writes it back.
func (e *editor) edit(f func(*format.Notebook) error) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	nb, err := e.read()
	if err != nil {
		return err
	}
	if err := f(nb); err != nil {
		return err
	}
	return writeAtomic(e.path, []byte(nb.String()))
}

func (e *editor) read() (*format.Notebook, error) {
	b, err := os.ReadFile(e.path)
	if err != nil {
		return nil, err
	}
	return format.Parse(string(b)), nil
}

// setSource replaces cell i's source. A code cell's source ends in a
// newline so its closing fence stays on its own line; prose keeps the
// blank line that separates it from what follows.
func setSource(nb *format.Notebook, i int, src string) error {
	if i < 0 || i >= len(nb.Cells) {
		return fmt.Errorf("%w: no cell %d", errEdit, i)
	}
	c := &nb.Cells[i]
	switch {
	case c.Kind == format.Code && src != "" && !strings.HasSuffix(src, "\n"):
		src += "\n"
	case c.Kind == format.Prose && !strings.HasSuffix(src, "\n\n"):
		src = strings.TrimRight(src, "\n") + "\n\n"
	}
	c.SetSource(src) // a source that would close the fence gets a longer one
	return nil
}

// insertAfter adds an empty cell of the given kind — "prose", "ruby", or
// anything else for Go — after cell i (before the first cell when i is -1).
// A text cell inserted next to text joins it when the notebook is read back:
// only a code cell separates two text cells.
func insertAfter(nb *format.Notebook, i int, kind string) error {
	if i < -1 || i >= len(nb.Cells) {
		return fmt.Errorf("%w: no cell %d", errEdit, i)
	}
	c := format.NewCode("")
	switch kind {
	case "prose":
		c = format.Cell{Kind: format.Prose, Source: "\n"}
	case "ruby":
		c = format.NewCodeIn("ruby", "")
	}
	nb.Cells = append(nb.Cells[:i+1], append([]format.Cell{c}, nb.Cells[i+1:]...)...)
	return nil
}

// deleteCell removes cell i.
func deleteCell(nb *format.Notebook, i int) error {
	if i < 0 || i >= len(nb.Cells) {
		return fmt.Errorf("%w: no cell %d", errEdit, i)
	}
	nb.Cells = append(nb.Cells[:i], nb.Cells[i+1:]...)
	return nil
}

// writeAtomic replaces a file's contents in one step, so the watch loop
// never reads it half written. Edits are serialised by the editor's mutex,
// so one temporary name suffices.
func writeAtomic(path string, data []byte) error {
	tmp := path + ".notebook-tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
