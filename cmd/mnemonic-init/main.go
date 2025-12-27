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
	"math/big"
	"os"
	"path/filepath"
	"strings"

	sdkrelayer "github.com/betbot/gobet/pkg/sdk/relayer"
	"github.com/betbot/gobet/pkg/secretstore"
	hdwallet "github.com/miguelmota/go-ethereum-hdwallet"
)

func main() {
	var (
		// init mode: write encrypted mnemonic file
		outPath = flag.String("out", getenv("GOBET_MNEMONIC_FILE", "data/mnemonic.enc"), "output file path for encrypted mnemonic (init mode)")
		force   = flag.Bool("force", false, "overwrite output file if exists (init mode)")

		// env / key management
		envPath      = flag.String("env", getenv("GOBET_ENV_FILE", ".env"), "env file path to write GOBET_MASTER_KEY/GOBET_MNEMONIC_FILE")
		writeEnv     = flag.Bool("write-env", true, "write GOBET_MASTER_KEY and GOBET_MNEMONIC_FILE to env file")
		genMasterKey = flag.Bool("gen-master-key", false, "generate a new random master key (hex) and use it for this run")

		// show mode: derive and print private key for a 3-digit account id
		showID   = flag.String("id", "", "3-digit account id to derive and print private key (show mode), e.g. 456")
		inPath   = flag.String("file", getenv("GOBET_MNEMONIC_FILE", "data/mnemonic.enc"), "encrypted mnemonic file path (legacy show mode)")
		showSafe = flag.Bool("show-safe", true, "also print expected safe(funder) address (show mode)")

		// badger secrets store (recommended)
		secretDBPath = flag.String("badger", getenv("GOBET_SECRET_DB", "data/secrets.badger"), "badger secrets db path (recommended)")
		importEnv    = flag.String("import-env", "", "import a .env file into badger under env/<KEY>")
	)
	flag.Parse()

	// If badger is used, key comes from GOBET_SECRET_KEY (or -gen-master-key will generate for legacy .env flow only).
	badgerKey, err := secretstore.ParseKey(os.Getenv("GOBET_SECRET_KEY"))
	if err != nil {
		fatal(err)
	}

	// Master key for encrypt/decrypt.
	masterKey, masterKeyHex, err := loadOrGenerateMasterKey(*genMasterKey)
	if err != nil {
		fatal(err)
	}

	// show mode: derive and print private key for given id
	if strings.TrimSpace(*showID) != "" {
		accountID := strings.TrimSpace(*showID)
		if err := validateAccountID(accountID); err != nil {
			fatal(err)
		}
		mn := ""
		// prefer badger
		if badgerKey != nil {
			ss, err := secretstore.Open(secretstore.OpenOptions{Path: *secretDBPath, EncryptionKey: badgerKey, ReadOnly: true})
			if err == nil {
				defer ss.Close()
				if v, ok, _ := ss.GetString("mnemonic"); ok {
					mn = strings.TrimSpace(v)
				}
			}
		}
		// fallback legacy file mode
		if mn == "" {
			encMnemonic, err := readFileTrim(*inPath)
			if err != nil {
				fatal(err)
			}
			mn, err = decryptFromString(masterKey, encMnemonic)
			if err != nil {
				fatal(fmt.Errorf("decrypt mnemonic failed: %w", err))
			}
		}
		path := derivationPathFromAccountID(accountID)
		derived, err := deriveWalletFromMnemonic(mn, path)
		if err != nil {
			fatal(err)
		}

		fmt.Println("account_id:", accountID)
		fmt.Println("derivation_path:", path)
		fmt.Println("eoa_address:", derived.EOAAddress)
		fmt.Println("private_key_hex:", derived.PrivateKeyHex)
		if *showSafe {
			chainID := big.NewInt(137)
			rc := sdkrelayer.NewClient("https://relayer-v2.polymarket.com", chainID, nil, nil)
			safeAddr, err := rc.GetExpectedSafe(derived.EOAAddress)
			if err != nil {
				fatal(err)
			}
			fmt.Println("expected_safe(funder_address):", safeAddr)
		}
		return
	}

	// If user asks to import .env into badger, do it (requires GOBET_SECRET_KEY).
	if strings.TrimSpace(*importEnv) != "" {
		if badgerKey == nil {
			fatal(fmt.Errorf("GOBET_SECRET_KEY is required for badger import"))
		}
		ss, err := secretstore.Open(secretstore.OpenOptions{Path: *secretDBPath, EncryptionKey: badgerKey, ReadOnly: false})
		if err != nil {
			fatal(err)
		}
		defer ss.Close()
		kv, err := parseDotEnvFile(*importEnv)
		if err != nil {
			fatal(err)
		}
		for k, v := range kv {
			_ = ss.SetString("env/"+k, v)
		}
		fmt.Fprintf(os.Stderr, "已导入 %d 项到 badger：%s（前缀 env/）\n", len(kv), *secretDBPath)
		return
	}

	// init mode: prompt mnemonic and write encrypted file
	// If GOBET_SECRET_KEY is set, we will prefer storing mnemonic into badger (encrypted at rest),
	// and NOT require writing plaintext secrets into .env.
	if badgerKey != nil {
		ss, err := secretstore.Open(secretstore.OpenOptions{Path: *secretDBPath, EncryptionKey: badgerKey, ReadOnly: false})
		if err != nil {
			fatal(err)
		}
		defer ss.Close()
		fmt.Fprintln(os.Stderr, "请输入助记词（12/15/18/21/24 个单词），输入完成后回车：")
		mn := strings.TrimSpace(readLine())
		if mn == "" {
			fatal(errors.New("mnemonic is empty"))
		}
		if err := ss.SetString("mnemonic", mn); err != nil {
			fatal(err)
		}
		fmt.Fprintf(os.Stderr, "已写入 badger：%s（key=mnemonic, encrypted-at-rest）\n", *secretDBPath)
		return
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

	if *writeEnv {
		// Best-effort write; do not print key unless generated in this run.
		if err := upsertEnvFile(*envPath, map[string]string{
			"GOBET_MASTER_KEY":    masterKeyHex,
			"GOBET_MNEMONIC_FILE": *outPath,
		}); err != nil {
			fatal(err)
		}
		fmt.Fprintf(os.Stderr, "已写入：%s（包含 GOBET_MASTER_KEY / GOBET_MNEMONIC_FILE）\n", *envPath)
	}
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

func loadOrGenerateMasterKey(gen bool) ([]byte, string, error) {
	if gen {
		b := make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, b); err != nil {
			return nil, "", err
		}
		hexKey := hex.EncodeToString(b)
		return b, hexKey, nil
	}

	raw := strings.TrimSpace(os.Getenv("GOBET_MASTER_KEY"))
	if raw == "" {
		return nil, "", errors.New("GOBET_MASTER_KEY is required (32 bytes, base64 or hex) or use -gen-master-key")
	}
	// try base64
	if b, err := base64.StdEncoding.DecodeString(raw); err == nil {
		if len(b) != 32 {
			return nil, "", fmt.Errorf("GOBET_MASTER_KEY base64 decoded length must be 32, got %d", len(b))
		}
		// If user provided base64, keep original for writing back.
		return b, raw, nil
	}
	// try hex
	raw = strings.TrimPrefix(raw, "0x")
	if b, err := hex.DecodeString(raw); err == nil {
		if len(b) != 32 {
			return nil, "", fmt.Errorf("GOBET_MASTER_KEY hex decoded length must be 32, got %d", len(b))
		}
		return b, raw, nil
	}
	return nil, "", errors.New("GOBET_MASTER_KEY must be base64(32 bytes) or hex(32 bytes)")
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

func readFileTrim(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "", fmt.Errorf("file is empty: %s", path)
	}
	return s, nil
}

func upsertEnvFile(path string, kv map[string]string) error {
	// read existing
	existing := ""
	if b, err := os.ReadFile(path); err == nil {
		existing = string(b)
	}
	lines := []string{}
	seen := map[string]bool{}
	for _, line := range strings.Split(existing, "\n") {
		l := strings.TrimRight(line, "\r")
		trim := strings.TrimSpace(l)
		if trim == "" || strings.HasPrefix(trim, "#") || !strings.Contains(l, "=") {
			lines = append(lines, l)
			continue
		}
		k := strings.SplitN(l, "=", 2)[0]
		k = strings.TrimSpace(k)
		if v, ok := kv[k]; ok {
			lines = append(lines, k+"="+v)
			seen[k] = true
			continue
		}
		lines = append(lines, l)
	}
	for k, v := range kv {
		if seen[k] {
			continue
		}
		if len(lines) > 0 && lines[len(lines)-1] != "" {
			// keep file nicely separated
		}
		lines = append(lines, k+"="+v)
	}
	out := strings.Join(lines, "\n")
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	// 0600: env file likely contains secrets
	return os.WriteFile(path, []byte(out), 0o600)
}

func parseDotEnvFile(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		l := strings.TrimSpace(strings.TrimRight(line, "\r"))
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		if !strings.Contains(l, "=") {
			continue
		}
		parts := strings.SplitN(l, "=", 2)
		k := strings.TrimSpace(parts[0])
		v := strings.TrimSpace(parts[1])
		if k == "" {
			continue
		}
		// strip optional quotes
		if len(v) >= 2 && ((v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'')) {
			v = v[1 : len(v)-1]
		}
		out[k] = v
	}
	return out, nil
}

