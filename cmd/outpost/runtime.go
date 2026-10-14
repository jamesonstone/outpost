package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/jamesonstone/outpost/internal/config"
	"github.com/jamesonstone/outpost/internal/service"
)

func serve(ctx context.Context, e *service.Engine, c config.Config) error {
	listener, err := net.Listen("tcp", c.Listen)
	if err != nil {
		return errors.New("health listener failed")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		now := time.Now().UTC()
		stats, err := e.Store.Stats(now)
		if err != nil || stats.LastSweep.IsZero() || now.Sub(stats.LastSweep) > 5*time.Minute {
			http.Error(w, "reconciliation unavailable", 503)
			return
		}
		w.Write([]byte("ready\n"))
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		now := time.Now().UTC()
		s, err := e.Store.Stats(now)
		if err != nil {
			http.Error(w, "state unavailable", 503)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		enabled := 0
		if e.Enabled {
			enabled = 1
		}
		age := -1.0
		if !s.LastSweep.IsZero() {
			age = now.Sub(s.LastSweep).Seconds()
		}
		fmt.Fprintf(w, "outpost_pending %d\noutpost_sending %d\noutpost_delivered %d\noutpost_failed %d\noutpost_conflicts %d\noutpost_classification_exceptions %d\noutpost_attempts_total %d\noutpost_ambiguous_total %d\noutpost_oldest_pending_seconds %f\noutpost_last_sweep_age_seconds %f\noutpost_dispatch_enabled %d\n", s.Pending, s.Sending, s.Delivered, s.Failed, s.Conflicts, s.Exceptions, s.Attempts, s.Ambiguous, s.OldestPendingSeconds, age, enabled)
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- server.Serve(listener) }()
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		loop(workerCtx, func() time.Duration {
			done, err := e.ScanPage(workerCtx)
			if err != nil {
				fmt.Fprintln(os.Stderr, "scan failed")
				return 10 * time.Second
			}
			if done {
				return time.Duration(c.PollSeconds) * time.Second
			}
			return time.Millisecond
		})
	}()
	go func() {
		defer workers.Done()
		loop(workerCtx, func() time.Duration {
			_, err := e.DispatchOne(workerCtx)
			if err != nil {
				fmt.Fprintln(os.Stderr, "dispatch state failed")
			}
			return time.Second
		})
	}()
	var result error
	select {
	case <-ctx.Done():
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			result = errors.New("health server failed")
		}
	}
	cancel()
	shutdownCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	server.Shutdown(shutdownCtx)
	workers.Wait()
	return result
}
func loop(ctx context.Context, step func() time.Duration) {
	for {
		if ctx.Err() != nil {
			return
		}
		timer := time.NewTimer(step())
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
