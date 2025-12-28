package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"

	pkgconfig "github.com/betbot/gobet/pkg/config"
	sdkrelayer "github.com/betbot/gobet/pkg/sdk/relayer"
	sdktypes "github.com/betbot/gobet/pkg/sdk/types"
	"github.com/ethereum/go-ethereum/crypto"
	"gopkg.in/yaml.v3"
	"strings"
)

func (s *Server) handleBotStart(w http.ResponseWriter, r *http.Request) {
	botID := chiURLParam(r, "botID")
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	log.Printf("[bot-start] bot_id=%s starting...", botID)
	pid, alreadyRunning, err := s.startBot(ctx, botID)
	if err != nil {
		log.Printf("[bot-start] bot_id=%s failed: %v", botID, err)
		if errors.Is(err, ErrBotNotFound) {
			writeError(w, 404, "bot not found")
			return
		}
		writeError(w, 500, fmt.Sprintf("start failed: %v", err))
		return
	}
	log.Printf("[bot-start] bot_id=%s success: pid=%d, already_running=%v", botID, pid, alreadyRunning)
	writeJSON(w, 200, map[string]any{"ok": true, "pid": pid, "already_running": alreadyRunning})
}

func (s *Server) handleBotStop(w http.ResponseWriter, r *http.Request) {
	botID := chiURLParam(r, "botID")
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	alreadyStopped, err := s.stopBot(ctx, botID)
	if err != nil {
		if errors.Is(err, ErrBotNotFound) {
			writeError(w, 404, "bot not found")
			return
		}
		writeError(w, 500, fmt.Sprintf("stop failed: %v", err))
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "already_stopped": alreadyStopped})
}

func (s *Server) handleBotRestart(w http.ResponseWriter, r *http.Request) {
	botID := chiURLParam(r, "botID")
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	if _, err := s.stopBot(ctx, botID); err != nil && !errors.Is(err, ErrBotNotFound) {
		writeError(w, 500, fmt.Sprintf("restart stop failed: %v", err))
		return
	}
	pid, _, err := s.startBot(ctx, botID)
	if err != nil {
		if errors.Is(err, ErrBotNotFound) {
			writeError(w, 404, "bot not found")
			return
		}
		writeError(w, 500, fmt.Sprintf("restart failed: %v", err))
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "pid": pid})
}

func (s *Server) startBot(ctx context.Context, botID string) (pid int, alreadyRunning bool, err error) {
	log.Printf("[start-bot] bot_id=%s starting...", botID)
	b, err := s.getBot(ctx, botID)
	if err != nil {
		log.Printf("[start-bot] bot_id=%s db get error: %v", botID, err)
		return 0, false, fmt.Errorf("db get: %w", err)
	}
	if b == nil {
		log.Printf("[start-bot] bot_id=%s not found", botID)
		return 0, false, ErrBotNotFound
	}

	// mark desired running for auto-restart
	_ = s.setDesiredRunning(ctx, botID, true)

	// 已在跑则直接返回
	p, _ := s.getBotProcess(ctx, botID)
	if p != nil && p.PID != nil && processAlive(*p.PID) {
		log.Printf("[start-bot] bot_id=%s already running: pid=%d", botID, *p.PID)
		return *p.PID, true, nil
	}

	// New security model: wallet is derived at start from local encrypted mnemonic file + bound account_id.
	if b.AccountID == nil || strings.TrimSpace(*b.AccountID) == "" {
		log.Printf("[start-bot] bot_id=%s error: account_id not bound", botID)
		return 0, false, fmt.Errorf("bot 未绑定账号：请先绑定 account_id（1账号1bot）")
	}
	accountID := strings.TrimSpace(*b.AccountID)
	log.Printf("[start-bot] bot_id=%s account_id=%s", botID, accountID)
	if _, err := normalizeAccountID(accountID); err != nil {
		log.Printf("[start-bot] bot_id=%s account_id invalid: %v", botID, err)
		return 0, false, fmt.Errorf("bot account_id invalid: %v", err)
	}
	a, err := s.getAccount(ctx, accountID)
	if err != nil || a == nil {
		log.Printf("[start-bot] bot_id=%s account not found: %s, err=%v", botID, accountID, err)
		return 0, false, fmt.Errorf("account not found: %s", accountID)
	}
	log.Printf("[start-bot] bot_id=%s loading mnemonic...", botID)
	mn, err := s.loadMnemonic()
	if err != nil {
		log.Printf("[start-bot] bot_id=%s load mnemonic failed: %v", botID, err)
		return 0, false, err
	}
	path, err := derivationPathFromAccountID(accountID)
	if err != nil {
		log.Printf("[start-bot] bot_id=%s derivation path error: %v", botID, err)
		return 0, false, err
	}
	log.Printf("[start-bot] bot_id=%s deriving wallet from path=%s", botID, path)
	derived, err := deriveWalletFromMnemonic(mn, path)
	if err != nil {
		log.Printf("[start-bot] bot_id=%s derive wallet failed: %v", botID, err)
		return 0, false, err
	}
	log.Printf("[start-bot] bot_id=%s eoa_address=%s, funder_address=%s", botID, derived.EOAAddress, a.FunderAddress)
	// Ensure expected safe is deployed (best-effort). This helps new wallets which never used polymarket.com.
	// If builder creds are missing, we proceed but bot may fail on relayer-required operations.
	if strings.TrimSpace(a.FunderAddress) != "" {
		log.Printf("[start-bot] bot_id=%s ensuring safe deployed...", botID)
		if warn, err := s.ensureSafeDeployed(derived.PrivateKeyHex, derived.EOAAddress, a.FunderAddress); err != nil {
			log.Printf("[start-bot] bot_id=%s ensure safe deployed failed: %v", botID, err)
			return 0, false, err
		} else if warn != "" {
			log.Printf("[start-bot] bot_id=%s safe deploy warning: %s", botID, warn)
			_ = s.clearBotPID(ctx, botID, nil, &warn)
		}
	}
	log.Printf("[start-bot] bot_id=%s injecting wallet into config...", botID)
	runtimeYAML, err := injectWalletIntoBotConfig(b.ConfigYAML, derived.PrivateKeyHex, a.FunderAddress, b.LogPath, b.PersistenceDir)
	if err != nil {
		log.Printf("[start-bot] bot_id=%s inject wallet failed: %v", botID, err)
		return 0, false, err
	}

	log.Printf("[start-bot] bot_id=%s spawning bot process...", botID)
	pid, err = s.spawnBotWithRuntimeConfig(*b, runtimeYAML)
	if err != nil {
		log.Printf("[start-bot] bot_id=%s spawn failed: %v", botID, err)
		_ = s.clearBotPID(ctx, botID, nil, ptrString(err.Error()))
		return 0, false, err
	}
	log.Printf("[start-bot] bot_id=%s spawned successfully: pid=%d", botID, pid)
	_ = s.setBotPID(ctx, botID, pid)
	return pid, false, nil
}

