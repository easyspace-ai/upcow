package datarecorder

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/betbot/gobet/clob/rtds"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/strategies/common"
	strategyports "github.com/betbot/gobet/internal/strategies/ports"
	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/betbot/gobet/pkg/config"
	"github.com/betbot/gobet/pkg/logger"
	"github.com/betbot/gobet/pkg/marketspec"
	"github.com/sirupsen/logrus"
)

const ID = "datarecorder"

var log = logrus.WithField("strategy", ID)

func init() {
	// bbgo main 风格：注册策略 struct，用于直接从 YAML/JSON 反序列化配置
	bbgo.RegisterStrategy(ID, &DataRecorderStrategy{})
}

// rtdsLoggerAdapter 适配器，将 RTDS 日志输出到我们的 logger 系统
type rtdsLoggerAdapter struct{}

func (l *rtdsLoggerAdapter) Printf(format string, v ...interface{}) {
	// 使用 Debugf 而不是 Infof，避免 RTDS 内部日志过多
	// 重要的连接状态和错误会在策略层面记录
	logger.Debugf("[RTDS] "+format, v...)
}

// DataRecorderStrategy 数据记录策略
type DataRecorderStrategy struct {
	Executor                   bbgo.CommandExecutor
	DataRecorderStrategyConfig `yaml:",inline" json:",inline"`
	config                     *DataRecorderStrategyConfig `json:"-" yaml:"-"`
	tradingService             strategyports.BasicTradingService // 交易服务（虽然不交易，但为了兼容性保留）
	recorder                   *DataRecorder
	targetPriceFetcher         *TargetPriceFetcher
	rtdsClient                 *rtds.Client
	currentMarket              *domain.Market
	btcTargetPrice             float64   // BTC 目标价（上一个周期收盘价）
	btcTargetPriceSet          bool      // 目标价是否已设置（防止周期内重复设置）
	btcRealtimePrice           float64   // BTC 实时价
	btcRealtimePriceUpdatedAt  time.Time // BTC 实时价最后更新时间
	upPrice                    float64   // UP 价格
	downPrice                  float64   // DOWN 价格

	// market spec（用于过滤市场 + 周期长度）
	marketSpec          marketspec.MarketSpec
	marketIntervalSecs  int64
	marketSlugPrefix    string
	underlyingSymbol    string // e.g. "BTC"
	chainlinkFeedSymbol string // e.g. "btc/usd"

	// 统一：单线程 loop（价格合并 + tick 周期检测）
	loopOnce     sync.Once
	loopCancel   context.CancelFunc
	priceSignalC chan struct{}
	priceMu      sync.Mutex
	latestPrices map[domain.TokenType]*events.PriceChangedEvent

	mu             sync.RWMutex
	ctx            context.Context
	cancel         context.CancelFunc
	cycleCheckStop chan struct{}  // 用于停止周期检查 goroutine
	cycleCheckWg   sync.WaitGroup // 等待周期检查 goroutine 退出
	switchingCycle bool           // 是否正在切换周期（防止重复切换）
}

// NewDataRecorderStrategy 创建新的数据记录策略
func NewDataRecorderStrategy() *DataRecorderStrategy {
	ctx, cancel := context.WithCancel(context.Background())
	return &DataRecorderStrategy{
		ctx:    ctx,
		cancel: cancel,
	}
}

// SetTradingService 设置交易服务（在初始化后调用）
// 注意：数据记录策略不进行交易，此方法仅为兼容性保留
func (s *DataRecorderStrategy) SetTradingService(ts interface{}) {
	if basicTS, ok := ts.(strategyports.BasicTradingService); ok {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.tradingService = basicTS
		logger.Debugf("数据记录策略: 交易服务已设置（策略不进行交易，仅用于兼容性）")
	}
}

// ID 返回策略ID（BBGO风格）
func (s *DataRecorderStrategy) ID() string {
	return ID
}

// Name 返回策略名称（兼容旧接口）
func (s *DataRecorderStrategy) Name() string {
	return ID
}

// Defaults 设置默认值（BBGO风格）
func (s *DataRecorderStrategy) Defaults() error {
	// 允许用户不显式配置 outputDir，给出默认值（更贴近 bbgo 的体验）
	if s.OutputDir == "" {
		s.OutputDir = "data/recordings"
	}
	// UseRTDSFallback 默认 true（用指针区分“未设置”和“显式 false”）
	if s.UseRTDSFallback == nil {
		def := true
		s.UseRTDSFallback = &def
	}
	return nil
}

// Validate 验证配置（BBGO风格）
func (s *DataRecorderStrategy) Validate() error {
	s.config = &s.DataRecorderStrategyConfig
	return s.DataRecorderStrategyConfig.Validate()
}

