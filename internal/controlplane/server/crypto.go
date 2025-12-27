package server

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func mnemonicFilePath() string {
	if v := strings.TrimSpace(os.Getenv("GOBET_MNEMONIC_FILE")); v != "" {
		return v
	}
	return filepath.Join("data", "mnemonic.enc")
}

func loadMnemonicFromFile(masterKey []byte) (string, error) {
	path := mnemonicFilePath()
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read mnemonic file failed (%s): %w", path, err)
	}
	enc := strings.TrimSpace(string(b))
	if enc == "" {
		return "", fmt.Errorf("mnemonic file is empty: %s", path)
	}
	mn, err := decryptFromString(masterKey, enc)
	if err != nil {
		return "", fmt.Errorf("decrypt mnemonic failed: %w", err)
	}
	mn = strings.TrimSpace(mn)
	if mn == "" {
		return "", fmt.Errorf("mnemonic decrypted to empty string")
	}
	return mn, nil
}

// loadMnemonicFromSecrets loads plaintext mnemonic from encrypted SecretStore (Badger).
// The Store must be opened with Badger encryption enabled to avoid plaintext at rest.
func (s *Server) loadMnemonicFromSecrets() (string, error) {
	if s == nil || s.secrets == nil {
		return "", errors.New("secrets store not configured")
	}
	mn, ok, err := s.secrets.GetString("mnemonic")
	if err != nil {
		return "", err
	}
	if !ok || strings.TrimSpace(mn) == "" {
		return "", fmt.Errorf("mnemonic not found in secrets store (key=mnemonic)")
	}
	return strings.TrimSpace(mn), nil
}

func (s *Server) loadMnemonic() (string, error) {
	// Prefer badger secrets store.
	if s != nil && s.secrets != nil {
		if mn, err := s.loadMnemonicFromSecrets(); err == nil {
			return mn, nil
		}
	}
	// Fallback legacy: encrypted mnemonic file + GOBET_MASTER_KEY
	mk, err := loadMasterKey()
	if err != nil {
		return "", err
	}
	return loadMnemonicFromFile(mk)
}

func loadMasterKey() ([]byte, error) {
	raw := strings.TrimSpace(os.Getenv("GOBET_MASTER_KEY"))
	if raw == "" {
		return nil, errors.New("GOBET_MASTER_KEY is required (32 bytes, base64 or hex)")
	}
	// try base64
	if b, err := base64.StdEncoding.DecodeString(raw); err == nil {
		if len(b) != 32 {
			return nil, fmt.Errorf("GOBET_MASTER_KEY base64 decoded length must be 32, got %d", len(b))
		}
		return b, nil
	}
	// try hex
	raw = strings.TrimPrefix(raw, "0x")
	if b, err := hex.DecodeString(raw); err == nil {
		if len(b) != 32 {
			return nil, fmt.Errorf("GOBET_MASTER_KEY hex decoded length must be 32, got %d", len(b))
		}
		return b, nil
	}
	return nil, errors.New("GOBET_MASTER_KEY must be base64(32 bytes) or hex(32 bytes)")
}

// encryptToString: returns base64(nonce|ciphertext)
func encryptToString(masterKey []byte, plaintext string) (string, error) {
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ct := gcm.Seal(nil, nonce, []byte(plaintext), nil)
	out := append(nonce, ct...)
	return base64.StdEncoding.EncodeToString(out), nil
}

func decryptFromString(masterKey []byte, enc string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(enc))
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("ciphertext too short")
	}
	nonce := raw[:gcm.NonceSize()]
	ct := raw[gcm.NonceSize():]
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}
