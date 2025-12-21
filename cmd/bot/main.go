package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/betbot/gobet/clob/client"
	"github.com/betbot/gobet/clob/signing"
	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/infrastructure/websocket"
	"github.com/betbot/gobet/internal/metrics"
	"github.com/betbot/gobet/internal/ports"
	"github.com/betbot/gobet/internal/services"
	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/sirupsen/logrus"

	"github.com/betbot/gobet/pkg/config"
	"github.com/betbot/gobet/pkg/logger"
	"github.com/betbot/gobet/pkg/persistence"

	// 导入策略集合以触发 init() 注册（bbgo 风格）
	_ "github.com/betbot/gobet/internal/strategies/all"
)

// sessionOrderRouter 只注册一次的订单更新路由器：
// - 周期切换时只更新“当前 session+market”
// - 避免 TradingService/OrderEngine 的 handler 无限累积
type sessionOrderRouter struct {
	mu      sync.RWMutex
	session *bbgo.ExchangeSession
	market  *domain.Market
}

var _ ports.OrderUpdateHandler = (*sessionOrderRouter)(nil)

func (r *sessionOrderRouter) Set(session *bbgo.ExchangeSession, market *domain.Market) {
	r.mu.Lock()
	r.session = session
	r.market = market
	r.mu.Unlock()
}

func (r *sessionOrderRouter) OnOrderUpdate(ctx context.Context, order *domain.Order) error {
	r.mu.RLock()
	session := r.session
	market := r.market
	r.mu.RUnlock()

	if session == nil {
		return nil
	}

	// 只把“当前周期”的订单更新转发给 Session/策略，避免跨周期串单
	if order != nil && market != nil {
		// 1) 有 MarketSlug：严格匹配
		if order.MarketSlug != "" && order.MarketSlug != market.Slug {
			return nil
		}
		// 2) 没有 MarketSlug：用 assetID 兜底匹配
		if order.MarketSlug == "" && order.AssetID != "" {
			if order.AssetID != market.YesAssetID && order.AssetID != market.NoAssetID {
				return nil
			}
		}
	}

	session.EmitOrderUpdate(ctx, order)
	return nil
}

func main() {
	// 解析命令行参数
	configPath := flag.String("config", "", "配置文件路径（支持 .yaml, .yml, .json）")
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
		defaultConfigPath := "config.yaml"
		if _, err := os.Stat(defaultConfigPath); err == nil {
			config.SetConfigPath(defaultConfigPath)
			logrus.Infof("使用默认配置文件: %s", defaultConfigPath)
		} else {
			logrus.Warnf("未指定配置文件，且默认配置文件 %s 不存在，将使用环境变量和默认值", defaultConfigPath)
		}
	}

	// 加载配置
	cfg, err := config.Load()
	if err != nil {
		logrus.Errorf("加载配置失败: %v", err)
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
		CycleDuration: 15 * time.Minute,
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
	if cfg.Proxy != nil {
		proxyURL := fmt.Sprintf("http://%s:%d", cfg.Proxy.Host, cfg.Proxy.Port)
		os.Setenv("HTTP_PROXY", proxyURL)
		os.Setenv("HTTPS_PROXY", proxyURL)
		os.Setenv("http_proxy", proxyURL)
		os.Setenv("https_proxy", proxyURL)
		logrus.Infof("已设置 HTTP 代理环境变量: %s（Gamma API 将使用此代理）", proxyURL)
	} else {
		// 如果没有配置代理，检查环境变量是否已设置
		if os.Getenv("HTTP_PROXY") == "" && os.Getenv("HTTPS_PROXY") == "" {
			// 使用默认代理
			defaultProxy := "http://127.0.0.1:15236"
			os.Setenv("HTTP_PROXY", defaultProxy)
			os.Setenv("HTTPS_PROXY", defaultProxy)
			logrus.Infof("未配置代理，使用默认代理: %s", defaultProxy)
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
	marketDataService := services.NewMarketDataService(clobClient)
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
	if addr := os.Getenv("METRICS_ADDR"); addr != "" {
		go func() {
			logrus.Infof("📊 metrics/pprof 启用: listen=%s (expvar:/debug/vars, pprof:/debug/pprof)", addr)
			if err := metrics.Start(addr); err != nil {
				logrus.Errorf("metrics server 启动失败: %v", err)
			}
		}()
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
	tradingService.SetCurrentMarket(market.Slug)

	// 订单路由器：TradingService 只注册一次；周期切换只更新指向
	orderRouter := &sessionOrderRouter{}
	orderRouter.Set(session, market)
	tradingService.OnOrderUpdate(orderRouter)
	if session != nil && session.UserDataStream != nil {
		session.UserDataStream.OnOrderUpdate(orderRouter)
	}

	// 设置会话切换回调，当周期切换时重新注册策略
	marketScheduler.OnSessionSwitch(func(oldSession *bbgo.ExchangeSession, newSession *bbgo.ExchangeSession, newMarket *domain.Market) {
		logrus.Infof("🔄 [周期切换] 检测到会话切换，重新注册策略到新会话: %s", newMarket.Slug)

		// 更新交易服务的当前市场（用于过滤订单状态同步）
		tradingService.SetCurrentMarket(newMarket.Slug)

		// 只管理本周期：先取消上一周期残留的 open orders，避免跨周期串单
		cancelCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		tradingService.CancelOrdersNotInMarket(cancelCtx, newMarket.Slug)
		// 可选：周期开始时也清空"本周期残留 open orders"（例如重启后同周期还有挂单）
		if cfg.CancelOpenOrdersOnCycleStart {
			tradingService.CancelOrdersForMarket(cancelCtx, newMarket.Slug)
		}
		cancel()

		// 更新订单路由器（TradingService handler 不新增，保持可控）
		orderRouter.Set(newSession, newMarket)
		if newSession != nil && newSession.UserDataStream != nil {
			newSession.UserDataStream.OnOrderUpdate(orderRouter)
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
