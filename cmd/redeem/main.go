package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/betbot/gobet/pkg/sdk/api"
	"github.com/betbot/gobet/pkg/sdk/redeem"

	"github.com/joho/godotenv"
)

type userConfig struct {
	PrivateKey   string `json:"private_key"`
	ProxyAddress string `json:"proxy_address"`
}

func main() {
	log.Println("[Redeem] Starting auto-redeem worker...")

	// Load environment variables
	if err := godotenv.Load(); err != nil {
		log.Println("[Redeem] No .env file found, using environment variables")
	}

	// Check proxy configuration
	setupProxyEnv()

	// Load user config from user.json and set related env vars
	if err := loadUserConfig(); err != nil {
		log.Fatalf("[Redeem] Failed to load user.json: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize API client
	baseURL := os.Getenv("POLYMARKET_API_URL")
	if baseURL == "" {
		baseURL = "https://clob.polymarket.com"
	}
	apiClient := api.NewClient(baseURL)

	// Start auto-redeemer (automatically redeems resolved positions)
	redeemer, err := redeem.NewAutoRedeemer(apiClient)
	if err != nil {
		log.Fatalf("[Redeem] Failed to create auto-redeemer: %v", err)
	}

	if err := redeemer.Start(ctx); err != nil {
		log.Fatalf("[Redeem] Failed to start auto-redeemer: %v", err)
	}
	defer redeemer.Stop()

	log.Println("[Redeem] Auto-redeemer started - runs immediately on startup and then every 3 minutes (gasless via Relayer)")
	log.Println("[Redeem] Press Ctrl+C to stop...")

	// Wait for interrupt signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)

	// Handle shutdown gracefully
	sig := <-sigCh
	log.Printf("[Redeem] Received signal: %v, shutting down...", sig)

	// Cancel context first to stop all goroutines
	log.Println("[Redeem] Cancelling context...")
	cancel()

	// Stop redeemer (this will wait for goroutines to finish with timeout)
	log.Println("[Redeem] Stopping redeemer...")
	redeemer.Stop()

	log.Println("[Redeem] Stopped")
}

// loadUserConfig reads example/redeem/user.json and maps values to env vars
// POLYMARKET_PRIVATE_KEY        <- private_key
// POLYMARKET_FUNDER_ADDRESS     <- proxy_address
func loadUserConfig() error {
	const userConfigPath = "/pm/data/user.json"

	data, err := os.ReadFile(userConfigPath)
	if err != nil {
		return err
	}

	var cfg userConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return err
	}

	// Set env vars for downstream code (redeem.NewAutoRedeemer)
	if cfg.PrivateKey != "" {
		if err := os.Setenv("POLYMARKET_PRIVATE_KEY", cfg.PrivateKey); err != nil {
			return err
		}
	}
	if cfg.ProxyAddress != "" {
		if err := os.Setenv("POLYMARKET_FUNDER_ADDRESS", cfg.ProxyAddress); err != nil {
			return err
		}
	}

	log.Printf("[Redeem] Loaded user config from %s, using proxy_address as POLYMARKET_FUNDER_ADDRESS", userConfigPath)
	return nil
}

// setupProxyEnv 设置代理环境变量（确保 resty 客户端也能使用）
func setupProxyEnv() {
	// 从环境变量读取代理配置（优先级：HTTPS_PROXY > HTTP_PROXY > https_proxy > http_proxy）
	proxyURL := os.Getenv("HTTPS_PROXY")
	if proxyURL == "" {
		proxyURL = os.Getenv("HTTP_PROXY")
	}
	if proxyURL == "" {
		proxyURL = os.Getenv("https_proxy")
	}
	if proxyURL == "" {
		proxyURL = os.Getenv("http_proxy")
	}

	if proxyURL != "" {
		log.Printf("[Redeem] Proxy configuration detected: %s", proxyURL)
		// 确保所有相关环境变量都设置（resty 会读取这些）
		if os.Getenv("HTTPS_PROXY") == "" {
			os.Setenv("HTTPS_PROXY", proxyURL)
		}
		if os.Getenv("HTTP_PROXY") == "" {
			os.Setenv("HTTP_PROXY", proxyURL)
		}
	} else {
		log.Println("[Redeem] No proxy configured - using direct connection")
		log.Println("[Redeem] If you encounter connection reset errors, consider setting HTTP_PROXY or HTTPS_PROXY environment variable")
	}
}
