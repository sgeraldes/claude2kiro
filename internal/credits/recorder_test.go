package credits

import (
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

var errTest = testErr("boom")

type testErr string

func (e testErr) Error() string { return string(e) }
