// Package state owns transactional bbolt delivery state. Network IO never runs in a transaction.
package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/jamesonstone/outpost/internal/core"
	bolt "go.etcd.io/bbolt"
)

var ErrPolicy = errors.New("state policy mismatch")
var ErrClaim = errors.New("stale delivery claim")
var buckets = []string{"meta", "deliveries", "due", "attempts", "exceptions"}

type Store struct {
	db     *bolt.DB
	policy core.Policy
}

func Open(path string, p core.Policy) (*Store, error) {
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, errors.New("state open failed")
	}
	s := &Store{db: db, policy: p}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, name := range buckets {
			if _, err := tx.CreateBucketIfNotExists([]byte(name)); err != nil {
				return err
			}
		}
		meta := tx.Bucket([]byte("meta"))
		v := meta.Get([]byte("version"))
		if v != nil && string(v) != "1" {
			return errors.New("unsupported state version")
		}
		b, _ := json.Marshal(p)
		old := meta.Get([]byte("policy"))
		if old != nil && !bytes.Equal(old, b) {
			return ErrPolicy
		}
		if err := meta.Put([]byte("policy"), b); err != nil {
			return err
		}
		return meta.Put([]byte("version"), []byte("1"))
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error { return s.db.Close() }
func get(b *bolt.Bucket, key string, v any) error {
	raw := b.Get([]byte(key))
	if raw == nil {
		return nil
	}
	return json.Unmarshal(raw, v)
}
func put(b *bolt.Bucket, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return b.Put([]byte(key), raw)
}
func dueKey(at time.Time, key string) string {
	return at.UTC().Format("20060102T150405.000000000") + "/" + key
}
func save(tx *bolt.Tx, d core.Delivery) error {
	if err := put(tx.Bucket([]byte("deliveries")), d.Key, d); err != nil {
		return err
	}
	if d.Status == "pending" {
		return tx.Bucket([]byte("due")).Put([]byte(dueKey(d.Due, d.Key)), []byte(d.Key))
	}
	if d.Status == "sending" {
		return tx.Bucket([]byte("due")).Put([]byte(dueKey(d.Lease, d.Key)), []byte(d.Key))
	}
	return nil
}
func removeDue(tx *bolt.Tx, d core.Delivery) error {
	at := d.Due
	if d.Status == "sending" {
		at = d.Lease
	}
	return tx.Bucket([]byte("due")).Delete([]byte(dueKey(at, d.Key)))
}
func (s *Store) Scan() (scan core.Scan, err error) {
	err = s.db.View(func(tx *bolt.Tx) error { return get(tx.Bucket([]byte("meta")), "scan", &scan) })
	return
}
func (s *Store) BeginSweep(now time.Time) (scan core.Scan, err error) {
	err = s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("meta"))
		if err := get(b, "scan", &scan); err != nil {
			return err
		}
		if scan.Upper.IsZero() {
			scan.Upper = now.UTC()
			return put(b, "scan", scan)
		}
		return nil
	})
	return
}

// ApplyPage saves logical deliveries and the scan checkpoint in one transaction.
func (s *Store) ApplyPage(events []core.Event, next core.Scan, now time.Time) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("deliveries"))
		for _, e := range events {
			ok, reason := s.policy.Eligibility(e)
			if !ok {
				if reason != "before_activation" && reason != "excluded" {
					key := s.policy.Key(e)
					if err := put(tx.Bucket([]byte("exceptions")), key, reason); err != nil {
						return err
					}
				}
				continue
			}
			key := s.policy.Key(e)
			var old core.Delivery
			if err := get(b, key, &old); err != nil {
				return err
			}
			if old.Key != "" {
				if old.Hash != core.PayloadHash(e) {
					old.Conflicted = true
					if old.Status == "pending" {
						if err := removeDue(tx, old); err != nil {
							return err
						}
						old.Status = "conflict"
					}
					if err := save(tx, old); err != nil {
						return err
					}
					if err := put(tx.Bucket([]byte("exceptions")), key, "payload_conflict"); err != nil {
						return err
					}
				}
				continue
			}
			d := core.Delivery{Key: key, Event: e, Hash: core.PayloadHash(e), Status: "pending", Due: now.UTC()}
			if err := save(tx, d); err != nil {
				return err
			}
		}
		return put(tx.Bucket([]byte("meta")), "scan", next)
	})
}
func record(tx *bolt.Tx, d core.Delivery) error {
	return put(tx.Bucket([]byte("attempts")), fmt.Sprintf("%s/%020d", d.Key, d.Attempts), d.Last)
}

// Backup writes a transactionally consistent snapshot to a new destination file.
func (s *Store) Backup(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	return s.db.View(func(tx *bolt.Tx) error {
		if _, err := tx.WriteTo(f); err != nil {
			return err
		}
		return f.Sync()
	})
}