// Initialize 初始化策略（BBGO风格）
func (s *DataRecorderStrategy) Initialize() error {
	// 确保 ctx 和 cancel 已初始化（通过 YAML/JSON 反序列化创建时可能为 nil）
	if s.ctx == nil {
		ctx, cancel := context.WithCancel(context.Background())
		s.ctx = ctx
		s.cancel = cancel
	}

	s.config = &s.DataRecorderStrategyConfig
	if err := s.DataRecorderStrategyConfig.Validate(); err != nil {
		return fmt.Errorf("配置验证失败: %w", err)
	}

	// market spec：默认 btc/15m/updown；如果全局配置存在则以全局 market 为准
	spec, _ := marketspec.New("btc", "15m", "updown")
	if globalConfig := config.Get(); globalConfig != nil {
		if sp, err := globalConfig.Market.Spec(); err == nil {
			spec = sp
		}
	}
	s.marketSpec = spec
	s.marketIntervalSecs = int64(spec.Duration().Seconds())
	s.marketSlugPrefix = strings.ToLower(spec.SlugPrefix())
	s.underlyingSymbol = strings.ToUpper(spec.Symbol)
	s.chainlinkFeedSymbol = fmt.Sprintf("%s/usd", strings.ToLower(spec.Symbol))

	// 创建数据记录器（流式写入）
	recorder, err := NewDataRecorder(s.OutputDir)
	if err != nil {
		return fmt.Errorf("创建数据记录器失败: %w", err)
	}
	s.recorder = recorder

	// 创建 RTDS 客户端
	// 创建一个适配器，将 RTDS 日志输出到我们的 logger
	rtdsLogger := &rtdsLoggerAdapter{}

	// 获取代理 URL（优先级：策略配置 > 全局配置 > 环境变量）
	proxyURL := s.ProxyURL
	if proxyURL == "" {
		// 尝试从全局配置获取
		if globalConfig := config.Get(); globalConfig != nil && globalConfig.Proxy != nil {
			proxyURL = fmt.Sprintf("http://%s:%d", globalConfig.Proxy.Host, globalConfig.Proxy.Port)
			logger.Debugf("数据记录策略: 从全局配置获取代理 URL: %s", proxyURL)
		} else {
			// 尝试从环境变量获取
			if envProxy := os.Getenv("HTTP_PROXY"); envProxy != "" {
				proxyURL = envProxy
				logger.Debugf("数据记录策略: 从环境变量获取代理 URL: %s", proxyURL)
			} else if envProxy := os.Getenv("HTTPS_PROXY"); envProxy != "" {
				proxyURL = envProxy
				logger.Debugf("数据记录策略: 从环境变量获取代理 URL: %s", proxyURL)
			}
		}
	}

	rtdsConfig := &rtds.ClientConfig{
		URL:            rtds.RTDSWebSocketURL,
		ProxyURL:       proxyURL, // 设置代理 URL
		PingInterval:   5 * time.Second,
		WriteTimeout:   10 * time.Second,
		ReadTimeout:    60 * time.Second,
		Reconnect:      true,
		ReconnectDelay: 5 * time.Second,
		MaxReconnect:   10,
		Logger:         rtdsLogger, // 使用我们的 logger 适配器
	}

	if proxyURL != "" {
		logger.Infof("数据记录策略: 使用代理连接 RTDS: %s", proxyURL)
	} else {
		logger.Warnf("数据记录策略: 未配置代理，将直接连接 RTDS（可能失败）")
	}

	rtdsClient := rtds.NewClientWithConfig(rtdsConfig)
	s.rtdsClient = rtdsClient

	// 创建目标价获取器
	useFallback := true
	if s.UseRTDSFallback != nil {
		useFallback = *s.UseRTDSFallback
	}
	s.targetPriceFetcher = NewTargetPriceFetcher(useFallback, rtdsClient, s.underlyingSymbol, s.marketIntervalSecs)

	// 连接 RTDS
	logger.Infof("数据记录策略: 正在连接 RTDS...")
	if err := rtdsClient.Connect(); err != nil {
		return fmt.Errorf("连接 RTDS 失败: %w", err)
	}
	logger.Infof("数据记录策略: RTDS 连接成功")

	// 订阅 Chainlink 标的价格（使用 Chainlink 作为实时价格数据源）
	// BTC 价格更新时，只更新内存中的价格，不记录数据
	// 数据记录以 UP/DOWN 价格变化为准
	var chainlinkFirstMsgOnce sync.Once
	var btcFirstMatchOnce sync.Once
	btcHandler := rtds.CreateCryptoPriceHandler(func(price *rtds.CryptoPrice) error {
		val := price.Value.Float64()
		sym := strings.ToLower(strings.TrimSpace(price.Symbol))
		chainlinkFirstMsgOnce.Do(func() {
			logger.Infof("数据记录策略: ✅ RTDS 已收到 crypto_prices_chainlink 首条消息 - symbol=%s ts=%d value=%.6f", sym, price.Timestamp, val)
		})
		// 提升日志级别，确保能看到所有 Chainlink 价格消息
		logger.Infof("数据记录策略: 📡 收到 Chainlink 价格消息 - Symbol=%s, Value=%.2f, Timestamp=%d", sym, val, price.Timestamp)
		if sym == strings.ToLower(strings.TrimSpace(s.chainlinkFeedSymbol)) {
			btcFirstMatchOnce.Do(func() {
				logger.Infof("数据记录策略: ✅ RTDS 已收到 BTC 实时报价首条有效消息 - symbol=%s ts=%d value=%.6f", sym, price.Timestamp, val)
			})
			// 格式化时间戳（毫秒转秒）
			timestamp := time.Unix(price.Timestamp/1000, (price.Timestamp%1000)*1000000)

			s.mu.Lock()
			oldPrice := s.btcRealtimePrice
			// 只更新 BTC 实时价格，不记录数据
			s.btcRealtimePrice = val
			s.btcRealtimePriceUpdatedAt = time.Now() // 记录更新时间
			s.mu.Unlock()

			// 在终端显示 Chainlink BTC 实时报价（醒目的格式，与价格更新日志格式一致）
			if oldPrice > 0 {
				change := val - oldPrice
				changePercent := (change / oldPrice) * 100
				if change != 0 {
					logger.Infof("💰 BTC 实时报价 (Chainlink): $%.2f (时间: %s) - 变化: $%.2f (%.2f%%)",
						val, timestamp.Format("15:04:05"), change, changePercent)
				} else {
					logger.Infof("💰 BTC 实时报价 (Chainlink): $%.2f (时间: %s) - 无变化",
						val, timestamp.Format("15:04:05"))
				}
			} else {
				logger.Infof("💰 BTC 实时报价 (Chainlink): $%.2f (时间: %s)",
					val, timestamp.Format("15:04:05"))
			}
			// 注意：不在 BTC 价格更新时记录数据，数据记录以 UP/DOWN 价格变化为准
		} else {
			logger.Debugf("数据记录策略: 收到非 BTC 的 Chainlink 价格消息 - Symbol=%s, Value=%.2f", sym, val)
		}
		return nil
	})

	// 注册 Chainlink 价格处理器
	logger.Infof("数据记录策略: 注册 Chainlink 价格处理器 (topic: crypto_prices_chainlink)")
	rtdsClient.RegisterHandler("crypto_prices_chainlink", btcHandler)

	logger.Infof("数据记录策略: 正在订阅 Chainlink 价格 (%s)...", s.chainlinkFeedSymbol)
	if err := rtdsClient.SubscribeToCryptoPrices("chainlink", s.chainlinkFeedSymbol); err != nil {
		return fmt.Errorf("订阅 Chainlink BTC 价格失败: %w", err)
	}
	logger.Infof("数据记录策略: Chainlink BTC 价格订阅成功 (等待首条报价...)")
	logger.Infof("数据记录策略: RTDS 状态快照(订阅后): %s", rtdsClient.DebugSnapshot())

	// 自检：订阅成功后若长期未收到 BTC 报价，输出快照便于定位（订阅未生效/topic 不一致/解析失败）
	go func() {
		select {
		case <-time.After(15 * time.Second):
			s.mu.RLock()
			btcRealtime := s.btcRealtimePrice
			s.mu.RUnlock()
			if btcRealtime <= 0 {
				logger.Warnf("数据记录策略: ⚠️ RTDS 订阅后 15s 仍未收到 BTC 实时报价（btcRealtime=%.2f）。可能原因：订阅未真正生效、topic/filters 不匹配、或上游返回非 JSON/空帧导致解析失败。RTDS 快照=%s",
					btcRealtime, rtdsClient.DebugSnapshot())
			}
		case <-s.ctx.Done():
			return
		}
	}()

	logger.Infof("数据记录策略已初始化: 输出目录=%s, RTDS备选=%v, 实时价格源=Chainlink",
		s.OutputDir, useFallback)

	return nil
}

