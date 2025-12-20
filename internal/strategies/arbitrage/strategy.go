package arbitrage

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/strategies"
	"github.com/betbot/gobet/internal/strategies/common"
	"github.com/betbot/gobet/internal/strategies/orderutil"
	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/betbot/gobet/pkg/logger"
	"github.com/sirupsen/logrus"
)

const ID = "arbitrage"

var log = logrus.WithField("strategy", ID)

func init() {
	// BBGO风格：在init函数中注册策略及其配置适配器
	bbgo.RegisterStrategyWithAdapter(ID, &ArbitrageStrategy{}, &ArbitrageConfigAdapter{})
}

// ArbitrageStrategy 套利策略实现
type ArbitrageStrategy struct {
	Executor       bbgo.CommandExecutor
	config         *ArbitrageStrategyConfig
	tradingService TradingServiceInterface
	positionState  *domain.ArbitragePositionState
	currentMarket  *domain.Market
	priceUp        float64 // 当前UP价格
	priceDown      float64 // 当前DOWN价格

	// 统一：单线程 loop（价格合并 + 订单更新 + 命令结果）
	loopOnce        sync.Once
	loopCancel      context.CancelFunc
	priceSignalC    chan struct{}
	priceMu         sync.Mutex
	latestPrices    map[domain.TokenType]*events.PriceChangedEvent
	orderC          chan *domain.Order
	cmdResultC      chan arbitrageCmdResult
	maxInFlight     int
	inFlightLimiter *common.InFlightLimiter

	mu             sync.RWMutex
	isPlacingOrder bool
	placeOrderMu   sync.Mutex
}

// TradingServiceInterface 交易服务接口（避免循环依赖）
type TradingServiceInterface interface {
	PlaceOrder(ctx context.Context, order *domain.Order) (*domain.Order, error)
	CancelOrder(ctx context.Context, orderID string) error
	GetOpenPositions() []*domain.Position
	GetBestPrice(ctx context.Context, assetID string) (bestBid float64, bestAsk float64, err error)
}

// NewArbitrageStrategy 创建新的套利策略
func NewArbitrageStrategy() *ArbitrageStrategy {
	return &ArbitrageStrategy{}
}

// SetTradingService 设置交易服务（在初始化后调用）
func (s *ArbitrageStrategy) SetTradingService(ts TradingServiceInterface) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tradingService = ts
}

// ID 返回策略ID（BBGO风格）
func (s *ArbitrageStrategy) ID() string {
	return ID
}

// Name 返回策略名称（兼容旧接口）
func (s *ArbitrageStrategy) Name() string {
	return ID
}

// Defaults 设置默认值（BBGO风格）
func (s *ArbitrageStrategy) Defaults() error {
	return nil
}

// Validate 验证配置（BBGO风格）
func (s *ArbitrageStrategy) Validate() error {
	if s.config == nil {
		return fmt.Errorf("策略配置未设置")
	}
	return s.config.Validate()
}

// Initialize 初始化策略（BBGO风格）
func (s *ArbitrageStrategy) Initialize() error {
	// BBGO风格的Initialize方法，使用已设置的config
	if s.config == nil {
		return fmt.Errorf("策略配置未设置")
	}
	return nil
}

// InitializeWithConfig 初始化策略（兼容旧接口）
func (s *ArbitrageStrategy) InitializeWithConfig(ctx context.Context, config strategies.StrategyConfig) error {
	arbitrageConfig, ok := config.(*ArbitrageStrategyConfig)
	if !ok {
		return fmt.Errorf("无效的配置类型")
	}

	if err := arbitrageConfig.Validate(); err != nil {
		return fmt.Errorf("配置验证失败: %w", err)
	}

	s.config = arbitrageConfig
	if s.maxInFlight <= 0 {
		// 参考 CSV：单秒可出现 10-30 笔成交，策略侧至少要允许小并发，避免“只能一笔一笔慢慢下”
		s.maxInFlight = 8
	}
	if s.inFlightLimiter == nil {
		s.inFlightLimiter = common.NewInFlightLimiter(s.maxInFlight)
	} else {
		s.inFlightLimiter.SetMax(s.maxInFlight)
	}
	s.inFlightLimiter.Reset()

	logger.Infof("套利策略已初始化: 周期时长=%v, 锁盈起始=%v, UP目标=%v, DOWN目标=%v",
		arbitrageConfig.CycleDuration,
		arbitrageConfig.LockStart,
		arbitrageConfig.TargetUpBase,
		arbitrageConfig.TargetDownBase)

	return nil
}

