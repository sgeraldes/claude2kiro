// Package credits records periodic snapshots of Kiro credit usage to a JSON
// Lines file so the web dashboard can chart usage over time, burn rate, and
// projected runout. The proxy already exposes the current point-in-time figure
// at /credits; this adds the time dimension, persisted across restarts.
package credits

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Snapshot is one recorded point of credit usage.
type Snapshot struct {
	T         int64   `json:"t"`         // unix seconds
	Used      float64 `json:"used"`      //
	Limit     float64 `json:"limit"`     //
	Remaining float64 `json:"remaining"` //
	Plan      string  `json:"plan"`      //
}

// Reading is the live credit figure supplied by the caller's snapshot function.
type Reading struct {
	Used      float64
	Limit     float64
	Remaining float64
	Plan      string
	Err       error
}

// Recorder periodically appends credit snapshots to a JSONL file and serves the
// recent history back. It is safe for concurrent use.
type Recorder struct {
	path     string
	interval time.Duration
	maxAge   time.Duration
	read     func() Reading

	mu   sync.RWMutex
	snap []Snapshot

	startOnce sync.Once
}

// NewRecorder creates a Recorder that calls read every interval and keeps up to
// maxAge of history in the file at path. read returns the current credit figure
// (typically a wrapper over cmd.GetCreditsInfo).
func NewRecorder(path string, interval, maxAge time.Duration, read func() Reading) *Recorder {
	return &Recorder{path: path, interval: interval, maxAge: maxAge, read: read}
}

// Start loads existing history and launches the background sampling loop. It is
// idempotent: only the first call starts the loop. Best-effort — file errors
// are swallowed so sampling never breaks the proxy.
func (r *Recorder) Start() {
	if r == nil {
		return
	}
	r.startOnce.Do(func() {
		r.load()
		// Sample off the calling goroutine: read() is a network call, and Start
		// runs inline in buildServerMux (TUI/server/run), so a slow Kiro API must
		// not delay server startup. Loaded file history already fills the chart.
		go func() {
			r.sampleOnce()
			r.loop()
		}()
	})
}

func (r *Recorder) loop() {
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for range t.C {
		r.sampleOnce()
	}
}

// sampleOnce records one reading if it's valid (no error and a real limit).
func (r *Recorder) sampleOnce() {
	rd := r.read()
	if rd.Err != nil || rd.Limit <= 0 {
		return
	}
	s := Snapshot{
		T:         time.Now().Unix(),
		Used:      rd.Used,
		Limit:     rd.Limit,
		Remaining: rd.Remaining,
		Plan:      rd.Plan,
	}

	r.appendLine(s)

	// Refresh memory from the file rather than only appending to our own
	// slice: several proxies (a persistent server plus `run` instances) may
	// sample to the same file, and re-reading makes each process's
	// /credits/history converge on the union of everyone's samples.
	if !r.reload() {
		// File unreadable — keep the sample in memory so History still works.
		r.mu.Lock()
		r.snap = append(r.snap, s)
		r.prune()
		r.mu.Unlock()
	}
}

// prune drops snapshots older than maxAge. Caller holds the write lock.
func (r *Recorder) prune() {
	if r.maxAge <= 0 {
		return
	}
	cutoff := time.Now().Add(-r.maxAge).Unix()
	i := 0
	for i < len(r.snap) && r.snap[i].T < cutoff {
		i++
	}
	if i > 0 {
		r.snap = append([]Snapshot(nil), r.snap[i:]...)
	}
}

// History returns a copy of the recorded snapshots, oldest first.
func (r *Recorder) History() []Snapshot {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Snapshot, len(r.snap))
	copy(out, r.snap)
	return out
}

// load reads the JSONL file into memory at startup, pruning stale points. If
// pruning dropped lines, the file is compacted once. This is the only full
// rewrite: steady-state persistence is append-only (see appendLine), so
// concurrent proxies sampling to the same file can't clobber each other.
func (r *Recorder) load() {
	snaps, ok := r.readFile()
	if !ok {
		return
	}

	r.mu.Lock()
	r.snap = snaps
	r.prune()
	pruned := len(snaps) - len(r.snap)
	kept := make([]Snapshot, len(r.snap))
	copy(kept, r.snap)
	r.mu.Unlock()

	if pruned > 0 {
		r.rewrite(kept)
	}
}

// reload refreshes the in-memory history from the file without rewriting it.
// Returns false when the file can't be read.
func (r *Recorder) reload() bool {
	snaps, ok := r.readFile()
	if !ok {
		return false
	}
	r.mu.Lock()
	r.snap = snaps
	r.prune()
	r.mu.Unlock()
	return true
}

// readFile parses the JSONL file, skipping malformed lines, oldest first.
func (r *Recorder) readFile() ([]Snapshot, bool) {
	f, err := os.Open(r.path)
	if err != nil {
		return nil, false
	}
	defer f.Close()

	var snaps []Snapshot
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var s Snapshot
		if json.Unmarshal(line, &s) == nil && s.T > 0 {
			snaps = append(snaps, s)
		}
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].T < snaps[j].T })
	return snaps, true
}

// appendLine appends one snapshot as a JSONL line. O_APPEND lands each line
// atomically, so multiple proxies writing to the same file interleave instead
// of overwriting each other (the previous whole-file rewrite dropped every
// sample the other process had recorded). Best-effort.
func (r *Recorder) appendLine(s Snapshot) {
	if r.path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0755); err != nil {
		return
	}
	data, err := json.Marshal(s)
	if err != nil {
		return
	}
	f, err := os.OpenFile(r.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(data, '\n'))
}

// rewrite atomically replaces the JSONL file with the given snapshots. Only
// used by load() to compact stale lines at startup. Best-effort.
func (r *Recorder) rewrite(snaps []Snapshot) {
	if r.path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0755); err != nil {
		return
	}
	tmp := r.path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return
	}
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for _, s := range snaps {
		if enc.Encode(s) != nil {
			break
		}
	}
	w.Flush()
	f.Close()
	if err := os.Rename(tmp, r.path); err != nil {
		os.Remove(tmp) // don't leave the temp file behind (e.g. Windows rename contention)
	}
}