func (s *Server) stopBot(ctx context.Context, botID string) (alreadyStopped bool, err error) {
	b, err := s.getBot(ctx, botID)
	if err != nil {
		return false, fmt.Errorf("db get: %w", err)
	}
	if b == nil {
		return false, ErrBotNotFound
	}

	// mark desired stopped to avoid auto-restart
	_ = s.setDesiredRunning(ctx, botID, false)

	p, err := s.getBotProcess(ctx, botID)
	if err != nil {
		return false, fmt.Errorf("db get: %w", err)
	}
	if p == nil || p.PID == nil {
		return true, nil
	}
	pid := *p.PID
	if !processAlive(pid) {
		_ = s.clearBotPID(ctx, botID, ptrInt(0), nil)
		return true, nil
	}

	if err := stopProcessGroup(pid, 4*time.Second); err != nil {
		return false, err
	}
	_ = s.clearBotPID(ctx, botID, ptrInt(0), nil)
	return false, nil
}

func (s *Server) handleBotStatus(w http.ResponseWriter, r *http.Request) {
	botID := chiURLParam(r, "botID")
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	b, err := s.getBot(ctx, botID)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("db get: %v", err))
		return
	}
	if b == nil {
		writeError(w, 404, "bot not found")
		return
	}

	p, _ := s.getBotProcess(ctx, botID)
	running := false
	var pid *int
	if p != nil && p.PID != nil {
		pid = p.PID
		running = processAlive(*p.PID)
	}
	writeJSON(w, 200, map[string]any{"bot_id": botID, "running": running, "pid": pid})
}

