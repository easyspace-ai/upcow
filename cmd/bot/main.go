package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/betbot/gobet/clob/client"
	"github.com/betbot/gobet/clob/signing"
	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/infrastructure/websocket"
	"github.com/betbot/gobet/internal/metrics"
	"github.com/betbot/gobet/internal/services"
	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/sirupsen/logrus"

	"github.com/betbot/gobet/pkg/config"
	"github.com/betbot/gobet/pkg/logger"
	"github.com/betbot/gobet/pkg/persistence"

	// 导入策略集合以触发 init() 注册（bbgo 风格）
	_ "github.com/betbot/gobet/internal/strategies/all"
)

// dropCompensator 实现 websocket.DropHandler：当 user WS 分发队列发生丢弃时触发一次严格对账（节流在 services 层处理）。
type dropCompensator struct {
	ts *services.TradingService
}

func (d dropCompensator) OnDrop(kind string, meta map[string]string) {
	_ = meta
	if d.ts == nil {
		return
	}
	d.ts.CompensateAfterUserWSDrop("user_ws_drop:" + kind)
}

func firstExistingFile(paths ...string) (string, bool) {
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
	}
	return "", false
}

func resolveStrategyConfigFile(strategyName string, strategyDir string) (string, error) {
	name := strings.TrimSpace(strategyName)
	if name == "" {
		return "", fmt.Errorf("策略名为空")
	}

	// 默认策略目录：yml/strategies（可用 -strategies-dir 覆盖）
	dir := strings.TrimSpace(strategyDir)
	candidatesDirs := []string{}
	if dir != "" {
		candidatesDirs = append(candidatesDirs, dir)
	} else {
		// 配置集中管理：只在 yml 下查找（不再扫描根目录）
		candidatesDirs = append(candidatesDirs, "yml/strategies", "yml")
	}

	exts := []string{".yaml", ".yml", ".json"}
	var candidates []string
	for _, d := range candidatesDirs {
		for _, ext := range exts {
			candidates = append(candidates, filepath.Join(d, name+ext))
		}
	}

	if p, ok := firstExistingFile(candidates...); ok {
		return p, nil
	}
	return "", fmt.Errorf("未找到策略配置文件：strategy=%s（已尝试：%s/{%s}.(yaml|yml|json)）", name, strings.Join(candidatesDirs, ","), name)
}

