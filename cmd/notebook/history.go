package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	irb "github.com/go-ruby-irb/irb"
	"github.com/go-ruby-reline/reline"
)

// maxHistoryFile bounds what is read of a history file: one larger than this
// is not a history notebook irb wrote, and is left alone.
const maxHistoryFile = 8 << 20

// history is the session's history file: the inputs, never their values,
// in irb's format (a multi-line input's lines joined by a backslash), the
// last size of them, readable by its owner only.
type history struct {
	path    string // "" without a history file
	size    int
	entries []string // read at the start
	added   []string // typed since
}

// userConfigDir is os.UserConfigDir, a seam for the tests.
var userConfigDir = os.UserConfigDir

// openHistory reads the history file; --history-size 0, or no config
// directory to keep it in, means none.
func (o *irbOptions) openHistory() (*history, error) {
	h := &history{path: o.history, size: o.historySize}
	if h.size == 0 {
		h.path = ""
		return h, nil
	}
	if h.path == "" {
		dir, err := userConfigDir()
		if err != nil {
			h.path = ""
			return h, nil
		}
		h.path = filepath.Join(dir, "go-notebook", "irb_history")
	}
	entries, err := readHistory(h.path)
	if err != nil {
		return nil, err
	}
	h.entries = irb.TrimHistory(entries, h.size)
	return h, nil
}

// readHistory reads the entries of a history file; a missing file has none.
func readHistory(path string) ([]string, error) {
	var b []byte
	f, err := os.Open(path)
	if err == nil {
		b, err = io.ReadAll(io.LimitReader(f, maxHistoryFile+1))
		_ = f.Close()
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(b) > maxHistoryFile {
		return nil, nil
	}
	text := strings.TrimSuffix(string(b), "\n")
	if text == "" {
		return nil, nil
	}
	return irb.DecodeHistoryLines(strings.Split(text, "\n"), true), nil
}

// load gives the line editor the entries read.
func (h *history) load(to *reline.History) {
	to.Push(h.entries...)
}

// add records an input that is not blank.
func (h *history) add(src string) {
	if h.path != "" && strings.TrimSpace(src) != "" {
		h.added = append(h.added, src)
	}
}

// save writes the history: what the file holds now — another session may
// have saved since this one read it — then the inputs of this session, the
// last size of them, through a file of mode 0600 renamed over the old one.
func (h *history) save() error {
	if h.path == "" || len(h.added) == 0 {
		return nil
	}
	now, err := readHistory(h.path)
	if err != nil {
		return err
	}
	entries := irb.TrimHistory(append(now, h.added...), h.size)
	dir := filepath.Dir(h.path)
	if err := mkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := createTemp(dir, ".irb_history-*") // mode 0600, which WriteFile keeps
	if err != nil {
		return err
	}
	_ = tmp.Close()
	defer os.Remove(tmp.Name()) // after a successful rename there is nothing to remove
	if err := writeHistory(tmp.Name(), []byte(strings.Join(irb.EncodeHistoryLines(entries), "\n")+"\n"), 0o600); err != nil {
		return err
	}
	return renameHistory(tmp.Name(), h.path)
}

// The file calls of save, seams for the tests.
var (
	mkdirAll      = os.MkdirAll
	createTemp    = os.CreateTemp
	writeHistory  = os.WriteFile
	renameHistory = os.Rename
)
