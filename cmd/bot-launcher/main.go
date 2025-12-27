package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	pkgconfig "github.com/betbot/gobet/pkg/config"
	sdkrelayer "github.com/betbot/gobet/pkg/sdk/relayer"
	"github.com/betbot/gobet/pkg/secretstore"
	_ "modernc.org/sqlite"

	hdwallet "github.com/miguelmota/go-ethereum-hdwallet"
	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"
)

func main() {
	var (
		cfgPath    = flag.String("config", "", "base bot config yaml path (without wallet.private_key)")
		accountID  = flag.String("id", "", "3-digit account id, e.g. 456")
		botBin     = flag.String("bot-bin", getenv("GOBET_BOT_BIN", "bot"), "bot executable (path or name in PATH)")
		secretDB   = flag.String("badger", getenv("GOBET_SECRET_DB", "data/secrets.badger"), "badger secrets db path")
		secretKey  = flag.String("secret-key", getenv("GOBET_SECRET_KEY", ""), "badger encryption key (32 bytes, base64 or hex)")
		funder     = flag.String("funder", "", "optional funder/safe address override (0x...)")
		accountsDB = flag.String("accounts-db", "", "optional sqlite db to lookup funder_address (accounts table), e.g. data/controlplane.db")
		dryRun     = flag.Bool("dry-run", false, "print derived info and exit without starting bot")
	)
	flag.Parse()

	if strings.TrimSpace(*cfgPath) == "" {
		fatal(fmt.Errorf("-config is required"))
	}
	if strings.TrimSpace(*accountID) == "" {
		fatal(fmt.Errorf("-id is required (3 digits)"))
	}
	id := strings.TrimSpace(*accountID)
	if err := validateAccountID(id); err != nil {
		fatal(err)
	}

	keyBytes, err := secretstore.ParseKey(*secretKey)
	if err != nil {
		fatal(err)
	}
	if keyBytes == nil {
		fatal(fmt.Errorf("secret key is required: set GOBET_SECRET_KEY or pass -secret-key"))
	}

	ss, err := secretstore.Open(secretstore.OpenOptions{
		Path:          *secretDB,
		EncryptionKey: keyBytes,
		ReadOnly:      true,
	})
	if err != nil {
		fatal(err)
	}
	defer ss.Close()

	mn, ok, err := ss.GetString("mnemonic")
	if err != nil {
		fatal(err)
	}
	if !ok || strings.TrimSpace(mn) == "" {
		fatal(fmt.Errorf("mnemonic not found in badger (key=mnemonic)"))
	}
	mn = strings.TrimSpace(mn)

	path := derivationPathFromAccountID(id)
	derived, err := deriveWalletFromMnemonic(mn, path)
	if err != nil {
		fatal(err)
	}

	funderAddr := strings.TrimSpace(*funder)
	if funderAddr == "" && strings.TrimSpace(*accountsDB) != "" {
		if v, err := lookupFunderFromDB(*accountsDB, id); err == nil && strings.TrimSpace(v) != "" {
			funderAddr = strings.TrimSpace(v)
		}
	}
	if funderAddr == "" {
		chainID := big.NewInt(137)
		rc := sdkrelayer.NewClient("https://relayer-v2.polymarket.com", chainID, nil, nil)
		safeAddr, err := rc.GetExpectedSafe(derived.EOAAddress)
		if err != nil {
			fatal(err)
		}
		funderAddr = safeAddr
	}

	if *dryRun {
		fmt.Println("account_id:", id)
		fmt.Println("derivation_path:", path)
		fmt.Println("eoa_address:", derived.EOAAddress)
		fmt.Println("private_key_hex:", derived.PrivateKeyHex)
		fmt.Println("funder_address:", funderAddr)
		return
	}

	baseCfgBytes, err := os.ReadFile(*cfgPath)
	if err != nil {
		fatal(err)
	}
	baseYAML := string(baseCfgBytes)

	runtimeYAML, err := injectWalletIntoConfig(baseYAML, derived.PrivateKeyHex, funderAddr)
	if err != nil {
		fatal(err)
	}
	if err := validateFullConfig(runtimeYAML); err != nil {
		fatal(fmt.Errorf("config invalid after wallet injection: %w", err))
	}

	if err := startBotWithMemfd(*botBin, runtimeYAML); err != nil {
		fatal(err)
	}
}