func main() {
	// 解析命令行参数
	configPath := flag.String("config", "", "配置文件路径（支持 .yaml, .yml, .json）")
	strategyNames := flag.String("strategy", "", "策略名（逗号分隔）：自动从默认目录加载对应策略配置（需包含 exchangeStrategies）")
	strategyFiles := flag.String("strategies", "", "额外的策略配置文件列表（逗号分隔，每个文件需包含 exchangeStrategies）")
	strategyDir := flag.String("strategies-dir", "", "额外的策略配置目录（加载目录下所有 .yaml/.yml/.json，需包含 exchangeStrategies）")
	flag.Parse()

	// BBGO风格：初始化logrus（保留现有日志功能）
	if err := logger.InitDefault(); err != nil {
		panic(fmt.Sprintf("初始化日志失败: %v", err))
	}

	// 设置配置文件路径
	if *configPath != "" {
		config.SetConfigPath(*configPath)
		logrus.Infof("使用配置文件: %s", *configPath)
	} else {
		// 配置集中管理：默认只加载 yml/base.yaml，并兼容旧名 yml/config.yaml
		if p, ok := firstExistingFile("yml/config.yaml"); ok {
			config.SetConfigPath(p)
			logrus.Infof("使用默认配置文件: %s", p)
		} else {
			logrus.Warnf("未指定配置文件，且默认 yml/base.yaml 不存在，将使用环境变量和默认值")
		}
	}

	// 加载配置
	allowEmptyBaseStrategies := strings.TrimSpace(*strategyNames) != "" || strings.TrimSpace(*strategyFiles) != "" || strings.TrimSpace(*strategyDir) != ""
	cfg, err := config.LoadFromFileWithOptions(config.GetConfigPath(), config.LoadOptions{
		AllowEmptyExchangeStrategies: allowEmptyBaseStrategies,
	})
	if err != nil {
		logrus.Errorf("加载配置失败: %v", err)
		os.Exit(1)
	}

	// 启动时追加策略配置（避免频繁改动全局配置）
	var extraMounts []config.ExchangeStrategyMount
	var strategyFilesLoaded []string // 记录已加载的策略文件路径

	// 简化用法：-strategy threshold,updown
	if strings.TrimSpace(*strategyNames) != "" {
		for _, name := range strings.Split(*strategyNames, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			p, err := resolveStrategyConfigFile(name, *strategyDir)
			if err != nil {
				logrus.Errorf("解析策略配置失败: %v", err)
				os.Exit(1)
			}
			sf, err := config.LoadStrategyFile(p)
			if err != nil {
				logrus.Errorf("加载策略文件失败: %v", err)
				os.Exit(1)
			}
			extraMounts = append(extraMounts, sf.ExchangeStrategies...)
			strategyFilesLoaded = append(strategyFilesLoaded, p)
			logrus.Infof("已加载策略配置: strategy=%s file=%s", name, p)
		}
	}

	if strings.TrimSpace(*strategyDir) != "" {
		entries, err := os.ReadDir(strings.TrimSpace(*strategyDir))
		if err != nil {
			logrus.Errorf("读取策略配置目录失败: %v", err)
			os.Exit(1)
		}
		var files []string
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			ext := strings.ToLower(filepath.Ext(e.Name()))
			if ext == ".yaml" || ext == ".yml" || ext == ".json" {
				files = append(files, filepath.Join(strings.TrimSpace(*strategyDir), e.Name()))
			}
		}
		sort.Strings(files)
		for _, p := range files {
			sf, err := config.LoadStrategyFile(p)
			if err != nil {
				logrus.Errorf("加载策略文件失败: %v", err)
				os.Exit(1)
			}
			extraMounts = append(extraMounts, sf.ExchangeStrategies...)
			strategyFilesLoaded = append(strategyFilesLoaded, p)
		}
	}
	if strings.TrimSpace(*strategyFiles) != "" {
		for _, p := range strings.Split(*strategyFiles, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			sf, err := config.LoadStrategyFile(p)
			if err != nil {
				logrus.Errorf("加载策略文件失败: %v", err)
				os.Exit(1)
			}
			extraMounts = append(extraMounts, sf.ExchangeStrategies...)
			strategyFilesLoaded = append(strategyFilesLoaded, p)
		}
	}

	// 从策略配置文件中提取并覆盖全局配置（market 和 dry_run）
	// 如果多个策略文件都设置了这些配置，最后一个文件的配置会生效
	for _, filePath := range strategyFilesLoaded {
		sf, err := config.LoadStrategyFile(filePath)
		if err != nil {
			continue // 跳过加载失败的文件
		}

		// 覆盖 market 配置（如果策略文件中设置了）
		if strings.TrimSpace(sf.Market.Symbol) != "" {
			cfg.Market.Symbol = sf.Market.Symbol
		}
		if strings.TrimSpace(sf.Market.Timeframe) != "" {
			cfg.Market.Timeframe = sf.Market.Timeframe
		}
		if strings.TrimSpace(sf.Market.Kind) != "" {
			cfg.Market.Kind = sf.Market.Kind
		}
		if strings.TrimSpace(sf.Market.SlugPrefix) != "" {
			cfg.Market.SlugPrefix = sf.Market.SlugPrefix
		}
		if sf.Market.SlugTemplates != nil && len(sf.Market.SlugTemplates) > 0 {
			cfg.Market.SlugTemplates = sf.Market.SlugTemplates
		}
		if sf.Market.Precision != nil {
			cfg.Market.Precision = sf.Market.Precision
		}

		// 覆盖 dry_run 配置（如果策略文件中设置了）
		if sf.DryRun != nil {
			cfg.DryRun = *sf.DryRun
			logrus.Infof("策略配置文件覆盖 dry_run: %v (来源: %s)", *sf.DryRun, filePath)
		}
	}

	if len(extraMounts) > 0 {
		cfg.ExchangeStrategies = append(cfg.ExchangeStrategies, extraMounts...)
	}
	// 合并完成后做一次严格校验（此时必须有 exchangeStrategies）
	if err := cfg.Validate(); err != nil {
		logrus.Errorf("配置验证失败: %v", err)
		os.Exit(1)
	}

	// 解析 market 配置（用于周期/市场选择）
	spec, err := cfg.Market.Spec()
	if err != nil {
		logrus.Errorf("market 配置无效: %v", err)
		os.Exit(1)
	}

	// 设置logrus日志级别（BBGO风格）
	level, err := logrus.ParseLevel(cfg.LogLevel)
	if err != nil {
		level = logrus.InfoLevel
		logrus.Warnf("无效的日志级别 %s，使用默认级别: info", cfg.LogLevel)
	}
	logrus.SetLevel(level)

	// 设置logrus格式（BBGO风格：使用TextFormatter）
	logrus.SetFormatter(&logrus.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: "2006-01-02 15:04:05",
	})

	// 使用配置重新初始化日志（保留现有日志文件功能）
	logConfig := logger.Config{
		Level:         cfg.LogLevel,
		OutputFile:    cfg.LogFile,
		MaxSize:       100,
		MaxBackups:    10,
		MaxAge:        30,
		Compress:      true,
		LogByCycle:    cfg.LogByCycle,
		CycleDuration: spec.Duration(),
	}
	if err := logger.Init(logConfig); err != nil {
		logrus.Errorf("重新初始化日志失败: %v", err)
		os.Exit(1)
	}

	if cfg.LogByCycle {
		logger.StartLogRotationChecker(logConfig)
		logrus.Infof("日志按周期命名已启用，周期时长: %v", logConfig.CycleDuration)
	}

	logrus.Info("🚀 启动交易机器人（BBGO 架构）...")

	// 设置代理环境变量（让 Gamma API 调用使用代理）
	// 仅在代理配置存在且启用时设置环境变量
	if cfg.Proxy != nil && cfg.Proxy.Enabled {
		proxyURL := fmt.Sprintf("http://%s:%d", cfg.Proxy.Host, cfg.Proxy.Port)
		os.Setenv("HTTP_PROXY", proxyURL)
		os.Setenv("HTTPS_PROXY", proxyURL)
		os.Setenv("http_proxy", proxyURL)
		os.Setenv("https_proxy", proxyURL)
		logrus.Infof("已设置 HTTP 代理环境变量: %s（Gamma API 将使用此代理）", proxyURL)
	} else {
		// 如果代理未启用，清除可能存在的环境变量（避免使用旧的代理配置）
		os.Unsetenv("HTTP_PROXY")
		os.Unsetenv("HTTPS_PROXY")
		os.Unsetenv("http_proxy")
		os.Unsetenv("https_proxy")
		if cfg.Proxy == nil {
			logrus.Info("代理未启用，已清除代理环境变量")
		} else {
			logrus.Infof("代理已禁用（enabled=false），已清除代理环境变量")
		}
	}
	//fmt.Println("======", cfg.Wallet.PrivateKey)
	// 初始化 CLOB 客户端
	privateKey, err := signing.PrivateKeyFromHex(cfg.Wallet.PrivateKey)
	if err != nil {
		logrus.Errorf("解析私钥失败: %v", err)
		os.Exit(1)
	}

	tempClient := client.NewClient(
		"https://clob.polymarket.com",
		types.ChainPolygon,
		privateKey,
		nil,
	)

	// root context：保证“周期切换/关停”可控
	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	logrus.Info("推导 API 凭证...")
	creds, err := tempClient.CreateOrDeriveAPIKey(rootCtx, nil)
	if err != nil {
		logrus.Errorf("推导 API 凭证失败: %v", err)
		os.Exit(1)
	}
	logrus.Infof("API 凭证已获取: key=%s...", creds.Key[:12])

	clobClient := client.NewClient(
		"https://clob.polymarket.com",
		types.ChainPolygon,
		privateKey,
		creds,
	)

	// 创建服务
	marketDataService := services.NewMarketDataService(clobClient, spec)
	marketDataService.Start()
	defer marketDataService.Stop()

	// 创建 TradingService（BBGO风格：不使用事件总线）
	// 支持纸交易模式（dry run）
	tradingService := services.NewTradingService(clobClient, cfg.DryRun)
	if cfg.DryRun {
		logrus.Warnf("📝 纸交易模式已启用：不会进行真实交易，订单信息仅记录在日志中")
	}
	if cfg.Wallet.FunderAddress != "" {
		tradingService.SetFunderAddress(cfg.Wallet.FunderAddress, types.SignatureTypeGnosisSafe)
		logrus.Infof("已配置代理钱包: funderAddress=%s", cfg.Wallet.FunderAddress)
	}

	// 注意：订单状态检查已由 OrderEngine 统一管理，不再需要单独配置

	// 设置订单状态同步配置
	tradingService.SetOrderStatusSyncConfig(cfg.OrderStatusSyncIntervalWithOrders, cfg.OrderStatusSyncIntervalWithoutOrders)
	logrus.Infof("订单状态同步配置: 有活跃订单时=%d秒, 无活跃订单时=%d秒（官方API限流：150请求/10秒，理论上可支持1秒，但建议3秒以上）",
		cfg.OrderStatusSyncIntervalWithOrders, cfg.OrderStatusSyncIntervalWithoutOrders)

	// 设置最小订单金额（全局配置，不再从某个策略"偷读"）
	tradingService.SetMinOrderSize(cfg.MinOrderSize)

	// 设置限价单最小 share 数量（仅限价单 GTC 时应用）
	tradingService.SetMinShareSize(cfg.MinShareSize)

	// 创建 Environment
	environ := bbgo.NewEnvironment()
	environ.SetMarketDataService(marketDataService)
	environ.SetTradingService(tradingService)

	// Binance Futures Klines（1s/1m）：供策略读取秒级与 1 分钟 K 线（尤其是"开盘 1 分钟"）
	// 【已禁用】暂时不使用 Binance WebSocket，避免不必要的网络连接和超时错误
	// binanceProxyURL := ""
	// if cfg.Proxy != nil {
	// 	binanceProxyURL = fmt.Sprintf("http://%s:%d", cfg.Proxy.Host, cfg.Proxy.Port)
	// }
	// binanceSymbol := strings.ToLower(strings.TrimSpace(cfg.Market.Symbol)) + "usdt"
	// environ.SetBinanceFuturesKlines(services.NewBinanceFuturesKlines(binanceSymbol, binanceProxyURL))

	// 创建并注入全局命令执行器（串行执行交易/网络 IO，策略 loop 不直接阻塞在网络调用上）
	environ.SetExecutor(bbgo.NewSerialCommandExecutor(2048))
	// 并发执行器：仅用于显式声明 concurrent 的策略（如 arbitrage）
	environ.SetConcurrentExecutor(bbgo.NewWorkerPoolCommandExecutor(2048, cfg.ConcurrentExecutorWorkers))

	// 设置系统级配置（直接回调模式防抖间隔，BBGO风格：只支持直接模式）
	if cfg.DirectModeDebounce > 0 {
		environ.SetDirectModeDebounce(cfg.DirectModeDebounce)
		logrus.Infof("系统级配置: 防抖间隔=%dms（BBGO风格：直接回调模式）", cfg.DirectModeDebounce)
	}

	// 创建持久化服务
	persistenceService := persistence.NewJSONFileService("data/persistence")
	environ.SetPersistenceService(persistenceService)
	// 交易服务使用同一套持久化（用于重启恢复快照）
	tradingService.SetPersistence(persistenceService, "bot")

	// 可选：启动 metrics/pprof（默认关闭，通过环境变量启用）
	addr := os.Getenv("GOBET_PPROF_ADDR")
	if addr == "" {
		// 兼容旧变量名
		addr = os.Getenv("METRICS_ADDR")
	}
	if addr != "" {
		if _, err := metrics.StartAsync(rootCtx, addr); err != nil {
			logrus.Errorf("metrics/pprof 启动失败: %v", err)
		} else {
			logrus.Infof("📊 metrics/pprof 启用: listen=%s (expvar:/debug/vars, pprof:/debug/pprof)", addr)
		}
	}

	// 创建 Trader
	trader := bbgo.NewTrader(environ)

	// 加载策略（bbgo main 风格：exchangeStrategies 动态挂载）
	loader := bbgo.NewStrategyLoader(tradingService)
	for _, mount := range cfg.ExchangeStrategies {
		// 本项目默认会话名是 polymarket；如果配置了其它 session，则直接跳过
		shouldMount := false
		for _, on := range mount.On {
			if on == "polymarket" {
				shouldMount = true
				break
			}
		}
		if !shouldMount {
			logrus.Infof("⏭️ 跳过策略 %s：未挂载到 polymarket（on=%v）", mount.StrategyID, mount.On)
			continue
		}

		strategy, err := loader.LoadStrategy(rootCtx, mount.StrategyID, mount.Config)
		if err != nil {
			logrus.Errorf("加载策略 %s 失败: %v", mount.StrategyID, err)
			continue
		}
		trader.AddStrategy(strategy)
		logrus.Infof("✅ 策略 %s 已加载（on=%v）", mount.StrategyID, mount.On)
	}

	// 注入服务
	if err := trader.InjectServices(rootCtx); err != nil {
		logrus.Errorf("注入服务失败: %v", err)
		os.Exit(1)
	}

	// 初始化策略
	if err := trader.Initialize(rootCtx); err != nil {
		logrus.Errorf("初始化策略失败: %v", err)
		os.Exit(1)
	}

	// 加载状态
	if err := trader.LoadState(rootCtx); err != nil {
		logrus.Warnf("加载状态失败: %v", err)
	}

	// 创建用户凭证（用于 UserWebSocket）
	userCreds := &websocket.UserCredentials{
		APIKey:     creds.Key,
		Secret:     creds.Secret,
		Passphrase: creds.Passphrase,
	}

	// 获取代理 URL
	proxyURL := ""
	if cfg.Proxy != nil {
		proxyURL = fmt.Sprintf("http://%s:%d", cfg.Proxy.Host, cfg.Proxy.Port)
	}

	// 创建市场调度器（BBGO风格：自动切换市场）
	marketScheduler := bbgo.NewMarketScheduler(
		environ,
		marketDataService,
		"polymarket",
		proxyURL,
		userCreds,
		spec,
	)

	// 启动市场调度器（这会创建初始会话）
	if err := marketScheduler.Start(rootCtx); err != nil {
		logrus.Errorf("启动市场调度器失败: %v", err)
		os.Exit(1)
	}

	// 获取当前会话和市场
	session := marketScheduler.CurrentSession()
	market := marketScheduler.CurrentMarket()
	if session == nil || market == nil {
		logrus.Errorf("无法获取当前会话或市场")
		os.Exit(1)
	}

	logrus.Infof("当前市场: %s", market.Slug)

	// 设置交易服务的当前市场（用于过滤订单状态同步）
	tradingService.SetCurrentMarketInfo(market)
	// 注入 WS top-of-book 原子快照（供 GetBestPrice/执行层使用）
	tradingService.SetBestBook(session.BestBook())

	// 架构层路由器：只注册一次；周期切换只更新指向（策略侧无需关心跨周期）
	eventRouter := bbgo.NewSessionEventRouter()
	eventRouter.SetSession(session)
	tradingService.OnOrderUpdate(eventRouter)
	if session != nil && session.UserDataStream != nil {
		session.UserDataStream.OnOrderUpdate(eventRouter)
		session.UserDataStream.OnTradeUpdate(eventRouter)
		// WS 分发队列丢弃补偿：一旦丢弃 trade/order，触发一次严格对账（节流）
		session.UserDataStream.SetDropHandler(dropCompensator{ts: tradingService})
	}
	// 成交事件：必须经由 Session gate（防止跨周期 trade 直接进入 OrderEngine）
	if session != nil {
		session.OnTradeUpdate(tradingService)
	}

	// 设置会话切换回调，当周期切换时重新注册策略
	marketScheduler.OnSessionSwitch(func(oldSession *bbgo.ExchangeSession, newSession *bbgo.ExchangeSession, newMarket *domain.Market) {
		logrus.Infof("🔄 [周期切换] 检测到会话切换，重新注册策略到新会话: %s", newMarket.Slug)

		// 更新交易服务的当前市场（用于过滤订单状态同步）
		tradingService.SetCurrentMarketInfo(newMarket)
		// 更新 WS bestBook 指向（新周期新 WS 连接）
		if newSession != nil {
			tradingService.SetBestBook(newSession.BestBook())
		} else {
			tradingService.SetBestBook(nil)
		}

		// 只管理本周期：先取消上一周期残留的 open orders，避免跨周期串单
		cancelCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		tradingService.CancelOrdersNotInMarket(cancelCtx, newMarket.Slug)
		// 可选：周期开始时也清空"本周期残留 open orders"（例如重启后同周期还有挂单）
		if cfg.CancelOpenOrdersOnCycleStart {
			tradingService.CancelOrdersForMarket(cancelCtx, newMarket.Slug)
		}
		cancel()

		// 更新架构层路由器指向（TradingService handler 不新增，保持可控）
		eventRouter.SetSession(newSession)
		if newSession != nil && newSession.UserDataStream != nil {
			newSession.UserDataStream.OnOrderUpdate(eventRouter)
			newSession.UserDataStream.OnTradeUpdate(eventRouter)
			newSession.UserDataStream.SetDropHandler(dropCompensator{ts: tradingService})
		}
		// 成交事件：必须经由 Session gate
		if newSession != nil {
			newSession.OnTradeUpdate(tradingService)
		}

		// 核心：周期切换时取消旧 Run，并用新 session 重新 Run（框架层解决“新周期仍用旧 market 状态”的问题）
		if err := trader.SwitchSession(rootCtx, newSession); err != nil {
			logrus.Errorf("❌ [周期切换] 切换策略运行 session 失败: %v", err)
		} else {
			logrus.Infof("✅ [周期切换] 策略已切换到新 session，market=%s", newMarket.Slug)
		}
	})

	// 启动环境（这会自动启动交易服务，避免重复调用）
	if err := environ.Start(rootCtx); err != nil {
		logrus.Errorf("启动环境失败: %v", err)
		os.Exit(1)
	}

	// 启动策略（每个策略独立 goroutine，不阻塞主线程）
	logrus.Info("🚀 正在启动策略...")
	if err := trader.StartWithSession(rootCtx, session); err != nil {
		logrus.Errorf("启动策略失败: %v", err)
		os.Exit(1)
	}

	logrus.Info("✅ 交易机器人已启动，按 Ctrl+C 停止")
	logrus.Info("📊 等待价格更新和交易信号...")
	logrus.Info("💡 提示：如果长时间没有价格更新，请检查 WebSocket 连接是否正常")

	// 等待中断信号（BBGO 风格：不自动停止，由用户手动停止）
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan
	logrus.Info("收到停止信号，正在关闭...")
	// 先 cancel root ctx，尽快让策略/IO 停止继续做事
	rootCancel()

	// 优雅关闭（按照 BBGO 的关闭顺序）
	gracefulShutdownPeriod := 10 * time.Second // 缩短超时时间，避免长时间等待
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), gracefulShutdownPeriod)
	defer shutdownCancel()

	// 1. 先停止市场调度器（关闭WebSocket连接，停止接收新消息）
	logrus.Info("正在停止市场调度器...")
	if err := marketScheduler.Stop(shutdownCtx); err != nil {
		logrus.Errorf("停止市场调度器失败: %v", err)
	}

	// 2. 调用 bbgo.Shutdown()（这会调用所有策略的 Shutdown）
	logrus.Info("正在关闭策略...")
	bbgo.Shutdown(shutdownCtx, environ.ShutdownManager())

	// 3. 停止交易服务（让订单队列处理完成）
	logrus.Info("正在停止交易服务...")
	tradingService.Stop()

	// 4. 保存策略状态
	if err := trader.SaveState(shutdownCtx); err != nil {
		logrus.Warnf("保存状态失败: %v", err)
	}

	// 5. 关闭所有会话的流（MarketDataStream, UserDataStream）
	for _, session := range environ.Sessions() {
		if session.MarketDataStream != nil {
			if err := session.MarketDataStream.Close(); err != nil {
				logrus.Errorf("[%s] 关闭市场数据流失败: %v", session.Name, err)
			}
		}
		if session.UserDataStream != nil {
			// UserDataStream 的关闭由市场调度器管理
		}
	}

	// 6. 关闭环境
	if err := environ.Close(); err != nil {
		logrus.Errorf("关闭环境失败: %v", err)
	}

	logrus.Info("✅ 交易机器人已停止")
}
