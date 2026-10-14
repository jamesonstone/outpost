// Package core defines the minimal event and delivery contracts.
package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

type Event struct {
	ID          string    `json:"id"`
	Environment string    `json:"environment"`
	Tenant      string    `json:"tenant"`
	OrderID     string    `json:"order_id"`
	Partner     string    `json:"partner"`
	PlacedAt    time.Time `json:"placed_at"`
	Link        string    `json:"link"`
	Kind        string    `json:"kind"`
	Traffic     string    `json:"traffic"`
	Origin      string    `json:"origin"`
	TenantClass string    `json:"tenant_class"`
}

type Policy struct {
	Source         string    `json:"source"`
	Channel        string    `json:"channel"`
	Partner        string    `json:"partner"`
	Activation     time.Time `json:"activation"`
	NewStatePolicy string    `json:"new_state_policy,omitempty"`
}

type Cursor struct {
	PlacedAt time.Time `json:"placed_at"`
	ID       string    `json:"id"`
}

type Scan struct {
	Cursor        Cursor    `json:"cursor"`
	Upper         time.Time `json:"upper"`
	LastCompleted time.Time `json:"last_completed"`
}

type Source interface {
	Page(context.Context, time.Time, time.Time, Cursor, int) ([]Event, error)
}

type Receipt struct {
	Channel   string `json:"channel"`
	Timestamp string `json:"timestamp"`
}

// SendError carries only a safe category. Never persist upstream response bodies.
type SendError struct {
	Category   string
	Permanent  bool
	Ambiguous  bool
	RetryAfter time.Duration
}

func (e *SendError) Error() string { return e.Category }

type Destination interface {
	Send(context.Context, Event) (Receipt, error)
}

type Attempt struct {
	Started   time.Time `json:"started"`
	Completed time.Time `json:"completed"`
	Outcome   string    `json:"outcome"`
	Ambiguous bool      `json:"ambiguous"`
}

type Delivery struct {
	Key        string    `json:"key"`
	Event      Event     `json:"event"`
	Hash       string    `json:"hash"`
	Conflicted bool      `json:"conflicted"`
	Status     string    `json:"status"`
	Due        time.Time `json:"due"`
	Lease      time.Time `json:"lease"`
	Token      string    `json:"token"`
	Attempts   uint64    `json:"attempts"`
	Ambiguous  uint64    `json:"ambiguous"`
	Last       Attempt   `json:"last_attempt"`
	Receipt    Receipt   `json:"receipt"`
}

// Eligibility requires explicit evidence, rather than treating missing fields as production.
func (p Policy) Eligibility(e Event) (bool, string) {
	if e.PlacedAt.Before(p.Activation) {
		return false, "before_activation"
	}
	if e.ID == "" || e.OrderID == "" || e.Tenant == "" || e.PlacedAt.IsZero() {
		return false, "missing_identity"
	}
	if e.Environment == "" || e.Kind == "" || e.Traffic == "" || e.Origin == "" || e.TenantClass == "" || e.Partner == "" {
		return false, "unclassified"
	}
	if e.Environment != "production" || e.Kind != "first_placement" || e.Traffic != "production" || e.Origin != "partner_api" || e.TenantClass != "production" || e.Partner != p.Partner {
		return false, "excluded"
	}
	u, err := url.Parse(e.Link)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return false, "invalid_link"
	}
	for _, v := range []string{e.ID, e.Tenant, e.OrderID, e.Partner, e.Link} {
		if len(v) > 2048 || strings.ContainsAny(v, "\r\n\x00") {
			return false, "invalid_field"
		}
	}
	return true, ""
}
func (p Policy) Key(e Event) string {
	b, _ := json.Marshal([]string{p.Source, e.Environment, e.Tenant, e.OrderID, e.Kind, p.Channel})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// PayloadHash excludes publication identity; re-publications share the same logical delivery.
func PayloadHash(e Event) string {
	b, _ := json.Marshal([]string{e.OrderID, e.Partner, e.PlacedAt.UTC().Format(time.RFC3339Nano), e.Link})
	h := sha256.Sum256(b)
	return fmt.Sprintf("%x", h)
}
