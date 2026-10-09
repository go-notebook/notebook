package kernel

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// entry is what the disk cache keeps for a cell key: its result and the
// encodings of the variables it made (nil for a volatile one).
type entry struct {
	Result Result            `json:"result"`
	Vars   map[string][]byte `json:"vars"`
}

// cachePath is where the entry for a cell key lives.
func (k *Kernel) cachePath(key string) string {
	return filepath.Join(k.opt.Dir, "cache", key[:2], key+".json")
}

// save writes a cell's entry. Failing to write is not an error: the cell will
// just run again next time.
func (k *Kernel) save(key string, res Result, names []string) {
	e := entry{Result: res, Vars: map[string][]byte{}}
	for _, n := range names {
		e.Vars[n] = k.values[n].data
	}
	b, _ := json.Marshal(e) // strings, durations and byte slices always marshal
	p := k.cachePath(key)
	if os.MkdirAll(filepath.Dir(p), 0o755) != nil {
		return
	}
	tmp := p + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, p)
	}
}

// load reads a cell's entry into memory, reporting whether there was one.
func (k *Kernel) load(key string) bool {
	if _, ok := k.results[key]; ok {
		return true
	}
	b, err := os.ReadFile(k.cachePath(key))
	if err != nil {
		return false
	}
	var e entry
	if json.Unmarshal(b, &e) != nil {
		return false
	}
	k.results[key] = e.Result
	for n, d := range e.Vars {
		if v, ok := k.values[n]; !ok || v.key != key {
			k.values[n] = value{data: d, key: key}
		}
	}
	return true
}
