package pairedtrading

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
	"github.com/sirupsen/logrus"
)

const ID = "paired_trading"

var log = logrus.WithField("strategy", ID)

func init() {
	// BBGO风格：在init函数中注册策略及其配置适配器
	bbgo.RegisterStrategyWithAdapter(ID, &PairedTradingStrategy{}, &PairedTradingConfigAdapter{})
}

// Phase 策略阶段
type Phase int

const (
	PhaseBuild   Phase = 1 // 建仓阶段
	PhaseLock    Phase = 2 // 锁定阶段
	PhaseAmplify Phase = 3 // 放大阶段
)

func (p Phase) String() string {
	switch p {
	case PhaseBuild:
		return "Build"
	case PhaseLock:
		return "Lock"
	case PhaseAmplify:
		return "Amplify"
	default:
		return "Unknown"
	}
}

// PairedTradingStrategy 成对交易策略实现
type PairedTradingStrategy struct {
	Executor       bbgo.CommandExecutor
	config         *PairedTradingConfig
	tradingService TradingServiceInterface

	// 状态管理
	positionState *domain.ArbitragePositionState
	currentMarket *domain.Market
	marketGuard   common.MarketSlugGuard
	currentPhase  Phase
	lockAchieved  bool // 是否已完成锁定（两个方向利润都为正）

	// 价格状态
	priceUp   float64
	priceDown float64

	// 统一：单线程 loop（价格合并 + 订单更新 + 命令结果）
	loopOnce        sync.Once
	loopCancel      context.CancelFunc
	priceSignalC    chan struct{}
	priceMu         sync.Mutex
	latestPrices    map[domain.TokenType]*events.PriceChangedEvent
	orderC          chan *domain.Order
	cmdResultC      chan pairedTradingCmdResult
	inFlightLimiter *common.InFlightLimiter

	mu             sync.RWMutex
	isPlacingOrder bool
	placeOrderMu   sync.Mutex
}

type pairedTradingCmdResult struct {
	tokenType domain.TokenType
	reason    string
	created   *domain.Order
	skipped   bool
	err       error
}

// TradingServiceInterface 交易服务接口（避免循环依赖）
type TradingServiceInterface interface {
	PlaceOrder(ctx context.Context, order *domain.Order) (*domain.Order, error)
	CancelOrder(ctx context.Context, orderID string) error
	GetOpenPositions() []*domain.Position
	GetBestPrice(ctx context.Context, assetID string) (bestBid float64, bestAsk float64, err error)
}

// NewPairedTradingStrategy 创建新的成对交易策略
func NewPairedTradingStrategy() *PairedTradingStrategy {
	return &PairedTradingStrategy{
		currentPhase:    PhaseBuild,
		inFlightLimiter: common.NewInFlightLimiter(8), // 默认允许8个并发订单
	}
}

// SetTradingService 设置交易服务（在初始化后调用）
func (s *PairedTradingStrategy) SetTradingService(ts TradingServiceInterface) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tradingService = ts
}

// ID 返回策略ID（BBGO风格）
func (s *PairedTradingStrategy) ID() string {
	return ID
}

// Name 返回策略名称（兼容旧接口）
func (s *PairedTradingStrategy) Name() string {
	return ID
}

// Defaults 设置默认值（BBGO风格）
func (s *PairedTradingStrategy) Defaults() error {
	return nil
}

// Validate 验证配置（BBGO风格）
func (s *PairedTradingStrategy) Validate() error {
	if s.config == nil {
		return fmt.Errorf("策略配置未设置")
	}
	return s.config.Validate()
}

// Initialize 初始化策略（BBGO风格）
func (s *PairedTradingStrategy) Initialize() error {
	if s.config == nil {
		return fmt.Errorf("策略配置未设置")
	}
	return nil
}