// OnPriceChanged 处理价格变化事件（快路径：只合并信号，实际逻辑在 loop 内串行执行）
func (s *DataRecorderStrategy) OnPriceChanged(ctx context.Context, event *events.PriceChangedEvent) error {
	if event == nil {
		return nil
	}
	// 添加诊断日志（仅在 Debug 级别，避免日志过多）
	if event.Market != nil && s.isSelectedMarket(event.Market) {
		logger.Debugf("数据记录策略: 收到价格变化事件 - 市场=%s, Token=%s, 价格=%.4f",
			event.Market.Slug, event.TokenType, event.NewPrice.ToDecimal())
	}
	// loop 使用策略自身长期 ctx，避免周期切换 cancel 导致 loop 停摆
	s.startLoop(s.ctx)
	s.priceMu.Lock()
	if s.latestPrices == nil {
		s.latestPrices = make(map[domain.TokenType]*events.PriceChangedEvent)
	}
	s.latestPrices[event.TokenType] = event
	s.priceMu.Unlock()
	common.TrySignal(s.priceSignalC)
	return nil
}

func (s *DataRecorderStrategy) onPriceChangedInternal(ctx context.Context, event *events.PriceChangedEvent) error {

	// NOTE: 不要在高频回调里 fmt.Println，会污染日志且影响性能
	// 只处理当前配置选择的市场（避免其它市场事件误入）
	if !s.isSelectedMarket(event.Market) {
		logger.Debugf("数据记录策略: 跳过非目标市场 - %s", getSlugOrEmpty(event.Market))
		return nil
	}

	logger.Debugf("数据记录策略: 处理价格变化 - 市场=%s, Token=%s, 价格=%.4f",
		event.Market.Slug, event.TokenType, event.NewPrice.ToDecimal())

	s.mu.Lock()

	// 只忽略“更旧周期”的延迟事件；新周期的第一条事件必须允许触发切换。
	// 这里以 Market.Timestamp 作为周期单调递增的判定依据（比 slug 字符串更稳）。
	if s.currentMarket != nil && event.Market != nil {
		if s.currentMarket.Timestamp > 0 && event.Market.Timestamp > 0 && event.Market.Timestamp < s.currentMarket.Timestamp {
			logger.Warnf("数据记录策略: ⚠️ 忽略更旧周期的延迟价格事件 - 当前周期=%s(ts=%d), 事件周期=%s(ts=%d), Token=%s, 价格=%.4f",
				s.currentMarket.Slug, s.currentMarket.Timestamp, event.Market.Slug, event.Market.Timestamp, event.TokenType, event.NewPrice.ToDecimal())
			s.mu.Unlock()
			return nil
		}
	}

	// 二次防护：即使 slug 相同，也要求事件时间不早于周期开始时间（避免对象复用/乱序导致的“旧事件混入”）
	// - event.Timestamp 来自 MarketStream 侧的 time.Now()，可作为“接收时间”近似
	// - Market.Timestamp 来自 slug 解析，代表周期开始时间
	if s.currentMarket != nil && s.currentMarket.Timestamp > 0 && !event.Timestamp.IsZero() {
		evtTs := event.Timestamp.Unix()
		if evtTs < s.currentMarket.Timestamp-1 {
			logger.Warnf("数据记录策略: ⚠️ 忽略疑似旧周期/乱序的价格事件 - 当前周期=%s(start=%d), eventTs=%d, Token=%s, 价格=%.4f",
				s.currentMarket.Slug, s.currentMarket.Timestamp, evtTs, event.TokenType, event.NewPrice.ToDecimal())
			s.mu.Unlock()
			return nil
		}
	}

	// 检查是否切换到新周期：只在“市场对象确实变更（timestamp/slug 单调前进）”时切换。
	// 注意：不要在这里仅凭 now>=cycleEndTs 去“猜下一个 market”，因为 asset_id 会变化且我们没有完整 market 信息。
	shouldSwitchCycle := false
	if s.currentMarket == nil {
		shouldSwitchCycle = true
		logger.Infof("数据记录策略: 初始化当前周期: %s(ts=%d)", event.Market.Slug, event.Market.Timestamp)
	} else if event.Market != nil {
		if event.Market.Timestamp > 0 && s.currentMarket.Timestamp > 0 && event.Market.Timestamp > s.currentMarket.Timestamp {
			shouldSwitchCycle = true
			logger.Infof("数据记录策略: 检测到周期切换 (timestamp 前进: %s[%d] -> %s[%d])",
				s.currentMarket.Slug, s.currentMarket.Timestamp, event.Market.Slug, event.Market.Timestamp)
		} else if event.Market.Slug != "" && s.currentMarket.Slug != "" && event.Market.Slug != s.currentMarket.Slug {
			// 兜底：timestamp 缺失/为 0 时，用 slug 变化触发
			shouldSwitchCycle = true
			logger.Infof("数据记录策略: 检测到周期切换 (slug 变化: %s -> %s)",
				s.currentMarket.Slug, event.Market.Slug)
		}
	}

	if shouldSwitchCycle {
		// 防止重复切换周期
		if s.switchingCycle {
			logger.Debugf("数据记录策略: 周期切换正在进行中，跳过重复切换")
			s.mu.Unlock()
			return nil
		}
		s.switchingCycle = true

		// 周期切换：先刷新并关闭上一个周期文件（只做一次）
		if s.currentMarket != nil {
			oldSlug := s.currentMarket.Slug
			logger.Infof("数据记录策略: 保存旧周期数据: %s", oldSlug)
			if err := s.recorder.SaveCurrentCycle(); err != nil {
				logger.Errorf("保存周期数据失败: %v", err)
			} else {
				logger.Infof("数据记录策略: 旧周期数据已保存: %s", oldSlug)
			}
		}

		// 更新市场信息（切换到新周期）
		s.currentMarket = event.Market

		// 重置所有价格状态（新周期需要重新获取）
		s.btcTargetPrice = 0
		s.btcTargetPriceSet = false
		s.upPrice = 0   // 清理旧周期的 UP 价格
		s.downPrice = 0 // 清理旧周期的 DOWN 价格
		logger.Debugf("数据记录策略: 周期切换时已清理所有价格状态")

		// 开始新周期（按 slug 打开对应 CSV 文件，后续实时追加）
		logger.Infof("数据记录策略: 开始新周期: %s (时间戳=%d)", event.Market.Slug, event.Market.Timestamp)
		if err := s.recorder.StartCycle(event.Market.Slug); err != nil {
			logger.Errorf("开始新周期失败: %v", err)
			s.switchingCycle = false
			s.mu.Unlock()
			return err
		}
		logger.Infof("数据记录策略: 新周期已启动: %s", event.Market.Slug)

		// 获取新周期的目标价（上一个周期收盘价）
		currentCycleStart := event.Market.Timestamp
		s.mu.Unlock() // IO/HTTP 放锁外

		// 同步获取目标价，确保在记录数据前目标价已设置。
		// 关键：不要使用 price event 的 ctx（它可能随着 WS/连接关闭而 cancel，导致 context canceled）。
		targetCtx, targetCancel := context.WithTimeout(s.ctx, 10*time.Second)
		defer targetCancel()

		targetPrice, err := s.targetPriceFetcher.FetchTargetPrice(targetCtx, currentCycleStart)
		if err != nil {
			// 退化策略（避免一直为 0 导致无法记录）：
			// - 优先用当前已知的 Chainlink 实时报价作为近似目标价（误差可接受时，至少保证数据可写入）
			s.mu.RLock()
			rt := s.btcRealtimePrice
			rtAge := time.Since(s.btcRealtimePriceUpdatedAt)
			s.mu.RUnlock()
			if rt > 0 && rtAge < 30*time.Second {
				targetPrice = rt
				logger.Warnf("获取目标价失败: %v，使用近期 Chainlink 实时报价作为目标价近似: %.2f (age=%s)", err, targetPrice, rtAge)
			} else {
				logger.Warnf("获取目标价失败: %v，且无可用的近期 Chainlink 报价作为退化方案（rt=%.2f age=%s），目标价保持 0", err, rt, rtAge)
				targetPrice = 0
			}
		}

		s.mu.Lock()
		s.btcTargetPrice = targetPrice
		s.btcTargetPriceSet = true
		s.switchingCycle = false
		s.mu.Unlock()

		logger.Infof("数据记录策略: 新周期 %s，目标价=%.2f (已设置)", event.Market.Slug, targetPrice)
		s.mu.Lock()
	}

	// 更新价格
	if event.TokenType == domain.TokenTypeUp {
		s.upPrice = event.NewPrice.ToDecimal()
	} else if event.TokenType == domain.TokenTypeDown {
		s.downPrice = event.NewPrice.ToDecimal()
	}

	// 获取当前所有价格（用于记录数据点）
	btcTarget := s.btcTargetPrice
	btcTargetSet := s.btcTargetPriceSet
	btcRealtime := s.btcRealtimePrice
	upPrice := s.upPrice
	downPrice := s.downPrice
	currentCycleSlug := ""
	if s.currentMarket != nil {
		currentCycleSlug = s.currentMarket.Slug
	}
	s.mu.Unlock()

	// 以 UP/DOWN 价格变化为准，记录数据点
	// 此时保存当前的 BTC 实时价格（由 RTDS 实时更新）
	// 如果 RTDS 价格未更新，使用目标价作为实时价格的降级方案
	if btcRealtime <= 0 && btcTarget > 0 {
		// RTDS 价格未更新，使用目标价作为实时价格（降级方案）
		logger.Debugf("数据记录策略: RTDS 价格未更新，使用目标价作为实时价格 (目标价=%.2f)", btcTarget)
		btcRealtime = btcTarget
	}

	// 记录 BTC 实时价格的时间戳，用于追踪价格更新情况
	// 注意：BTC 价格更新频率可能低于 UP/DOWN 价格变化频率，这是正常的

	// 价格合理性保护（降低误报，主要防止“旧周期残留/极端值”）：
	priceSum := upPrice + downPrice
	if priceSum > 1.1 {
		logger.Warnf("数据记录策略: ⚠️ 检测到异常价格总和（可能是旧周期残留），跳过记录 - UP=%.4f, DOWN=%.4f, 总和=%.4f, 当前周期=%s",
			upPrice, downPrice, priceSum, getSlugOrEmpty(s.currentMarket))
		return nil
	}
	// 注意：priceSum 可能 < 1（例如使用 best_bid 或市场处于稀疏/价差较大阶段），不应仅凭 <=1.0 直接判异常。

	//// 3. 检查价格差异是否合理：正常情况下 UP 和 DOWN 的价格应该比较接近
	////    如果价格差异过大（如 UP=0.01, DOWN=1.00），说明数据异常
	//priceDiff := upPrice - downPrice
	//if priceDiff < 0 {
	//	priceDiff = -priceDiff // 取绝对值
	//}
	//// 正常情况下，两个价格的差异不应该超过 0.5（50美分）
	//// 如果差异过大，可能是数据错误或旧周期残留
	//if priceDiff > 0.5 {
	//	logger.Warnf("数据记录策略: ⚠️ 检测到价格差异过大（可能是数据错误），跳过记录 - UP=%.4f, DOWN=%.4f, 差异=%.4f, 总和=%.4f, 当前周期=%s",
	//		upPrice, downPrice, priceDiff, priceSum, getSlugOrEmpty(s.currentMarket))
	//	return nil
	//}

	// 4. 新周期早期保护：在周期开始后短窗口内，过滤异常数据
	//    新周期开始时，市场可能处于异常状态（如结算、初始化），价格可能极端
	if s.currentMarket != nil && s.currentMarket.Timestamp > 0 {
		now := time.Now().Unix()
		cycleAge := now - s.currentMarket.Timestamp
		// 默认窗口：新周期开始 60 秒内
		if cycleAge <= 60 {
			// 4.1: 单个价格 >= 0.99（可能是旧周期残留或市场异常）
			if upPrice >= 0.99 || downPrice >= 0.99 {
				logger.Warnf("数据记录策略: ⚠️ 新周期早期检测到异常高价（可能是市场异常状态），跳过记录 - UP=%.4f, DOWN=%.4f, 总和=%.4f, 周期年龄=%d秒, 当前周期=%s",
					upPrice, downPrice, priceSum, cycleAge, getSlugOrEmpty(s.currentMarket))
				return nil
			}
			// 4.2: 单个价格 <= 0.05（可能是市场异常状态）
			if upPrice <= 0.05 || downPrice <= 0.05 {
				logger.Warnf("数据记录策略: ⚠️ 新周期早期检测到异常低价（可能是市场异常状态），跳过记录 - UP=%.4f, DOWN=%.4f, 总和=%.4f, 周期年龄=%d秒, 当前周期=%s",
					upPrice, downPrice, priceSum, cycleAge, getSlugOrEmpty(s.currentMarket))
				return nil
			}
		}
	}

	// 只有在目标价已设置时才记录数据，避免记录0值
	if btcRealtime > 0 && upPrice > 0 && downPrice > 0 {
		if !btcTargetSet || btcTarget <= 0 {
			logger.Warnf("数据记录策略: 目标价未就绪，跳过记录 (BTC目标=%.2f, BTC实时=%.2f, UP=%.4f, DOWN=%.4f)",
				btcTarget, btcRealtime, upPrice, downPrice)
			return nil
		}
		// 记录数据点
		// 检查 BTC 实时价格是否是最新的（在最近 5 秒内更新过）
		s.mu.RLock()
		priceAge := time.Since(s.btcRealtimePriceUpdatedAt)
		s.mu.RUnlock()

		priceStatus := "最新"
		if priceAge > 5*time.Second {
			priceStatus = fmt.Sprintf("已过期(%.0f秒前)", priceAge.Seconds())
		}

		if err := s.recorder.Record(DataPoint{
			Timestamp:        time.Now().Unix(),
			BTCTargetPrice:   btcTarget,
			BTCRealtimePrice: btcRealtime,
			UpPrice:          upPrice,
			DownPrice:        downPrice,
			CycleSlug:        currentCycleSlug,
		}); err != nil {
			logger.Errorf("数据记录策略: 记录数据点失败: %v", err)
			return err
		}
		logger.Infof("数据记录策略: ✅ 已记录数据点 (BTC目标=%.2f, BTC实时=%.2f[%s], UP=%.4f, DOWN=%.4f)",
			btcTarget, btcRealtime, priceStatus, upPrice, downPrice)
	} else {
		logger.Warnf("数据记录策略: 价格未就绪，跳过记录 (BTC实时=%.2f, UP=%.4f, DOWN=%.4f, 目标价已设置=%v)",
			btcRealtime, upPrice, downPrice, btcTargetSet)
	}

	return nil
}

