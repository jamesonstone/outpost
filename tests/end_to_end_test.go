package tests

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jamesonstone/outpost/internal/core"
	"github.com/jamesonstone/outpost/internal/service"
	"github.com/jamesonstone/outpost/internal/slack"
	"github.com/jamesonstone/outpost/internal/source"
	"github.com/jamesonstone/outpost/internal/state"
)

func TestSyntheticEndToEnd(t *testing.T) {
	dsn := os.Getenv("OUTPOST_TEST_DSN")
	if dsn == "" {
		t.Skip("OUTPOST_TEST_DSN unset; end-to-end integration not run")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid synthetic database configuration")
	}
	ip := net.ParseIP(config.Host)
	if config.Host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		t.Fatal("integration database must be loopback")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal("synthetic database unavailable")
	}
	defer db.Close(ctx)
	schema := "outpost_e2e_" + time.Now().UTC().Format("20060102150405_000000000")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = db.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal("scratch schema setup failed")
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := db.Exec(cleanupCtx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error("scratch schema cleanup failed")
		}
	}()
	if _, err = db.Exec(ctx, "SET search_path TO "+quoted); err != nil {
		t.Fatal("scratch search path failed")
	}
	fixture, err := os.ReadFile("../examples/events.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, string(fixture)); err != nil {
		t.Fatal("synthetic event fixture setup failed")
	}
	pg, err := source.New(ctx, dsn, "SELECT * FROM "+quoted+".outpost_test_events")
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	activation := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	now := activation.Add(time.Minute)
	policy := core.Policy{Source: "synthetic-postgres", Channel: "CSYNTHETIC", Partner: "synthetic-partner", Activation: activation}
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := state.Open(path, policy)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if store != nil {
			store.Close()
		}
	}()
	var posts atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer synthetic-token" {
			t.Error("unexpected synthetic request metadata")
		}
		var payload struct {
			Channel     string `json:"channel"`
			Text        string `json:"text"`
			Parse       string `json:"parse"`
			LinkNames   bool   `json:"link_names"`
			UnfurlLinks bool   `json:"unfurl_links"`
			UnfurlMedia bool   `json:"unfurl_media"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error("invalid synthetic Slack payload")
		}
		if payload.Channel != policy.Channel || payload.Parse != "none" || payload.LinkNames || payload.UnfurlLinks || payload.UnfurlMedia {
			t.Error("unexpected destination or formatting")
		}
		expected := "Order ID: synthetic-order-a\nPartner: synthetic-partner\nPlaced at: 2030-01-01T00:00:00Z\n<https://example.test/orders/synthetic-order-a|View order>"
		if payload.Text != expected || strings.Contains(payload.Text, "synthetic-tenant") {
			t.Error("payload violated approved minimal fields")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"channel":"CSYNTHETIC","ts":"123.456"}`))
	}))
	defer server.Close()
	destination := &slack.Client{Token: "synthetic-token", Channel: policy.Channel, HTTP: server.Client(), Endpoint: server.URL}
	engine := &service.Engine{Store: store, Source: pg, Destination: destination, Activation: activation, PageSize: 1, Lease: time.Minute, RequestTimeout: time.Second, Now: func() time.Time { return now }}
	sweep := func() {
		t.Helper()
		for page := 0; page < 10; page++ {
			complete, err := engine.ScanPage(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if complete {
				return
			}
		}
		t.Fatal("synthetic reconciliation did not finish")
	}
	sweep()
	deliveries, err := store.List(10)
	if err != nil || len(deliveries) != 1 || deliveries[0].Event.ID != "synthetic-a" || deliveries[0].Status != "pending" {
		t.Fatal("normalized PostgreSQL event did not reach durable pending state")
	}
	if sent, err := engine.DispatchOne(ctx); err != nil || sent || posts.Load() != 0 {
		t.Fatal("disabled dispatch posted a message")
	}
	engine.Enabled = true
	if sent, err := engine.DispatchOne(ctx); err != nil || !sent || posts.Load() != 1 {
		t.Fatal("enabled dispatch did not send exactly once to HTTP fixture")
	}
	deliveries, err = store.List(10)
	if err != nil || len(deliveries) != 1 || deliveries[0].Status != "delivered" || deliveries[0].Receipt.Channel != policy.Channel || deliveries[0].Receipt.Timestamp != "123.456" {
		t.Fatal("confirmed receipt missing from durable state")
	}
	key := deliveries[0].Key
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store = nil
	store, err = state.Open(path, policy)
	if err != nil {
		t.Fatal(err)
	}
	engine.Store = store
	deliveries, err = store.List(10)
	if err != nil || len(deliveries) != 1 || deliveries[0].Key != key || deliveries[0].Status != "delivered" || deliveries[0].Receipt.Timestamp != "123.456" {
		t.Fatal("receipt did not persist across reopen")
	}
	for iteration := 0; iteration < 3; iteration++ {
		now = now.Add(time.Minute)
		sweep()
		if sent, err := engine.DispatchOne(ctx); err != nil || sent {
			t.Fatal("reconciliation redelivered confirmed event")
		}
	}
	if posts.Load() != 1 {
		t.Fatal("duplicate Slack fixture posts")
	}
	attempts, err := store.Attempts(key)
	if err != nil || len(attempts) != 1 || attempts[0].Outcome != "delivered" {
		t.Fatal("durable attempt evidence missing")
	}
}