// InitializeWithConfig 初始化策略（兼容旧接口）
func (s *PairedTradingStrategy) InitializeWithConfig(ctx context.Context, config strategies.StrategyConfig) error {
	pairedConfig, ok := config.(*PairedTradingConfig)
	if !ok {
		return fmt.Errorf("无效的配置类型")
	}

	if err := pairedConfig.Validate(); err != nil {
		return fmt.Errorf("配置验证失败: %w", err)
	}

	s.config = pairedConfig
	s.currentPhase = PhaseBuild
	s.lockAchieved = false
	if s.inFlightLimiter == nil {
		s.inFlightLimiter = common.NewInFlightLimiter(8)
	}
	s.inFlightLimiter.Reset()

	log.Infof("成对交易策略已初始化: 建仓阶段=%v, 锁定起始=%v, 放大起始=%v, 周期时长=%v",
		pairedConfig.BuildDuration,
		pairedConfig.LockStart,
		pairedConfig.AmplifyStart,
		pairedConfig.CycleDuration)

	return nil
}

// OnPriceChanged 处理价格变化事件（快路径：只合并信号，实际逻辑在 loop 内串行执行）
func (s *PairedTradingStrategy) OnPriceChanged(ctx context.Context, event *events.PriceChangedEvent) error {
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

func (s *PairedTradingStrategy) onPriceChangedInternal(ctx context.Context, event *events.PriceChangedEvent) error {
	return s.onPricesChangedInternal(ctx, event, nil)
}

// onPricesChangedInternal 合并处理 UP/DOWN 两侧价格变动
func (s *PairedTradingStrategy) onPricesChangedInternal(ctx context.Context, upEvent *events.PriceChangedEvent, downEvent *events.PriceChangedEvent) error {
	// 选择一个有效事件作为"主事件"
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
	if event.Market != nil && s.marketGuard.Update(event.Market.Slug) {
		// 周期切换：重置状态
		if s.inFlightLimiter != nil {
			s.inFlightLimiter.Reset()
		}
		s.currentMarket = event.Market
		s.positionState = domain.NewArbitragePositionState(event.Market)
		s.currentPhase = PhaseBuild
		s.lockAchieved = false
		log.Infof("成对交易策略: 初始化新市场 %s, 周期开始时间=%d", event.Market.Slug, event.Market.Timestamp)
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
	if !event.Timestamp.IsZero() {
		nowUnix = event.Timestamp.Unix()
	}
	elapsed := s.positionState.GetElapsedTimeAt(nowUnix)

	// 检测并更新当前阶段
	s.updatePhase(elapsed)

	// 检查锁定状态
	s.updateLockStatus()

	// 日志输出当前状态
	pu := s.positionState.ProfitIfUpWin()
	pd := s.positionState.ProfitIfDownWin()
	lockStatus := "✗ 未锁定"
	if s.lockAchieved {
		lockStatus = "✓ 已锁定"
	}

	log.Debugf("成对交易策略: 市场=%s, 阶段=%s, 锁定=%s, 已过时间=%ds, UP价格=%.4f, DOWN价格=%.4f, QUp=%.2f, QDown=%.2f, P_up=%.2f, P_down=%.2f",
		event.Market.Slug, s.currentPhase, lockStatus, elapsed,
		s.priceUp, s.priceDown,
		s.positionState.QUp, s.positionState.QDown,
		pu, pd)

	// 执行对应阶段的策略
	switch s.currentPhase {
	case PhaseBuild:
		return s.executeBuildPhase(ctx, event)
	case PhaseLock:
		return s.executeLockPhase(ctx, event)
	case PhaseAmplify:
		if s.lockAchieved {
			return s.executeAmplifyPhase(ctx, event)
		} else {
			// 未锁定，继续执行锁定逻辑
			log.Infof("成对交易策略: 阶段3但未锁定，继续执行锁定逻辑")
			return s.executeLockPhase(ctx, event)
		}
	default:
		return nil
	}
}

// updatePhase 更新当前阶段
func (s *PairedTradingStrategy) updatePhase(elapsed int64) {
	buildDuration := int64(s.config.BuildDuration.Seconds())
	amplifyStart := int64(s.config.AmplifyStart.Seconds())

	oldPhase := s.currentPhase

	// 基于时间判断阶段
	if elapsed < buildDuration {
		s.currentPhase = PhaseBuild
	} else if elapsed < amplifyStart {
		s.currentPhase = PhaseLock
	} else {
		s.currentPhase = PhaseAmplify
	}

	// 基于价格提前切换阶段
	earlyLockPrice := s.config.EarlyLockPrice
	earlyAmplifyPrice := s.config.EarlyAmplifyPrice

	// 如果价格极端，提前进入锁定阶段
	if (s.priceUp >= earlyLockPrice || s.priceDown >= earlyLockPrice) && s.currentPhase == PhaseBuild {
		log.Warnf("成对交易策略: 检测到极端价格（UP=%.4f, DOWN=%.4f），提前进入锁定阶段", s.priceUp, s.priceDown)
		s.currentPhase = PhaseLock
	}

	// 如果价格更加极端且已锁定，提前进入放大阶段
	if (s.priceUp >= earlyAmplifyPrice || s.priceDown >= earlyAmplifyPrice) && s.lockAchieved && s.currentPhase != PhaseAmplify {
		log.Warnf("成对交易策略: 检测到极端价格（UP=%.4f, DOWN=%.4f）且已锁定，提前进入放大阶段", s.priceUp, s.priceDown)
		s.currentPhase = PhaseAmplify
	}

	if oldPhase != s.currentPhase {
		log.Infof("成对交易策略: 阶段切换 %s → %s", oldPhase, s.currentPhase)
	}
}

// updateLockStatus 更新锁定状态
func (s *PairedTradingStrategy) updateLockStatus() {
	pu := s.positionState.ProfitIfUpWin()
	pd := s.positionState.ProfitIfDownWin()

	wasLocked := s.lockAchieved
	s.lockAchieved = (pu > 0 && pd > 0)

	if !wasLocked && s.lockAchieved {
		log.Infof("✅ 成对交易策略: 锁定完成！UP利润=%.2f USDC, DOWN利润=%.2f USDC", pu, pd)
	} else if wasLocked && !s.lockAchieved {
		log.Warnf("⚠️ 成对交易策略: 锁定失效！UP利润=%.2f USDC, DOWN利润=%.2f USDC", pu, pd)
	}
}

// executeBuildPhase 执行建仓阶段
func (s *PairedTradingStrategy) executeBuildPhase(ctx context.Context, event *events.PriceChangedEvent) error {
	if s.positionState == nil {
		return nil
	}

	baseTarget := s.config.BaseTarget
	buildLotSize := s.config.BuildLotSize
	buildThreshold := s.config.BuildThreshold
	minRatio := s.config.MinRatio
	maxRatio := s.config.MaxRatio

	total := s.positionState.QUp + s.positionState.QDown

	// 计算当前持仓比例
	var upRatio float64
	if total > 0 {
		upRatio = s.positionState.QUp / total
	} else {
		upRatio = 0.5 // 初始状态，假设平衡
	}

	// 建仓逻辑：快速建立双边仓位，保持平衡
	// 条件1：UP持仓不足或比例过低，且价格低于建仓阈值
	if (s.positionState.QUp < baseTarget || upRatio < minRatio) && s.priceUp > 0 && s.priceUp < buildThreshold {
		need := math.Min(buildLotSize, baseTarget-s.positionState.QUp)
		if need >= s.config.MinOrderSize {
			log.Infof("成对交易策略: [建仓阶段] 买入UP - 当前持仓=%.2f < 目标=%.2f, 价格=%.4f < 阈值=%.2f",
				s.positionState.QUp, baseTarget, s.priceUp, buildThreshold)
			if err := s.placeBuyOrderSplit(ctx, event.Market, domain.TokenTypeUp, need, "build_up"); err != nil {
				log.Errorf("成对交易策略: 建仓阶段买入UP失败: %v", err)
			}
		}
	}

	// 条件2：DOWN持仓不足或比例过低，且价格低于建仓阈值
	if (s.positionState.QDown < baseTarget || upRatio > maxRatio) && s.priceDown > 0 && s.priceDown < buildThreshold {
		need := math.Min(buildLotSize, baseTarget-s.positionState.QDown)
		if need >= s.config.MinOrderSize {
			log.Infof("成对交易策略: [建仓阶段] 买入DOWN - 当前持仓=%.2f < 目标=%.2f, 价格=%.4f < 阈值=%.2f",
				s.positionState.QDown, baseTarget, s.priceDown, buildThreshold)
			if err := s.placeBuyOrderSplit(ctx, event.Market, domain.TokenTypeDown, need, "build_down"); err != nil {
				log.Errorf("成对交易策略: 建仓阶段买入DOWN失败: %v", err)
			}
		}
	}

	return nil
}

// executeLockPhase 执行锁定阶段
func (s *PairedTradingStrategy) executeLockPhase(ctx context.Context, event *events.PriceChangedEvent) error {
	if s.positionState == nil {
		return nil
	}

	pu := s.positionState.ProfitIfUpWin()
	pd := s.positionState.ProfitIfDownWin()

	lockThreshold := s.config.LockThreshold
	lockPriceMax := s.config.LockPriceMax
	extremeHigh := s.config.ExtremeHigh
	targetProfit := s.config.TargetProfitBase
	insuranceSize := s.config.InsuranceSize

	// 优先级1：消除负利润（风险对冲）
	// 如果UP方向亏损，且价格合适，补充UP
	if pu < -lockThreshold && s.priceUp > 0 && s.priceUp < lockPriceMax {
		// 计算需要补充的数量：使 P_up_win = 0
		// P_up_win = Q_up * 1.0 - (C_up + C_down)
		// 需要：Q_up * 1.0 - (C_up + C_down) = 0
		// Q_up = C_up + C_down
		targetQUp := s.positionState.CUp + s.positionState.CDown
		need := targetQUp - s.positionState.QUp
		// 限制单次加仓量
		need = math.Min(need, s.config.BuildLotSize*2)
		need = math.Max(need, s.config.MinOrderSize)

		log.Infof("成对交易策略: [锁定阶段] 检测到UP方向风险敞口（利润=%.2f），补充UP %.2f shares", pu, need)
		if err := s.placeBuyOrderSplit(ctx, event.Market, domain.TokenTypeUp, need, "lock_risk_up"); err != nil {
			log.Errorf("成对交易策略: 锁定阶段补充UP失败: %v", err)
		}
		return nil // 单次只处理一个方向
	}

	// 如果DOWN方向亏损，且价格合适，补充DOWN
	if pd < -lockThreshold && s.priceDown > 0 && s.priceDown < lockPriceMax {
		targetQDown := s.positionState.CUp + s.positionState.CDown
		need := targetQDown - s.positionState.QDown
		need = math.Min(need, s.config.BuildLotSize*2)
		need = math.Max(need, s.config.MinOrderSize)

		log.Infof("成对交易策略: [锁定阶段] 检测到DOWN方向风险敞口（利润=%.2f），补充DOWN %.2f shares", pd, need)
		if err := s.placeBuyOrderSplit(ctx, event.Market, domain.TokenTypeDown, need, "lock_risk_down"); err != nil {
			log.Errorf("成对交易策略: 锁定阶段补充DOWN失败: %v", err)
		}
		return nil
	}

	// 优先级2：利用极端价格锁定反向利润
	// 如果UP价格极高，DOWN价格极低，买入DOWN保险
	if s.priceUp >= extremeHigh && s.priceDown < (1.0-extremeHigh) && pd < targetProfit {
		log.Infof("成对交易策略: [锁定阶段] 利用极端价格（UP=%.4f, DOWN=%.4f）锁定DOWN利润", s.priceUp, s.priceDown)
		if s.priceDown > 0 && s.priceDown < 0.30 {
			if err := s.placeBuyOrderSplit(ctx, event.Market, domain.TokenTypeDown, insuranceSize, "lock_extreme_down"); err != nil {
				log.Errorf("成对交易策略: 锁定阶段极端DOWN失败: %v", err)
			}
		}
		return nil
	}

	// 如果DOWN价格极高，UP价格极低，买入UP保险
	if s.priceDown >= extremeHigh && s.priceUp < (1.0-extremeHigh) && pu < targetProfit {
		log.Infof("成对交易策略: [锁定阶段] 利用极端价格（UP=%.4f, DOWN=%.4f）锁定UP利润", s.priceUp, s.priceDown)
		if s.priceUp > 0 && s.priceUp < 0.30 {
			if err := s.placeBuyOrderSplit(ctx, event.Market, domain.TokenTypeUp, insuranceSize, "lock_extreme_up"); err != nil {
				log.Errorf("成对交易策略: 锁定阶段极端UP失败: %v", err)
			}
		}
		return nil
	}

	// 优先级3：双边利润均衡
	// 如果UP利润为正但DOWN利润为负，补充DOWN
	if pu > 0 && pd < 0 && s.priceDown > 0 && s.priceDown < lockPriceMax {
		// 计算需要补充的数量
		targetQDown := s.positionState.CUp + s.positionState.CDown
		need := targetQDown - s.positionState.QDown
		need = math.Min(need, s.config.BuildLotSize)
		need = math.Max(need, s.config.MinOrderSize)

		log.Infof("成对交易策略: [锁定阶段] 双边均衡 - UP利润=%.2f > 0, DOWN利润=%.2f < 0, 补充DOWN", pu, pd)
		if err := s.placeBuyOrderSplit(ctx, event.Market, domain.TokenTypeDown, need, "lock_balance_down"); err != nil {
			log.Errorf("成对交易策略: 锁定阶段均衡DOWN失败: %v", err)
		}
		return nil
	}

	// 如果DOWN利润为正但UP利润为负，补充UP
	if pd > 0 && pu < 0 && s.priceUp > 0 && s.priceUp < lockPriceMax {
		targetQUp := s.positionState.CUp + s.positionState.CDown
		need := targetQUp - s.positionState.QUp
		need = math.Min(need, s.config.BuildLotSize)
		need = math.Max(need, s.config.MinOrderSize)

		log.Infof("成对交易策略: [锁定阶段] 双边均衡 - DOWN利润=%.2f > 0, UP利润=%.2f < 0, 补充UP", pd, pu)
		if err := s.placeBuyOrderSplit(ctx, event.Market, domain.TokenTypeUp, need, "lock_balance_up"); err != nil {
			log.Errorf("成对交易策略: 锁定阶段均衡UP失败: %v", err)
		}
		return nil
	}

	// 优先级4：提升利润到目标值
	// 如果两个方向都为正但未达到目标，优先补充利润较低的一侧
	if pu > 0 && pd > 0 {
		if pu < targetProfit && pu < pd && s.priceUp > 0 && s.priceUp < lockPriceMax {
			need := s.config.InsuranceSize
			log.Infof("成对交易策略: [锁定阶段] 提升UP利润（当前=%.2f < 目标=%.2f）", pu, targetProfit)
			if err := s.placeBuyOrderSplit(ctx, event.Market, domain.TokenTypeUp, need, "lock_boost_up"); err != nil {
				log.Errorf("成对交易策略: 锁定阶段提升UP失败: %v", err)
			}
			return nil
		}

		if pd < targetProfit && pd < pu && s.priceDown > 0 && s.priceDown < lockPriceMax {
			need := s.config.InsuranceSize
			log.Infof("成对交易策略: [锁定阶段] 提升DOWN利润（当前=%.2f < 目标=%.2f）", pd, targetProfit)
			if err := s.placeBuyOrderSplit(ctx, event.Market, domain.TokenTypeDown, need, "lock_boost_down"); err != nil {
				log.Errorf("成对交易策略: 锁定阶段提升DOWN失败: %v", err)
			}
			return nil
		}
	}

	return nil
}

// executeAmplifyPhase 执行放大阶段
func (s *PairedTradingStrategy) executeAmplifyPhase(ctx context.Context, event *events.PriceChangedEvent) error {
	if s.positionState == nil || !s.lockAchieved {
		return nil
	}

	pu := s.positionState.ProfitIfUpWin()
	pd := s.positionState.ProfitIfDownWin()

	amplifyTarget := s.config.AmplifyTarget
	amplifyPriceMax := s.config.AmplifyPriceMax
	insurancePriceMax := s.config.InsurancePriceMax
	directionThreshold := s.config.DirectionThreshold

	// 判定主方向
	mainDirection := "NEUTRAL"
	if s.priceUp >= directionThreshold && s.priceDown < (1.0-directionThreshold) {
		mainDirection = "UP"
	} else if s.priceDown >= directionThreshold && s.priceUp < (1.0-directionThreshold) {
		mainDirection = "DOWN"
	}

	// 如果是中性市场，不放大
	if mainDirection == "NEUTRAL" {
		log.Debugf("成对交易策略: [放大阶段] 中性市场（UP=%.4f, DOWN=%.4f），不放大", s.priceUp, s.priceDown)
		return nil
	}

	// 放大主方向利润
	if mainDirection == "UP" && pu < amplifyTarget && s.priceUp > 0 && s.priceUp < amplifyPriceMax {
		// 计算需要加仓的数量
		targetQUp := amplifyTarget + s.positionState.CUp + s.positionState.CDown
		need := targetQUp - s.positionState.QUp
		need = math.Min(need, s.config.BuildLotSize)
		need = math.Max(need, s.config.MinOrderSize)

		log.Infof("成对交易策略: [放大阶段] 放大UP利润（当前=%.2f → 目标=%.2f）", pu, amplifyTarget)
		if err := s.placeBuyOrderSplit(ctx, event.Market, domain.TokenTypeUp, need, "amplify_up"); err != nil {
			log.Errorf("成对交易策略: 放大阶段UP失败: %v", err)
		}

		// 同时买入少量反向保险
		if s.priceDown > 0 && s.priceDown < insurancePriceMax {
			log.Infof("成对交易策略: [放大阶段] 买入DOWN保险（价格=%.4f）", s.priceDown)
			if err := s.placeBuyOrderSplit(ctx, event.Market, domain.TokenTypeDown, s.config.InsuranceSize*0.5, "amplify_insurance_down"); err != nil {
				log.Errorf("成对交易策略: 放大阶段DOWN保险失败: %v", err)
			}
		}
		return nil
	}

	if mainDirection == "DOWN" && pd < amplifyTarget && s.priceDown > 0 && s.priceDown < amplifyPriceMax {
		targetQDown := amplifyTarget + s.positionState.CUp + s.positionState.CDown
		need := targetQDown - s.positionState.QDown
		need = math.Min(need, s.config.BuildLotSize)
		need = math.Max(need, s.config.MinOrderSize)

		log.Infof("成对交易策略: [放大阶段] 放大DOWN利润（当前=%.2f → 目标=%.2f）", pd, amplifyTarget)
		if err := s.placeBuyOrderSplit(ctx, event.Market, domain.TokenTypeDown, need, "amplify_down"); err != nil {
			log.Errorf("成对交易策略: 放大阶段DOWN失败: %v", err)
		}

		// 同时买入少量反向保险
		if s.priceUp > 0 && s.priceUp < insurancePriceMax {
			log.Infof("成对交易策略: [放大阶段] 买入UP保险（价格=%.4f）", s.priceUp)
			if err := s.placeBuyOrderSplit(ctx, event.Market, domain.TokenTypeUp, s.config.InsuranceSize*0.5, "amplify_insurance_up"); err != nil {
				log.Errorf("成对交易策略: 放大阶段UP保险失败: %v", err)
			}
		}
		return nil
	}

	return nil
}

// placeBuyOrderSplit 将大单拆成若干笔
func (s *PairedTradingStrategy) placeBuyOrderSplit(ctx context.Context, market *domain.Market, tokenType domain.TokenType, size float64, reason string) error {
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
			log.Warnf("成对交易策略: in-flight 订单数量已达上限（%d），暂停下单", s.inFlightLimiter.Max())
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
func (s *PairedTradingStrategy) placeBuyOrder(ctx context.Context, market *domain.Market, tokenType domain.TokenType, size float64, reason string) error {
	// 限制并发订单数量
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

	// 滑点保护
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

	// 没有 executor 时仍保持兼容
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

		adjustedSize, skipped, adjusted, adjustRatio, orderAmount, newOrderAmount := common.AdjustSizeForMinOrderUSDC(
			size,
			bestAskPrice,
			minOrderUSDC,
			s.config.AutoAdjustSize,
			s.config.MaxSizeAdjustRatio,
		)
		if skipped {
			// 不自动调整：直接跳过
			if !s.config.AutoAdjustSize {
				log.Warnf("成对交易策略: %s - 订单金额 %.4f USDC < 最小要求 %.2f USDC，跳过下单（数量=%.2f, 价格=%.4f）",
					reason, orderAmount, minOrderUSDC, size, bestAskPrice.ToDecimal())
				return nil
			}
			// 自动调整但超过最大允许倍数：跳过
			requiredSize := minOrderUSDC / bestAskPrice.ToDecimal()
			log.Warnf("成对交易策略: %s - 所需调整倍数 %.2f > 最大允许 %.2f，跳过下单（原数量=%.2f, 需要数量=%.2f, 价格=%.4f）",
				reason, adjustRatio, s.config.MaxSizeAdjustRatio, size, requiredSize, bestAskPrice.ToDecimal())
			return nil
		}
		if adjusted {
			log.Infof("成对交易策略: %s - ⚠️ 自动调整数量以满足最小金额：%.2f → %.2f shares (%.2fx), 原金额=%.4f → 新金额=%.4f USDC (价格=%.4f)",
				reason, size, adjustedSize, adjustRatio, orderAmount, newOrderAmount, bestAskPrice.ToDecimal())
		}

		order := orderutil.NewOrder(market.Slug, assetID, types.SideBuy, bestAskPrice, adjustedSize, tokenType, true, types.OrderTypeFAK)
		_, err = ts.PlaceOrder(ctx, order)
		return err
	}

	s.initLoopIfNeeded()
	if s.inFlightLimiter != nil && !s.inFlightLimiter.TryAcquire() {
		return nil
	}
	ok := exec.Submit(bbgo.Command{
		Name:    fmt.Sprintf("paired_trading_buy_%s_%s", tokenType, reason),
		Timeout: 25 * time.Second,
		Do: func(runCtx context.Context) {
			bestAskPrice, err := orderutil.QuoteBuyPrice(runCtx, ts, assetID, maxCents)
			if err != nil {
				select {
				case s.cmdResultC <- pairedTradingCmdResult{tokenType: tokenType, reason: reason, err: err}:
				default:
				}
				return
			}

			adjustedSize, skipped, _, _, _, _ := common.AdjustSizeForMinOrderUSDC(
				size,
				bestAskPrice,
				minOrderUSDC,
				s.config.AutoAdjustSize,
				s.config.MaxSizeAdjustRatio,
			)
			if skipped {
				select {
				case s.cmdResultC <- pairedTradingCmdResult{tokenType: tokenType, reason: reason, skipped: true}:
				default:
				}
				return
			}

			mSlug := ""
			if s.currentMarket != nil {
				mSlug = s.currentMarket.Slug
			}
			order := orderutil.NewOrder(mSlug, assetID, types.SideBuy, bestAskPrice, adjustedSize, tokenType, true, types.OrderTypeFAK)

			created, err := ts.PlaceOrder(runCtx, order)
			select {
			case s.cmdResultC <- pairedTradingCmdResult{tokenType: tokenType, reason: reason, created: created, err: err}:
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

// OnOrderUpdate 处理订单更新事件（实现 OrderHandler 接口）
func (s *PairedTradingStrategy) OnOrderUpdate(ctx context.Context, order *domain.Order) error {
	if order == nil {
		return nil
	}
	s.startLoop(ctx)
	select {
	case s.orderC <- order:
	default:
		log.Warnf("成对交易策略: orderC 已满，丢弃订单更新 %s", order.OrderID)
	}
	return nil
}

// onOrderUpdateInternal 内部处理订单更新
func (s *PairedTradingStrategy) onOrderUpdateInternal(ctx context.Context, order *domain.Order) error {
	if order == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// 只处理当前市场的订单
	if s.marketGuard.Current() == "" || s.marketGuard.Current() != order.MarketSlug {
		return nil
	}

	// 只处理成交的买入订单
	if order.Status != domain.OrderStatusFilled || order.Side != types.SideBuy {
		return nil
	}

	// 更新持仓状态
	cost := order.Size * order.Price.ToDecimal()
	if order.TokenType == domain.TokenTypeUp {
		s.positionState.QUp += order.Size
		s.positionState.CUp += cost
		log.Infof("成对交易策略: UP订单成交, 数量=%.2f, 价格=%.4f, 成本=%.2f, QUp=%.2f, CUp=%.2f",
			order.Size, order.Price.ToDecimal(), cost, s.positionState.QUp, s.positionState.CUp)
	} else if order.TokenType == domain.TokenTypeDown {
		s.positionState.QDown += order.Size
		s.positionState.CDown += cost
		log.Infof("成对交易策略: DOWN订单成交, 数量=%.2f, 价格=%.4f, 成本=%.2f, QDown=%.2f, CDown=%.2f",
			order.Size, order.Price.ToDecimal(), cost, s.positionState.QDown, s.positionState.CDown)
	}

	// 记录即时利润
	pu := s.positionState.ProfitIfUpWin()
	pd := s.positionState.ProfitIfDownWin()
	log.Infof("成对交易策略: 即时利润 - UP胜=%.2f USDC, DOWN胜=%.2f USDC", pu, pd)

	// 更新锁定状态
	s.updateLockStatus()

	return nil
}

// OnOrderFilled 处理订单成交事件（兼容旧接口）
func (s *PairedTradingStrategy) OnOrderFilled(ctx context.Context, event *events.OrderFilledEvent) error {
	if event == nil || event.Order == nil {
		return nil
	}
	return s.OnOrderUpdate(ctx, event.Order)
}

// CanOpenPosition 检查是否可以开仓
func (s *PairedTradingStrategy) CanOpenPosition(ctx context.Context, market *domain.Market) (bool, error) {
	return s.isBTC15mMarket(market), nil
}

// CalculateEntry 计算入场价格和数量（不使用）
func (s *PairedTradingStrategy) CalculateEntry(ctx context.Context, market *domain.Market, price domain.Price) (*domain.Order, error) {
	return nil, fmt.Errorf("成对交易策略不使用此方法")
}

// CalculateHedge 计算对冲订单（不使用）
func (s *PairedTradingStrategy) CalculateHedge(ctx context.Context, entryOrder *domain.Order) (*domain.Order, error) {
	return nil, fmt.Errorf("成对交易策略不使用此方法")
}

// CheckTakeProfitStopLoss 检查止盈止损（不使用）
func (s *PairedTradingStrategy) CheckTakeProfitStopLoss(ctx context.Context, position *domain.Position, currentPrice domain.Price) (*domain.Order, error) {
	return nil, fmt.Errorf("成对交易策略不使用此方法")
}

// Cleanup 清理资源
func (s *PairedTradingStrategy) Cleanup(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.positionState = nil
	s.currentMarket = nil
	s.isPlacingOrder = false
	s.currentPhase = PhaseBuild
	s.lockAchieved = false

	return nil
}

// Subscribe 订阅会话事件（BBGO 风格）
func (s *PairedTradingStrategy) Subscribe(session *bbgo.ExchangeSession) {
	session.OnPriceChanged(s)
	session.OnOrderUpdate(s)
	log.Infof("成对交易策略已订阅价格变化和订单更新事件")
}

// Run 运行策略（BBGO 风格）
func (s *PairedTradingStrategy) Run(ctx context.Context, orderExecutor bbgo.OrderExecutor, session *bbgo.ExchangeSession) error {
	log.Infof("成对交易策略已启动")
	s.startLoop(ctx)
	return nil
}

// Shutdown 优雅关闭（BBGO 风格）
func (s *PairedTradingStrategy) Shutdown(ctx context.Context, wg *sync.WaitGroup) {
	log.Infof("成对交易策略: 开始优雅关闭...")
	s.stopLoop()
	if err := s.Cleanup(ctx); err != nil {
		log.Errorf("成对交易策略清理失败: %v", err)
	}
	log.Infof("成对交易策略: 优雅关闭完成")
}

// isBTC15mMarket 检查是否为 BTC 15分钟市场
func (s *PairedTradingStrategy) isBTC15mMarket(market *domain.Market) bool {
	return market != nil && len(market.Slug) > 13 && market.Slug[:13] == "btc-updown-15m"
}
