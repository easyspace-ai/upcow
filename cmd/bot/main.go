package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
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

	// 导入策略包以触发 init() 函数注册策略
	_ "github.com/betbot/gobet/internal/strategies/arbitrage"
	_ "github.com/betbot/gobet/internal/strategies/datarecorder"
	_ "github.com/betbot/gobet/internal/strategies/grid"
	_ "github.com/betbot/gobet/internal/strategies/threshold"
)

// sessionOrderHandler 将订单更新转发到Session（BBGO风格）
type sessionOrderHandler struct {
	session *bbgo.ExchangeSession
	market  *domain.Market
}

func (h *sessionOrderHandler) OnOrderUpdate(ctx context.Context, order *domain.Order) error {
	// 只把“当前周期”的订单更新转发给 Session/策略，避免跨周期串单
	if order != nil && h.market != nil {
		// 1) 有 MarketSlug：严格匹配
		if order.MarketSlug != "" && order.MarketSlug != h.market.Slug {
			return nil
		}
		// 2) 没有 MarketSlug：用 assetID 兜底匹配（当前 market 的 yes/no assetID 必须命中其一）
		if order.MarketSlug == "" && order.AssetID != "" {
			if order.AssetID != h.market.YesAssetID && order.AssetID != h.market.NoAssetID {
				return nil
			}
		}
	}
	h.session.EmitOrderUpdate(ctx, order)
	return nil
}