// recordDataPoint 记录数据点（已废弃，直接使用 recorder.Record）
// 保留此方法以保持向后兼容
func (s *DataRecorderStrategy) recordDataPoint(btcTarget, btcRealtime, upPrice, downPrice float64) {
	point := DataPoint{
		Timestamp:        time.Now().Unix(),
		BTCTargetPrice:   btcTarget,
		BTCRealtimePrice: btcRealtime,
		UpPrice:          upPrice,
		DownPrice:        downPrice,
		CycleSlug:        "", // 已废弃方法，周期名称由 Record 方法自动从 currentCycle 获取
	}

	if err := s.recorder.Record(point); err != nil {
		logger.Errorf("数据记录策略: 记录数据点失败: %v", err)
	}
}

// OnOrderFilled 处理订单成交事件（空实现，不交易）
func (s *DataRecorderStrategy) OnOrderFilled(ctx context.Context, event *events.OrderFilledEvent) error {
	// 不进行交易，空实现
	return nil
}

// CanOpenPosition 检查是否可以开仓（返回 false，不交易）
func (s *DataRecorderStrategy) CanOpenPosition(ctx context.Context, market *domain.Market) (bool, error) {
	return false, nil
}

// CalculateEntry 计算入场价格和数量（返回 nil，不交易）
func (s *DataRecorderStrategy) CalculateEntry(ctx context.Context, market *domain.Market, price domain.Price) (*domain.Order, error) {
	return nil, nil
}

