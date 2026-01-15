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
		// 捕获原始错误信息
		originalErr := err.Error()

		// 包装错误以提供更多上下文信息
		errMsg := fmt.Sprintf("badger.Open 失败\n")
		errMsg += fmt.Sprintf("原始错误: %v\n", err)
		errMsg += fmt.Sprintf("错误类型: %T\n", err)

		if len(opts.EncryptionKey) > 0 {
			errMsg += fmt.Sprintf("\n数据库路径: %s\n", opts.Path)
			errMsg += fmt.Sprintf("加密模式: 是 (密钥长度: %d 字节)\n", len(opts.EncryptionKey))

			// 检查是否是 "invalid argument" 错误
			if strings.Contains(strings.ToLower(originalErr), "invalid argument") {
				errMsg += "\n⚠️  检测到 'invalid argument' 错误（常见于 macOS 加密数据库）\n"
				errMsg += "\n可能的原因:\n"
				errMsg += "  1. 加密密钥不匹配：数据库是用不同的密钥创建的\n"
				errMsg += "  2. macOS 文件系统限制：某些文件系统不支持 badger 加密\n"
				errMsg += "  3. 数据库文件损坏或不完整\n"
				errMsg += "\n建议的解决方案:\n"
				errMsg += "  1. 确认 GOBET_SECRET_KEY 与创建数据库时使用的密钥一致\n"
				errMsg += "  2. 检查数据库是否在 APFS 文件系统上（badger 加密需要 APFS）\n"
				errMsg += "  3. 如果密钥正确但仍失败，可能需要重新初始化数据库\n"
				errMsg += "  4. 尝试将数据库移动到 APFS 文件系统\n"
			} else {
				errMsg += "\n可能的原因:\n"
				errMsg += "  1. 加密密钥不正确（与创建数据库时使用的密钥不匹配）\n"
				errMsg += "  2. 数据库文件损坏\n"
				errMsg += "  3. 数据库文件权限问题\n"
				errMsg += "  4. 在 macOS 上，加密的 badger 数据库可能需要特定的文件系统支持\n"
			}
		} else {
			errMsg += fmt.Sprintf("\n数据库路径: %s\n", opts.Path)
			errMsg += "加密模式: 否\n"
		}
		return nil, fmt.Errorf("%s", errMsg)
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

// GetAllWithPrefix returns all key-value pairs with the given prefix
func (s *Store) GetAllWithPrefix(prefix string) (map[string]string, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("secretstore: not opened")
	}
	result := make(map[string]string)
	prefixBytes := []byte(prefix)
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefixBytes
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			key := string(item.Key())
			err := item.Value(func(val []byte) error {
				result[key] = string(val)
				return nil
			})
			if err != nil {
				return err
			}
		}
		return nil
	})
	return result, err
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