func validateAccountID(id string) error {
	id = strings.TrimSpace(id)
	if len(id) != 3 {
		return fmt.Errorf("id must be 3 digits (e.g. 456)")
	}
	for _, c := range id {
		if c < '0' || c > '9' {
			return fmt.Errorf("id must be 3 digits (e.g. 456)")
		}
	}
	return nil
}

func derivationPathFromAccountID(id string) string {
	// "456" -> "m/44'/60'/4'/5/6"
	d0, d1, d2 := id[0], id[1], id[2]
	return fmt.Sprintf("m/44'/60'/%c'/%c/%c", d0, d1, d2)
}

type derivedWallet struct {
	PrivateKeyHex string
	EOAAddress    string
}

func deriveWalletFromMnemonic(mnemonic string, derivationPath string) (*derivedWallet, error) {
	mnemonic = strings.TrimSpace(mnemonic)
	derivationPath = strings.TrimSpace(derivationPath)
	if mnemonic == "" {
		return nil, fmt.Errorf("mnemonic is required")
	}
	if derivationPath == "" {
		return nil, fmt.Errorf("derivation_path is required")
	}

	// reuse same hdwallet implementation as server side
	w, err := hdwallet.NewFromMnemonic(mnemonic)
	if err != nil {
		return nil, fmt.Errorf("invalid mnemonic: %w", err)
	}
	path, err := hdwallet.ParseDerivationPath(derivationPath)
	if err != nil {
		return nil, fmt.Errorf("invalid derivation_path: %w", err)
	}
	acct, err := w.Derive(path, false)
	if err != nil {
		return nil, fmt.Errorf("derive failed: %w", err)
	}
	pk, err := w.PrivateKeyHex(acct)
	if err != nil {
		return nil, fmt.Errorf("private key failed: %w", err)
	}
	return &derivedWallet{
		PrivateKeyHex: pk,
		EOAAddress:    strings.ToLower(acct.Address.Hex()),
	}, nil
}
