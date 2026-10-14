package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"time"

	"github.com/jamesonstone/outpost/internal/core"
	bolt "go.etcd.io/bbolt"
)

var ErrMetadata = errors.New("invalid state metadata")

func initialize(tx *bolt.Tx, p core.Policy, now time.Time) (time.Time, error) {
	first, _ := tx.Cursor().First()
	fresh := first == nil
	if fresh {
		for _, name := range buckets {
			if _, err := tx.CreateBucket([]byte(name)); err != nil {
				return time.Time{}, err
			}
		}
	} else {
		for _, name := range buckets {
			if tx.Bucket([]byte(name)) == nil {
				return time.Time{}, ErrMetadata
			}
		}
	}
	meta := tx.Bucket([]byte("meta"))
	configured, err := json.Marshal(p)
	if err != nil {
		return time.Time{}, err
	}
	activation := p.Activation.UTC()
	if fresh {
		if p.NewStatePolicy == "from_startup" && now.After(activation) {
			activation = now.UTC()
		}
		if err := meta.Put([]byte("policy"), configured); err != nil {
			return time.Time{}, err
		}
		if err := meta.Put([]byte("version"), []byte("1")); err != nil {
			return time.Time{}, err
		}
	} else {
		version := meta.Get([]byte("version"))
		old := meta.Get([]byte("policy"))
		if version == nil || old == nil || !json.Valid(old) {
			return time.Time{}, ErrMetadata
		}
		if string(version) != "1" {
			return time.Time{}, errors.New("unsupported state version")
		}
		if !bytes.Equal(old, configured) {
			return time.Time{}, ErrPolicy
		}
		if raw := meta.Get([]byte("effective_activation")); raw != nil {
			activation = time.Time{}
			if err := json.Unmarshal(raw, &activation); err != nil || activation.Before(p.Activation) || activation.IsZero() {
				return time.Time{}, ErrMetadata
			}
			return activation, nil
		}
		// Original version-1 ledgers used configured activation. Migrate only that
		// known legacy policy; a missing cutoff for an opt-in ledger is corruption.
		if p.NewStatePolicy != "" {
			return time.Time{}, ErrMetadata
		}
	}
	if err := put(meta, "effective_activation", activation); err != nil {
		return time.Time{}, err
	}
	return activation, nil
}

// EffectiveActivation returns the persisted occurrence cutoff for source scanning.
func (s *Store) EffectiveActivation() (activation time.Time, err error) {
	err = s.db.View(func(tx *bolt.Tx) error {
		return get(tx.Bucket([]byte("meta")), "effective_activation", &activation)
	})
	return
}

// BeginRun establishes a fresh source baseline for the opt-in startup policy.
// Recorded deliveries remain eligible for retries even when older than the floor.
// Inspection and backup must open the ledger without calling BeginRun.
func (s *Store) BeginRun(now time.Time) error {
	if s.policy.NewStatePolicy == "" {
		return nil
	}
	if now.IsZero() {
		return errors.New("invalid startup clock")
	}
	var activation time.Time
	err := s.db.Update(func(tx *bolt.Tx) error {
		meta := tx.Bucket([]byte("meta"))
		if err := get(meta, "effective_activation", &activation); err != nil {
			return err
		}
		if activation.IsZero() || activation.Before(s.policy.Activation) {
			return ErrMetadata
		}
		if now.After(activation) {
			activation = now.UTC()
		}
		if err := put(meta, "effective_activation", activation); err != nil {
			return err
		}
		// An old checkpoint cannot represent coverage of this new runtime.
		return put(meta, "scan", core.Scan{})
	})
	if err == nil {
		s.policy.Activation = activation
	}
	return err
}
