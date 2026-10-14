package state

import (
	"encoding/json"
	"time"

	"github.com/jamesonstone/outpost/internal/core"
	bolt "go.etcd.io/bbolt"
)

func decode(raw []byte, v any) error { return json.Unmarshal(raw, v) }

type Stats struct {
	Pending              int       `json:"pending"`
	Sending              int       `json:"sending"`
	Delivered            int       `json:"delivered"`
	Failed               int       `json:"failed"`
	Conflicts            int       `json:"conflicts"`
	Exceptions           int       `json:"exceptions"`
	Attempts             uint64    `json:"attempts"`
	Ambiguous            uint64    `json:"ambiguous"`
	OldestPendingSeconds float64   `json:"oldest_pending_seconds"`
	LastSweep            time.Time `json:"last_sweep"`
}

func (s *Store) Stats(now time.Time) (out Stats, err error) {
	err = s.db.View(func(tx *bolt.Tx) error {
		var scan core.Scan
		if err := get(tx.Bucket([]byte("meta")), "scan", &scan); err != nil {
			return err
		}
		out.LastSweep = scan.LastCompleted
		out.Exceptions = tx.Bucket([]byte("exceptions")).Stats().KeyN
		return tx.Bucket([]byte("deliveries")).ForEach(func(_, v []byte) error {
			var d core.Delivery
			if err := decode(v, &d); err != nil {
				return err
			}
			switch d.Status {
			case "pending":
				out.Pending++
			case "sending":
				out.Sending++
			case "delivered":
				out.Delivered++
			case "failed":
				out.Failed++
			case "conflict":
			}
			if d.Conflicted {
				out.Conflicts++
			}
			out.Attempts += d.Attempts
			out.Ambiguous += d.Ambiguous
			if d.Status == "pending" || d.Status == "sending" {
				age := now.Sub(d.Event.PlacedAt).Seconds()
				if age > out.OldestPendingSeconds {
					out.OldestPendingSeconds = age
				}
			}
			return nil
		})
	})
	return
}