// OnPriceChanged 处理价格变化事件（快路径：只合并信号，实际逻辑在 loop 内串行执行）
func (s *ArbitrageStrategy) OnPriceChanged(ctx context.Context, event *events.PriceChangedEvent) error {
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

func (s *ArbitrageStrategy) onPriceChangedInternal(ctx context.Context, event *events.PriceChangedEvent) error {
	return s.onPricesChangedInternal(ctx, event, nil)
}

// onPricesChangedInternal 合并处理 UP/DOWN 两侧价格变动，避免同一轮内重复决策与重复下单。
func (s *ArbitrageStrategy) onPricesChangedInternal(ctx context.Context, upEvent *events.PriceChangedEvent, downEvent *events.PriceChangedEvent) error {
	// 选择一个有效事件作为“主事件”（用于 market/时间等）
	event := upEvent
	if event == nil {
		event = downEvent
	}
	if event == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.tradingService == nil {
		return fmt.Errorf("交易服务未设置")
	}

	// 只处理 btc-updown-15m-* 市场
	if !s.isBTC15mMarket(event.Market) {
		return nil
	}

	// 初始化或更新市场信息
	if s.currentMarket == nil || s.currentMarket.Slug != event.Market.Slug {
		// 周期切换：清理 in-flight 计数，避免新周期被旧状态卡住
		if s.inFlightLimiter != nil {
			s.inFlightLimiter.Reset()
		}
		s.currentMarket = event.Market
		s.positionState = domain.NewArbitragePositionState(event.Market)
		logger.Infof("套利策略: 初始化新市场 %s, 周期开始时间=%d", event.Market.Slug, event.Market.Timestamp)
	}

	// 更新当前价格
	if upEvent != nil {
		s.priceUp = upEvent.NewPrice.ToDecimal()
	}
	if downEvent != nil {
		s.priceDown = downEvent.NewPrice.ToDecimal()
	}

	// 获取已过时间（秒）
	nowUnix := time.Now().Unix()
	// 尽量用事件时间做阶段判断（更贴近市场真实时间线）
	if !event.Timestamp.IsZero() {
		nowUnix = event.Timestamp.Unix()
	}
	elapsed := s.positionState.GetElapsedTimeAt(nowUnix)
	cycleDuration := int64(s.config.CycleDuration.Seconds())
	lockStart := int64(s.config.LockStart.Seconds())

	// 判断当前阶段（基于时间）
	phase := s.positionState.DetectPhaseAt(nowUnix, cycleDuration, lockStart)

	// 检测极端价格，提前进入放大利润阶段
	// 如果UP或DOWN价格达到提前锁盈阈值，提前进入放大利润阶段（锁盈已经在实时进行）
	earlyLockThreshold := s.config.EarlyLockPriceThreshold
	if (s.priceUp >= earlyLockThreshold || s.priceDown >= earlyLockThreshold) && phase != domain.PhaseLock {
		logger.Warnf("套利策略: 检测到极端价格！UP=%.4f, DOWN=%.4f, 提前进入放大利润阶段（原阶段=%d）",
			s.priceUp, s.priceDown, phase)
		phase = domain.PhaseLock
	}

	logger.Debugf("套利策略: 市场=%s, 阶段=%d, 已过时间=%ds, UP价格=%.4f, DOWN价格=%.4f, QUp=%.2f, QDown=%.2f",
		event.Market.Slug, phase, elapsed, s.priceUp, s.priceDown, s.positionState.QUp, s.positionState.QDown)

	// 关键：优先快速锁定最差情景收益（参考 CSV：通常开盘后 1 分钟内完成锁定）
	// 这里不做 phase gating：任何阶段如果 worst-case < 0，都要尽快用低成本补齐。
	if err := s.ensureWorstCaseNonNegative(ctx, event.Market, "ensure_worstcase_nonneg"); err != nil {
		logger.Warnf("套利策略: 最差收益锁定尝试失败: %v", err)
	}

	// 核心原则：混合策略 - 结合高手机器人策略和我们的实时锁定优势
	// 阶段1：快速建仓，不急于锁定（降低实时锁定优先级）
	// 阶段2：开始锁定，逐步降低风险敞口
	// 阶段3：优先锁定，在锁定基础上放大利润

	// 根据阶段决定实时锁定的优先级
	switch phase {
	case domain.PhaseBuild:
		// 阶段1：降低实时锁定优先级，专注于快速建仓
		// 只在风险敞口严重时才锁定
		pu := s.positionState.ProfitIfUpWin()
		pd := s.positionState.ProfitIfDownWin()
		if pu < -50 || pd < -50 {
			// 严重风险敞口，才执行实时锁定
			if err := s.handleRealTimeLockIn(ctx, event); err != nil {
				logger.Errorf("套利策略: 阶段1严重风险敞口锁定失败: %v", err)
			}
		}
		// 执行阶段1逻辑：快速建仓
		return s.handleBuildPhase(ctx, event)

	case domain.PhaseAdjust:
		// 阶段2：开始实时锁定，逐步降低风险敞口
		if err := s.handleRealTimeLockIn(ctx, event); err != nil {
			logger.Errorf("套利策略: 实时锁盈处理失败: %v", err)
			// 不返回错误，继续执行阶段逻辑
		}
		// 执行阶段2逻辑：调整仓位
		return s.handleAdjustPhase(ctx, event)

	case domain.PhaseLock:
		// 阶段3：优先锁定，在锁定基础上放大利润
		// 1. 先执行实时锁定（确保利润）
		if err := s.handleRealTimeLockIn(ctx, event); err != nil {
			logger.Errorf("套利策略: 实时锁盈处理失败: %v", err)
			// 不返回错误，继续执行阶段逻辑
		}
		// 2. 在锁定基础上放大利润
		return s.handleProfitAmplification(ctx, event)

	default:
		return nil
	}
}

// ensureWorstCaseNonNegative 尝试把最差情景收益（min(Pu, Pd)）拉回到 >= 0。
// 设计目标：用“更便宜的一侧”优先补齐，并采用拆单/并发下单提升锁定速度。
func (s *ArbitrageStrategy) ensureWorstCaseNonNegative(ctx context.Context, market *domain.Market, reason string) error {
	if s.positionState == nil || market == nil || s.tradingService == nil || s.config == nil {
		return nil
	}
	pu := s.positionState.ProfitIfUpWin()
	pd := s.positionState.ProfitIfDownWin()
	if pu >= 0 && pd >= 0 {
		return nil
	}

	// 选择当前更“危险”的方向（利润更低的那个）
	token := domain.TokenTypeUp
	profitSide := pu
	qSide := s.positionState.QUp
	if pd < pu {
		token = domain.TokenTypeDown
		profitSide = pd
		qSide = s.positionState.QDown
	}

	// 估算需要补多少：x >= (-profitSide) / (1 - ask)
	// 为了避免多一次行情读取，这里用当前观测价做近似（实价由下单时 bestAsk 决定）。
	obs := s.priceUp
	if token == domain.TokenTypeDown {
		obs = s.priceDown
	}
	if obs <= 0 || obs >= 0.99 {
		return nil
	}
	deficit := -profitSide
	if deficit <= 0 {
		return nil
	}

	need := deficit / (1.0 - obs)

	// 单次补仓上限：参考 CSV 的典型拆单逻辑（18 为主），并避免一口气把仓位推太大
	capSize := math.Max(s.config.BuildLotSize, s.config.SmallIncrement)
	if capSize <= 0 {
		capSize = 18
	}
	if need > capSize {
		need = capSize
	}

	// 额外保护：如果这一侧已经远高于基础目标，避免过度单边补（留给后续 tick 逐步处理）
	baseTarget := s.config.BaseTarget
	if baseTarget > 0 && qSide > baseTarget*2.5 {
		need = math.Min(need, capSize*0.5)
	}

	return s.placeBuyOrderSplit(ctx, market, token, need, reason)
}

// OnOrderFilled 处理订单成交事件
func (s *ArbitrageStrategy) OnOrderFilled(ctx context.Context, event *events.OrderFilledEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.positionState == nil {
		return nil
	}

	// 只处理当前市场的订单
	if s.currentMarket == nil || s.currentMarket.Slug != event.Market.Slug {
		return nil
	}

	// 只处理买入订单（套利策略只买入）
	if event.Order.Side != types.SideBuy {
		return nil
	}

	// 更新持仓状态
	cost := event.Order.Size * event.Order.Price.ToDecimal()
	if event.Order.TokenType == domain.TokenTypeUp {
		s.positionState.QUp += event.Order.Size
		s.positionState.CUp += cost
		logger.Infof("套利策略: UP订单成交, 数量=%.2f, 价格=%.4f, 成本=%.2f, QUp=%.2f, CUp=%.2f",
			event.Order.Size, event.Order.Price.ToDecimal(), cost, s.positionState.QUp, s.positionState.CUp)
	} else if event.Order.TokenType == domain.TokenTypeDown {
		s.positionState.QDown += event.Order.Size
		s.positionState.CDown += cost
		logger.Infof("套利策略: DOWN订单成交, 数量=%.2f, 价格=%.4f, 成本=%.2f, QDown=%.2f, CDown=%.2f",
			event.Order.Size, event.Order.Price.ToDecimal(), cost, s.positionState.QDown, s.positionState.CDown)
	}

	// 记录即时利润
	pu := s.positionState.ProfitIfUpWin()
	pd := s.positionState.ProfitIfDownWin()
	logger.Infof("套利策略: 即时利润 - UP胜=%.2f USDC, DOWN胜=%.2f USDC", pu, pd)

	return nil
}

// CanOpenPosition 检查是否可以开仓（套利策略总是可以开仓）
func (s *ArbitrageStrategy) CanOpenPosition(ctx context.Context, market *domain.Market) (bool, error) {
	return s.isBTC15mMarket(market), nil
}

// CalculateEntry 计算入场价格和数量（套利策略不使用此方法）
func (s *ArbitrageStrategy) CalculateEntry(ctx context.Context, market *domain.Market, price domain.Price) (*domain.Order, error) {
	return nil, fmt.Errorf("套利策略不使用此方法")
}

// CalculateHedge 计算对冲订单（套利策略不使用此方法）
func (s *ArbitrageStrategy) CalculateHedge(ctx context.Context, entryOrder *domain.Order) (*domain.Order, error) {
	return nil, fmt.Errorf("套利策略不使用此方法")
}

// CheckTakeProfitStopLoss 检查止盈止损（套利策略不使用此方法）
func (s *ArbitrageStrategy) CheckTakeProfitStopLoss(ctx context.Context, position *domain.Position, currentPrice domain.Price) (*domain.Order, error) {
	return nil, fmt.Errorf("套利策略不使用此方法")
}

// Cleanup 清理资源
func (s *ArbitrageStrategy) Cleanup(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.positionState = nil
	s.currentMarket = nil
	s.isPlacingOrder = false
	return nil
}

// Subscribe 订阅会话事件（BBGO 风格）
func (s *ArbitrageStrategy) Subscribe(session *bbgo.ExchangeSession) {
	// 注册价格变化回调
	session.OnPriceChanged(s)
	session.OnOrderUpdate(s)
	log.Infof("套利策略已订阅价格变化事件")
}

// Run 运行策略（BBGO 风格）
func (s *ArbitrageStrategy) Run(ctx context.Context, orderExecutor bbgo.OrderExecutor, session *bbgo.ExchangeSession) error {
	log.Infof("套利策略已启动")
	s.startLoop(ctx)
	return nil
}

// Shutdown 优雅关闭（BBGO 风格）
// Shutdown 优雅关闭（BBGO 风格）
// 注意：wg 参数由 shutdown.Manager 统一管理，策略的 Shutdown 方法不应该调用 wg.Done()
func (s *ArbitrageStrategy) Shutdown(ctx context.Context, wg *sync.WaitGroup) {
	log.Infof("套利策略: 开始优雅关闭...")
	s.stopLoop()
	if err := s.Cleanup(ctx); err != nil {
		log.Errorf("套利策略清理失败: %v", err)
	}
	log.Infof("套利策略: 资源清理完成")
}

// isBTC15mMarket 检查是否为 BTC 15分钟市场
func (s *ArbitrageStrategy) isBTC15mMarket(market *domain.Market) bool {
	return market != nil && len(market.Slug) > 13 && market.Slug[:13] == "btc-updown-15m"
}

// handleBuildPhase 阶段一：基础建仓（0-5分钟）
func (s *ArbitrageStrategy) handleBuildPhase(ctx context.Context, event *events.PriceChangedEvent) error {
	if s.positionState == nil {
		return nil
	}

	baseTarget := s.config.BaseTarget
	lotSize := s.config.BuildLotSize

	// 优先填充持仓较少的一侧
	// 放宽价格上限到0.85（从0.7放宽），与高手机器人策略一致（0.14-0.87范围）
	if s.positionState.QUp < baseTarget && s.priceUp > 0 && s.priceUp < 0.85 {
		need := math.Min(lotSize, baseTarget-s.positionState.QUp)
		if err := s.placeBuyOrderSplit(ctx, event.Market, domain.TokenTypeUp, need, "build_base_up"); err != nil {
			logger.Errorf("套利策略: 建仓阶段买入UP失败: %v", err)
			return err
		}
	}

	if s.positionState.QDown < baseTarget && s.priceDown > 0 && s.priceDown < 0.85 {
		need := math.Min(lotSize, baseTarget-s.positionState.QDown)
		if err := s.placeBuyOrderSplit(ctx, event.Market, domain.TokenTypeDown, need, "build_base_down"); err != nil {
			logger.Errorf("套利策略: 建仓阶段买入DOWN失败: %v", err)
			return err
		}
	}

	// 小幅校正持仓比例（保持45%-55%）
	// 放宽价格上限到0.85（从0.7放宽），与高手机器人策略一致
	total := s.positionState.QUp + s.positionState.QDown
	if total > 0 {
		upRatio := s.positionState.QUp / total
		// 使用 build_lot_size 的一半作为再平衡订单量，避免过大
		rebalanceSize := math.Max(s.config.BuildLotSize*0.5, s.config.MinOrderSize)
		if upRatio > 0.55 && s.priceDown > 0 && s.priceDown < 0.85 {
			if err := s.placeBuyOrderSplit(ctx, event.Market, domain.TokenTypeDown, rebalanceSize, "rebalance_down_in_build"); err != nil {
				logger.Errorf("套利策略: 建仓阶段再平衡DOWN失败: %v", err)
			}
		} else if upRatio < 0.45 && s.priceUp > 0 && s.priceUp < 0.85 {
			if err := s.placeBuyOrderSplit(ctx, event.Market, domain.TokenTypeUp, rebalanceSize, "rebalance_up_in_build"); err != nil {
				logger.Errorf("套利策略: 建仓阶段再平衡UP失败: %v", err)
			}
		}
	}

	return nil
}

// handleAdjustPhase 阶段二：中段调整（5-10分钟）
func (s *ArbitrageStrategy) handleAdjustPhase(ctx context.Context, event *events.PriceChangedEvent) error {
	if s.positionState == nil {
		return nil
	}

	pu := s.positionState.ProfitIfUpWin()
	pd := s.positionState.ProfitIfDownWin()

	total := s.positionState.QUp + s.positionState.QDown
	if total == 0 {
		return nil
	}
	upRatio := s.positionState.QUp / total

	// 若盘口略偏向UP，则在不破坏45-55%持仓比例前提下，小幅增加UP加仓频率
	// 使用 small_increment 作为调整订单量，适合小资金量
	adjustSize := math.Max(s.config.SmallIncrement, s.config.MinOrderSize)
	if s.priceUp > 0.55 && s.priceUp < 0.8 && upRatio < 0.55 {
		if err := s.placeBuyOrderSplit(ctx, event.Market, domain.TokenTypeUp, adjustSize, "phase2_soft_tilt_up"); err != nil {
			logger.Errorf("套利策略: 调整阶段轻微倾斜UP失败: %v", err)
		}
	}

	// 对称：盘口偏向DOWN
	if s.priceDown > 0.55 && s.priceDown < 0.8 && upRatio > 0.45 {
		if err := s.placeBuyOrderSplit(ctx, event.Market, domain.TokenTypeDown, adjustSize, "phase2_soft_tilt_down"); err != nil {
			logger.Errorf("套利策略: 调整阶段轻微倾斜DOWN失败: %v", err)
		}
	}

	// 可选：若某一方向即时利润绝对值过大，用对侧小单微调
	const pnlCap = 200.0
	if pu > pnlCap && s.priceDown > 0 && s.priceDown < 0.7 {
		if err := s.placeBuyOrderSplit(ctx, event.Market, domain.TokenTypeDown, 8, "phase2_clip_up_profit"); err != nil {
			logger.Errorf("套利策略: 调整阶段削减UP利润失败: %v", err)
		}
	}
	if pd > pnlCap && s.priceUp > 0 && s.priceUp < 0.7 {
		if err := s.placeBuyOrderSplit(ctx, event.Market, domain.TokenTypeUp, 8, "phase2_clip_down_profit"); err != nil {
			logger.Errorf("套利策略: 调整阶段削减DOWN利润失败: %v", err)
		}
	}

	return nil
}

// handleRealTimeLockIn 实时锁盈：在整个周期内持续检测风险敞口并锁定利润
// 这是整个周期的终极目标：越早锁定越好
func (s *ArbitrageStrategy) handleRealTimeLockIn(ctx context.Context, event *events.PriceChangedEvent) error {
	if s.positionState == nil {
		return nil
	}

	pu := s.positionState.ProfitIfUpWin()
	pd := s.positionState.ProfitIfDownWin()

	// 计算动态加仓上限（基于总持仓）
	totalQty := s.positionState.QUp + s.positionState.QDown
	smallIncrement := s.config.SmallIncrement
	if totalQty > 0 && smallIncrement <= 20 { // 默认值较小，使用总持仓的1%
		smallIncrement = totalQty * 0.01
	}

	// 1. 检测风险敞口：如果某个方向的即时利润为负，立即补充订单锁定
	// UP方向风险敞口
	if pu < 0 && s.priceUp > 0 && s.priceUp < 0.5 {
		// UP方向亏损，且价格较低（可以低成本补充）
		need := (0.0 + s.positionState.CUp + s.positionState.CDown - s.positionState.QUp) / (1.0 - s.priceUp)
		dQ := math.Max(0, math.Min(need, smallIncrement))
		if dQ > s.config.MinOrderSize {
			logger.Warnf("套利策略: 检测到UP方向风险敞口（利润=%.2f），立即补充订单锁定", pu)
			if err := s.placeBuyOrderSplit(ctx, event.Market, domain.TokenTypeUp, dQ, "realtime_lock_up_risk"); err != nil {
				logger.Errorf("套利策略: 实时锁盈UP风险失败: %v", err)
			}
		}
	}

	// DOWN方向风险敞口
	if pd < 0 && s.priceDown > 0 && s.priceDown < 0.5 {
		// DOWN方向亏损，且价格较低（可以低成本补充）
		need := (0.0 + s.positionState.CUp + s.positionState.CDown - s.positionState.QDown) / (1.0 - s.priceDown)
		dQ := math.Max(0, math.Min(need, smallIncrement))
		if dQ > s.config.MinOrderSize {
			logger.Warnf("套利策略: 检测到DOWN方向风险敞口（利润=%.2f），立即补充订单锁定", pd)
			if err := s.placeBuyOrderSplit(ctx, event.Market, domain.TokenTypeDown, dQ, "realtime_lock_down_risk"); err != nil {
				logger.Errorf("套利策略: 实时锁盈DOWN风险失败: %v", err)
			}
		}
	}

	// 2. 利用极端价格锁定利润：当价格极端化时，买入反向保险
	earlyLockThreshold := s.config.EarlyLockPriceThreshold
	if s.priceUp >= earlyLockThreshold && s.priceDown < (1.0-earlyLockThreshold) {
		// UP价格极高，DOWN价格极低，买入DOWN保险
		if pd < s.config.TargetDownBase && s.priceDown > 0 && s.priceDown < 0.25 {
			dQ := math.Min(smallIncrement*2, 50.0) // 极端价格时可以稍微多买
			if dQ > s.config.MinOrderSize {
				logger.Infof("套利策略: 利用极端价格锁定DOWN利润（UP=%.4f, DOWN=%.4f）", s.priceUp, s.priceDown)
				if err := s.placeBuyOrderSplit(ctx, event.Market, domain.TokenTypeDown, dQ, "realtime_lock_extreme_down"); err != nil {
					logger.Errorf("套利策略: 实时锁盈极端DOWN价格失败: %v", err)
				}
			}
		}
	} else if s.priceDown >= earlyLockThreshold && s.priceUp < (1.0-earlyLockThreshold) {
		// DOWN价格极高，UP价格极低，买入UP保险
		if pu < s.config.TargetUpBase && s.priceUp > 0 && s.priceUp < 0.25 {
			dQ := math.Min(smallIncrement*2, 50.0) // 极端价格时可以稍微多买
			if dQ > s.config.MinOrderSize {
				logger.Infof("套利策略: 利用极端价格锁定UP利润（UP=%.4f, DOWN=%.4f）", s.priceUp, s.priceDown)
				if err := s.placeBuyOrderSplit(ctx, event.Market, domain.TokenTypeUp, dQ, "realtime_lock_extreme_up"); err != nil {
					logger.Errorf("套利策略: 实时锁盈极端UP价格失败: %v", err)
				}
			}
		}
	}

	return nil
}

// handleProfitAmplification 阶段三：优先锁定 + 在锁定基础上放大利润（10-15分钟）
// 核心原则：优先锁定（确保利润），在锁定基础上放大利润
func (s *ArbitrageStrategy) handleProfitAmplification(ctx context.Context, event *events.PriceChangedEvent) error {
	if s.positionState == nil {
		return nil
	}

	pu := s.positionState.ProfitIfUpWin()
	pd := s.positionState.ProfitIfDownWin()

	// 检查是否已经锁定（两个方向的利润都为正）
	isLocked := pu > 0 && pd > 0

	if !isLocked {
		// 还未锁定，优先锁定（不放大利润）
		// 实时锁定已经在 OnPriceChanged 中执行，这里只记录日志
		logger.Infof("套利策略: 阶段3 - 还未完全锁定（UP利润=%.2f, DOWN利润=%.2f），优先锁定，不放大利润", pu, pd)
		return nil
	}

	// 已经锁定，可以在锁定基础上放大利润
	logger.Infof("套利策略: 阶段3 - 已经锁定（UP利润=%.2f, DOWN利润=%.2f），开始放大利润", pu, pd)

	// 判定主方向（基于盘口价格）
	main := "NEUTRAL"
	if s.priceUp > 0.7 && s.priceDown < 0.3 {
		main = "UP"
	} else if s.priceDown > 0.7 && s.priceUp < 0.3 {
		main = "DOWN"
	}

	// 动态目标：越接近到期，要求越高
	elapsed := s.positionState.GetElapsedTime()
	cycleDuration := int64(s.config.CycleDuration.Seconds())
	lockStart := int64(s.config.LockStart.Seconds())
	progress := float64(elapsed-lockStart) / float64(cycleDuration-lockStart)
	if progress < 0 {
		progress = 0
	}
	if progress > 1 {
		progress = 1
	}
	targetUp := s.config.TargetUpBase * (1.0 + 0.5*progress)
	targetDown := s.config.TargetDownBase * (1.0 + 0.5*progress)

	// 计算动态加仓上限（基于总持仓）
	totalQty := s.positionState.QUp + s.positionState.QDown
	maxUpIncrement := s.config.MaxUpIncrement
	maxDownIncrement := s.config.MaxDownIncrement
	smallIncrement := s.config.SmallIncrement
	if totalQty > 0 {
		// 如果配置了基于总持仓的比例，则使用比例
		if maxUpIncrement <= 100 { // 默认值较小，使用总持仓的5%
			maxUpIncrement = totalQty * 0.05
		}
		if maxDownIncrement <= 100 {
			maxDownIncrement = totalQty * 0.05
		}
		if smallIncrement <= 20 { // 默认值较小，使用总持仓的1%
			smallIncrement = totalQty * 0.01
		}
	}

	// 最后阶段的目标：放大利润（锁盈已经在实时进行）
	// 1) 推高主方向利润到更高目标
	if main == "UP" && pu < targetUp && s.priceUp > 0 && s.priceUp < 0.98 {
		need := (targetUp + s.positionState.CUp + s.positionState.CDown - s.positionState.QUp) / (1.0 - s.priceUp)
		dQ := math.Max(0, math.Min(need, maxUpIncrement))
		if dQ > s.config.MinOrderSize {
			logger.Infof("套利策略: 放大利润阶段 - 推高UP利润到目标 %.2f（当前=%.2f）", targetUp, pu)
			if err := s.placeBuyOrderSplit(ctx, event.Market, domain.TokenTypeUp, dQ, "amplify_profit_up"); err != nil {
				logger.Errorf("套利策略: 放大利润阶段推高UP失败: %v", err)
			}
		}
	} else if main == "DOWN" && pd < targetDown && s.priceDown > 0 && s.priceDown < 0.3 {
		need := (targetDown + s.positionState.CUp + s.positionState.CDown - s.positionState.QDown) / (1.0 - s.priceDown)
		dQ := math.Max(0, math.Min(need, maxDownIncrement))
		if dQ > s.config.MinOrderSize {
			logger.Infof("套利策略: 放大利润阶段 - 推高DOWN利润到目标 %.2f（当前=%.2f）", targetDown, pd)
			if err := s.placeBuyOrderSplit(ctx, event.Market, domain.TokenTypeDown, dQ, "amplify_profit_down"); err != nil {
				logger.Errorf("套利策略: 放大利润阶段推高DOWN失败: %v", err)
			}
		}
	}

	// 2) 利用极端价格进一步放大反向利润（如果还有空间）
	pu = s.positionState.ProfitIfUpWin()
	pd = s.positionState.ProfitIfDownWin()

	// 如果反向利润还有提升空间，且价格极端，可以进一步放大
	if main == "UP" && pd < targetDown && s.priceDown > 0 && s.priceDown < 0.15 {
		// UP是主方向，但DOWN利润还可以提升
		need := (targetDown + s.positionState.CUp + s.positionState.CDown - s.positionState.QDown) / (1.0 - s.priceDown)
		dQ := math.Max(0, math.Min(need, smallIncrement*2)) // 可以稍微多买
		if dQ > s.config.MinOrderSize {
			logger.Infof("套利策略: 放大利润阶段 - 利用极端DOWN价格（%.4f）放大DOWN利润", s.priceDown)
			if err := s.placeBuyOrderSplit(ctx, event.Market, domain.TokenTypeDown, dQ, "amplify_profit_extreme_down"); err != nil {
				logger.Errorf("套利策略: 放大利润阶段DOWN极端价格失败: %v", err)
			}
		}
	} else if main == "DOWN" && pu < targetUp && s.priceUp > 0 && s.priceUp < 0.15 {
		// DOWN是主方向，但UP利润还可以提升
		need := (targetUp + s.positionState.CUp + s.positionState.CDown - s.positionState.QUp) / (1.0 - s.priceUp)
		dQ := math.Max(0, math.Min(need, smallIncrement*2)) // 可以稍微多买
		if dQ > s.config.MinOrderSize {
			logger.Infof("套利策略: 放大利润阶段 - 利用极端UP价格（%.4f）放大UP利润", s.priceUp)
			if err := s.placeBuyOrderSplit(ctx, event.Market, domain.TokenTypeUp, dQ, "amplify_profit_extreme_up"); err != nil {
				logger.Errorf("套利策略: 放大利润阶段UP极端价格失败: %v", err)
			}
		}
	}

	return nil
}

// placeBuyOrderSplit 将大单拆成若干笔（默认参考 BuildLotSize=18），并受 in-flight 限制。
func (s *ArbitrageStrategy) placeBuyOrderSplit(ctx context.Context, market *domain.Market, tokenType domain.TokenType, size float64, reason string) error {
	if size <= 0 {
		return nil
	}
	if s.config == nil {
		return nil
	}
	chunk := s.config.BuildLotSize
	if chunk <= 0 {
		chunk = size
	}
	remaining := size
	for remaining > 0 {
		if s.inFlightLimiter != nil && s.inFlightLimiter.AtLimit() {
			return nil
		}
		q := remaining
		if q > chunk {
			q = chunk
		}
		if err := s.placeBuyOrder(ctx, market, tokenType, q, reason); err != nil {
			return err
		}
		remaining -= q
	}
	return nil
}

// placeBuyOrder 下买入订单
func (s *ArbitrageStrategy) placeBuyOrder(ctx context.Context, market *domain.Market, tokenType domain.TokenType, size float64, reason string) error {
	// 统一：loop 内限并发，避免在高频价格下无限投递
	if s.inFlightLimiter != nil && s.inFlightLimiter.AtLimit() {
		return nil
	}
	if market == nil || s.tradingService == nil || s.config == nil {
		return nil
	}

	assetID := market.GetAssetID(tokenType)
	minOrderUSDC := s.config.MinOrderSize
	ts := s.tradingService
	exec := s.Executor

	// 滑点保护：相对当前观测价（非盘口价）设置上限
	maxCents := 0
	if s.config.MaxBuySlippageCents > 0 {
		ref := 0.0
		if tokenType == domain.TokenTypeUp {
			ref = s.priceUp
		} else if tokenType == domain.TokenTypeDown {
			ref = s.priceDown
		}
		if ref > 0 {
			refCents := int(ref*100 + 0.5)
			maxCents = refCents + s.config.MaxBuySlippageCents
		}
	}

	// 没有 executor 时仍保持兼容（但会阻塞 loop，不推荐）
	if exec == nil {
		if s.inFlightLimiter != nil && !s.inFlightLimiter.TryAcquire() {
			return nil
		}
		if s.inFlightLimiter != nil {
			defer s.inFlightLimiter.Release()
		}

		bestAskPrice, err := orderutil.QuoteBuyPrice(ctx, ts, assetID, maxCents)
		if err != nil {
			return fmt.Errorf("获取订单簿失败: %w", err)
		}

		orderAmount := size * bestAskPrice.ToDecimal()
		if orderAmount < minOrderUSDC {
			logger.Warnf("套利策略: %s - 订单金额 %.2f USDC 小于最小要求 %.2f USDC，跳过下单（数量=%.2f, 价格=%.4f）",
				reason, orderAmount, minOrderUSDC, size, bestAskPrice.ToDecimal())
			return nil
		}

		order := orderutil.NewOrder(market.Slug, assetID, types.SideBuy, bestAskPrice, size, tokenType, true, types.OrderTypeFAK)
		_, err = ts.PlaceOrder(ctx, order)
		return err
	}

	s.initLoopIfNeeded()
	if s.inFlightLimiter != nil && !s.inFlightLimiter.TryAcquire() {
		return nil
	}
	ok := exec.Submit(bbgo.Command{
		Name:    fmt.Sprintf("arbitrage_buy_%s_%s", tokenType, reason),
		Timeout: 25 * time.Second,
		Do: func(runCtx context.Context) {
			bestAskPrice, err := orderutil.QuoteBuyPrice(runCtx, ts, assetID, maxCents)
			if err != nil {
				select {
				case s.cmdResultC <- arbitrageCmdResult{tokenType: tokenType, reason: reason, err: err}:
				default:
				}
				return
			}

			orderAmount := size * bestAskPrice.ToDecimal()
			if orderAmount < minOrderUSDC {
				select {
				case s.cmdResultC <- arbitrageCmdResult{tokenType: tokenType, reason: reason, skipped: true}:
				default:
				}
				return
			}

			// marketSlug 通过当前 market 注入，确保只管理本周期
			mSlug := ""
			if s.currentMarket != nil {
				mSlug = s.currentMarket.Slug
			}
			order := orderutil.NewOrder(mSlug, assetID, types.SideBuy, bestAskPrice, size, tokenType, true, types.OrderTypeFAK)

			created, err := ts.PlaceOrder(runCtx, order)
			select {
			case s.cmdResultC <- arbitrageCmdResult{tokenType: tokenType, reason: reason, created: created, err: err}:
			default:
			}
		},
	})
	if !ok {
		if s.inFlightLimiter != nil {
			s.inFlightLimiter.Release()
		}
		return fmt.Errorf("执行器队列已满，无法提交订单")
	}
	return nil
}
