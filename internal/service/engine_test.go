package service

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jamesonstone/outpost/internal/core"
	"github.com/jamesonstone/outpost/internal/state"
)

type fakeSource struct {
	events []core.Event
	fail   bool
}

func (s *fakeSource) Page(_ context.Context, start, upper time.Time, c core.Cursor, limit int) (out []core.Event, err error) {
	if s.fail {
		return nil, errors.New("synthetic source failure")
	}
	for _, ev := range s.events {
		if !ev.PlacedAt.Before(start) && !ev.PlacedAt.After(upper) && after(core.Cursor{PlacedAt: ev.PlacedAt, ID: ev.ID}, c) {
			out = append(out, ev)
			if len(out) == limit {
				break
			}
		}
	}
	return
}

type fakeDestination struct {
	calls   int
	failure error
}

func (d *fakeDestination) Send(context.Context, core.Event) (core.Receipt, error) {
	d.calls++
	return core.Receipt{Channel: "C123", Timestamp: "1.2"}, d.failure
}
func fixture(t *testing.T) (*Engine, *fakeSource, *fakeDestination, *time.Time) {
	t.Helper()
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	p := core.Policy{Source: "synthetic", Channel: "C123", Partner: "Example", Activation: now}
	st, err := state.Open(filepath.Join(t.TempDir(), "state.db"), p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	src := &fakeSource{}
	dst := &fakeDestination{}
	return &Engine{Store: st, Source: src, Destination: dst, Activation: now, PageSize: 1, Lease: time.Minute, RequestTimeout: time.Second, Now: func() time.Time { return now }}, src, dst, &now
}
func synthetic(id string, at time.Time) core.Event {
	return core.Event{ID: id, Environment: "production", Tenant: "example", OrderID: id, Partner: "Example", PlacedAt: at, Link: "https://example.test/order/" + id, Kind: "first_placement", Traffic: "production", Origin: "partner_api", TenantClass: "production"}
}
func TestLateCommitAndAtomicScan(t *testing.T) {
	e, src, _, now := fixture(t)
	src.events = []core.Event{synthetic("b", *now)}
	done, err := e.ScanPage(context.Background())
	if err != nil || done {
		t.Fatal("first page failed")
	}
	scan, _ := e.Store.Scan()
	src.fail = true
	if _, err = e.ScanPage(context.Background()); err == nil {
		t.Fatal("source failure ignored")
	}
	unchanged, _ := e.Store.Scan()
	if scan != unchanged {
		t.Fatal("failed scan advanced checkpoint")
	}
	src.fail = false
	src.events = []core.Event{synthetic("a", *now), synthetic("b", *now)}
	done, err = e.ScanPage(context.Background())
	if err != nil || !done {
		t.Fatal("sweep completion failed")
	}
	for i := 0; i < 3; i++ {
		if _, err = e.ScanPage(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	list, _ := e.Store.List(100)
	if len(list) != 2 {
		t.Fatal("late commit missed or duplicated")
	}
}
func TestDisabledAndCoverageFirstRetry(t *testing.T) {
	e, src, dst, now := fixture(t)
	src.events = []core.Event{synthetic("a", *now)}
	e.ScanPage(context.Background())
	if sent, err := e.DispatchOne(context.Background()); sent || err != nil || dst.calls != 0 {
		t.Fatal("disabled dispatch called gateway")
	}
	e.Enabled = true
	dst.failure = &core.SendError{Category: "timeout", Ambiguous: true, RetryAfter: time.Minute}
	if sent, err := e.DispatchOne(context.Background()); !sent || err != nil {
		t.Fatal(err)
	}
	list, _ := e.Store.List(100)
	if list[0].Ambiguous != 1 || list[0].Attempts != 1 || list[0].Status != "pending" {
		t.Fatal("ambiguity lost")
	}
	*now = now.Add(30 * time.Second)
	if sent, _ := e.DispatchOne(context.Background()); sent {
		t.Fatal("retried before server delay")
	}
	*now = now.Add(time.Minute)
	dst.failure = nil
	if sent, err := e.DispatchOne(context.Background()); !sent || err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		e.ScanPage(context.Background())
	}
	list, _ = e.Store.List(100)
	if len(list) != 1 || list[0].Status != "delivered" || dst.calls != 2 {
		t.Fatal("reconciliation redelivered receipt")
	}
}
func TestExcludedHasZeroOutboundAttempts(t *testing.T) {
	e, src, dst, now := fixture(t)
	ev := synthetic("a", *now)
	ev.Origin = "manual"
	src.events = []core.Event{ev}
	e.Enabled = true
	if _, err := e.ScanPage(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.DispatchOne(context.Background())
	if dst.calls != 0 {
		t.Fatal("excluded event sent")
	}
}

func TestFutureActivationDoesNotPersistInvalidSweep(t *testing.T) {
	e, src, _, now := fixture(t)
	activation := *now
	*now = now.Add(-time.Hour)
	if done, err := e.ScanPage(context.Background()); !done || err != nil {
		t.Fatal(err)
	}
	scan, _ := e.Store.Scan()
	if !scan.Upper.IsZero() {
		t.Fatal("future activation persisted early boundary")
	}
	*now = activation
	src.events = []core.Event{synthetic("a", activation)}
	if _, err := e.ScanPage(context.Background()); err != nil {
		t.Fatal(err)
	}
	list, _ := e.Store.List(10)
	if len(list) != 1 {
		t.Fatal("activation failed to start")
	}
}

func TestGuardFailurePausesDispatch(t *testing.T) {
	e, src, dst, now := fixture(t)
	src.events = []core.Event{synthetic("a", *now)}
	e.Enabled = true
	if _, err := e.ScanPage(context.Background()); err != nil {
		t.Fatal(err)
	}
	dst.failure = &core.SendError{Category: "destination_guard_rejected", Permanent: true}
	if _, err := e.DispatchOne(context.Background()); !errors.Is(err, ErrDestinationUnsafe) {
		t.Fatal("unsafe destination did not pause", err)
	}
	if !e.SafetyPaused.Load() {
		t.Fatal("pause not visible")
	}
	if sent, err := e.DispatchOne(context.Background()); sent || !errors.Is(err, ErrDestinationUnsafe) || dst.calls != 1 {
		t.Fatal("paused dispatcher called destination")
	}
}
func TestScannerUsesLedgerFloor(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	p := core.Policy{Source: "synthetic", Channel: "C123", Partner: "Example", Activation: now.Add(-time.Hour), NewStatePolicy: "from_startup"}
	st, err := state.OpenAt(filepath.Join(t.TempDir(), "ledger.db"), p, now)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	src := &fakeSource{events: []core.Event{synthetic("historical", now.Add(-time.Minute)), synthetic("current", now)}}
	e := &Engine{Store: st, Source: src, Activation: p.Activation, PageSize: 100, Now: func() time.Time { return now }}
	if _, err := e.ScanPage(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, _ := st.List(100)
	if len(rows) != 1 || rows[0].Event.ID != "current" {
		t.Fatal("scanner resurrected missing historical event")
	}
}
