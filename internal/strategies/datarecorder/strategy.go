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
	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/betbot/gobet/pkg/config"
	"github.com/betbot/gobet/pkg/logger"
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
	logger.Infof("[RTDS] "+format, v...)
}

// DataRecorderStrategy 数据记录策略
type DataRecorderStrategy struct {
	Executor                   bbgo.CommandExecutor
	DataRecorderStrategyConfig `yaml:",inline" json:",inline"`
	config                     *DataRecorderStrategyConfig `json:"-" yaml:"-"`
	recorder                   *DataRecorder
	targetPriceFetcher         *TargetPriceFetcher
	rtdsClient                 *rtds.Client
	currentMarket              *domain.Market
	btcTargetPrice             float64 // BTC 目标价（上一个周期收盘价）
	btcTargetPriceSet          bool    // 目标价是否已设置（防止周期内重复设置）
	btcRealtimePrice           float64 // BTC 实时价
	upPrice                    float64 // UP 价格
	downPrice                  float64 // DOWN 价格

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
	s.config = &s.DataRecorderStrategyConfig
	if err := s.DataRecorderStrategyConfig.Validate(); err != nil {
		return fmt.Errorf("配置验证失败: %w", err)
	}

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
	s.targetPriceFetcher = NewTargetPriceFetcher(useFallback, rtdsClient)

	// 连接 RTDS
	logger.Infof("数据记录策略: 正在连接 RTDS...")
	if err := rtdsClient.Connect(); err != nil {
		return fmt.Errorf("连接 RTDS 失败: %w", err)
	}
	logger.Infof("数据记录策略: RTDS 连接成功")

	// 订阅 Chainlink BTC 价格（使用 Chainlink 作为实时价格数据源）
	// BTC 价格更新时，只更新内存中的价格，不记录数据
	// 数据记录以 UP/DOWN 价格变化为准
	btcHandler := rtds.CreateCryptoPriceHandler(func(price *rtds.CryptoPrice) error {
		val := price.Value.Float64()
		sym := strings.ToLower(strings.TrimSpace(price.Symbol))
		logger.Debugf("数据记录策略: 收到 Chainlink 价格消息 - Symbol=%s, Value=%.2f", sym, val)
		if sym == "btc/usd" || sym == "btcusdt" || sym == "btc/usdt" {
			// 格式化时间戳（毫秒转秒）
			timestamp := time.Unix(price.Timestamp/1000, (price.Timestamp%1000)*1000000)

			// 在终端显示 Chainlink BTC 实时报价（醒目的格式，与价格更新日志格式一致）
			logger.Infof("💰 BTC 实时报价 (Chainlink): $%.2f (时间: %s)",
				val, timestamp.Format("15:04:05"))

			s.mu.Lock()
			oldPrice := s.btcRealtimePrice
			// 只更新 BTC 实时价格，不记录数据
			s.btcRealtimePrice = val
			s.mu.Unlock()

			// 如果有价格变化，显示变化趋势
			if oldPrice > 0 {
				change := val - oldPrice
				changePercent := (change / oldPrice) * 100
				if change > 0 {
					logger.Infof("📈 BTC 价格变化: +$%.2f (+%.2f%%)", change, changePercent)
				} else if change < 0 {
					logger.Infof("📉 BTC 价格变化: $%.2f (%.2f%%)", change, changePercent)
				}
			}
			// 注意：不在 BTC 价格更新时记录数据，数据记录以 UP/DOWN 价格变化为准
		}
		return nil
	})

	// 注册 Chainlink 价格处理器
	logger.Infof("数据记录策略: 注册 Chainlink 价格处理器 (topic: crypto_prices_chainlink)")
	rtdsClient.RegisterHandler("crypto_prices_chainlink", btcHandler)

	logger.Infof("数据记录策略: 正在订阅 Chainlink BTC 价格 (btc/usd)...")
	if err := rtdsClient.SubscribeToCryptoPrices("chainlink", "btc/usd"); err != nil {
		return fmt.Errorf("订阅 Chainlink BTC 价格失败: %w", err)
	}
	logger.Infof("数据记录策略: Chainlink BTC 价格订阅成功")

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
	if event.Market != nil && s.isBTC15mMarket(event.Market) {
		logger.Debugf("数据记录策略: 收到价格变化事件 - 市场=%s, Token=%s, 价格=%.4f",
			event.Market.Slug, event.TokenType, event.NewPrice.ToDecimal())
	}
	s.startLoop(ctx)
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
	// 只处理 btc-updown-15m-* 市场
	if !s.isBTC15mMarket(event.Market) {
		logger.Debugf("数据记录策略: 跳过非 BTC 15分钟市场 - %s", getSlugOrEmpty(event.Market))
		return nil
	}

	logger.Debugf("数据记录策略: 处理价格变化 - 市场=%s, Token=%s, 价格=%.4f",
		event.Market.Slug, event.TokenType, event.NewPrice.ToDecimal())

	s.mu.Lock()

	// 首先验证事件是否属于当前周期（防止旧周期的延迟事件污染数据）
	if s.currentMarket != nil && s.currentMarket.Slug != "" && s.currentMarket.Slug != event.Market.Slug {
		logger.Warnf("数据记录策略: ⚠️ 忽略非当前周期的价格事件 - 当前周期=%s, 事件周期=%s, Token=%s, 价格=%.4f",
			s.currentMarket.Slug, event.Market.Slug, event.TokenType, event.NewPrice.ToDecimal())
		s.mu.Unlock()
		return nil // 直接返回，不处理旧周期的事件
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

	// 检查是否切换到新周期（基于 Market.Slug 变化）
	// 同时检查时间戳，确保即使 Market.Slug 相同但时间已过周期结束时间，也要切换
	shouldSwitchCycle := false
	if s.currentMarket == nil || s.currentMarket.Slug != event.Market.Slug {
		shouldSwitchCycle = true
		logger.Infof("数据记录策略: 检测到周期切换 (Slug变化: %s -> %s)",
			getSlugOrEmpty(s.currentMarket), event.Market.Slug)
	} else if s.currentMarket != nil {
		// 基于时间戳的周期检测：如果当前时间超过周期结束时间（周期开始时间 + 15分钟），也要切换
		now := time.Now().Unix()
		cycleEndTs := s.currentMarket.Timestamp + 900 // 15 分钟 = 900 秒
		if now >= cycleEndTs {
			shouldSwitchCycle = true
			logger.Infof("数据记录策略: 检测到周期切换 (时间戳检测: 当前=%d, 周期结束=%d)",
				now, cycleEndTs)
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

		// 周期切换：先刷新并关闭上一个周期文件
		if s.currentMarket != nil {
			oldSlug := s.currentMarket.Slug
			logger.Infof("数据记录策略: 保存旧周期数据: %s", oldSlug)
			if err := s.recorder.SaveCurrentCycle(); err != nil {
				logger.Errorf("保存周期数据失败: %v", err)
			} else {
				logger.Infof("数据记录策略: 旧周期数据已保存: %s", oldSlug)
			}
		}

		// 更新市场信息
		oldMarket := s.currentMarket
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
		s.mu.Unlock()

		// 同步获取目标价，确保在记录数据前目标价已设置
		// 使用带超时的 context 避免无限期等待
		targetCtx, targetCancel := context.WithTimeout(ctx, 10*time.Second)
		defer targetCancel()

		targetPrice, err := s.targetPriceFetcher.FetchTargetPrice(targetCtx, currentCycleStart)
		if err != nil {
			logger.Warnf("获取目标价失败: %v，尝试使用上一个周期的目标价", err)
			// 如果获取失败，尝试使用上一个周期的目标价（如果有）
			if oldMarket != nil {
				// 尝试使用上一个周期的目标价（如果之前有设置）
				s.mu.Lock()
				oldTargetPrice := s.btcTargetPrice
				s.mu.Unlock()
				if oldTargetPrice > 0 {
					targetPrice = oldTargetPrice
					logger.Infof("数据记录策略: 使用上一个周期的目标价: %.2f", targetPrice)
				} else {
					targetPrice = 0
					logger.Warnf("数据记录策略: 上一个周期的目标价也为0，将使用0（数据可能无法记录）")
				}
			} else {
				targetPrice = 0
				logger.Warnf("数据记录策略: 没有上一个周期，将使用0（数据可能无法记录）")
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
	s.mu.Unlock()

	// 以 UP/DOWN 价格变化为准，记录数据点
	// 此时保存当前的 BTC 实时价格（由 RTDS 实时更新）
	// 如果 RTDS 价格未更新，使用目标价作为实时价格的降级方案
	if btcRealtime <= 0 && btcTarget > 0 {
		// RTDS 价格未更新，使用目标价作为实时价格（降级方案）
		logger.Debugf("数据记录策略: RTDS 价格未更新，使用目标价作为实时价格 (目标价=%.2f)", btcTarget)
		btcRealtime = btcTarget
	}

	// 价格合理性保护（更严格/可控）：
	// - 0.99+ 在真实市场也可能出现（并不一定是旧周期）
	// - 真正“像旧周期残留”的情况通常发生在【刚切换到新周期的很短窗口】内收到接近 1 的价格
	// 因此仅在“周期开始后短窗口”触发该保护，避免误杀正常数据。
	if s.currentMarket != nil && s.currentMarket.Timestamp > 0 && (upPrice >= 0.99 || downPrice >= 0.99) {
		now := time.Now().Unix()
		// 默认窗口：新周期开始 45 秒内
		if now-s.currentMarket.Timestamp <= 45 {
			logger.Warnf("数据记录策略: ⚠️ 新周期早期检测到异常高价（更可能是旧订阅残留），跳过记录 - UP=%.4f, DOWN=%.4f, 当前周期=%s(start=%d, now=%d)",
				upPrice, downPrice, getSlugOrEmpty(s.currentMarket), s.currentMarket.Timestamp, now)
			return nil
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
		if err := s.recorder.Record(DataPoint{
			Timestamp:        time.Now().Unix(),
			BTCTargetPrice:   btcTarget,
			BTCRealtimePrice: btcRealtime,
			UpPrice:          upPrice,
			DownPrice:        downPrice,
		}); err != nil {
			logger.Errorf("数据记录策略: 记录数据点失败: %v", err)
			return err
		}
		logger.Infof("数据记录策略: ✅ 已记录数据点 (BTC目标=%.2f, BTC实时=%.2f, UP=%.4f, DOWN=%.4f)",
			btcTarget, btcRealtime, upPrice, downPrice)
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
	cycleEndTs := currentMarket.Timestamp + 900 // 15 分钟 = 900 秒

	// 如果当前时间超过周期结束时间，需要切换到下一个周期
	if now >= cycleEndTs {
		nextTs := cycleEndTs
		nextSlug := fmt.Sprintf("btc-updown-15m-%d", nextTs)

		logger.Infof("数据记录策略: 定时检查检测到周期切换 (当前时间=%d, 周期结束=%d, 下一个周期=%s)",
			now, cycleEndTs, nextSlug)

		// 如果下一个周期的 slug 与当前不同，触发周期切换
		if currentMarket.Slug != nextSlug {
			// 防止重复切换周期
			s.mu.Lock()
			if s.switchingCycle {
				logger.Debugf("数据记录策略: 定时检查时周期切换正在进行中，跳过重复切换")
				s.mu.Unlock()
				return
			}
			s.switchingCycle = true

			// 保存当前市场的字段（用于创建新市场对象）
			yesAssetID := currentMarket.YesAssetID
			noAssetID := currentMarket.NoAssetID
			conditionID := currentMarket.ConditionID
			question := currentMarket.Question

			// 创建临时 Market 对象用于周期切换
			nextMarket := &domain.Market{
				Slug:        nextSlug,
				Timestamp:   nextTs,
				YesAssetID:  yesAssetID,
				NoAssetID:   noAssetID,
				ConditionID: conditionID,
				Question:    question,
			}

			// 保存旧周期数据（currentMarket 已经在上方检查过不为 nil）
			logger.Infof("数据记录策略: 定时检查保存旧周期数据: %s", currentMarket.Slug)
			if err := s.recorder.SaveCurrentCycle(); err != nil {
				logger.Errorf("定时检查保存周期数据失败: %v", err)
			} else {
				logger.Infof("数据记录策略: 定时检查旧周期数据已保存: %s", currentMarket.Slug)
			}

			// 更新市场信息
			s.currentMarket = nextMarket

			// 重置所有价格状态（新周期需要重新获取）
			s.btcTargetPrice = 0
			s.btcTargetPriceSet = false
			s.upPrice = 0   // 清理旧周期的 UP 价格
			s.downPrice = 0 // 清理旧周期的 DOWN 价格
			logger.Debugf("数据记录策略: 定时检查周期切换时已清理所有价格状态")

			// 开始新周期
			logger.Infof("数据记录策略: 定时检查开始新周期: %s (时间戳=%d)", nextMarket.Slug, nextMarket.Timestamp)
			if err := s.recorder.StartCycle(nextMarket.Slug); err != nil {
				logger.Errorf("定时检查开始新周期失败: %v", err)
				s.switchingCycle = false
				s.mu.Unlock()
				return
			}
			logger.Infof("数据记录策略: 定时检查新周期已启动: %s", nextMarket.Slug)

			// 获取新周期的目标价
			currentCycleStart := nextMarket.Timestamp
			// 在解锁前保存旧目标价
			oldTargetPrice := s.btcTargetPrice
			s.mu.Unlock()

			// 同步获取目标价，确保在记录数据前目标价已设置
			targetCtx, targetCancel := context.WithTimeout(ctx, 10*time.Second)
			targetPrice, err := s.targetPriceFetcher.FetchTargetPrice(targetCtx, currentCycleStart)
			targetCancel()

			if err != nil {
				logger.Warnf("定时检查获取目标价失败: %v，尝试使用上一个周期的目标价", err)
				// 尝试使用上一个周期的目标价
				if oldTargetPrice > 0 {
					targetPrice = oldTargetPrice
					logger.Infof("数据记录策略: 定时检查使用上一个周期的目标价: %.2f", targetPrice)
				} else {
					targetPrice = 0
					logger.Warnf("数据记录策略: 定时检查上一个周期的目标价也为0，将使用0（数据可能无法记录）")
				}
			}

			s.mu.Lock()
			s.btcTargetPrice = targetPrice
			s.btcTargetPriceSet = true
			s.switchingCycle = false
			s.mu.Unlock()

			logger.Infof("数据记录策略: 定时检查新周期 %s，目标价=%.2f (已设置)", nextMarket.Slug, targetPrice)
		}
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
	s.startLoop(ctx)
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

// isBTC15mMarket 检查是否是 BTC 15分钟市场
func (s *DataRecorderStrategy) isBTC15mMarket(market *domain.Market) bool {
	if market == nil {
		return false
	}
	return strings.HasPrefix(market.Slug, "btc-updown-15m-")
}