// CalculateHedge 计算对冲订单（返回 nil，不交易）
func (s *DataRecorderStrategy) CalculateHedge(ctx context.Context, entryOrder *domain.Order) (*domain.Order, error) {
	return nil, nil
}

// CheckTakeProfitStopLoss 检查止盈止损（返回 nil，不交易）
func (s *DataRecorderStrategy) CheckTakeProfitStopLoss(ctx context.Context, position *domain.Position, currentPrice domain.Price) (*domain.Order, error) {
	return nil, nil
}

// cycleCheckLoop 周期检查循环，每秒检查当前时间，主动触发周期切换
func (s *DataRecorderStrategy) cycleCheckLoop(ctx context.Context) {
	defer s.cycleCheckWg.Done()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Debugf("数据记录策略: 周期检查循环收到取消信号")
			return
		case <-s.cycleCheckStop:
			logger.Debugf("数据记录策略: 周期检查循环收到停止信号")
			return
		case <-ticker.C:
			s.checkAndSwitchCycleByTime(ctx)
		}
	}
}

// checkAndSwitchCycleByTime 基于时间戳检查并切换周期
func (s *DataRecorderStrategy) checkAndSwitchCycleByTime(ctx context.Context) {
	s.mu.Lock()
	currentMarket := s.currentMarket
	s.mu.Unlock()

	if currentMarket == nil {
		return
	}

	now := time.Now().Unix()
	interval := s.marketIntervalSecs
	if interval <= 0 {
		interval = 900
	}
	cycleEndTs := currentMarket.Timestamp + interval

	// 如果当前时间超过周期结束时间：只做“落盘/封存”。
	// 不要在这里“猜下一个 market 并 StartCycle”，因为：
	// - 15m 市场的 asset_id/condition_id 会变化，策略不掌握完整 market 信息
	// - MarketScheduler 会负责真正的市场切换与重新订阅，价格事件会携带正确的新 market
	if now >= cycleEndTs {
		// 防止重复落盘刷屏：使用 switchingCycle 作为“正在 finalize”的简易互斥
		s.mu.Lock()
		if s.switchingCycle {
			s.mu.Unlock()
			return
		}
		s.switchingCycle = true
		s.mu.Unlock()

		logger.Infof("数据记录策略: 周期已结束，执行落盘封存: %s (now=%d end=%d)", currentMarket.Slug, now, cycleEndTs)
		if err := s.recorder.SaveCurrentCycle(); err != nil {
			logger.Errorf("数据记录策略: 周期落盘失败: %v", err)
		} else {
			logger.Infof("数据记录策略: 周期已落盘封存: %s", currentMarket.Slug)
		}

		s.mu.Lock()
		s.switchingCycle = false
		s.mu.Unlock()
	}
}

