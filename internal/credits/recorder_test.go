package credits

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecorderSampleAndHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	rd := Reading{Used: 100, Limit: 1000, Remaining: 900, Plan: "KIRO PRO"}
	r := NewRecorder(path, time.Hour, 24*time.Hour, func() Reading { return rd })

	r.sampleOnce()
	h := r.History()
	if len(h) != 1 {
		t.Fatalf("got %d snapshots, want 1", len(h))
	}
	if h[0].Used != 100 || h[0].Limit != 1000 || h[0].Plan != "KIRO PRO" {
		t.Errorf("snapshot fields wrong: %+v", h[0])
	}
	if h[0].T == 0 {
		t.Error("timestamp not set")
	}
}

func TestRecorderSkipsInvalidReadings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	var rd Reading
	r := NewRecorder(path, time.Hour, 24*time.Hour, func() Reading { return rd })

	rd = Reading{Err: errTest}
	r.sampleOnce()
	rd = Reading{Limit: 0} // no error but no real limit
	r.sampleOnce()
	if len(r.History()) != 0 {
		t.Errorf("expected invalid readings to be skipped, got %d", len(r.History()))
	}

	rd = Reading{Used: 5, Limit: 1000, Remaining: 995}
	r.sampleOnce()
	if len(r.History()) != 1 {
		t.Errorf("expected one valid snapshot, got %d", len(r.History()))
	}
}

func TestRecorderPersistAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	rd := Reading{Used: 50, Limit: 1000, Remaining: 950, Plan: "KIRO PRO"}
	r1 := NewRecorder(path, time.Hour, 24*time.Hour, func() Reading { return rd })
	r1.sampleOnce()
	r1.sampleOnce()

	// A fresh recorder over the same file should load the persisted points.
	r2 := NewRecorder(path, time.Hour, 24*time.Hour, func() Reading { return rd })
	r2.load()
	if len(r2.History()) != 2 {
		t.Errorf("reloaded %d snapshots, want 2", len(r2.History()))
	}
}

func TestRecorderPrunesOld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	r := NewRecorder(path, time.Hour, time.Hour, func() Reading { return Reading{} })

	now := time.Now().Unix()
	r.snap = []Snapshot{
		{T: now - 7200, Used: 1, Limit: 1000}, // 2h old -> pruned
		{T: now - 1800, Used: 2, Limit: 1000}, // 30m old -> kept
	}
	r.prune()
	if len(r.snap) != 1 || r.snap[0].Used != 2 {
		t.Errorf("prune kept wrong set: %+v", r.snap)
	}
}

func TestRecorderConcurrentWritersDoNotClobber(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	rd1 := Reading{Used: 10, Limit: 1000, Remaining: 990, Plan: "A"}
	rd2 := Reading{Used: 20, Limit: 1000, Remaining: 980, Plan: "B"}
	r1 := NewRecorder(path, time.Hour, 24*time.Hour, func() Reading { return rd1 })
	r2 := NewRecorder(path, time.Hour, 24*time.Hour, func() Reading { return rd2 })

	// Interleaved samples from two independent recorders over the same file —
	// the old whole-file rewrite made the last writer win; append-only must
	// keep everything, and each recorder's view converges on the union.
	r1.sampleOnce()
	r2.sampleOnce()
	r1.sampleOnce()

	if got := len(r1.History()); got != 3 {
		t.Fatalf("r1 sees %d snapshots, want 3 (union of both writers)", got)
	}
	r2.reload()
	if got := len(r2.History()); got != 3 {
		t.Fatalf("r2 sees %d snapshots after reload, want 3", got)
	}
}

func TestRecorderLoadCompactsStaleLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	old := NewRecorder(path, time.Hour, 0, func() Reading { return Reading{} })
	now := time.Now().Unix()
	old.appendLine(Snapshot{T: now - 7200, Used: 1, Limit: 1000})
	old.appendLine(Snapshot{T: now - 60, Used: 2, Limit: 1000})

	r := NewRecorder(path, time.Hour, time.Hour, func() Reading { return Reading{} })
	r.load()
	if got := len(r.History()); got != 1 {
		t.Fatalf("loaded %d snapshots, want 1 after pruning", got)
	}

	// The stale line must be compacted out of the file itself, not just memory.
	snaps, ok := r.readFile()
	if !ok {
		t.Fatal("readFile failed after compaction")
	}
	if len(snaps) != 1 || snaps[0].Used != 2 {
		t.Fatalf("file after compaction = %+v, want only the fresh snapshot", snaps)
	}
}

var errTest = testErr("boom")

type testErr string

func (e testErr) Error() string { return string(e) }

// The proxy's history follows the identity it is reading credits for: when
// the resolved file changes between samples, the in-memory history is
// reloaded from the new file and the new reading is appended there.
func TestRecorderFollowsTheResolvedFile(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "a.jsonl")
	rd := Reading{Used: 100, Limit: 1000, Remaining: 900, Plan: "A"}
	r := NewRecorderFor(func() string { return current }, time.Hour, 24*time.Hour, func() Reading { return rd })

	r.sampleOnce()
	if h := r.History(); len(h) != 1 || h[0].Plan != "A" {
		t.Fatalf("history on a: %+v", h)
	}

	current = filepath.Join(dir, "b.jsonl")
	rd = Reading{Used: 5, Limit: 1000, Remaining: 995, Plan: "B"}
	r.sampleOnce()
	h := r.History()
	if len(h) != 1 || h[0].Plan != "B" {
		t.Fatalf("history must be b's alone after the switch: %+v", h)
	}
	if _, err := os.Stat(filepath.Join(dir, "b.jsonl")); err != nil {
		t.Fatalf("b's file not written: %v", err)
	}
	a := NewRecorder(filepath.Join(dir, "a.jsonl"), time.Hour, 24*time.Hour, func() Reading { return Reading{} })
	a.load()
	if h := a.History(); len(h) != 1 || h[0].Plan != "A" {
		t.Fatalf("a's file must keep only a's sample: %+v", h)
	}
}

// N05: Start loads the persisted history before the first network read returns.
func TestRecorderStartLoadsPersistedHistoryBeforeFirstRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	seed := NewRecorder(path, time.Hour, 0, func() Reading { return Reading{} })
	seed.appendLine(Snapshot{T: time.Now().Unix(), Used: 7, Limit: 1000})

	release := make(chan struct{})
	r := NewRecorder(path, time.Hour, 24*time.Hour, func() Reading { <-release; return Reading{Err: os.ErrNotExist} })
	r.Start()
	got := r.History()
	close(release)
	if len(got) != 1 || got[0].Used != 7 {
		t.Fatalf("persisted history must be visible right after Start: %+v", got)
	}
}

// H09: after the resolved file moves to an identity with no history yet,
// History shows an empty series, never the previous identity's points.
func TestRecorderHistoryFollowsTheResolvedFileEvenWithoutSamples(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "a.jsonl")
	rd := Reading{Used: 100, Limit: 1000, Remaining: 900, Plan: "A"}
	r := NewRecorderFor(func() string { return current }, time.Hour, 24*time.Hour, func() Reading { return rd })
	r.sampleOnce()
	if h := r.History(); len(h) != 1 {
		t.Fatalf("a: %+v", h)
	}

	current = filepath.Join(dir, "b.jsonl") // no file yet
	if h := r.History(); len(h) != 0 {
		t.Fatalf("b has no history yet, got a's: %+v", h)
	}
	rd = Reading{Err: os.ErrNotExist}
	r.sampleOnce() // failed read: still nothing of a's
	if h := r.History(); len(h) != 0 {
		t.Fatalf("after a failed read b must still be empty: %+v", h)
	}
}