func (s *Server) spawnBotWithRuntimeConfig(b Bot, cfgYAML string) (int, error) {
	log.Printf("[spawn-bot] bot_id=%s, platform=%s", b.ID, runtime.GOOS)
	
	// 额外确保目录存在
	if err := os.MkdirAll(filepath.Dir(b.LogPath), 0o755); err != nil {
		return 0, fmt.Errorf("mkdir log dir: %w", err)
	}
	logFile, err := os.OpenFile(b.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, fmt.Errorf("open log file: %w", err)
	}

	var cfgFile *os.File
	var cfgPath string

	if runtime.GOOS == "linux" {
		// Linux: 使用 memfd（内存文件描述符，不落盘）
		log.Printf("[spawn-bot] bot_id=%s using memfd (linux)", b.ID)
		fd, err := createMemfd("gobet-config")
		if err != nil {
			_ = logFile.Close()
			return 0, fmt.Errorf("create memfd: %w", err)
		}
		cfgFile = os.NewFile(uintptr(fd), "gobet-config")
		if cfgFile == nil {
			_ = logFile.Close()
			_ = syscall.Close(fd)
			return 0, fmt.Errorf("memfd: os.NewFile failed")
		}
		if _, err := io.WriteString(cfgFile, cfgYAML+"\n"); err != nil {
			_ = logFile.Close()
			_ = cfgFile.Close()
			return 0, fmt.Errorf("write memfd: %w", err)
		}
		if _, err := cfgFile.Seek(0, 0); err != nil {
			_ = logFile.Close()
			_ = cfgFile.Close()
			return 0, fmt.Errorf("seek memfd: %w", err)
		}
	} else {
		// 非 Linux 平台：使用临时文件（macOS 等）
		log.Printf("[spawn-bot] bot_id=%s using temp file (non-linux: %s)", b.ID, runtime.GOOS)
		tmpDir := os.TempDir()
		p := filepath.Join(tmpDir, fmt.Sprintf("gobet-config-%s-%d.yaml", b.ID, time.Now().UnixNano()))
		if err := os.WriteFile(p, []byte(cfgYAML+"\n"), 0o600); err != nil {
			_ = logFile.Close()
			return 0, fmt.Errorf("write temp config: %w", err)
		}
		cfgPath = p
		log.Printf("[spawn-bot] bot_id=%s temp config: %s", b.ID, cfgPath)
	}

	cmd := exec.Command(s.cfg.BotBin)
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if runtime.GOOS == "linux" {
		// Linux: 通过 /proc/self/fd/<n> 传递 memfd
		idx := len(cmd.ExtraFiles)
		cmd.ExtraFiles = append(cmd.ExtraFiles, cfgFile)
		childFD := 3 + idx
		cfgPath = fmt.Sprintf("/proc/self/fd/%d", childFD)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	} else {
		// 非 Linux: 直接使用临时文件路径
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}

	cmd.Args = []string{s.cfg.BotBin, "-config", cfgPath}
	log.Printf("[spawn-bot] bot_id=%s executing: %s -config %s", b.ID, s.cfg.BotBin, cfgPath)

	// 注意：Start 后不 Wait，让 bot 常驻运行；server 只记录 pid。
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		if cfgFile != nil {
			_ = cfgFile.Close()
		}
		if runtime.GOOS != "linux" && cfgPath != "" {
			_ = os.Remove(cfgPath) // 清理临时文件
		}
		return 0, fmt.Errorf("start command: %w", err)
	}
	
	// Parent can close its fd copy; child has its own.
	if cfgFile != nil {
		_ = cfgFile.Close()
	}
	pid := cmd.Process.Pid
	log.Printf("[spawn-bot] bot_id=%s started: pid=%d", b.ID, pid)

	// 记录退出信息（不影响 bot 性能：Wait 只在 bot 退出时触发）
	tempConfigPath := cfgPath // 保存临时文件路径（非 Linux 平台）
	go func() {
		waitErr := cmd.Wait()
		_ = logFile.Close()
		
		// 清理临时配置文件（非 Linux 平台）
		if runtime.GOOS != "linux" && tempConfigPath != "" {
			if err := os.Remove(tempConfigPath); err != nil {
				log.Printf("[spawn-bot] bot_id=%s failed to remove temp config: %v", b.ID, err)
			} else {
				log.Printf("[spawn-bot] bot_id=%s cleaned up temp config: %s", b.ID, tempConfigPath)
			}
		}

		exitCode := 0
		lastErr := ""
		var lastErrPtr *string
		if waitErr != nil {
			lastErr = waitErr.Error()
			lastErrPtr = &lastErr
			var ee *exec.ExitError
			if errors.As(waitErr, &ee) {
				exitCode = ee.ExitCode()
			} else {
				exitCode = 1
			}
			log.Printf("[spawn-bot] bot_id=%s exited with error: %v (exit_code=%d)", b.ID, waitErr, exitCode)
		} else {
			log.Printf("[spawn-bot] bot_id=%s exited normally (exit_code=0)", b.ID)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.clearBotPID(ctx, b.ID, &exitCode, lastErrPtr)

		// auto-restart (best-effort) when desired_running=1
		if !autoRestartEnabled() {
			return
		}
		desired, _, err := s.getRestartState(ctx, b.ID)
		if err != nil || !desired {
			return
		}
		max := parseIntEnv("GOBET_BOT_RESTART_MAX", 5, 0, 100)
		// increment attempt counter in DB
		n, err := s.incRestartAttempts(ctx, b.ID)
		if err != nil {
			return
		}
		if max > 0 && n > max {
			msg := fmt.Sprintf("auto-restart disabled after %d attempts", n)
			_ = s.clearBotPID(ctx, b.ID, ptrInt(exitCode), &msg)
			return
		}

		base := parseDurationEnv("GOBET_BOT_RESTART_BASE_DELAY", 2*time.Second)
		maxDelay := parseDurationEnv("GOBET_BOT_RESTART_MAX_DELAY", 60*time.Second)
		delay := base
		if n > 1 {
			for i := 1; i < n; i++ {
				delay *= 2
				if delay >= maxDelay {
					delay = maxDelay
					break
				}
			}
		}
		if delay < 0 {
			delay = 0
		}

		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return
		}

		restartCtx, cancel2 := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel2()
		_, _, _ = s.startBot(restartCtx, b.ID)
	}()

	return pid, nil
}

