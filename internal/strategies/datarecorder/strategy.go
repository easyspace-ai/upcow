package datarecorder

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/betbot/gobet/clob/rtds"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/strategies"
	"github.com/betbot/gobet/internal/strategies/common"
	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/betbot/gobet/pkg/logger"
	"github.com/sirupsen/logrus"
)

const ID = "datarecorder"

var log = logrus.WithField("strategy", ID)

func init() {
	// BBGO风格：在init函数中注册策略及其配置适配器
	bbgo.RegisterStrategyWithAdapter(ID, &DataRecorderStrategy{}, &DataRecorderConfigAdapter{})
}

// rtdsLoggerAdapter 适配器，将 RTDS 日志输出到我们的 logger 系统
type rtdsLoggerAdapter struct{}

func (l *rtdsLoggerAdapter) Printf(format string, v ...interface{}) {
	logger.Infof("[RTDS] "+format, v...)
}

// DataRecorderStrategy 数据记录策略
type DataRecorderStrategy struct {
	Executor           bbgo.CommandExecutor
	config             *DataRecorderStrategyConfig
	recorder           *DataRecorder
	targetPriceFetcher *TargetPriceFetcher
	rtdsClient         *rtds.Client
	currentMarket      *domain.Market
	btcTargetPrice     float64 // BTC 目标价（上一个周期收盘价）
	btcTargetPriceSet  bool    // 目标价是否已设置（防止周期内重复设置）
	btcRealtimePrice   float64 // BTC 实时价
	upPrice            float64 // UP 价格
	downPrice          float64 // DOWN 价格

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
	return nil
}

// Validate 验证配置（BBGO风格）
func (s *DataRecorderStrategy) Validate() error {
	if s.config == nil {
		return fmt.Errorf("策略配置未设置")
	}
	return s.config.Validate()
}

// Initialize 初始化策略（BBGO风格）
func (s *DataRecorderStrategy) Initialize() error {
	// BBGO风格的Initialize方法，使用已设置的config
	if s.config == nil {
		return fmt.Errorf("策略配置未设置")
	}
	return nil
}

