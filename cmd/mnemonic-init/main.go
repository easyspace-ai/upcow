package main

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	var (
		outPath = flag.String("out", getenv("GOBET_MNEMONIC_FILE", "data/mnemonic.enc"), "output file path for encrypted mnemonic")
		force   = flag.Bool("force", false, "overwrite output file if exists")
	)
	flag.Parse()

	masterKey, err := loadMasterKey()
	if err != nil {
		fatal(err)
	}

	if st, err := os.Stat(*outPath); err == nil && !st.IsDir() && !*force {
		fatal(fmt.Errorf("output file already exists: %s (use -force to overwrite)", *outPath))
	}
	if err := os.MkdirAll(filepath.Dir(*outPath), 0o755); err != nil {
		fatal(fmt.Errorf("mkdir: %w", err))
	}

	fmt.Fprintln(os.Stderr, "请输入助记词（12/15/18/21/24 个单词），输入完成后回车：")
	mn := strings.TrimSpace(readLine())
	if mn == "" {
		fatal(errors.New("mnemonic is empty"))
	}

	enc, err := encryptToString(masterKey, mn)
	if err != nil {
		fatal(err)
	}

	// 0600: only owner can read
	if err := os.WriteFile(*outPath, []byte(enc+"\n"), 0o600); err != nil {
		fatal(err)
	}
	fmt.Fprintf(os.Stderr, "已写入：%s\n", *outPath)
}

func getenv(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func readLine() string {
	br := bufio.NewReader(os.Stdin)
	s, _ := br.ReadString('\n')
	return strings.TrimSpace(s)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err.Error())
	os.Exit(1)
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
