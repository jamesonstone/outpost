package state

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jamesonstone/outpost/internal/core"
)

var epoch = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

func policy() core.Policy {
	return core.Policy{Source: "fixture", Channel: "C123", Partner: "Synthetic", Activation: epoch}
}
func event() core.Event {
	return core.Event{ID: "event-a", Environment: "production", Tenant: "tenant", OrderID: "order", Partner: "Synthetic", PlacedAt: epoch, Link: "https://example.test/orders/order", Kind: "first_placement", Traffic: "production", Origin: "partner_api", TenantClass: "production"}
}
func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"), policy())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func enqueue(t *testing.T, s *Store) {
	t.Helper()
	if err := s.ApplyPage([]core.Event{event()}, core.Scan{Upper: epoch, Cursor: core.Cursor{PlacedAt: epoch, ID: "event-a"}}, epoch); err != nil {
		t.Fatal(err)
	}
}
func TestDurabilityDedupAndPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path, policy())
	if err != nil {
		t.Fatal(err)
	}
	enqueue(t, s)
	ev := event()
	ev.ID = "republished"
	if err := s.ApplyPage([]core.Event{ev}, core.Scan{LastCompleted: epoch}, epoch); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path, policy())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	list, err := s.List(100)
	if err != nil || len(list) != 1 {
		t.Fatalf("dedup lost: %v %d", err, len(list))
	}
	scan, err := s.Scan()
	if err != nil || !scan.LastCompleted.Equal(epoch) {
		t.Fatal("checkpoint lost")
	}
	s.Close()
	p := policy()
	p.Activation = p.Activation.Add(time.Second)
	if _, err := Open(path, p); !errors.Is(err, ErrPolicy) {
		t.Fatal("mutable activation accepted")
	}
}
func TestCrashLeaseAndStaleWorker(t *testing.T) {
	s := open(t)
	enqueue(t, s)
	a, ok, err := s.Claim(epoch, time.Minute)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if _, ok, err := s.Claim(epoch.Add(time.Second), time.Minute); err != nil || ok {
		t.Fatal("live lease duplicated")
	}
	b, ok, err := s.Claim(epoch.Add(time.Minute), time.Minute)
	if err != nil || !ok || b.Ambiguous != 1 || b.Attempts != 2 {
		t.Fatal("crash ambiguity missing")
	}
	if err := s.Finish(a.Key, a.Token, core.Receipt{Channel: "C123", Timestamp: "1.2"}, nil, epoch.Add(time.Minute), 0); !errors.Is(err, ErrClaim) {
		t.Fatal("stale worker overwrote claim")
	}
	if err := s.Finish(b.Key, b.Token, core.Receipt{Channel: "C123", Timestamp: "1.2"}, nil, epoch.Add(time.Minute+time.Second), 0); err != nil {
		t.Fatal(err)
	}
	attempts, err := s.Attempts(b.Key)
	if err != nil || len(attempts) != 2 || !attempts[0].Ambiguous || attempts[1].Outcome != "delivered" {
		t.Fatal("attempt evidence missing")
	}
	if _, ok, err := s.Claim(epoch.Add(time.Hour), time.Minute); err != nil || ok {
		t.Fatal("delivered retry")
	}
}
func TestConflictExclusionRetryAndBackup(t *testing.T) {
	s := open(t)
	enqueue(t, s)
	ev := event()
	ev.Link = "https://example.test/changed"
	if err := s.ApplyPage([]core.Event{ev}, core.Scan{}, epoch); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Claim(epoch, time.Minute); err != nil || ok {
		t.Fatal("conflicting payload sent")
	}
	stats, err := s.Stats(epoch)
	if err != nil || stats.Conflicts != 1 || stats.Exceptions != 1 {
		t.Fatal("conflict not visible")
	}
	if err := s.Retry(policy().Key(ev), epoch); err == nil {
		t.Fatal("conflict manually resent")
	}
	backup := filepath.Join(t.TempDir(), "backup.db")
	if err := s.Backup(backup); err != nil {
		t.Fatal(err)
	}
	copy, err := Open(backup, policy())
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	stats, err = copy.Stats(epoch)
	if err != nil || stats.Conflicts != 1 {
		t.Fatal("backup lost state")
	}
}
func TestEligibilityIsFailClosed(t *testing.T) {
	for _, mutate := range []func(*core.Event){func(e *core.Event) { e.Origin = "manual" }, func(e *core.Event) { e.Traffic = "test" }, func(e *core.Event) { e.TenantClass = "simulation" }, func(e *core.Event) { e.Environment = "" }, func(e *core.Event) { e.PlacedAt = epoch.Add(-time.Second) }, func(e *core.Event) { e.Link = "http://example.test" }, func(e *core.Event) { e.Partner = "Other" }, func(e *core.Event) { e.Kind = "update" }} {
		s := open(t)
		ev := event()
		mutate(&ev)
		if err := s.ApplyPage([]core.Event{ev}, core.Scan{}, epoch); err != nil {
			t.Fatal(err)
		}
		list, err := s.List(100)
		if err != nil || len(list) != 0 {
			t.Fatal("excluded event enqueued")
		}
	}
}
func TestPermanentFailureControlledRetry(t *testing.T) {
	s := open(t)
	enqueue(t, s)
	d, _, _ := s.Claim(epoch, time.Minute)
	failure := &core.SendError{Category: "destination_rejected", Permanent: true}
	if err := s.Finish(d.Key, d.Token, core.Receipt{}, failure, epoch.Add(time.Second), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Claim(epoch.Add(time.Hour), time.Minute); ok {
		t.Fatal("permanent failure retried")
	}
	if err := s.Retry(d.Key, epoch.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Claim(epoch.Add(time.Hour), time.Minute); err != nil || !ok {
		t.Fatal("controlled retry lost")
	}
}

func TestRateLimitGatesWholeChannel(t *testing.T) {
	s := open(t)
	enqueue(t, s)
	ev := event()
	ev.ID = "b"
	ev.OrderID = "b"
	if err := s.ApplyPage([]core.Event{ev}, core.Scan{}, epoch); err != nil {
		t.Fatal(err)
	}
	d, _, _ := s.Claim(epoch, time.Minute)
	if err := s.Finish(d.Key, d.Token, core.Receipt{}, &core.SendError{Category: "rate_limited", RetryAfter: time.Minute}, epoch.Add(time.Second), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Claim(epoch.Add(30*time.Second), time.Minute); err != nil || ok {
		t.Fatal("channel ignored rate limit")
	}
	if _, ok, err := s.Claim(epoch.Add(61*time.Second), time.Minute); err != nil || !ok {
		t.Fatal("channel cooldown never cleared")
	}
}
func TestBackupDoesNotOverwrite(t *testing.T) {
	s := open(t)
	path := filepath.Join(t.TempDir(), "backup.db")
	if err := s.Backup(path); err != nil {
		t.Fatal(err)
	}
	if err := s.Backup(path); err == nil {
		t.Fatal("backup overwritten")
	}
}

func TestConflictPreservesActiveAttempt(t *testing.T) {
	for _, failure := range []*core.SendError{nil, {Category: "timeout", Ambiguous: true}} {
		s := open(t)
		enqueue(t, s)
		d, _, _ := s.Claim(epoch, time.Minute)
		ev := event()
		ev.Link = "https://example.test/changed"
		if err := s.ApplyPage([]core.Event{ev}, core.Scan{}, epoch); err != nil {
			t.Fatal(err)
		}
		receipt := core.Receipt{Channel: "C123", Timestamp: "1.2"}
		if err := s.Finish(d.Key, d.Token, receipt, failure, epoch.Add(time.Second), time.Minute); err != nil {
			t.Fatal(err)
		}
		list, _ := s.List(10)
		if !list[0].Conflicted {
			t.Fatal("conflict missing")
		}
		attempts, _ := s.Attempts(d.Key)
		if len(attempts) != 1 || attempts[0].Outcome == "in_flight" {
			t.Fatal("attempt discarded")
		}
		if failure == nil && list[0].Receipt.Timestamp != "1.2" {
			t.Fatal("receipt discarded")
		}
		if failure != nil && (list[0].Ambiguous != 1 || list[0].Status != "conflict") {
			t.Fatal("ambiguous conflict lost")
		}
		if _, ok, err := s.Claim(epoch.Add(time.Hour), time.Minute); err != nil || ok {
			t.Fatal("conflict re-sent")
		}
	}
}