func autoRestartEnabled() bool {
	v := strings.TrimSpace(os.Getenv("GOBET_BOT_AUTO_RESTART"))
	return v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
}

func (s *Server) ensureSafeDeployed(privateKeyHex string, eoaAddress string, funderAddress string) (warning string, err error) {
	chainID := big.NewInt(137)
	relayerURL := strings.TrimSpace(s.getenv("POLYMARKET_RELAYER_URL"))
	if relayerURL == "" {
		relayerURL = "https://relayer-v2.polymarket.com"
	}
	rc := sdkrelayer.NewClient(relayerURL, chainID, nil, nil)
	expected, err := rc.GetExpectedSafe(eoaAddress)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(strings.TrimSpace(expected), strings.TrimSpace(funderAddress)) {
		return "", fmt.Errorf("funder_address mismatch: expected %s got %s", expected, funderAddress)
	}
	deployed, err := rc.GetDeployed(expected)
	if err == nil && deployed.Deployed {
		return "", nil
	}
	bc, err := s.loadBuilderCreds()
	if err != nil {
		return "safe not deployed yet; missing builder creds for deploy", nil
	}
	pk, err := crypto.HexToECDSA(strings.TrimPrefix(strings.TrimSpace(privateKeyHex), "0x"))
	if err != nil {
		return "", err
	}
	signFn := func(signer string, digest []byte) ([]byte, error) {
		_ = signer
		sig, err := crypto.Sign(digest, pk)
		if err != nil {
			return nil, err
		}
		if sig[64] < 27 {
			sig[64] += 27
		}
		return sig, nil
	}
	rc2 := sdkrelayer.NewClient(relayerURL, chainID, signFn, &sdktypes.BuilderApiKeyCreds{
		Key:        bc.Key,
		Secret:     bc.Secret,
		Passphrase: bc.Passphrase,
	})
	_, err = rc2.Deploy(&sdktypes.AuthOption{SingerAddress: eoaAddress, FunderAddress: expected})
	if err != nil {
		return fmt.Sprintf("safe deploy failed: %v", err), nil
	}
	// poll for deployed
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		st, err2 := rc2.GetDeployed(expected)
		if err2 == nil && st.Deployed {
			return "", nil
		}
	}
	return "safe deploy submitted; not confirmed deployed yet", nil
}

func injectWalletIntoBotConfig(yamlText string, privateKeyHex string, funderAddress string, logPath string, persistenceDir string) (string, error) {
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

	// Keep isolation constraints
	m["log_file"] = strings.TrimSpace(logPath)
	m["persistence_dir"] = strings.TrimSpace(persistenceDir)

	out, err := yaml.Marshal(m)
	if err != nil {
		return "", err
	}
	// Validate with full config rules now that wallet is injected.
	var cf pkgconfig.ConfigFile
	if err := yaml.Unmarshal(out, &cf); err != nil {
		return "", err
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
			Kind:          strings.TrimSpace(cf.Market.Kind),
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
	if err := cfg.Validate(); err != nil {
		return "", err
	}
	return string(out), nil
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0：仅检查是否存在
	if err := p.Signal(syscall.Signal(0)); err != nil {
		return false
	}
	return true
}

func stopProcessGroup(pid int, timeout time.Duration) error {
	if pid <= 0 {
		return nil
	}
	// 先 SIGTERM
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		// 进程组可能不存在，回退尝试单进程
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	// 再 SIGKILL
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = syscall.Kill(pid, syscall.SIGKILL)
	return fmt.Errorf("stop timeout after %s (pid=%s)", timeout, strconv.Itoa(pid))
}

func ptrString(s string) *string { return &s }
func ptrInt(v int) *int          { return &v }
