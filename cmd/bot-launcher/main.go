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

	fmt.Fprintf(os.Stderr, "[1/8] 检查命令行参数...\n")
	if strings.TrimSpace(*cfgPath) == "" {
		fatalWithStep("参数检查", fmt.Errorf("-config is required"))
	}
	if strings.TrimSpace(*accountID) == "" {
		fatalWithStep("参数检查", fmt.Errorf("-id is required (3 digits)"))
	}
	id := strings.TrimSpace(*accountID)
	if err := validateAccountID(id); err != nil {
		fatalWithStep("参数验证", err)
	}
	fmt.Fprintf(os.Stderr, "  ✓ config: %s\n", *cfgPath)
	fmt.Fprintf(os.Stderr, "  ✓ account_id: %s\n", id)
	fmt.Fprintf(os.Stderr, "  ✓ badger_db: %s\n", *secretDB)
	fmt.Fprintf(os.Stderr, "  ✓ dry_run: %v\n", *dryRun)

	fmt.Fprintf(os.Stderr, "[2/8] 解析 secret key...\n")
	hasSecretKey := strings.TrimSpace(*secretKey) != ""
	if !hasSecretKey {
		fmt.Fprintf(os.Stderr, "  ⚠ GOBET_SECRET_KEY 环境变量未设置，尝试从 badger 读取（如果数据库未加密）\n")
	}
	keyBytes, err := secretstore.ParseKey(*secretKey)
	if err != nil {
		fatalWithStep("解析 secret key", fmt.Errorf("secret key 格式错误: %w", err))
	}
	if keyBytes == nil {
		fmt.Fprintf(os.Stderr, "  ⚠ secret key 为空，尝试以只读模式打开未加密的 badger 数据库\n")
	} else {
		fmt.Fprintf(os.Stderr, "  ✓ secret key 已提供（长度: %d 字节）\n", len(keyBytes))
	}

	fmt.Fprintf(os.Stderr, "[3/8] 打开 badger 数据库...\n")
	fmt.Fprintf(os.Stderr, "  路径: %s\n", *secretDB)
	fmt.Fprintf(os.Stderr, "  只读模式: true\n")
	fmt.Fprintf(os.Stderr, "  使用加密: %v\n", keyBytes != nil)
	
	// 检查文件是否存在和权限
	if fileInfo, err := os.Stat(*secretDB); err != nil {
		if os.IsNotExist(err) {
			fatalWithStep("打开 badger 数据库", fmt.Errorf("数据库文件不存在: %s", *secretDB))
		}
		fatalWithStep("打开 badger 数据库", fmt.Errorf("无法访问数据库文件 %s: %w", *secretDB, err))
	} else {
		fmt.Fprintf(os.Stderr, "  文件大小: %d 字节\n", fileInfo.Size())
		fmt.Fprintf(os.Stderr, "  文件权限: %s\n", fileInfo.Mode().String())
		fmt.Fprintf(os.Stderr, "  操作系统: %s\n", runtime.GOOS)
	}
	
	// 检查是否是目录（badger 数据库是一个目录）
	if fileInfo, err := os.Stat(*secretDB); err == nil && fileInfo.IsDir() {
		fmt.Fprintf(os.Stderr, "  检测到数据库目录，检查内部文件...\n")
		entries, err := os.ReadDir(*secretDB)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ⚠ 无法读取数据库目录内容: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "  数据库目录包含 %d 个文件/目录\n", len(entries))
			if len(entries) > 0 {
				fmt.Fprintf(os.Stderr, "  示例文件: %s\n", entries[0].Name())
			}
		}
	}
	// 检查文件系统类型（macOS 特定）
	if runtime.GOOS == "darwin" {
		fmt.Fprintf(os.Stderr, "  检查文件系统类型...\n")
		// 尝试获取文件系统信息
		if absPath, err := filepath.Abs(*secretDB); err == nil {
			// 使用 diskutil 命令检查文件系统（macOS）
			// 先获取挂载点
			cmd := exec.Command("df", absPath)
			if output, err := cmd.Output(); err == nil {
				lines := strings.Split(string(output), "\n")
				if len(lines) > 1 {
					// 第二行包含文件系统信息
					fields := strings.Fields(lines[1])
					if len(fields) > 0 {
						mountPoint := fields[len(fields)-1]
						// 使用 diskutil 获取文件系统类型
						cmd2 := exec.Command("diskutil", "info", mountPoint)
						if output2, err2 := cmd2.Output(); err2 == nil {
							outputStr := string(output2)
							if strings.Contains(outputStr, "APFS") {
								fmt.Fprintf(os.Stderr, "  ✓ 文件系统: APFS (支持 badger 加密)\n")
							} else if strings.Contains(outputStr, "HFS") {
								fmt.Fprintf(os.Stderr, "  ⚠ 文件系统: HFS+ (可能不支持 badger 加密)\n")
							} else {
								fmt.Fprintf(os.Stderr, "  ⚠ 文件系统: 未知，请检查是否支持 badger 加密\n")
							}
						}
					}
				}
			}
		}
	}
	
	ss, err := secretstore.Open(secretstore.OpenOptions{
		Path:          *secretDB,
		EncryptionKey: keyBytes,
		ReadOnly:      true,
	})
	if err != nil {
		// 直接使用 secretstore 返回的详细错误信息
		fatalWithStep("打开 badger 数据库", err)
	}
	defer ss.Close()
	fmt.Fprintf(os.Stderr, "  ✓ badger 数据库打开成功\n")

	fmt.Fprintf(os.Stderr, "[4/8] 从 badger 读取 mnemonic...\n")
	mn, ok, err := ss.GetString("mnemonic")
	if err != nil {
		fatalWithStep("读取 mnemonic", fmt.Errorf("读取 mnemonic 失败: %w", err))
	}
	if !ok || strings.TrimSpace(mn) == "" {
		fatalWithStep("读取 mnemonic", fmt.Errorf("mnemonic 未在 badger 中找到 (key=mnemonic)\n提示: 使用 cmd/mnemonic-init 初始化 mnemonic"))
	}
	mn = strings.TrimSpace(mn)
	fmt.Fprintf(os.Stderr, "  ✓ mnemonic 读取成功（长度: %d 字符）\n", len(mn))

	fmt.Fprintf(os.Stderr, "[5/8] 派生钱包地址...\n")
	path := derivationPathFromAccountID(id)
	fmt.Fprintf(os.Stderr, "  derivation_path: %s\n", path)
	derived, err := deriveWalletFromMnemonic(mn, path)
	if err != nil {
		fatalWithStep("派生钱包", err)
	}
	fmt.Fprintf(os.Stderr, "  ✓ EOA 地址: %s\n", derived.EOAAddress)
	fmt.Fprintf(os.Stderr, "  ✓ 私钥已派生（长度: %d 字符）\n", len(derived.PrivateKeyHex))

	fmt.Fprintf(os.Stderr, "[6/8] 获取 funder 地址...\n")
	funderAddr := strings.TrimSpace(*funder)
	if funderAddr != "" {
		fmt.Fprintf(os.Stderr, "  使用命令行参数提供的 funder: %s\n", funderAddr)
	} else if strings.TrimSpace(*accountsDB) != "" {
		fmt.Fprintf(os.Stderr, "  从 accounts 数据库查询: %s\n", *accountsDB)
		if v, err := lookupFunderFromDB(*accountsDB, id); err == nil && strings.TrimSpace(v) != "" {
			funderAddr = strings.TrimSpace(v)
			fmt.Fprintf(os.Stderr, "  ✓ 从数据库查询到 funder: %s\n", funderAddr)
		} else {
			fmt.Fprintf(os.Stderr, "  ⚠ 数据库查询失败或未找到，将从 relayer API 获取\n")
		}
	}
	if funderAddr == "" {
		fmt.Fprintf(os.Stderr, "  从 relayer API 获取 expected safe 地址...\n")
		chainID := big.NewInt(137)
		rc := sdkrelayer.NewClient("https://relayer-v2.polymarket.com", chainID, nil, nil)
		safeAddr, err := rc.GetExpectedSafe(derived.EOAAddress)
		if err != nil {
			fatalWithStep("获取 funder 地址", fmt.Errorf("从 relayer API 获取 expected safe 失败: %w", err))
		}
		funderAddr = safeAddr
		fmt.Fprintf(os.Stderr, "  ✓ 从 relayer API 获取到 funder: %s\n", funderAddr)
	}

	if *dryRun {
		fmt.Fprintf(os.Stderr, "[7/8] Dry-run 模式，输出派生信息...\n")
		fmt.Println("account_id:", id)
		fmt.Println("derivation_path:", path)
		fmt.Println("eoa_address:", derived.EOAAddress)
		fmt.Println("private_key_hex:", derived.PrivateKeyHex)
		fmt.Println("funder_address:", funderAddr)
		fmt.Fprintf(os.Stderr, "[8/8] ✓ Dry-run 完成\n")
		return
	}

	fmt.Fprintf(os.Stderr, "[7/8] 读取并注入钱包配置...\n")
	fmt.Fprintf(os.Stderr, "  读取配置文件: %s\n", *cfgPath)
	if _, err := os.Stat(*cfgPath); os.IsNotExist(err) {
		fatalWithStep("读取配置文件", fmt.Errorf("配置文件不存在: %s", *cfgPath))
	}
	baseCfgBytes, err := os.ReadFile(*cfgPath)
	if err != nil {
		fatalWithStep("读取配置文件", fmt.Errorf("无法读取配置文件 %s: %w", *cfgPath, err))
	}
	baseYAML := string(baseCfgBytes)
	fmt.Fprintf(os.Stderr, "  ✓ 配置文件读取成功（大小: %d 字节）\n", len(baseCfgBytes))

	fmt.Fprintf(os.Stderr, "  注入钱包信息到配置...\n")
	runtimeYAML, err := injectWalletIntoConfig(baseYAML, derived.PrivateKeyHex, funderAddr)
	if err != nil {
		fatalWithStep("注入钱包配置", fmt.Errorf("注入钱包信息失败: %w", err))
	}
	fmt.Fprintf(os.Stderr, "  ✓ 钱包信息已注入\n")

	fmt.Fprintf(os.Stderr, "  验证完整配置...\n")
	if err := validateFullConfig(runtimeYAML); err != nil {
		fatalWithStep("验证配置", fmt.Errorf("配置验证失败: %w", err))
	}
	fmt.Fprintf(os.Stderr, "  ✓ 配置验证通过\n")

	fmt.Fprintf(os.Stderr, "[8/8] 启动 bot 进程...\n")
	// 从 badger 读取环境变量（env/* 键值对）
	envVars := loadEnvFromBadger(ss)
	fmt.Fprintf(os.Stderr, "  从 badger 加载了 %d 个环境变量\n", len(envVars))
	fmt.Fprintf(os.Stderr, "  bot 可执行文件: %s\n", *botBin)

	if err := startBotWithMemfd(*botBin, runtimeYAML, envVars); err != nil {
		fatalWithStep("启动 bot", err)
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

func fatalWithStep(step string, err error) {
	fmt.Fprintf(os.Stderr, "\n❌ 错误发生在步骤: %s\n", step)
	fmt.Fprintf(os.Stderr, "错误详情: %v\n", err)
	fmt.Fprintf(os.Stderr, "\n提示: 检查上述步骤的配置和依赖项\n")
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

	fmt.Fprintf(os.Stderr, "  操作系统: %s\n", runtime.GOOS)
	if runtime.GOOS != "linux" {
		// fallback: temp file (best-effort cleanup)
		tmpDir := os.TempDir()
		p := filepath.Join(tmpDir, fmt.Sprintf("gobet-config-%d.yaml", time.Now().UnixNano()))
		fmt.Fprintf(os.Stderr, "  使用临时文件传递配置: %s\n", p)
		if err := os.WriteFile(p, []byte(cfgYAML+"\n"), 0o600); err != nil {
			return fmt.Errorf("写入临时配置文件失败: %w", err)
		}
		tempConfigPath = p
		defer func() {
			if tempConfigPath != "" {
				if err := os.Remove(tempConfigPath); err != nil {
					fmt.Fprintf(os.Stderr, "  警告: 清理临时文件失败: %v\n", err)
				}
			}
		}()
		cmd = exec.Command(botBin, "-config", p)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		fmt.Fprintf(os.Stderr, "  ✓ 临时配置文件已创建\n")
	} else {
		fmt.Fprintf(os.Stderr, "  使用 memfd 传递配置\n")
		fd, err := createMemfd("gobet-config")
		if err != nil {
			return fmt.Errorf("创建 memfd 失败: %w", err)
		}
		cfgFile := os.NewFile(uintptr(fd), "gobet-config")
		if cfgFile == nil {
			_ = syscall.Close(fd)
			return fmt.Errorf("memfd: os.NewFile failed")
		}
		defer cfgFile.Close()

		if _, err := io.WriteString(cfgFile, cfgYAML+"\n"); err != nil {
			return fmt.Errorf("写入 memfd 失败: %w", err)
		}
		if _, err := cfgFile.Seek(0, 0); err != nil {
			return fmt.Errorf("memfd seek 失败: %w", err)
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
		fmt.Fprintf(os.Stderr, "  ✓ memfd 已创建 (fd=%d)\n", childFD)
	}

	// 设置环境变量：继承当前环境，并添加从 badger 读取的变量
	cmd.Env = os.Environ()
	for k, v := range envVars {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	fmt.Fprintf(os.Stderr, "  环境变量总数: %d (继承 %d + badger %d)\n", len(cmd.Env), len(os.Environ()), len(envVars))

	// 启动 bot 进程
	fmt.Fprintf(os.Stderr, "  启动命令: %s %v\n", botBin, cmd.Args)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 bot 失败: %w\n提示: 检查 bot 可执行文件路径是否正确，或使用 -bot-bin 参数指定", err)
	}
	fmt.Fprintf(os.Stderr, "  ✓ bot 进程已启动 (PID: %d)\n", cmd.Process.Pid)

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
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return "", fmt.Errorf("accounts 数据库文件不存在: %s", dbPath)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return "", fmt.Errorf("打开 accounts 数据库失败: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	row := db.QueryRowContext(ctx, `SELECT funder_address FROM accounts WHERE id=?`, accountID)
	var fa string
	if err := row.Scan(&fa); err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("account id %s 在数据库中未找到", accountID)
		}
		return "", fmt.Errorf("查询 accounts 表失败: %w", err)
	}
	return fa, nil
}
