package state

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/jamesonstone/outpost/internal/core"
	bolt "go.etcd.io/bbolt"
)

func (s *Store) Claim(now time.Time, lease time.Duration) (d core.Delivery, found bool, err error) {
	if lease <= 0 {
		return d, false, errors.New("invalid lease")
	}
	err = s.db.Update(func(tx *bolt.Tx) error {
		var gate time.Time
		if err := get(tx.Bucket([]byte("meta")), "dispatch_after", &gate); err != nil {
			return err
		}
		if now.Before(gate) {
			return nil
		}
		c := tx.Bucket([]byte("due")).Cursor()
		k, v := c.First()
		if k == nil {
			return nil
		}
		if string(k) > dueKey(now, "~") {
			return nil
		}
		if err := get(tx.Bucket([]byte("deliveries")), string(v), &d); err != nil {
			return err
		}
		if d.Key == "" {
			return errors.New("invalid due index")
		}
		if err := c.Delete(); err != nil {
			return err
		}
		if d.Status == "sending" {
			d.Ambiguous++
			d.Last.Completed = now
			d.Last.Outcome = "lease_expired"
			d.Last.Ambiguous = true
			if err := record(tx, d); err != nil {
				return err
			}
			if d.Conflicted {
				d.Status = "conflict"
				d.Token = ""
				d.Lease = time.Time{}
				return save(tx, d)
			}
		} else if d.Status != "pending" {
			return errors.New("invalid due status")
		}
		var token [16]byte
		if _, err := rand.Read(token[:]); err != nil {
			return err
		}
		d.Token = hex.EncodeToString(token[:])
		d.Status = "sending"
		d.Lease = now.Add(lease)
		d.Attempts++
		d.Last = core.Attempt{Started: now, Outcome: "in_flight"}
		if err := record(tx, d); err != nil {
			return err
		}
		found = true
		return save(tx, d)
	})
	return
}
func (s *Store) Finish(key, token string, receipt core.Receipt, result *core.SendError, now time.Time, retry time.Duration) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		var d core.Delivery
		if err := get(tx.Bucket([]byte("deliveries")), key, &d); err != nil {
			return err
		}
		if d.Status != "sending" || d.Token != token || !now.Before(d.Lease) {
			return ErrClaim
		}
		if err := removeDue(tx, d); err != nil {
			return err
		}
		d.Last.Completed = now
		d.Token = ""
		d.Lease = time.Time{}
		if result == nil {
			if receipt.Channel != s.policy.Channel || receipt.Timestamp == "" {
				return errors.New("invalid receipt")
			}
			d.Status = "delivered"
			d.Receipt = receipt
			d.Last.Outcome = "delivered"
		} else {
			if result.RetryAfter > 0 {
				var gate time.Time
				meta := tx.Bucket([]byte("meta"))
				if err := get(meta, "dispatch_after", &gate); err != nil {
					return err
				}
				until := now.Add(result.RetryAfter)
				if until.After(gate) {
					if err := put(meta, "dispatch_after", until); err != nil {
						return err
					}
				}
			}
			d.Last.Outcome = result.Category
			d.Last.Ambiguous = result.Ambiguous
			if result.Ambiguous {
				d.Ambiguous++
			}
			d.Status = "pending"
			d.Due = now.Add(retry)
			if result.Permanent {
				d.Status = "failed"
			}
			if d.Conflicted {
				d.Status = "conflict"
			}
		}
		if err := record(tx, d); err != nil {
			return err
		}
		return save(tx, d)
	})
}

// Retry preserves the original identity and payload and only requeues terminal failures.
func (s *Store) Retry(key string, now time.Time) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		var d core.Delivery
		if err := get(tx.Bucket([]byte("deliveries")), key, &d); err != nil {
			return err
		}
		if d.Status != "failed" || d.Conflicted {
			return errors.New("only failed deliveries may be retried")
		}
		d.Status = "pending"
		d.Due = now
		return save(tx, d)
	})
}
func (s *Store) List(limit int) (out []core.Delivery, err error) {
	if limit < 1 || limit > 1000 {
		return nil, errors.New("invalid inspection limit")
	}
	err = s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket([]byte("deliveries")).Cursor()
		for k, _ := c.First(); k != nil && len(out) < limit; k, _ = c.Next() {
			var d core.Delivery
			if err := get(tx.Bucket([]byte("deliveries")), string(k), &d); err != nil {
				return err
			}
			out = append(out, d)
		}
		return nil
	})
	return
}
func (s *Store) Attempts(key string) (out []core.Attempt, err error) {
	err = s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket([]byte("attempts")).Cursor()
		prefix := key + "/"
		for k, v := c.Seek([]byte(prefix)); k != nil && strings.HasPrefix(string(k), prefix); k, v = c.Next() {
			var a core.Attempt
			if err := decode(v, &a); err != nil {
				return err
			}
			out = append(out, a)
		}
		return nil
	})
	return
}