// adaptStrategyConfig 适配配置，将 config.StrategyConfig 转换为策略特定的配置结构
// 使用配置适配器模式，每个策略负责自己的配置适配逻辑
func adaptStrategyConfig(strategyName string, strategyConfig config.StrategyConfig, proxyConfig *config.ProxyConfig) (interface{}, error) {
	// 查找配置适配器
	adapter, exists := bbgo.GetConfigAdapter(strategyName)
	if !exists {
		return nil, fmt.Errorf("策略 %s 未注册配置适配器", strategyName)
	}

	// 使用适配器转换配置
	return adapter.AdaptConfig(strategyConfig, proxyConfig)
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

	logrus.Info("推导 API 凭证...")
	initCtx := context.Background()
	creds, err := tempClient.CreateOrDeriveAPIKey(initCtx, nil)
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

	// 设置最小订单金额（从网格策略配置中读取，如果没有则使用默认值 1.1）
	minOrderSize := 1.1 // 默认值
	if cfg.Strategies.Grid != nil && cfg.Strategies.Grid.MinOrderSize > 0 {
		minOrderSize = cfg.Strategies.Grid.MinOrderSize
	}
	tradingService.SetMinOrderSize(minOrderSize)

	// 创建 Environment
	environ := bbgo.NewEnvironment()
	environ.SetMarketDataService(marketDataService)
	environ.SetTradingService(tradingService)

	// 创建并注入全局命令执行器（串行执行交易/网络 IO，策略 loop 不直接阻塞在网络调用上）
	environ.SetExecutor(bbgo.NewSerialCommandExecutor(2048))

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

	// 加载策略（使用策略加载器，BBGO风格）
	loader := bbgo.NewStrategyLoader(tradingService)
	for _, strategyName := range cfg.Strategies.EnabledStrategies {
		// 适配配置
		adaptedConfig, err := adaptStrategyConfig(strategyName, cfg.Strategies, cfg.Proxy)
		if err != nil {
			logrus.Errorf("适配策略 %s 配置失败: %v", strategyName, err)
			continue
		}

		// 使用策略加载器加载策略
		strategy, err := loader.LoadStrategy(strategyName, adaptedConfig)
		if err != nil {
			logrus.Errorf("加载策略 %s 失败: %v", strategyName, err)
			continue
		}

		trader.AddStrategy(strategy)
		logrus.Infof("✅ 策略 %s 已加载", strategyName)
	}

	// 注入服务
	if err := trader.InjectServices(initCtx); err != nil {
		logrus.Errorf("注入服务失败: %v", err)
		os.Exit(1)
	}

	// 初始化策略
	if err := trader.Initialize(initCtx); err != nil {
		logrus.Errorf("初始化策略失败: %v", err)
		os.Exit(1)
	}

	// 加载状态
	if err := trader.LoadState(initCtx); err != nil {
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
	if err := marketScheduler.Start(initCtx); err != nil {
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

	// 注册策略到会话的辅助函数
	registerStrategiesToSession := func(session *bbgo.ExchangeSession, market *domain.Market) {
		// 检查连接状态和 handlers（用于调试）
		if session.MarketDataStream != nil {
			if ms, ok := session.MarketDataStream.(*websocket.MarketStream); ok {
				handlerCount := ms.HandlerCount()
				logrus.Debugf("🔄 [周期切换] 策略注册前 MarketStream handlers 数量=%d", handlerCount)
			}
		}
		handlerCountBefore := session.PriceChangeHandlerCount()
		logrus.Debugf("🔄 [周期切换] 策略注册前 Session priceChangeHandlers 数量=%d", handlerCountBefore)

		// 将 Session 注册为 UserWebSocket 的订单更新处理器（BBGO风格）
		if session.UserDataStream != nil {
			session.UserDataStream.OnOrderUpdate(&sessionOrderHandler{session: session, market: market})
		}

		// 将 Session 注册为 TradingService 的订单更新处理器（BBGO风格）
		tradingService.OnOrderUpdate(&sessionOrderHandler{session: session, market: market})

		// 订阅策略（BBGO风格：策略在 Subscribe 方法中自己注册回调到Session）
		logrus.Debugf("🔄 [周期切换] 准备调用 trader.Subscribe，session=%s, market=%s", session.Name, market.Slug)
		if err := trader.Subscribe(initCtx, session); err != nil {
			logrus.Errorf("订阅策略失败: %v", err)
			return
		}

		// 检查注册后的 handlers 数量（用于调试）
		handlerCountAfter := session.PriceChangeHandlerCount()
		logrus.Infof("🔄 [周期切换] 策略注册后 Session priceChangeHandlers 数量=%d (之前=%d)", 
			handlerCountAfter, handlerCountBefore)
		if handlerCountAfter == 0 {
			logrus.Errorf("❌ [周期切换] 错误：策略注册后 Session priceChangeHandlers 仍为空！市场=%s", market.Slug)
		} else {
			logrus.Infof("✅ [周期切换] 策略注册成功，Session priceChangeHandlers 数量=%d", handlerCountAfter)
		}

		logrus.Infof("✅ 策略已重新注册到新会话: %s", market.Slug)
	}

	// 初始注册策略到会话
	registerStrategiesToSession(session, market)

	// 设置会话切换回调，当周期切换时重新注册策略
	marketScheduler.OnSessionSwitch(func(oldSession *bbgo.ExchangeSession, newSession *bbgo.ExchangeSession, newMarket *domain.Market) {
		logrus.Infof("🔄 [周期切换] 检测到会话切换，重新注册策略到新会话: %s", newMarket.Slug)
		// 只管理本周期：先取消上一周期残留的 open orders，避免跨周期串单
		cancelCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		tradingService.CancelOrdersNotInMarket(cancelCtx, newMarket.Slug)
		cancel()
		registerStrategiesToSession(newSession, newMarket)
	})

	// 启动环境（这会自动启动交易服务，避免重复调用）
	if err := environ.Start(initCtx); err != nil {
		logrus.Errorf("启动环境失败: %v", err)
		os.Exit(1)
	}

	// 运行策略
	logrus.Info("🚀 正在启动策略...")
	if err := trader.Run(initCtx); err != nil {
		logrus.Errorf("运行策略失败: %v", err)
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

	// 优雅关闭（按照 BBGO 的关闭顺序）
	gracefulShutdownPeriod := 30 * time.Second
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), gracefulShutdownPeriod)
	defer shutdownCancel()

	// 1. 调用 bbgo.Shutdown()（这会调用所有策略的 Shutdown）
	bbgo.Shutdown(shutdownCtx, environ.ShutdownManager())

	// 2. 停止交易服务（让订单队列处理完成）
	logrus.Info("正在停止交易服务...")
	tradingService.Stop()

	// 3. 停止市场调度器（关闭WebSocket连接）
	logrus.Info("正在停止市场调度器...")
	if err := marketScheduler.Stop(shutdownCtx); err != nil {
		logrus.Errorf("停止市场调度器失败: %v", err)
	}

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
