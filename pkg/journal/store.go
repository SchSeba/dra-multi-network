// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package journal

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"
)

type Store struct{ db *bolt.DB }

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	db, err := bolt.Open(filepath.Join(dir, "state.db"), 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, fmt.Errorf("journal authority lock: %w", err)
	}
	if err := db.Update(func(tx *bolt.Tx) error { _, err := tx.CreateBucketIfNotExists([]byte("records-v1")); return err }); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	// Persist the database directory entry as well as bbolt's synchronous commits.
	d, err := os.Open(dir)
	if err != nil {
		return nil, errors.Join(err, db.Close())
	}
	err = d.Sync()
	err = errors.Join(err, d.Close())
	if err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return &Store{db: db}, nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Put(key string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error { return tx.Bucket([]byte("records-v1")).Put([]byte(key), data) })
}
func (s *Store) Get(key string, value any) (bool, error) {
	found := false
	err := s.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket([]byte("records-v1")).Get([]byte(key))
		if data == nil {
			return nil
		}
		found = true
		return json.Unmarshal(data, value)
	})
	return found, err
}
func (s *Store) List(prefix string) (map[string]json.RawMessage, error) {
	result := map[string]json.RawMessage{}
	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket([]byte("records-v1")).Cursor()
		for k, v := c.Seek([]byte(prefix)); k != nil && len(k) >= len(prefix) && string(k[:len(prefix)]) == prefix; k, v = c.Next() {
			result[string(k)] = append(json.RawMessage(nil), v...)
		}
		return nil
	})
	return result, err
}
