package main

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-notebook/notebook/format"
	"github.com/go-notebook/notebook/kernel"
)

func TestLiveHandler(t *testing.T) {
	l := newLive(newEditor(filepath.Join(t.TempDir(), "nb.md")))
	srv := httptest.NewServer(l)
	defer srv.Close()

	get := func(path string) (int, string) {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := get("/"); code != 200 || !strings.Contains(body, "Running the notebook") {
		t.Errorf("before the first run: %d %q", code, body)
	}
	if code, _ := get("/nope"); code != 404 {
		t.Errorf("unknown path: %d", code)
	}
	l.set([]byte("<html><body>v1</body></html>"))
	code, body := get("/")
	if code != 200 || !strings.Contains(body, "v1") || !strings.Contains(body, `new EventSource("/events?v=1")`) {
		t.Errorf("page: %d %q", code, body)
	}

	// A browser behind by a version hears about it at once.
	if _, body := get("/events?v=0"); body != "data: 1\n\n" {
		t.Errorf("events behind: %q", body)
	}
	// A browser up to date waits for the next change.
	go func() {
		time.Sleep(50 * time.Millisecond)
		l.set([]byte("<html><body>v2</body></html>"))
	}()
	resp, err := http.Get(srv.URL + "/events?v=1")
	if err != nil {
		t.Fatal(err)
	}
	line, _ := bufio.NewReader(resp.Body).ReadString('\n')
	resp.Body.Close()
	if line != "data: 2\n" {
		t.Errorf("events after a change: %q", line)
	}
	// A browser that leaves ends its stream.
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/events?v=2", nil)
	done := make(chan struct{})
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_, _ = io.ReadAll(resp.Body)
			resp.Body.Close()
		}
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
}

func TestServe(t *testing.T) {
	path := notebook(t, good)
	k, err := newKernel(path, filepath.Join(t.TempDir(), "w"), "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	out := &syncBuffer{}
	stop := make(chan struct{})
	done := make(chan error)
	go func() { done <- serve(k, path, "127.0.0.1:0", 10*time.Millisecond, out, stop) }()
	waitFor(t, out, "2 of 2 cells ran")
	url := regexp.MustCompile(`http://\S+/`).FindString(out.String())
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "<h1>Demo</h1>") || !strings.Contains(string(body), "EventSource") ||
		!strings.Contains(string(body), "X-Notebook-Token") {
		t.Errorf("served page:\n%s", body)
	}
	// Editing from the page rewrites the notebook, and the watch loop runs
	// the change: the leaf cell shows 48 instead of 42.
	tok := regexp.MustCompile(`const token = "([0-9a-f]{32})"`).FindStringSubmatch(string(body))
	if tok == nil {
		t.Fatal("no token in the page")
	}
	req, _ := http.NewRequest("POST", url+"source/3", strings.NewReader("x * 8"))
	req.Header.Set("X-Notebook-Token", tok[1])
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("edit: %v %v", resp, err)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "```go\nx * 8\n```") {
		t.Errorf("notebook after edit:\n%s", b)
	}
	waitFor(t, out, "1 of 2 cells ran")
	// A notebook that cannot run at all is shown as an error page.
	t.Setenv("GOPROXY", "off")
	later := time.Now().Add(2 * time.Second)
	_ = os.WriteFile(path, []byte("```go\nimport \"example.invalid/x\"\nx.Y()\n```\n"), 0o644)
	_ = os.Chtimes(path, later, later)
	waitFor(t, out, "tidy")
	// the page is polled, not read once: it is published with the run's
	// result, and a read at one instant is a race with it
	deadline := time.Now().Add(30 * time.Second)
	for {
		resp, err = http.Get(url)
		if err != nil {
			t.Fatal(err)
		}
		body, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if strings.Contains(string(body), "go mod tidy") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no error page after 30 s:\n%s", body)
		}
		time.Sleep(20 * time.Millisecond)
	}
	close(stop)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := serve(k, path, "256.0.0.1:x", time.Millisecond, out, stop); err == nil {
		t.Error("a bad address was accepted")
	}
}

func TestServeVerb(t *testing.T) {
	path := notebook(t, good)
	stop := make(chan struct{})
	close(stop)
	seam(t, &stopWatch, (<-chan struct{})(stop))
	var out syncBuffer
	if err := run([]string{"serve", "-addr", "127.0.0.1:0", "-work", t.TempDir(), path}, &out, &out); err != nil || !strings.Contains(out.String(), "serving") {
		t.Errorf("serve verb: %v %q", err, out.String())
	}
}

// When a run fails before any cell, the page that says so is published BEFORE
// the log line that names the failure: whoever waits for the line (a person
// watching the terminal, a test) and then reloads must find that page. This
// checks the order itself, from the page callback, so it fails on any
// machine when the order is reversed: no race is waited for.
func TestWatchPublishesTheFailureBeforeLoggingIt(t *testing.T) {
	path := notebook(t, "```go\nimport \"example.invalid/x\"\nx.Y()\n```\n")
	t.Setenv("GOPROXY", "off")
	k, err := newKernel(path, filepath.Join(t.TempDir(), "w"), "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	out := &syncBuffer{}
	stop := make(chan struct{})
	var loggedAtPublish string
	published := false
	err = watch(k, path, time.Millisecond, out, stop, func(nb *format.Notebook, rep *kernel.Report) error {
		if nb == nil && !published {
			published, loggedAtPublish = true, out.String()
			close(stop)
		}
		return nil
	})
	if err != nil || !published {
		t.Fatalf("watch: %v, published %v", err, published)
	}
	if loggedAtPublish != "" {
		t.Errorf("the log line came before the page:\n%s", loggedAtPublish)
	}
	if !strings.Contains(out.String(), "go mod tidy") {
		t.Errorf("the failure is not logged:\n%s", out.String())
	}
}
