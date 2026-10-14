// Package source reads operator-owned event projections without changing the source.
package source

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jamesonstone/outpost/internal/core"
)

var (
	ErrConfiguration = errors.New("source_configuration")
	ErrUnavailable   = errors.New("source_unavailable")
	ErrPage          = errors.New("source_page")
	ErrProjection    = errors.New("source_projection")
)

type Postgres struct {
	pool  *pgxpool.Pool
	query string
}

// New accepts a trusted operator-owned SELECT projecting the documented columns.
// The projection is configuration, never event input. No schema is created here.
func New(ctx context.Context, dsn, projectionQuery string) (*Postgres, error) {
	config, err := connectionConfig(dsn)
	if err != nil {
		return nil, err
	}
	query, err := pageQuery(projectionQuery)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, ErrUnavailable
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, ErrUnavailable
	}
	return &Postgres{pool: pool, query: query}, nil
}

func connectionConfig(dsn string) (*pgxpool.Config, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, ErrConfiguration
	}
	mode := ""
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return nil, ErrConfiguration
		}
		values := u.Query()
		mode = values.Get("sslmode")
		if mode == "" {
			values.Set("sslmode", "verify-full")
			u.RawQuery = values.Encode()
			dsn = u.String()
		}
	} else {
		// Always append the verified default when no explicit sslmode is supplied.
		// pgx owns keyword parsing (including quoted values).
		for _, field := range strings.Fields(dsn) {
			if strings.HasPrefix(field, "sslmode=") {
				mode = strings.Trim(strings.TrimPrefix(field, "sslmode="), "'")
			}
		}
		if mode == "" {
			dsn += " sslmode=verify-full"
		}
	}
	if mode != "" && mode != "verify-full" && mode != "disable" {
		return nil, ErrConfiguration
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, ErrConfiguration
	}
	if mode == "disable" {
		ip := net.ParseIP(config.ConnConfig.Host)
		if config.ConnConfig.Host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return nil, ErrConfiguration
		}
	} else if config.ConnConfig.TLSConfig == nil || config.ConnConfig.TLSConfig.InsecureSkipVerify {
		return nil, ErrConfiguration
	}
	// Never fall back to an unverified or plaintext connection.
	config.ConnConfig.Fallbacks = nil
	config.ConnConfig.ConnectTimeout = 10 * time.Second
	config.MaxConns = 2
	return config, nil
}

func pageQuery(projection string) (string, error) {
	projection = strings.TrimSpace(projection)
	for {
		if strings.HasPrefix(projection, "--") {
			end := strings.IndexByte(projection, '\n')
			if end < 0 {
				return "", ErrConfiguration
			}
			projection = strings.TrimSpace(projection[end+1:])
			continue
		}
		if strings.HasPrefix(projection, "/*") {
			end := strings.Index(projection, "*/")
			if end < 0 {
				return "", ErrConfiguration
			}
			projection = strings.TrimSpace(projection[end+2:])
			continue
		}
		break
	}
	projection = strings.TrimSuffix(projection, ";")
	upper := strings.ToUpper(projection)
	if projection == "" || strings.Contains(projection, ";") || (!strings.HasPrefix(upper, "SELECT ") && !strings.HasPrefix(upper, "SELECT\n") && !strings.HasPrefix(upper, "WITH ") && !strings.HasPrefix(upper, "WITH\n")) {
		return "", ErrConfiguration
	}
	return `WITH projected AS (` + projection + `)
SELECT id, environment, tenant, order_id, partner, placed_at, link, kind, traffic, origin, tenant_class
FROM projected
WHERE placed_at >= $1 AND placed_at <= $2
AND ($3::timestamptz IS NULL OR placed_at > $3 OR (placed_at = $3 AND id COLLATE "C" > $4 COLLATE "C"))
ORDER BY placed_at, id COLLATE "C" LIMIT $5`, nil
}

func (p *Postgres) Close() { p.pool.Close() }

// Page applies a fixed sweep cutoff and a keyset cursor. A new sweep starts with
// an empty cursor at the retained activation boundary, capturing late commits.
func (p *Postgres) Page(ctx context.Context, activation, upper time.Time, cursor core.Cursor, limit int) ([]core.Event, error) {
	if activation.IsZero() || upper.Before(activation) || limit < 1 || limit > 1000 || (cursor.ID == "") != cursor.PlacedAt.IsZero() {
		return nil, ErrPage
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, ErrUnavailable
	}
	defer func() {
		rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer rollbackCancel()
		_ = tx.Rollback(rollbackCtx)
	}()
	if _, err = tx.Exec(ctx, "SET LOCAL statement_timeout = '10s'"); err != nil {
		return nil, ErrUnavailable
	}
	var after any
	if !cursor.PlacedAt.IsZero() {
		after = cursor.PlacedAt
	}
	rows, err := tx.Query(ctx, p.query, activation, upper, after, cursor.ID, limit)
	if err != nil {
		return nil, ErrProjection
	}
	defer rows.Close()
	events := make([]core.Event, 0, limit)
	for rows.Next() {
		var e core.Event
		var fields [10]pgtype.Text
		var placed pgtype.Timestamptz
		err = rows.Scan(&fields[0], &fields[1], &fields[2], &fields[3], &fields[4], &placed, &fields[5], &fields[6], &fields[7], &fields[8], &fields[9])
		if err != nil || !fields[0].Valid || fields[0].String == "" || !fields[2].Valid || fields[2].String == "" || !fields[3].Valid || fields[3].String == "" || !placed.Valid || placed.InfinityModifier != pgtype.Finite || placed.Time.IsZero() {
			return nil, ErrProjection
		}
		e.ID = fields[0].String
		e.Environment = fields[1].String
		e.Tenant = fields[2].String
		e.OrderID = fields[3].String
		e.Partner = fields[4].String
		e.PlacedAt = placed.Time
		e.Link = fields[5].String
		e.Kind = fields[6].String
		e.Traffic = fields[7].String
		e.Origin = fields[8].String
		e.TenantClass = fields[9].String
		events = append(events, e)
	}
	if rows.Err() != nil {
		return nil, ErrProjection
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, ErrUnavailable
	}
	return events, nil
}

// ValidateProjection checks local projection shape without a connection.
// PostgreSQL type and syntax validation still requires a read-only preflight.
func ValidateProjection(query string) error { _, err := pageQuery(query); return err }
