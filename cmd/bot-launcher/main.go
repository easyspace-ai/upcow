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
	"os/signal"
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

	// 从 badger 读取环境变量（env/* 键值对）
	envVars := loadEnvFromBadger(ss)

	if err := startBotWithMemfd(*botBin, runtimeYAML, envVars); err != nil {
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

func loadEnvFromBadger(ss *secretstore.Store) map[string]string {
	envVars := make(map[string]string)
	if ss == nil {
		return envVars
	}
	// 从 badger 读取所有 env/* 键值对
	allEnv, err := ss.GetAllWithPrefix("env/")
	if err != nil {
		// 如果遍历失败，fallback 到读取常见键
		envKeys := []string{
			"BUILDER_API_KEY",
			"BUILDER_SECRET",
			"BUILDER_PASS_PHRASE",
			"POLYMARKET_RELAYER_URL",
			"RPC_URL",
			"HTTP_PROXY",
			"HTTPS_PROXY",
		}
		for _, key := range envKeys {
			if v, ok, err := ss.GetString("env/" + key); err == nil && ok && strings.TrimSpace(v) != "" {
				envVars[key] = strings.TrimSpace(v)
			}
		}
		return envVars
	}
	// 转换 "env/KEY" -> "KEY"
	for key, value := range allEnv {
		if strings.HasPrefix(key, "env/") {
			envKey := strings.TrimPrefix(key, "env/")
			if strings.TrimSpace(envKey) != "" && strings.TrimSpace(value) != "" {
				envVars[envKey] = strings.TrimSpace(value)
			}
		}
	}
	return envVars
}

func startBotWithMemfd(botBin string, cfgYAML string, envVars map[string]string) error {
	botBin = strings.TrimSpace(botBin)
	if botBin == "" {
		return fmt.Errorf("bot-bin is empty")
	}

	var cmd *exec.Cmd
	var tempConfigPath string

	if runtime.GOOS != "linux" {
		// fallback: temp file (best-effort cleanup)
		tmpDir := os.TempDir()
		p := filepath.Join(tmpDir, fmt.Sprintf("gobet-config-%d.yaml", time.Now().UnixNano()))
		if err := os.WriteFile(p, []byte(cfgYAML+"\n"), 0o600); err != nil {
			return err
		}
		tempConfigPath = p
		defer func() {
			if tempConfigPath != "" {
				os.Remove(tempConfigPath)
			}
		}()
		cmd = exec.Command(botBin, "-config", p)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	} else {
		fd, err := createMemfd("gobet-config")
		if err != nil {
			return err
		}
		cfgFile := os.NewFile(uintptr(fd), "gobet-config")
		if cfgFile == nil {
			_ = syscall.Close(fd)
			return fmt.Errorf("memfd: os.NewFile failed")
		}
		defer cfgFile.Close()

		if _, err := io.WriteString(cfgFile, cfgYAML+"\n"); err != nil {
			return err
		}
		if _, err := cfgFile.Seek(0, 0); err != nil {
			return err
		}

		cmd = exec.Command(botBin)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		idx := len(cmd.ExtraFiles)
		cmd.ExtraFiles = append(cmd.ExtraFiles, cfgFile)
		childFD := 3 + idx
		cfgPath := fmt.Sprintf("/proc/self/fd/%d", childFD)
		cmd.Args = []string{botBin, "-config", cfgPath}
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}

	// 设置环境变量：继承当前环境，并添加从 badger 读取的变量
	cmd.Env = os.Environ()
	for k, v := range envVars {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	// 启动 bot 进程
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 bot 失败: %w", err)
	}

	// 设置信号处理：优雅关闭 bot
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)

	// 等待 bot 退出或收到信号
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case sig := <-sigCh:
		fmt.Fprintf(os.Stderr, "\n收到信号 %v，正在关闭 bot...\n", sig)
		// 发送信号到 bot 进程组
		if cmd.Process != nil {
			pid := cmd.Process.Pid
			if runtime.GOOS == "linux" {
				// 发送信号到整个进程组
				_ = syscall.Kill(-pid, syscall.SIGTERM)
			} else {
				// macOS: 发送信号到进程
				_ = cmd.Process.Signal(sig)
			}
			// 等待进程退出（最多 5 秒）
			select {
			case <-time.After(5 * time.Second):
				fmt.Fprintf(os.Stderr, "bot 未在 5 秒内退出，强制终止...\n")
				if runtime.GOOS == "linux" {
					_ = syscall.Kill(-pid, syscall.SIGKILL)
				} else {
					_ = cmd.Process.Kill()
				}
			case <-done:
				// bot 已退出
			}
		}
		return fmt.Errorf("被信号中断: %v", sig)
	case err := <-done:
		return err
	}
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
