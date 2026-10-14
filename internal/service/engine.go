// Package service owns reconciliation and dispatch policy.
package service

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/jamesonstone/outpost/internal/core"
	"github.com/jamesonstone/outpost/internal/state"
)

type Engine struct {
	Store          *state.Store
	Source         core.Source
	Destination    core.Destination
	Activation     time.Time
	PageSize       int
	Enabled        bool
	Lease          time.Duration
	RequestTimeout time.Duration
	Now            func() time.Time
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}
func after(a, b core.Cursor) bool {
	return a.PlacedAt.After(b.PlacedAt) || a.PlacedAt.Equal(b.PlacedAt) && a.ID > b.ID
}

// ScanPage completes one bounded page; a completed sweep resets to the activation boundary.
func (e *Engine) ScanPage(ctx context.Context) (complete bool, err error) {
	if e.PageSize < 1 || e.PageSize > 1000 {
		return false, errors.New("invalid page size")
	}
	now := e.now()
	if now.Before(e.Activation) {
		return true, nil
	}
	scan, err := e.Store.Scan()
	if err != nil {
		return false, err
	}
	if !scan.Upper.IsZero() && scan.Upper.Before(e.Activation) {
		scan.Upper = time.Time{}
		scan.Cursor = core.Cursor{}
		if err = e.Store.ApplyPage(nil, scan, now); err != nil {
			return false, err
		}
	}
	scan, err = e.Store.BeginSweep(now)
	if err != nil {
		return false, err
	}
	events, err := e.Source.Page(ctx, e.Activation, scan.Upper, scan.Cursor, e.PageSize)
	if err != nil {
		return false, err
	}
	if len(events) > e.PageSize {
		return false, errors.New("source exceeded page limit")
	}
	last := scan.Cursor
	for _, ev := range events {
		c := core.Cursor{PlacedAt: ev.PlacedAt, ID: ev.ID}
		if ev.ID == "" || ev.PlacedAt.IsZero() || ev.PlacedAt.Before(e.Activation) || ev.PlacedAt.After(scan.Upper) || !after(c, last) {
			return false, errors.New("invalid source page ordering")
		}
		last = c
	}
	scan.Cursor = last
	complete = len(events) < e.PageSize
	if complete {
		scan.Cursor = core.Cursor{}
		scan.Upper = time.Time{}
		scan.LastCompleted = now
	}
	return complete, e.Store.ApplyPage(events, scan, now)
}
func backoff(attempt uint64) time.Duration {
	if attempt > 10 {
		attempt = 10
	}
	max := time.Second * time.Duration(uint64(1)<<attempt)
	if max > 15*time.Minute {
		max = 15 * time.Minute
	}
	return max/2 + time.Duration(rand.Int64N(int64(max/2)+1))
}
func (e *Engine) DispatchOne(ctx context.Context) (bool, error) {
	if !e.Enabled {
		return false, nil
	}
	if e.RequestTimeout <= 0 || e.Lease <= e.RequestTimeout {
		return false, errors.New("lease must exceed request timeout")
	}
	d, ok, err := e.Store.Claim(e.now(), e.Lease)
	if err != nil || !ok {
		return false, err
	}
	sendCtx, cancel := context.WithTimeout(ctx, e.RequestTimeout)
	defer cancel()
	receipt, sendErr := e.Destination.Send(sendCtx, d.Event)
	var result *core.SendError
	if sendErr != nil {
		if !errors.As(sendErr, &result) {
			result = &core.SendError{Category: "destination_error", Ambiguous: true}
		}
	}
	retry := backoff(d.Attempts)
	if result != nil && result.RetryAfter > retry {
		retry = result.RetryAfter
	}
	err = e.Store.Finish(d.Key, d.Token, receipt, result, e.now(), retry)
	return true, err
}
