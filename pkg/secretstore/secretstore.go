package secretstore

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	badger "github.com/dgraph-io/badger/v4"
)

// Store is a small encrypted-at-rest KV wrapper (Badger).
// Note: encryption is provided by Badger options (value log + key registry), not by this wrapper.
type Store struct {
	db *badger.DB
}

type OpenOptions struct {
	Path          string
	EncryptionKey []byte // 32 bytes; if nil, DB is opened without encryption (not recommended)
	ReadOnly      bool
}

func Open(opts OpenOptions) (*Store, error) {
	if strings.TrimSpace(opts.Path) == "" {
		return nil, errors.New("secretstore: path is required")
	}
	bopts := badger.DefaultOptions(opts.Path).
		WithLogger(nil).
		WithReadOnly(opts.ReadOnly)
	if len(opts.EncryptionKey) > 0 {
		// Badger requires index cache for encrypted workloads
		// Default cache size: 100MB (100 * 1024 * 1024 bytes)
		bopts = bopts.
			WithEncryptionKey(opts.EncryptionKey).
			WithIndexCacheSize(100 << 20) // 100MB
	}
	db, err := badger.Open(bopts)
	if err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) GetString(key string) (string, bool, error) {
	if s == nil || s.db == nil {
		return "", false, errors.New("secretstore: not opened")
	}
	k := []byte(strings.TrimSpace(key))
	if len(k) == 0 {
		return "", false, errors.New("secretstore: key is empty")
	}
	var out string
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(k)
		if err != nil {
			if errors.Is(err, badger.ErrKeyNotFound) {
				return nil
			}
			return err
		}
		return item.Value(func(val []byte) error {
			out = string(val)
			return nil
		})
	})
	if err != nil {
		return "", false, err
	}
	if out == "" {
		// distinguish not found vs empty value by checking again
		found := false
		_ = s.db.View(func(txn *badger.Txn) error {
			_, err := txn.Get(k)
			found = err == nil
			return nil
		})
		if !found {
			return "", false, nil
		}
	}
	return out, true, nil
}

func (s *Store) SetString(key string, val string) error {
	if s == nil || s.db == nil {
		return errors.New("secretstore: not opened")
	}
	k := []byte(strings.TrimSpace(key))
	if len(k) == 0 {
		return errors.New("secretstore: key is empty")
	}
	v := []byte(val)
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set(k, v)
	})
}

// ParseKey expects 32 bytes (base64 or hex). Returns nil if input is empty.
func ParseKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	// Prefer hex if it looks like hex (64 hex chars = 32 bytes)
	// This avoids misinterpreting hex strings as base64
	rawHex := strings.TrimPrefix(raw, "0x")
	if len(rawHex) == 64 {
		// Check if it's valid hex
		if b, err := hex.DecodeString(rawHex); err == nil {
			if len(b) == 32 {
				return b, nil
			}
		}
	}
	// Try hex first (even if not 64 chars, might be valid hex)
	rawHex = strings.TrimPrefix(raw, "0x")
	if b, err := hex.DecodeString(rawHex); err == nil {
		if len(b) == 32 {
			return b, nil
		}
		return nil, fmt.Errorf("decoded key length must be 32, got %d", len(b))
	}
	// Try base64
	if b, err := base64.StdEncoding.DecodeString(raw); err == nil {
		if len(b) != 32 {
			return nil, fmt.Errorf("decoded key length must be 32, got %d", len(b))
		}
		return b, nil
	}
	return nil, errors.New("key must be base64(32 bytes) or hex(32 bytes)")
}