// getSlugOrEmpty 安全获取 Market.Slug，如果 Market 为 nil 返回空字符串
func getSlugOrEmpty(market *domain.Market) string {
	if market == nil {
		return ""
	}
	return market.Slug
}

// Cleanup 清理资源
func (s *DataRecorderStrategy) Cleanup(ctx context.Context) error {
	logger.Info("数据记录策略: 开始清理资源...")

	// 停止周期检查循环
	if s.cycleCheckStop != nil {
		close(s.cycleCheckStop)
		logger.Debugf("数据记录策略: 已发送周期检查循环停止信号")
	}

	// 取消上下文（这会触发周期检查循环退出）
	if s.cancel != nil {
		s.cancel()
		logger.Debugf("数据记录策略: 已取消上下文")
	}

	// 等待周期检查循环退出（带超时）
	done := make(chan struct{})
	go func() {
		s.cycleCheckWg.Wait()
		close(done)
	}()

	select {
	case <-done:
		logger.Debugf("数据记录策略: 周期检查循环已退出")
	case <-time.After(2 * time.Second):
		logger.Warnf("数据记录策略: 等待周期检查循环退出超时，继续执行清理")
	}

	// 保存当前周期数据
	if s.recorder != nil {
		s.mu.RLock()
		currentCycle := s.recorder.GetCurrentCycle()
		s.mu.RUnlock()

		if currentCycle != "" {
			logger.Infof("数据记录策略: 保存最后周期数据: %s", currentCycle)
		}
		if err := s.recorder.SaveCurrentCycle(); err != nil {
			logger.Errorf("保存最后周期数据失败: %v", err)
		} else {
			if currentCycle != "" {
				logger.Infof("数据记录策略: 最后周期数据已保存: %s", currentCycle)
			}
		}
	}

	// 断开 RTDS 连接（带超时）
	if s.rtdsClient != nil {
		logger.Debugf("数据记录策略: 开始断开 RTDS 连接...")
		disconnectDone := make(chan error, 1)
		go func() {
			disconnectDone <- s.rtdsClient.Disconnect()
		}()

		select {
		case err := <-disconnectDone:
			if err != nil {
				logger.Errorf("断开 RTDS 连接失败: %v", err)
			} else {
				logger.Debugf("数据记录策略: RTDS 连接已断开")
			}
		case <-time.After(5 * time.Second):
			logger.Warnf("数据记录策略: 断开 RTDS 连接超时（5秒），继续执行清理")
		}
	}

	logger.Info("数据记录策略已清理")
	return nil
}

