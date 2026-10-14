package state

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jamesonstone/outpost/internal/core"
	bolt "go.etcd.io/bbolt"
)

func TestFreshLedgerCutoffAndRetainedRetries(t *testing.T) {
	p := policy()
	p.NewStatePolicy = "from_startup"
	startup := epoch.Add(time.Hour)
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := OpenAt(path, p, startup)
	if err != nil {
		t.Fatal(err)
	}
	old := event()
	current := event()
	current.ID, current.OrderID, current.PlacedAt = "new", "new", startup
	if err := s.ApplyPage([]core.Event{old, current}, core.Scan{}, startup); err != nil {
		t.Fatal(err)
	}
	d, found, err := s.Claim(startup, time.Minute)
	if err != nil || !found || d.Event.ID != "new" {
		t.Fatalf("claim: %+v %v %v", d, found, err)
	}
	if err := s.Finish(d.Key, d.Token, core.Receipt{}, &core.SendError{Category: "timeout", Ambiguous: true}, startup.Add(time.Second), time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	later := startup.Add(time.Hour)
	s, err = OpenAt(path, p, later)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cutoff, err := s.EffectiveActivation()
	if err != nil || !cutoff.Equal(startup) {
		t.Fatalf("retained cutoff: %v %v", cutoff, err)
	}
	stats, err := s.Stats(later)
	if err != nil || !stats.EffectiveActivation.Equal(startup) || stats.Pending != 1 {
		t.Fatalf("stats: %+v %v", stats, err)
	}
	retry, found, err := s.Claim(later, time.Minute)
	if err != nil || !found || retry.Key != d.Key || retry.Attempts != 2 || retry.Ambiguous != 1 {
		t.Fatalf("retained retry: %+v %v %v", retry, found, err)
	}
	// A missing volume creates a new ledger. Old source events cannot recreate
	// unknown deliveries, including occurrences lost during downtime.
	replacement, err := OpenAt(filepath.Join(t.TempDir(), "replacement.db"), p, later)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	newest := current
	newest.ID, newest.OrderID, newest.PlacedAt = "latest", "latest", later
	if err := replacement.ApplyPage([]core.Event{old, current, newest}, core.Scan{}, later); err != nil {
		t.Fatal(err)
	}
	list, err := replacement.List(100)
	if err != nil || len(list) != 1 || list[0].Event.ID != "latest" {
		t.Fatalf("lost ledger replay: %+v %v", list, err)
	}
}

func TestNewLedgerUsesLaterConfiguredActivation(t *testing.T) {
	p := policy()
	p.NewStatePolicy = "from_startup"
	p.Activation = epoch.Add(time.Hour)
	s, err := OpenAt(filepath.Join(t.TempDir(), "state.db"), p, epoch)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cutoff, err := s.EffectiveActivation()
	if err != nil || !cutoff.Equal(p.Activation) {
		t.Fatalf("cutoff: %v %v", cutoff, err)
	}
}

func TestLegacyDefaultKeepsConfiguredCutoff(t *testing.T) {
	p := policy()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := OpenAt(path, p, epoch.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	enqueue(t, s)
	// Model a valid ledger written by the original version without cutoff metadata.
	if err := s.db.Update(func(tx *bolt.Tx) error { return tx.Bucket([]byte("meta")).Delete([]byte("effective_activation")) }); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenAt(path, p, epoch.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	cutoff, err := s.EffectiveActivation()
	if err != nil || !cutoff.Equal(epoch) {
		t.Fatalf("legacy cutoff: %v %v", cutoff, err)
	}
	list, err := s.List(100)
	if err != nil || len(list) != 1 {
		t.Fatalf("legacy state lost: %+v %v", list, err)
	}
	s.Close()
	p.NewStatePolicy = "from_startup"
	if _, err := OpenAt(path, p, epoch.Add(3*time.Hour)); !errors.Is(err, ErrPolicy) {
		t.Fatalf("policy mutated: %v", err)
	}
}

func TestExistingLedgerCorruptionFailsClosed(t *testing.T) {
	cases := map[string]func(*bolt.Tx) error{
		"missing_policy":  func(tx *bolt.Tx) error { return tx.Bucket([]byte("meta")).Delete([]byte("policy")) },
		"missing_version": func(tx *bolt.Tx) error { return tx.Bucket([]byte("meta")).Delete([]byte("version")) },
		"missing_cutoff":  func(tx *bolt.Tx) error { return tx.Bucket([]byte("meta")).Delete([]byte("effective_activation")) },
		"bad_cutoff": func(tx *bolt.Tx) error {
			return tx.Bucket([]byte("meta")).Put([]byte("effective_activation"), []byte("null"))
		},
		"earlier_cutoff": func(tx *bolt.Tx) error {
			return put(tx.Bucket([]byte("meta")), "effective_activation", epoch.Add(-time.Second))
		},
		"bad_policy":     func(tx *bolt.Tx) error { return tx.Bucket([]byte("meta")).Put([]byte("policy"), []byte("{")) },
		"missing_bucket": func(tx *bolt.Tx) error { return tx.DeleteBucket([]byte("deliveries")) },
		"missing_meta":   func(tx *bolt.Tx) error { return tx.DeleteBucket([]byte("meta")) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := policy()
			p.NewStatePolicy = "from_startup"
			path := filepath.Join(t.TempDir(), "state.db")
			s, err := OpenAt(path, p, epoch)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.db.Update(mutate); err != nil {
				t.Fatal(err)
			}
			s.Close()
			if _, err := OpenAt(path, p, epoch.Add(time.Hour)); !errors.Is(err, ErrMetadata) {
				t.Fatalf("corrupt ledger reset: %v", err)
			}
		})
	}
}

func TestInvalidStartupPolicy(t *testing.T) {
	for _, p := range []core.Policy{{NewStatePolicy: "unknown"}, {NewStatePolicy: "from_startup"}} {
		if _, err := OpenAt(filepath.Join(t.TempDir(), "state.db"), p, time.Time{}); err == nil {
			t.Fatal("invalid startup accepted")
		}
	}
}
