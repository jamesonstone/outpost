package source

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jamesonstone/outpost/internal/core"
)

func TestConnectionPolicy(t *testing.T) {
	for _, dsn := range []string{"postgres://user@db.example.test/events", "host=db.example.test user=synthetic dbname=events"} {
		c, err := connectionConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		if c.ConnConfig.TLSConfig == nil || c.ConnConfig.TLSConfig.InsecureSkipVerify || len(c.ConnConfig.Fallbacks) != 0 {
			t.Fatal("connection can bypass verified TLS")
		}
	}
	for _, dsn := range []string{"", "postgres://user@db.example.test/events?sslmode=disable", "postgres://user@localhost/events?sslmode=require", "host=db.example.test sslmode=prefer"} {
		if _, err := connectionConfig(dsn); !errors.Is(err, ErrConfiguration) {
			t.Fatalf("accepted unsafe configuration: %v", err)
		}
	}
	for _, host := range []string{"localhost", "127.0.0.1", "[::1]"} {
		if _, err := connectionConfig("postgres://synthetic@" + host + "/events?sslmode=disable"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProjectionContract(t *testing.T) {
	for _, query := range []string{"", "DELETE FROM events", "SELECT 1; SELECT 2", "SELECT 1; -- comment"} {
		if _, err := pageQuery(query); !errors.Is(err, ErrConfiguration) {
			t.Fatal("accepted invalid projection")
		}
	}
	q, err := pageQuery("SELECT * FROM events;")
	if err != nil || !strings.Contains(q, `id COLLATE "C"`) || !strings.Contains(q, "placed_at >= $1 AND placed_at <= $2") {
		t.Fatal("projection lacks stable sweep boundaries")
	}
}

func TestPageArguments(t *testing.T) {
	p := &Postgres{}
	now := time.Now()
	for _, tt := range []struct {
		activation, upper time.Time
		cursor            core.Cursor
		limit             int
	}{
		{time.Time{}, now, core.Cursor{}, 1}, {now, now.Add(-time.Second), core.Cursor{}, 1},
		{now, now, core.Cursor{}, 0}, {now, now, core.Cursor{}, 1001},
		{now, now, core.Cursor{ID: "x"}, 1}, {now, now, core.Cursor{PlacedAt: now}, 1},
	} {
		if _, err := p.Page(context.Background(), tt.activation, tt.upper, tt.cursor, tt.limit); !errors.Is(err, ErrPage) {
			t.Fatal("invalid argument reached database")
		}
	}
}

// Integration writes are restricted to an explicitly selected loopback database.
// Each test owns an isolated schema and removes only that schema on teardown.
func TestPostgresIntegration(t *testing.T) {
	dsn := os.Getenv("OUTPOST_TEST_DSN")
	if dsn == "" {
		t.Skip("OUTPOST_TEST_DSN unset; PostgreSQL integration not run")
	}
	config, err := connectionConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if config.ConnConfig.Host != "localhost" && config.ConnConfig.Host != "127.0.0.1" && config.ConnConfig.Host != "::1" {
		t.Fatal("integration requires loopback database")
	}
	ctx := context.Background()
	db, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("synthetic database unavailable")
	}
	defer db.Close(ctx)
	schema := "outpost_test_" + time.Now().UTC().Format("20060102150405_000000000")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = db.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal("synthetic schema setup failed")
	}
	defer func() {
		if _, err := db.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error("synthetic schema cleanup failed")
		}
	}()
	fixture, err := os.ReadFile("../../examples/events.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, "SET search_path TO "+quoted); err != nil {
		t.Fatal("synthetic search path failed")
	}
	if _, err = db.Exec(ctx, string(fixture)); err != nil {
		t.Fatal("synthetic fixture failed")
	}
	projection := "SELECT * FROM " + quoted + ".outpost_test_events"
	p, err := New(ctx, dsn, projection)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	start := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	upper := start.Add(time.Minute)
	// Start a transaction before the sweep, but commit only after its first page.
	late, err := db.Begin(ctx)
	if err != nil {
		t.Fatal("late transaction setup failed")
	}
	defer late.Rollback(ctx)
	if _, err = late.Exec(ctx, "INSERT INTO "+quoted+".outpost_test_events SELECT 'synthetic-0',environment,tenant,'synthetic-order-0',partner,placed_at,link,kind,traffic,origin,tenant_class FROM "+quoted+".outpost_test_events"); err != nil {
		t.Fatal("late synthetic insert failed")
	}
	events, err := p.Page(ctx, start, upper, core.Cursor{}, 1)
	if err != nil || len(events) != 1 || events[0].ID != "synthetic-a" {
		t.Fatalf("first page: count=%d error=%v", len(events), err)
	}
	cursor := core.Cursor{PlacedAt: events[0].PlacedAt, ID: events[0].ID}
	if err = late.Commit(ctx); err != nil {
		t.Fatal("late commit failed")
	}
	events, err = p.Page(ctx, start, upper, cursor, 1)
	if err != nil || len(events) != 0 {
		t.Fatal("keyset page repeated older identity")
	}
	events, err = p.Page(ctx, start, upper, core.Cursor{}, 1000)
	if err != nil || len(events) != 2 || events[0].ID != "synthetic-0" {
		t.Fatal("new sweep missed late commit")
	}
	next, err := p.Page(ctx, start, upper, core.Cursor{PlacedAt: events[0].PlacedAt, ID: events[0].ID}, 1)
	if err != nil || len(next) != 1 || next[0].ID != "synthetic-a" {
		t.Fatal("timestamp tie pagination failed")
	}
	if _, err = db.Exec(ctx, "UPDATE "+quoted+".outpost_test_events SET environment=NULL WHERE id='synthetic-0'"); err != nil {
		t.Fatal("null classification setup failed")
	}
	events, err = p.Page(ctx, start, upper, core.Cursor{}, 1000)
	if err != nil || events[0].Environment != "" {
		t.Fatal("null classification did not fail closed")
	}
	// A SELECT wrapper can call a mutating function; read-only transaction must block it.
	functionSQL := "CREATE FUNCTION " + quoted + ".mutate() RETURNS SETOF " + quoted + ".outpost_test_events LANGUAGE plpgsql AS $$ BEGIN UPDATE " + quoted + ".outpost_test_events SET partner='changed'; RETURN QUERY SELECT * FROM " + quoted + ".outpost_test_events; END $$"
	if _, err = db.Exec(ctx, functionSQL); err != nil {
		t.Fatal("synthetic mutator setup failed")
	}
	mutator, err := New(ctx, dsn, "SELECT * FROM "+quoted+".mutate()")
	if err != nil {
		t.Fatal(err)
	}
	defer mutator.Close()
	if _, err = mutator.Page(ctx, start, upper, core.Cursor{}, 10); !errors.Is(err, ErrProjection) {
		t.Fatalf("read-only mutation guard failed: %v", err)
	}
	var changed int
	if err = db.QueryRow(ctx, "SELECT count(*) FROM "+quoted+".outpost_test_events WHERE partner='changed'").Scan(&changed); err != nil || changed != 0 {
		t.Fatal("source adapter mutated fixture")
	}
}

func TestCommentedProjection(t *testing.T) {
	for _, q := range []string{"-- synthetic example\nSELECT * FROM events", "/* synthetic example */\nSELECT * FROM events"} {
		if err := ValidateProjection(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{"-- comment only", "/* unterminated"} {
		if err := ValidateProjection(q); err == nil {
			t.Fatal("invalid comment accepted")
		}
	}
}
