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

// live holds the page serve shows and tells browsers when it changes; edit
// changes the notebook from the page.
type live struct {
	edit    *editor
	mu      sync.Mutex
	page    []byte
	version int
	changed chan struct{} // closed and replaced on every change
}

func newLive(e *editor) *live {
	return &live{edit: e, page: []byte(waiting), changed: make(chan struct{})}
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

// editing is added to the served page: a tool bar on each cell (edit, add a
// Go, a Ruby or a text cell below, delete), double-click to edit, Ctrl/Cmd+Enter
// to save, Escape to cancel. Saving rewrites the notebook file, and the
// watch loop runs what changed and reloads the page.
const editing = `<style>
section[data-i] { position: relative; }
.nb-tools { position: absolute; top: -.6rem; right: 0; display: none; gap: .25rem; z-index: 1; }
section[data-i]:hover > .nb-tools, .nb-top .nb-tools { display: flex; }
.nb-top { position: relative; height: 1rem; } .nb-top .nb-tools { top: 0; }
.nb-tools button { font: 12px system-ui, sans-serif; padding: .1rem .45rem; border-radius: 4px;
  border: 1px solid var(--line); background: var(--bg); color: var(--muted); cursor: pointer; }
.nb-tools button:hover { color: var(--fg); }
.nb-editor { box-sizing: border-box; width: 100%%; margin: .25rem 0; padding: .75rem 1rem; border-radius: 6px;
  border: 1px solid var(--accent); background: var(--code); color: var(--fg); font: 14px/1.5 ui-monospace, SFMono-Regular, Menlo, monospace; }
</style><script>
(() => {
  const token = "%s";
  const post = (url, body) => fetch(url, {method: "POST", headers: {"X-Notebook-Token": token}, body});
  const tools = (i, withEdit) => {
    const bar = document.createElement("div");
    bar.className = "nb-tools";
    bar.innerHTML = (withEdit ? '<button data-act="edit" title="Edit (or double-click)">edit</button>' : "") +
      '<button data-act="code">+ go</button><button data-act="ruby">+ ruby</button><button data-act="prose">+ text</button>' +
      (withEdit ? '<button data-act="delete">delete</button>' : "");
    bar.addEventListener("click", async e => {
      const act = e.target.dataset.act;
      if (!act || act === "edit") return;
      if (act === "delete" && !confirm("Delete this cell?")) return;
      const url = act === "delete" ? "/delete/" + i : "/insert/" + i + "?kind=" + act;
      const r = await post(url, "");
      if (!r.ok) alert(await r.text());
    });
    return bar;
  };
  const main = document.querySelector("main");
  if (main) {
    const top = document.createElement("div");
    top.className = "nb-top";
    top.append(tools(-1, false));
    main.prepend(top);
  }
  document.querySelectorAll("section[data-i]").forEach(sec => {
    const i = sec.dataset.i;
    const bar = tools(i, true);
    sec.prepend(bar);
    const edit = async () => {
      if (sec.querySelector("textarea")) return;
      const src = await (await fetch("/source/" + i)).text();
      const ta = document.createElement("textarea");
      ta.className = "nb-editor";
      ta.value = src;
      ta.spellcheck = false;
      ta.rows = Math.max(3, src.split("\n").length + 1);
      sec.append(ta);
      ta.focus();
      ta.addEventListener("keydown", async e => {
        if ((e.ctrlKey || e.metaKey) && e.key === "Enter") {
          e.preventDefault();
          ta.disabled = true;
          const r = await post("/source/" + i, ta.value);
          if (!r.ok) { alert(await r.text()); ta.disabled = false; }
        } else if (e.key === "Escape") {
          ta.remove();
        }
      });
    };
    sec.addEventListener("dblclick", edit);
    bar.querySelector('[data-act="edit"]').addEventListener("click", edit);
  });
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
		script := fmt.Sprintf(reload, v) + fmt.Sprintf(editing, l.edit.token)
		_, _ = w.Write(bytes.Replace(page, []byte("</body>"), []byte(script+"</body>"), 1))
	case "/events":
		l.events(w, r)
	default:
		l.edit.ServeHTTP(w, r)
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
	l := newLive(newEditor(path))
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