func getenv(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err.Error())
	os.Exit(1)
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
	return fmt.Sprintf("m/44'/60'/%c'/%c/%c", id[0], id[1], id[2])
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

func injectWalletIntoConfig(yamlText string, privateKeyHex string, funderAddress string) (string, error) {
	var m map[string]any
	if err := yaml.Unmarshal([]byte(yamlText), &m); err != nil {
		return "", err
	}
	w, ok := m["wallet"].(map[string]any)
	if !ok || w == nil {
		w = map[string]any{}
	}
	w["private_key"] = strings.TrimSpace(privateKeyHex)
	w["funder_address"] = strings.TrimSpace(funderAddress)
	m["wallet"] = w
	out, err := yaml.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func validateFullConfig(yamlText string) error {
	var cf pkgconfig.ConfigFile
	if err := yaml.Unmarshal([]byte(yamlText), &cf); err != nil {
		return err
	}
	kind := strings.TrimSpace(cf.Market.Kind)
	if kind == "" {
		kind = "updown"
	}
	cfg := &pkgconfig.Config{
		Wallet: pkgconfig.WalletConfig{
			PrivateKey:    strings.TrimSpace(cf.Wallet.PrivateKey),
			FunderAddress: strings.TrimSpace(cf.Wallet.FunderAddress),
		},
		Proxy:              nil,
		ExchangeStrategies: cf.ExchangeStrategies,
		Market: pkgconfig.MarketConfig{
			Symbol:        strings.TrimSpace(cf.Market.Symbol),
			Timeframe:     strings.TrimSpace(cf.Market.Timeframe),
			Kind:          kind,
			SlugPrefix:    strings.TrimSpace(cf.Market.SlugPrefix),
			SlugTemplates: cf.Market.SlugTemplates,
			Precision:     cf.Market.Precision,
		},
		LogLevel:       strings.TrimSpace(cf.LogLevel),
		LogFile:        strings.TrimSpace(cf.LogFile),
		LogByCycle:     cf.LogByCycle,
		PersistenceDir: strings.TrimSpace(cf.PersistenceDir),
		MinOrderSize:   cf.MinOrderSize,
		MinShareSize:   cf.MinShareSize,
		DryRun:         cf.DryRun,
	}
	return cfg.Validate()
}

func startBotWithMemfd(botBin string, cfgYAML string) error {
	botBin = strings.TrimSpace(botBin)
	if botBin == "" {
		return fmt.Errorf("bot-bin is empty")
	}
	if runtime.GOOS != "linux" {
		// fallback: temp file (best-effort cleanup)
		tmpDir := os.TempDir()
		p := filepath.Join(tmpDir, fmt.Sprintf("gobet-config-%d.yaml", time.Now().UnixNano()))
		if err := os.WriteFile(p, []byte(cfgYAML+"\n"), 0o600); err != nil {
			return err
		}
		defer os.Remove(p)
		cmd := exec.Command(botBin, "-config", p)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		return cmd.Run()
	}

	fd, err := unix.MemfdCreate("gobet-config", 0)
	if err != nil {
		return err
	}
	cfgFile := os.NewFile(uintptr(fd), "gobet-config")
	if cfgFile == nil {
		_ = unix.Close(fd)
		return fmt.Errorf("memfd: os.NewFile failed")
	}
	defer cfgFile.Close()

	if _, err := io.WriteString(cfgFile, cfgYAML+"\n"); err != nil {
		return err
	}
	if _, err := cfgFile.Seek(0, 0); err != nil {
		return err
	}

	cmd := exec.Command(botBin)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	idx := len(cmd.ExtraFiles)
	cmd.ExtraFiles = append(cmd.ExtraFiles, cfgFile)
	childFD := 3 + idx
	cfgPath := fmt.Sprintf("/proc/self/fd/%d", childFD)
	cmd.Args = []string{botBin, "-config", cfgPath}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	return cmd.Run()
}

func lookupFunderFromDB(dbPath string, accountID string) (string, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return "", err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	row := db.QueryRowContext(ctx, `SELECT funder_address FROM accounts WHERE id=?`, accountID)
	var fa string
	if err := row.Scan(&fa); err != nil {
		return "", err
	}
	return fa, nil
}
