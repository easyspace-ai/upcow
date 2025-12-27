package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"

	pkgconfig "github.com/betbot/gobet/pkg/config"
	"gopkg.in/yaml.v3"
	"strings"
)

func (s *Server) handleBotStart(w http.ResponseWriter, r *http.Request) {
	botID := chiURLParam(r, "botID")
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	pid, alreadyRunning, err := s.startBot(ctx, botID)
	if err != nil {
		if errors.Is(err, ErrBotNotFound) {
			writeError(w, 404, "bot not found")
			return
		}
		writeError(w, 500, fmt.Sprintf("start failed: %v", err))
		return
	}
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
	b, err := s.getBot(ctx, botID)
	if err != nil {
		return 0, false, fmt.Errorf("db get: %w", err)
	}
	if b == nil {
		return 0, false, ErrBotNotFound
	}

	// 已在跑则直接返回
	p, _ := s.getBotProcess(ctx, botID)
	if p != nil && p.PID != nil && processAlive(*p.PID) {
		return *p.PID, true, nil
	}

	// Preflight: ensure wallet is configured (either in config YAML or via account binding).
	// We allow creating bots without wallet, but we refuse to start them until wallet info exists.
	pk, funder, err := extractWalletFromBotConfig(b.ConfigYAML)
	if err != nil {
		return 0, false, fmt.Errorf("config parse failed: %w", err)
	}
	if pk == "" || funder == "" {
		return 0, false, fmt.Errorf("bot 未配置钱包：请先绑定账号（1账号1bot）或在配置中填写 wallet.private_key / wallet.funder_address")
	}

	pid, err = s.spawnBot(*b)
	if err != nil {
		_ = s.clearBotPID(ctx, botID, nil, ptrString(err.Error()))
		return 0, false, err
	}
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

func (s *Server) spawnBot(b Bot) (int, error) {
	// 额外确保目录存在
	if err := os.MkdirAll(filepath.Dir(b.LogPath), 0o755); err != nil {
		return 0, err
	}
	logFile, err := os.OpenFile(b.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, err
	}

	cmd := exec.Command(s.cfg.BotBin, "-config", b.ConfigPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	// 尽量让 bot 与 server 故障域隔离：单独进程组
	if runtime.GOOS == "linux" {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}

	// 注意：Start 后不 Wait，让 bot 常驻运行；server 只记录 pid。
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return 0, err
	}
	pid := cmd.Process.Pid

	// 记录退出信息（不影响 bot 性能：Wait 只在 bot 退出时触发）
	go func() {
		waitErr := cmd.Wait()
		_ = logFile.Close()

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
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.clearBotPID(ctx, b.ID, &exitCode, lastErrPtr)
	}()

	return pid, nil
}

func extractWalletFromBotConfig(yamlText string) (privateKey string, funderAddress string, err error) {
	var cf pkgconfig.ConfigFile
	if err := yaml.Unmarshal([]byte(yamlText), &cf); err != nil {
		return "", "", err
	}
	return strings.TrimSpace(cf.Wallet.PrivateKey), strings.TrimSpace(cf.Wallet.FunderAddress), nil
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
