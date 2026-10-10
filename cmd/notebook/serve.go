package main

import (
	"bytes"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/go-notebook/notebook/export"
	"github.com/go-notebook/notebook/format"
	"github.com/go-notebook/notebook/kernel"
)

// live holds the page serve shows and tells browsers when it changes.
type live struct {
	mu      sync.Mutex
	page    []byte
	version int
	changed chan struct{} // closed and replaced on every change
}

func newLive() *live {
	return &live{page: []byte(waiting), changed: make(chan struct{})}
}

// set publishes a new page.
func (l *live) set(page []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.page = page
	l.version++
	close(l.changed)
	l.changed = make(chan struct{})
}

// current returns the page, its version, and a channel closed at the next
// change.
func (l *live) current() ([]byte, int, <-chan struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.page, l.version, l.changed
}

// reload is added to the served page: it reloads it when the notebook's
// results change, keeping the scroll position.
const reload = `<script>
(() => {
  const k = "nb-scroll:" + location.pathname;
  const y = sessionStorage.getItem(k);
  if (y !== null) window.scrollTo(0, +y);
  const es = new EventSource("/events?v=%d");
  es.onmessage = () => { sessionStorage.setItem(k, String(window.scrollY)); location.reload(); };
})();
</script>`

const waiting = "<!doctype html><title>notebook</title><p>Running the notebook…</p>"

// ServeHTTP serves the page at / and its change notifications at /events.
func (l *live) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/":
		page, v, _ := l.current()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(bytes.Replace(page, []byte("</body>"), []byte(fmt.Sprintf(reload, v)+"</body>"), 1))
	case "/events":
		l.events(w, r)
	default:
		http.NotFound(w, r)
	}
}

// events streams one message when the page moves past the version the
// browser has, then ends: the browser reloads and opens a new stream.
func (l *live) events(w http.ResponseWriter, r *http.Request) {
	seen, _ := strconv.Atoi(r.URL.Query().Get("v"))
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush() // the headers: a browser's EventSource opens on them
	}
	for {
		_, v, changed := l.current()
		if v != seen {
			fmt.Fprintf(w, "data: %d\n\n", v)
			if flusher != nil {
				flusher.Flush()
			}
			return
		}
		select {
		case <-changed:
		case <-r.Context().Done():
			return
		}
	}
}

// serve runs the notebook on every change and shows it at addr.
func serve(k *kernel.Kernel, path, addr string, interval time.Duration, w io.Writer, stop <-chan struct{}) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	l := newLive()
	srv := &http.Server{Handler: l, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	fmt.Fprintf(w, "serving %s at http://%s/\n", path, ln.Addr())
	title := filepath.Base(path)
	return watch(k, path, interval, w, stop, func(nb *format.Notebook, rep *kernel.Report) error {
		var b bytes.Buffer
		if nb == nil { // the run failed before any cell
			fmt.Fprintf(&b, "<!doctype html><title>%s</title><body><pre>%s</pre></body>",
				html.EscapeString(title), html.EscapeString(rep.Diags[0].Msg))
		} else {
			_ = export.HTML(&b, title, nb, rep) // a buffer does not fail
		}
		l.set(b.Bytes())
		return nil
	})
}