// Subscribe 订阅会话事件（BBGO 风格）
func (s *DataRecorderStrategy) Subscribe(session *bbgo.ExchangeSession) {
	// 注册价格变化回调
	session.OnPriceChanged(s)
	log.Infof("数据记录策略已订阅价格变化事件")
}

// Run 运行策略（BBGO 风格）
func (s *DataRecorderStrategy) Run(ctx context.Context, orderExecutor bbgo.OrderExecutor, session *bbgo.ExchangeSession) error {
	log.Infof("数据记录策略已启动")
	// loop 使用策略自身长期 ctx，避免周期切换 cancel 导致 loop 停摆
	s.startLoop(s.ctx)
	return nil
}

// Shutdown 优雅关闭（BBGO 风格）
// Shutdown 优雅关闭（BBGO 风格）
// 注意：wg 参数由 shutdown.Manager 统一管理，策略的 Shutdown 方法不应该调用 wg.Done()
func (s *DataRecorderStrategy) Shutdown(ctx context.Context, wg *sync.WaitGroup) {
	log.Infof("数据记录策略: 开始优雅关闭...")
	s.stopLoop()
	if err := s.Cleanup(ctx); err != nil {
		log.Errorf("数据记录策略清理失败: %v", err)
	}
	log.Infof("数据记录策略: 资源清理完成")
}

// isSelectedMarket 检查是否是当前配置选择的市场（通过 slug 前缀匹配）。
func (s *DataRecorderStrategy) isSelectedMarket(market *domain.Market) bool {
	if market == nil {
		return false
	}
	prefix := strings.TrimSpace(s.marketSlugPrefix)
	if prefix == "" {
		prefix = "btc-updown-15m-"
	}
	return strings.HasPrefix(strings.ToLower(market.Slug), strings.ToLower(prefix))
}
