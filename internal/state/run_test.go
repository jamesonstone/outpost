package state

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/jamesonstone/outpost/internal/core"
)

func TestBeginRunSkipsMissingHistoryAndPreservesRecordedRetries(t *testing.T) {
	p := policy()
	p.NewStatePolicy = "from_startup"
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := OpenAt(path, p, epoch)
	if err != nil {
		t.Fatal(err)
	}
	enqueue(t, s)
	first, ok, err := s.Claim(epoch, time.Minute)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err := s.Finish(first.Key, first.Token, core.Receipt{}, &core.SendError{Category: "timeout", Ambiguous: true}, epoch.Add(time.Second), time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyPage(nil, core.Scan{Upper: epoch.Add(time.Minute), Cursor: core.Cursor{PlacedAt: epoch, ID: "event-a"}, LastCompleted: epoch}, epoch); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if err := s.Backup(snapshot); err != nil {
		t.Fatal(err)
	}
	s.Close()
	// Reopening a snapshot for inspection preserves its old cutoff and checkpoint.
	later := epoch.Add(time.Hour)
	restored, err := OpenAt(snapshot, p, later)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	cutoff, err := restored.EffectiveActivation()
	if err != nil || !cutoff.Equal(epoch) {
		t.Fatalf("inspection advanced floor: %v %v", cutoff, err)
	}
	scan, err := restored.Scan()
	if err != nil || scan.Cursor.ID != "event-a" {
		t.Fatalf("inspection reset scan: %+v %v", scan, err)
	}
	if err := restored.BeginRun(later); err != nil {
		t.Fatal(err)
	}
	cutoff, err = restored.EffectiveActivation()
	if err != nil || !cutoff.Equal(later) {
		t.Fatalf("run floor: %v %v", cutoff, err)
	}
	scan, err = restored.Scan()
	if err != nil || !scan.Upper.IsZero() || scan.Cursor.ID != "" || !scan.LastCompleted.IsZero() {
		t.Fatalf("run kept stale coverage: %+v %v", scan, err)
	}
	missing := event()
	missing.ID, missing.OrderID, missing.PlacedAt = "missing", "missing", epoch.Add(time.Minute)
	current := event()
	current.ID, current.OrderID, current.PlacedAt = "current", "current", later
	if err := restored.ApplyPage([]core.Event{missing, current}, core.Scan{}, later); err != nil {
		t.Fatal(err)
	}
	list, err := restored.List(100)
	if err != nil || len(list) != 2 {
		t.Fatalf("snapshot rebuilt missing history: %+v %v", list, err)
	}
	for _, d := range list {
		if d.Event.ID == "missing" {
			t.Fatal("historic absent delivery recreated")
		}
	}
	retry, ok, err := restored.Claim(later, time.Minute)
	if err != nil || !ok || retry.Key != first.Key || retry.Attempts != 2 || retry.Ambiguous != 1 {
		t.Fatalf("recorded retry lost: %+v %v %v", retry, ok, err)
	}
	// Clock regression cannot lower a previously stored runtime floor.
	if err := restored.BeginRun(epoch); err != nil {
		t.Fatal(err)
	}
	cutoff, err = restored.EffectiveActivation()
	if err != nil || !cutoff.Equal(later) {
		t.Fatalf("floor regressed: %v %v", cutoff, err)
	}
}

func TestBeginRunLegacyLeavesCoverageUnchanged(t *testing.T) {
	s := open(t)
	scan := core.Scan{Upper: epoch.Add(time.Minute), Cursor: core.Cursor{PlacedAt: epoch, ID: "event-a"}, LastCompleted: epoch}
	if err := s.ApplyPage(nil, scan, epoch); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginRun(epoch.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	cutoff, err := s.EffectiveActivation()
	if err != nil || !cutoff.Equal(epoch) {
		t.Fatalf("legacy floor: %v %v", cutoff, err)
	}
	retained, err := s.Scan()
	if err != nil || retained != scan {
		t.Fatalf("legacy scan changed: %+v %v", retained, err)
	}
}

func TestBeginRunRejectsZeroClock(t *testing.T) {
	p := policy()
	p.NewStatePolicy = "from_startup"
	s, err := OpenAt(filepath.Join(t.TempDir(), "state.db"), p, epoch)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.BeginRun(time.Time{}); err == nil {
		t.Fatal("zero runtime clock accepted")
	}
}