// InitializeWithConfig 初始化策略（兼容旧接口）
func (s *DataRecorderStrategy) InitializeWithConfig(ctx context.Context, config strategies.StrategyConfig) error {
	recorderConfig, ok := config.(*DataRecorderStrategyConfig)
	if !ok {
		return fmt.Errorf("无效的配置类型")
	}

	if err := recorderConfig.Validate(); err != nil {
		return fmt.Errorf("配置验证失败: %w", err)
	}

	s.config = recorderConfig

	// 创建数据记录器（流式写入）
	recorder, err := NewDataRecorder(recorderConfig.OutputDir)
	if err != nil {
		return fmt.Errorf("创建数据记录器失败: %w", err)
	}
	s.recorder = recorder

	// 创建 RTDS 客户端
	// 创建一个适配器，将 RTDS 日志输出到我们的 logger
	rtdsLogger := &rtdsLoggerAdapter{}
	rtdsConfig := &rtds.ClientConfig{
		URL:            rtds.RTDSWebSocketURL,
		ProxyURL:       recorderConfig.ProxyURL, // 设置代理 URL
		PingInterval:   5 * time.Second,
		WriteTimeout:   10 * time.Second,
		ReadTimeout:    60 * time.Second,
		Reconnect:      true,
		ReconnectDelay: 5 * time.Second,
		MaxReconnect:   10,
		Logger:         rtdsLogger, // 使用我们的 logger 适配器
	}
	rtdsClient := rtds.NewClientWithConfig(rtdsConfig)
	s.rtdsClient = rtdsClient

	// 创建目标价获取器
	s.targetPriceFetcher = NewTargetPriceFetcher(recorderConfig.UseRTDSFallback, rtdsClient)

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
		logger.Debugf("数据记录策略: 收到 Chainlink 价格消息 - Symbol=%s, Value=%.2f", price.Symbol, price.Value)
		if price.Symbol == "btc/usd" {
			// 格式化时间戳（毫秒转秒）
			timestamp := time.Unix(price.Timestamp/1000, (price.Timestamp%1000)*1000000)

			// 在终端显示 Chainlink BTC 实时报价（醒目的格式，与价格更新日志格式一致）
			logger.Infof("💰 BTC 实时报价 (Chainlink): $%.2f (时间: %s)",
				price.Value, timestamp.Format("15:04:05"))

			s.mu.Lock()
			oldPrice := s.btcRealtimePrice
			// 只更新 BTC 实时价格，不记录数据
			s.btcRealtimePrice = price.Value
			s.mu.Unlock()

			// 如果有价格变化，显示变化趋势
			if oldPrice > 0 {
				change := price.Value - oldPrice
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
		recorderConfig.OutputDir, recorderConfig.UseRTDSFallback)

	return nil
}

// OnPriceChanged 处理价格变化事件（快路径：只合并信号，实际逻辑在 loop 内串行执行）
func (s *DataRecorderStrategy) OnPriceChanged(ctx context.Context, event *events.PriceChangedEvent) error {
	if event == nil {
		return nil
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
	// 只处理 btc-updown-15m-* 市场
	if !s.isBTC15mMarket(event.Market) {
		return nil
	}

	s.mu.Lock()

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

		// 重置目标价状态（新周期需要重新获取目标价）
		s.btcTargetPrice = 0
		s.btcTargetPriceSet = false

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
			logger.Warnf("获取目标价失败: %v，将使用上一个周期的目标价或0", err)
			// 如果获取失败，尝试使用上一个周期的目标价（如果有）
			if oldMarket != nil {
				// 这里可以尝试从旧周期数据中获取，但为了简化，先使用0
				targetPrice = 0
			} else {
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
	s.mu.Unlock()

	// 以 UP/DOWN 价格变化为准，记录数据点
	// 此时保存当前的 BTC 实时价格（由 RTDS 实时更新）
	// 只有在目标价已设置时才记录数据，避免记录0值
	if btcRealtime > 0 && upPrice > 0 && downPrice > 0 {
		if !btcTargetSet || btcTarget <= 0 {
			logger.Debugf("数据记录策略: 目标价未就绪，跳过记录 (BTC目标=%.2f, BTC实时=%.2f, UP=%.4f, DOWN=%.4f)",
				btcTarget, btcRealtime, upPrice, downPrice)
			return nil
		}
		s.recordDataPoint(btcTarget, btcRealtime, upPrice, downPrice)
	} else {
		logger.Debugf("数据记录策略: 价格未就绪，跳过记录 (BTC实时=%.2f, UP=%.4f, DOWN=%.4f)", btcRealtime, upPrice, downPrice)
	}

	return nil
}

// recordDataPoint 记录数据点
func (s *DataRecorderStrategy) recordDataPoint(btcTarget, btcRealtime, upPrice, downPrice float64) {
	point := DataPoint{
		Timestamp:        time.Now().Unix(),
		BTCTargetPrice:   btcTarget,
		BTCRealtimePrice: btcRealtime,
		UpPrice:          upPrice,
		DownPrice:        downPrice,
	}

	s.recorder.Record(point)
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

			// 重置目标价状态（新周期需要重新获取目标价）
			s.btcTargetPrice = 0
			s.btcTargetPriceSet = false

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
			s.mu.Unlock()

			// 同步获取目标价，确保在记录数据前目标价已设置
			targetCtx, targetCancel := context.WithTimeout(ctx, 10*time.Second)
			targetPrice, err := s.targetPriceFetcher.FetchTargetPrice(targetCtx, currentCycleStart)
			targetCancel()

			if err != nil {
				logger.Warnf("定时检查获取目标价失败: %v，将使用0作为默认值", err)
				targetPrice = 0
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
