package main

import (
	"bytes"
	"runtime/debug"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

// legacy rewrites the single-dash long flags of earlier releases and nothing
// else: every case names the arguments, what pflag must receive, and the
// note on stderr ("" for none).
func TestLegacy(t *testing.T) {
	for _, c := range []struct {
		name       string
		args, want []string
		note       string
	}{
		{"long flag", []string{"run", "-timeout", "5s", "x.md"},
			[]string{"run", "--timeout", "5s", "x.md"}, "read -timeout as --timeout"},
		{"long flag with =", []string{"run", "-timeout=5s", "x.md"},
			[]string{"run", "--timeout=5s", "x.md"}, "read -timeout as --timeout"},
		{"several, one note", []string{"run", "-work=w", "-runtime-src", "src", "-memory", "64", "x.md"},
			[]string{"run", "--work=w", "--runtime-src", "src", "--memory", "64", "x.md"},
			"read -work, -runtime-src, -memory as --work, --runtime-src, --memory"},
		{"negative value", []string{"run", "-memory", "-1", "x.md"},
			[]string{"run", "--memory", "-1", "x.md"}, "read -memory as --memory"},
		{"a value is not a flag", []string{"html", "-o", "-timeout", "x.md"},
			[]string{"html", "-o", "-timeout", "x.md"}, ""},
		{"a value after a long flag is not a flag", []string{"run", "--work", "-timeout", "x.md"},
			[]string{"run", "--work", "-timeout", "x.md"}, ""},
		{"a value after a rewritten flag is not a flag", []string{"run", "-work", "-memory", "x.md"},
			[]string{"run", "--work", "-memory", "x.md"}, "read -work as --work"},
		{"inline value: the next word is an argument", []string{"run", "--work=w", "-timeout", "1s", "x.md"},
			[]string{"run", "--work=w", "--timeout", "1s", "x.md"}, "read -timeout as --timeout"},
		{"shorthand with an attached value: the next word is a flag", []string{"html", "-oout.html", "-timeout", "1s", "x.md"},
			[]string{"html", "-oout.html", "--timeout", "1s", "x.md"}, "read -timeout as --timeout"},
		{"shorthand with its value apart", []string{"html", "-o", "out.html", "-timeout", "1s", "x.md"},
			[]string{"html", "-o", "out.html", "--timeout", "1s", "x.md"}, "read -timeout as --timeout"},
		{"unknown flag left to pflag", []string{"run", "-nope", "x.md"},
			[]string{"run", "-nope", "x.md"}, ""},
		{"flag of another verb left to pflag", []string{"run", "-addr", ":1", "x.md"},
			[]string{"run", "-addr", ":1", "x.md"}, ""},
		{"after --", []string{"run", "--", "-timeout"},
			[]string{"run", "--", "-timeout"}, ""},
		{"a lone dash", []string{"run", "-", "-t", "1s"},
			[]string{"run", "-", "-t", "1s"}, ""},
		{"trailing flag without its value", []string{"run", "x.md", "-timeout"},
			[]string{"run", "x.md", "--timeout"}, "read -timeout as --timeout"},
		{"serve flags", []string{"serve", "-addr", ":0", "-interval", "1s", "x.md"},
			[]string{"serve", "--addr", ":0", "--interval", "1s", "x.md"}, "read -addr, -interval as --addr, --interval"},
		{"watch flags", []string{"watch", "-output", "p.html", "-interval=1s", "x.md"},
			[]string{"watch", "--output", "p.html", "--interval=1s", "x.md"}, "read -output, -interval as --output, --interval"},
		{"help", []string{"run", "-help"}, []string{"run", "--help"}, "read -help as --help"},
		{"version", []string{"-version"}, []string{"--version"}, "read -version as --version"},
		{"unknown verb", []string{"dance", "-timeout", "1s"}, []string{"dance", "-timeout", "1s"}, ""},
		{"nothing", nil, []string{}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			var warn bytes.Buffer
			got := legacy(RootCmd(), c.args, &warn)
			if !slices.Equal(got, c.want) && !(len(got) == 0 && len(c.want) == 0) {
				t.Errorf("legacy(%q) = %q, want %q", c.args, got, c.want)
			}
			if c.note == "" && warn.Len() != 0 {
				t.Errorf("unexpected note: %q", warn.String())
			}
			if c.note != "" && !strings.Contains(warn.String(), c.note) {
				t.Errorf("note %q, want %q", warn.String(), c.note)
			}
		})
	}
}

