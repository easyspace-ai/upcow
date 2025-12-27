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
	"strings"
)

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
