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
	pathFn   func() string
	interval time.Duration
	maxAge   time.Duration
	read     func() Reading

	mu   sync.RWMutex
	path string // file the in-memory history belongs to
	snap []Snapshot

	startOnce sync.Once
}

// NewRecorder creates a Recorder that calls read every interval and keeps up to
// maxAge of history in the file at path. read returns the current credit figure
// (typically a wrapper over cmd.GetCreditsInfo).
func NewRecorder(path string, interval, maxAge time.Duration, read func() Reading) *Recorder {
	return NewRecorderFor(func() string { return path }, interval, maxAge, read)
}

// NewRecorderFor is NewRecorder with the file resolved on every sample. The
// proxy's history follows the identity whose credits it is reading (see the
// quota failover in the main package), so the file can change while the
// recorder runs; when it does, the in-memory history is reloaded from the new
// file so two identities' balances are never shown as one series.
func NewRecorderFor(pathFn func() string, interval, maxAge time.Duration, read func() Reading) *Recorder {
	return &Recorder{pathFn: pathFn, path: pathFn(), interval: interval, maxAge: maxAge, read: read}
}

// track points the recorder at the file for the current identity, reloading
// memory when the file changed since the last sample. Returns the path.
func (r *Recorder) track() string {
	p := r.pathFn()
	r.mu.Lock()
	changed := p != r.path
	r.path = p
	r.mu.Unlock()
	if changed {
		r.load(p)
	}
	return p
}

// currentPath is the file the recorder is bound to right now.
func (r *Recorder) currentPath() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.path
}

// Start loads existing history and launches the background sampling loop. It is
// idempotent: only the first call starts the loop. Best-effort — file errors
// are swallowed so sampling never breaks the proxy.
func (r *Recorder) Start() {
	if r == nil {
		return
	}
	r.startOnce.Do(func() {
		r.load(r.track())
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
	// Resolve the identity's file before reading and check it did not move
	// while the read was out: a reading of identity B must not be appended
	// to identity A's file.
	before := r.track()
	rd := r.read()
	if rd.Err != nil || rd.Limit <= 0 {
		return
	}
	if r.pathFn() != before {
		return
	}
	s := Snapshot{
		T:         time.Now().Unix(),
		Used:      rd.Used,
		Limit:     rd.Limit,
		Remaining: rd.Remaining,
		Plan:      rd.Plan,
	}

	// Everything below is bound to the file the reading was taken for. The
	// recorder may move to another identity's file at any moment (a History
	// call after a switch does that); a sample of A is written to A's file or
	// dropped, never to whichever file is current by then.
	r.appendLine(before, s)

	// Refresh memory from the file rather than only appending to our own
	// slice: several proxies (a persistent server plus `run` instances) may
	// sample to the same file, and re-reading makes each process's
	// /credits/history converge on the union of everyone's samples.
	if !r.reload(before) {
		// File unreadable: keep the sample in memory so History still works,
		// as long as memory still belongs to that file.
		r.mu.Lock()
		if r.path == before {
			r.snap = append(r.snap, s)
			r.prune()
		}
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
	// The view follows the identity the proxy is on right now, even between
	// samples: after a switch the previous identity's series is never shown.
	r.track()
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Snapshot, len(r.snap))
	copy(out, r.snap)
	return out
}

// load reads the JSONL file at path into memory (at startup and when the
// recorder moves to another file), pruning stale points. If pruning dropped
// lines, the file is compacted once. This is the only full rewrite:
// steady-state persistence is append-only (see appendLine), so concurrent
// proxies sampling to the same file can't clobber each other. Memory is only
// replaced while the recorder is still bound to path.
func (r *Recorder) load(path string) {
	snaps, ok := r.readFile(path)
	r.mu.Lock()
	if r.path != path {
		r.mu.Unlock()
		return
	}
	if !ok {
		// No file for this identity yet: an empty series, not the previous
		// identity's points.
		r.snap = nil
		r.mu.Unlock()
		return
	}
	r.snap = snaps
	r.prune()
	pruned := len(snaps) - len(r.snap)
	kept := make([]Snapshot, len(r.snap))
	copy(kept, r.snap)
	r.mu.Unlock()

	if pruned > 0 {
		r.rewrite(path, kept)
	}
}

// reload refreshes the in-memory history from the file at path without
// rewriting it, if the recorder is still bound to that file. Returns false
// when the file can't be read.
func (r *Recorder) reload(path string) bool {
	snaps, ok := r.readFile(path)
	if !ok {
		return false
	}
	r.mu.Lock()
	if r.path == path {
		r.snap = snaps
		r.prune()
	}
	r.mu.Unlock()
	return true
}

// readFile parses the JSONL file at path, skipping malformed lines, oldest first.
func (r *Recorder) readFile(path string) ([]Snapshot, bool) {
	f, err := os.Open(path)
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

// appendLine appends one snapshot as a JSONL line to the file at path.
// O_APPEND lands each line atomically, so multiple proxies writing to the same
// file interleave instead of overwriting each other (the previous whole-file
// rewrite dropped every sample the other process had recorded). Best-effort.
func (r *Recorder) appendLine(path string, s Snapshot) {
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return
	}
	data, err := json.Marshal(s)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(data, '\n'))
}

// rewrite atomically replaces the JSONL file at path with the given
// snapshots. Only used by load() to compact stale lines. Best-effort.
func (r *Recorder) rewrite(path string, snaps []Snapshot) {
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return
	}
	tmp := path + ".tmp"
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
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp) // don't leave the temp file behind (e.g. Windows rename contention)
	}
}