// When Find fails the arguments go through untouched; cobra reports the
// error itself.
func TestLegacyFindError(t *testing.T) {
	root := RootCmd()
	root.Args = nil // cobra's own check: an unknown verb is an error of Find
	args := []string{"dance", "-timeout"}
	if got := legacy(root, args, &bytes.Buffer{}); !slices.Equal(got, args) {
		t.Errorf("got %q", got)
	}
}

// No long flag is spelled only with registered shorthand letters: if one
// were, -name would mean a cluster of shorthands to pflag and the long flag
// to earlier releases, and the rewrite would silently pick one.
func TestNoLongFlagIsACluster(t *testing.T) {
	root := RootCmd()
	for _, cmd := range append(root.Commands(), root) {
		cmd.InitDefaultHelpFlag()
		cmd.InitDefaultVersionFlag()
		flags := cmd.Flags()
		flags.VisitAll(func(f *pflag.Flag) {
			cluster := true
			for _, r := range f.Name {
				if flags.ShorthandLookup(string(r)) == nil {
					cluster = false
				}
			}
			if cluster {
				t.Errorf("%s: --%s is also a cluster of shorthands", cmd.Name(), f.Name)
			}
		})
	}
}

// Each verb takes exactly the flags it reads, with POSIX long and short
// spellings.
func TestFlagsPerVerb(t *testing.T) {
	want := map[string][]string{
		"run":   {"memory/m", "runtime-src/", "timeout/t", "work/w"},
		"html":  {"memory/m", "output/o", "runtime-src/", "timeout/t", "work/w"},
		"watch": {"interval/", "memory/m", "output/o", "runtime-src/", "timeout/t", "work/w"},
		"serve": {"addr/", "interval/", "memory/m", "runtime-src/", "timeout/t", "work/w"},
	}
	root := RootCmd()
	if len(root.Commands()) != len(want) {
		t.Fatalf("%d verbs, want %d", len(root.Commands()), len(want))
	}
	for _, cmd := range root.Commands() {
		var got []string
		cmd.Flags().VisitAll(func(f *pflag.Flag) { got = append(got, f.Name+"/"+f.Shorthand) })
		if !slices.Equal(got, want[cmd.Name()]) {
			t.Errorf("%s: flags %q, want %q", cmd.Name(), got, want[cmd.Name()])
		}
	}
}

// Errors of the command line, before anything runs.
func TestCommandLineErrors(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{nil, "missing command"},
		{[]string{"dance", "x.md"}, `unknown command "dance"`},
		{[]string{"run"}, "run takes one notebook file, got 0"},
		{[]string{"html", "a.md", "b.md"}, "html takes one notebook file, got 2"},
		{[]string{"run", "--addr", ":1", "x.md"}, "unknown flag: --addr"},
		{[]string{"run", "-o", "x.html", "x.md"}, "unknown shorthand flag: 'o'"},
		{[]string{"serve", "--output", "x.html", "x.md"}, "unknown flag: --output"},
		{[]string{"run", "-nope", "x.md"}, "unknown shorthand flag: 'n'"},
		{[]string{"run", "--timeout", "soon", "x.md"}, `invalid argument "soon"`},
		{[]string{"run", "-memory", "lots", "x.md"}, `invalid argument "lots"`},
		{[]string{"run", "--timeout"}, "flag needs an argument"},
	} {
		var out, errs bytes.Buffer
		err := run(c.args, &out, &errs)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: got %v, want an error containing %q", c.args, err, c.want)
		}
		if strings.Contains(out.String(), "Usage:") {
			t.Errorf("%q: usage printed on an error", c.args)
		}
	}
}

func TestHelp(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"--help"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"run", "html", "watch", "serve", "notebook serve --addr"} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("notebook --help lacks %q:\n%s", s, out.String())
		}
	}
	out.Reset()
	if err := run([]string{"serve", "-h"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"--addr string", "--interval duration", "-t, --timeout duration", "-m, --memory int", "-w, --work string"} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("notebook serve -h lacks %q:\n%s", s, out.String())
		}
	}
}

func TestVersion(t *testing.T) {
	info := func(v string, ok bool) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) { return &debug.BuildInfo{Main: debug.Module{Version: v}}, ok }
	}
	for _, c := range []struct {
		v    string
		ok   bool
		want string
	}{
		{"v0.4.0", true, "notebook version v0.4.0\n"},
		{"", true, "notebook version (devel)\n"},
		{"v9.9.9", false, "notebook version (devel)\n"},
	} {
		seam(t, &readBuildInfo, info(c.v, c.ok))
		var out bytes.Buffer
		if err := run([]string{"--version"}, &out, &out); err != nil || out.String() != c.want {
			t.Errorf("%q %v: --version = %q, %v; want %q", c.v, c.ok, out.String(), err, c.want)
		}
	}
}
