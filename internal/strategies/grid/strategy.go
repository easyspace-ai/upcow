package grid

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/strategies"
	"github.com/betbot/gobet/internal/strategies/common"
	"github.com/betbot/gobet/pkg/bbgo"
)

const ID = "grid"

// BBGO风格：使用logrus.WithField创建策略专用logger
var log = logrus.WithField("strategy", ID)

func init() {
	// BBGO风格：在init函数中注册策略及其配置适配器
	bbgo.RegisterStrategyWithAdapter(ID, &GridStrategy{}, &GridConfigAdapter{})
}

// GridStrategy 网格策略实现
type GridStrategy struct {
	// Executor 串行 IO 执行器（由 Environment 注入）
	Executor           bbgo.CommandExecutor
	config             *GridStrategyConfig
	grid               *domain.Grid
	tradingService     TradingServiceInterface // 交易服务接口
	directModeDebounce int                     // 直接回调模式的防抖间隔（毫秒），默认100ms
	// activeOrders 已移除：现在由 OrderEngine 管理，通过 tradingService.GetActiveOrders() 查询
	activePosition        *domain.Position
	roundsThisPeriod      int
	currentPeriod         int64
	currentPriceUp        int       // 当前 UP 币价格（分）
	currentPriceDown      int       // 当前 DOWN 币价格（分）
	lastPriceUpdateUp     time.Time // UP 币最后更新时间
	lastPriceUpdateDown   time.Time // DOWN 币最后更新时间
	mu                    sync.RWMutex
	isPlacingOrder        bool
	placeOrderMu          sync.Mutex
	isPlacingOrderSetTime time.Time // 记录isPlacingOrder设置为true的时间（用于超时检测）
	// 双向持仓跟踪
	upTotalCost   float64 // UP 总成本（USDC）
	upHoldings    float64 // UP 持仓量（shares）
	downTotalCost float64 // DOWN 总成本（USDC）
	downHoldings  float64 // DOWN 持仓量（shares）
	// 待提交的对冲订单（主单 OrderID -> 对冲订单），等待主单成交后再提交
	pendingHedgeOrders map[string]*domain.Order
	// 当前市场周期（用于检测周期切换）
	currentMarketSlug string
	currentMarket     *domain.Market // 当前市场引用（用于订单更新处理）
	// 已处理的网格层级（防止重复触发）：tokenType:gridLevel -> timestamp
	processedGridLevels map[string]*common.Debouncer
	processedLevelsMu   sync.RWMutex // 保护 processedGridLevels 的锁 // 当前市场的 Slug，用于检测周期切换
	// 价格更新诊断
	priceUpdateCount        int               // 价格更新计数（用于诊断）
	priceUpdateLogDebouncer *common.Debouncer // 价格更新诊断日志防抖（默认按10次输出节奏）

	// UI/日志输出防抖（避免高频刷屏；不影响交易决策）
	displayDebouncer *common.Debouncer
	// 对冲订单提交防抖
	hedgeSubmitDebouncer *common.Debouncer // 对冲订单提交防抖（默认2s）
	// 风险8修复：对冲订单提交锁（防止多个对冲机制并发提交）
	hedgeOrderSubmitMu sync.Mutex // 保护对冲订单提交的锁，确保同一时间只有一个goroutine提交对冲订单
	// 订单成交事件去重：orderID -> filledAt timestamp
	processedFilledOrders   map[string]*common.Debouncer // 已处理的订单成交事件（用于去重；每个 orderID 记录最后一次 filledAt）
	processedFilledOrdersMu sync.RWMutex                 // 保护 processedFilledOrders 的锁

	// 单线程事件循环（确定性优先）
	loopOnce     sync.Once
	loopCancel   context.CancelFunc
	priceSignalC chan struct{}
	priceMu      sync.Mutex
	latestPrice  map[domain.TokenType]*events.PriceChangedEvent
	orderC       chan orderUpdate

	// 命令执行结果（由全局 Executor 回传到策略 loop）
	cmdResultC chan gridCmdResult

	// HedgePlan：统一的入场/对冲状态机（下一阶段工程化）
	plan *HedgePlan
}

type orderUpdate struct {
	ctx   context.Context
	order *domain.Order
}

// TradingServiceInterface 交易服务接口（避免循环依赖）
type TradingServiceInterface interface {
	PlaceOrder(ctx context.Context, order *domain.Order) (*domain.Order, error)
	CancelOrder(ctx context.Context, orderID string) error
	CreatePosition(ctx context.Context, position *domain.Position) error
	UpdatePosition(ctx context.Context, positionID string, updater func(*domain.Position)) error
	ClosePosition(ctx context.Context, positionID string, exitPrice domain.Price, exitOrder *domain.Order) error
	GetOpenPositions() []*domain.Position
	GetActiveOrders() []*domain.Order // 重构后：添加此方法用于查询活跃订单
	GetBestPrice(ctx context.Context, assetID string) (bestBid float64, bestAsk float64, err error)
	SyncOrderStatus(ctx context.Context, orderID string) error // 同步订单状态（通过 API 查询）
}

// NewGridStrategy 创建新的网格策略
func NewGridStrategy() *GridStrategy {
	return &GridStrategy{
		// activeOrders 已移除：现在由 OrderEngine 管理
		pendingHedgeOrders:    make(map[string]*domain.Order),
		processedFilledOrders: make(map[string]*common.Debouncer),
		upTotalCost:           0,
		upHoldings:            0,
		downTotalCost:         0,
		downHoldings:          0,
		priceSignalC:          make(chan struct{}, 1),
		latestPrice:           make(map[domain.TokenType]*events.PriceChangedEvent),
		orderC:                make(chan orderUpdate, 4096),
		cmdResultC:            make(chan gridCmdResult, 4096),
	}
}

// SetTradingService 设置交易服务（在初始化后调用）
// 重构后：移除锁，因为设置交易服务只在初始化时调用一次
func (s *GridStrategy) SetTradingService(ts TradingServiceInterface) {
	s.tradingService = ts
}

// ID 返回策略ID（BBGO风格）
func (s *GridStrategy) ID() string {
	return ID
}

// getActiveOrders 获取活跃订单（重构后：从 TradingService 查询，而不是从本地状态）
func (s *GridStrategy) getActiveOrders() []*domain.Order {
	if s.tradingService == nil {
		return nil
	}
	return s.tradingService.GetActiveOrders()
}

// hasActiveOrders 检查是否有活跃订单（重构后：从 TradingService 查询）
func (s *GridStrategy) hasActiveOrders() bool {
	orders := s.getActiveOrders()
	return len(orders) > 0
}

// Name 返回策略名称（兼容旧接口）
func (s *GridStrategy) Name() string {
	return ID
}

// Defaults 设置默认值（BBGO风格）
func (s *GridStrategy) Defaults() error {
	// 可以在这里设置默认值
	return nil
}

// Validate 验证配置（BBGO风格）
func (s *GridStrategy) Validate() error {
	if s.config == nil {
		return fmt.Errorf("策略配置未设置")
	}
	return s.config.Validate()
}

// Initialize 初始化策略
func (s *GridStrategy) Initialize(ctx context.Context, config strategies.StrategyConfig) error {
	gridConfig, ok := config.(*GridStrategyConfig)
	if !ok {
		return fmt.Errorf("无效的配置类型")
	}

	s.config = gridConfig

	// BBGO风格：调用Validate方法验证配置
	if err := s.Validate(); err != nil {
		return fmt.Errorf("配置验证失败: %w", err)
	}

	// 设置默认值（BBGO风格：只支持直接回调模式）
	if s.directModeDebounce <= 0 {
		s.directModeDebounce = 100 // 默认100ms防抖
	}
	if s.displayDebouncer == nil {
		s.displayDebouncer = common.NewDebouncer(time.Duration(s.directModeDebounce) * time.Millisecond)
	} else {
		s.displayDebouncer.SetInterval(time.Duration(s.directModeDebounce) * time.Millisecond)
	}
	// 对冲订单提交防抖：默认 2s（只在成功提交后 Mark）
	if s.hedgeSubmitDebouncer == nil {
		s.hedgeSubmitDebouncer = common.NewDebouncer(2 * time.Second)
	}

	// 使用手工定义的网格层级创建网格
	s.grid = domain.NewGridFromLevels(gridConfig.GridLevels)
	// BBGO风格：使用logrus.WithField的logger
	log.Infof("网格策略已初始化: 网格层级数量=%d, 层级=%v, 防抖间隔=%dms, EnableDoubleSide=%v",
		len(s.grid.Levels), gridConfig.GridLevels, s.directModeDebounce, gridConfig.EnableDoubleSide)

	// 清空双向持仓跟踪（新周期开始）
	s.ResetHoldings()

	// BBGO风格：使用直接回调模式（无队列）
	log.Infof("✅ 使用直接回调模式（BBGO风格），防抖间隔=%dms", s.directModeDebounce)

	// 注意：实时UI配置从应用级配置读取，通过SetAppConfig方法设置
	// 注意：智能对冲检查在每次价格变化时实时触发，无需定时器

	return nil
}

// onPriceChangedInternal 内部价格变化处理逻辑（直接回调模式）

// ResetHoldings 清空双向持仓跟踪（用于市场周期切换）

// OnPriceChanged 处理价格变化事件
// 网格策略规则：
// 1. 同时监控 UP 币和 DOWN 币的价格变化
// 2. 不论 UP 还是 DOWN，只要价格达到 62分（网格层级）就买入该币
// 3. 因为只有涨的币（价格高的币），代表周期结束后才大概率胜出
// 4. 如果买了 UP 币，对冲买入 DOWN 币
// 5. 如果买了 DOWN 币，对冲买入 UP 币

// displayGridPosition 实时显示当前 UP/DOWN 所处的网格位置和仓位情况

// displayStrategyStatus 在终端显示策略状态信息

// displayHoldingsAndProfit 显示双向持仓和利润信息

// logPriceUpdate 格式化并显示价格更新信息
// 格式: UP:   0.78 → 0.78 (78c) | Grid: 78 | Interval: [78, 78)
func (s *GridStrategy) logPriceUpdate(event *events.PriceChangedEvent, oldPriceUp, oldPriceDown int) {
	// 确定当前更新的币种的新价格和旧价格
	var upNewPrice, downNewPrice int
	var upOldPrice, downOldPrice int

	if event.TokenType == domain.TokenTypeUp {
		// UP 币更新
		upNewPrice = event.NewPrice.Cents
		if event.OldPrice != nil {
			upOldPrice = event.OldPrice.Cents
		} else {
			upOldPrice = oldPriceUp
		}
		// DOWN 币保持不变
		downNewPrice = s.currentPriceDown
		downOldPrice = oldPriceDown
	} else if event.TokenType == domain.TokenTypeDown {
		// DOWN 币更新
		downNewPrice = event.NewPrice.Cents
		if event.OldPrice != nil {
			downOldPrice = event.OldPrice.Cents
		} else {
			downOldPrice = oldPriceDown
		}
		// UP 币保持不变
		upNewPrice = s.currentPriceUp
		upOldPrice = oldPriceUp
	} else {
		// 未知币种，使用当前价格
		upNewPrice = s.currentPriceUp
		upOldPrice = oldPriceUp
		downNewPrice = s.currentPriceDown
		downOldPrice = oldPriceDown
	}

	// 格式化并显示价格更新信息
	log.Infof("✅ Price updated:")

	// 显示 UP 币信息
	s.logTokenPriceUpdate("UP", upOldPrice, upNewPrice)

	// 显示 DOWN 币信息
	s.logTokenPriceUpdate("DOWN", downOldPrice, downNewPrice)

	// 显示仓位和利润信息
	s.logPositionAndProfit()
}

// logTokenPriceUpdate 格式化并显示单个币种的价格更新信息
func (s *GridStrategy) logTokenPriceUpdate(tokenName string, oldPriceCents, newPriceCents int) {
	// 格式化价格显示（小数形式）
	oldDecimal := float64(oldPriceCents) / 100.0
	newDecimal := float64(newPriceCents) / 100.0

	// 构建价格变化显示
	priceChangeStr := fmt.Sprintf("%.2f → %.2f", oldDecimal, newDecimal)

	// 获取网格区间
	lower, upper := s.grid.GetInterval(newPriceCents)

	// 找到价格达到或超过的最高网格层级
	var gridLevel *int
	for i := len(s.grid.Levels) - 1; i >= 0; i-- {
		level := s.grid.Levels[i]
		if newPriceCents >= level {
			gridLevel = &level
			break
		}
	}

	// 构建网格层级显示
	var gridStr string
	if gridLevel != nil && newPriceCents == *gridLevel {
		// 价格正好在网格层级上
		gridStr = fmt.Sprintf("%d", *gridLevel)
	} else {
		// 价格不在网格层级上
		gridStr = "N/A"
	}

	// 构建区间显示
	var intervalStr string
	if lower == nil && upper == nil {
		intervalStr = "[-∞, +∞)"
	} else if lower == nil && upper != nil {
		intervalStr = fmt.Sprintf("[-∞, %d)", *upper)
	} else if lower != nil && upper == nil {
		intervalStr = fmt.Sprintf("[%d, +∞)", *lower)
	} else {
		if *lower == *upper {
			intervalStr = fmt.Sprintf("[%d, %d)", *lower, *upper)
		} else {
			intervalStr = fmt.Sprintf("[%d, %d)", *lower, *upper)
		}
	}

	// 格式化输出
	log.Infof("   %-5s %s (%dc) | Grid: %s | Interval: %s",
		tokenName+":", priceChangeStr, newPriceCents, gridStr, intervalStr)
}

// logPositionAndProfit 显示仓位和利润信息
func (s *GridStrategy) logPositionAndProfit() {
	// 显示当前仓位信息
	if s.activePosition != nil {
		pos := s.activePosition
		var profitInfo string

		// 计算当前利润/亏损
		if pos.TokenType == domain.TokenTypeUp && s.currentPriceUp > 0 {
			currentPrice := domain.Price{Cents: s.currentPriceUp}
			profit := pos.CalculateProfit(currentPrice)
			if profit > 0 {
				profitInfo = fmt.Sprintf(" | 利润: +%dc", profit)
			} else if profit < 0 {
				profitInfo = fmt.Sprintf(" | 亏损: %dc", profit)
			} else {
				profitInfo = " | 盈亏: 0"
			}
		} else if pos.TokenType == domain.TokenTypeDown && s.currentPriceDown > 0 {
			currentPrice := domain.Price{Cents: s.currentPriceDown}
			profit := pos.CalculateProfit(currentPrice)
			if profit > 0 {
				profitInfo = fmt.Sprintf(" | 利润: +%dc", profit)
			} else if profit < 0 {
				profitInfo = fmt.Sprintf(" | 亏损: %dc", profit)
			} else {
				profitInfo = " | 盈亏: 0"
			}
		}

		// 显示对冲状态
		hedgeStatus := ""
		if pos.IsHedged() {
			hedgeStatus = " ✅ 已对冲"
		} else {
			hedgeStatus = " ⚠️ 未对冲"
		}

		log.Infof("   💼 仓位: %s币 @ %dc, 数量=%.2f%s%s",
			pos.TokenType, pos.EntryPrice.Cents, pos.Size, hedgeStatus, profitInfo)
	} else {
		log.Infof("   💼 仓位: 无")
	}

	// 显示双向持仓和利润信息
	s.mu.RLock()
	upTotalCost := s.upTotalCost
	upHoldings := s.upHoldings
	downTotalCost := s.downTotalCost
	downHoldings := s.downHoldings
	s.mu.RUnlock()

	// 计算均价
	var upAvgPrice float64
	if upHoldings > 0 {
		upAvgPrice = upTotalCost / upHoldings
	}

	var downAvgPrice float64
	if downHoldings > 0 {
		downAvgPrice = downTotalCost / downHoldings
	}

	// 计算利润
	// UP胜利润 = UP持仓量 * 1 USDC - UP总成本 - DOWN总成本
	upWinProfit := upHoldings*1.0 - upTotalCost - downTotalCost

	// DOWN胜利润 = DOWN持仓量 * 1 USDC - UP总成本 - DOWN总成本
	downWinProfit := downHoldings*1.0 - upTotalCost - downTotalCost

	// 显示双向持仓和利润信息
	log.Infof("   📊 双向持仓: UP成本=%.8f USDC, 持仓=%.8f, 均价=%.8f | DOWN成本=%.8f USDC, 持仓=%.8f, 均价=%.8f",
		upTotalCost, upHoldings, upAvgPrice, downTotalCost, downHoldings, downAvgPrice)
	log.Infof("   💰 利润: UP胜=%.8f USDC, DOWN胜=%.8f USDC", upWinProfit, downWinProfit)
}

// formatGridPosition 格式化单个币种的网格位置信息
// formatGridPosition 格式化网格位置信息
// 格式: UP:   N/A → 1.00 (100c) | Grid: 65c | Interval: [65, 71)
func (s *GridStrategy) formatGridPosition(tokenName string, priceCents int, isChanged bool, event *events.PriceChangedEvent) string {
	priceDecimal := float64(priceCents) / 100.0

	// 获取网格层级和区间信息
	lower, upper := s.grid.GetInterval(priceCents)

	// 找到价格达到或超过的最高网格层级（用于显示应该触发的层级）
	var targetLevel *int
	for i := len(s.grid.Levels) - 1; i >= 0; i-- {
		level := s.grid.Levels[i]
		if priceCents >= level {
			targetLevel = &level
			break
		}
	}

	// 构建价格变化显示
	var priceChangeStr string
	if isChanged && event.OldPrice != nil {
		oldDecimal := event.OldPrice.ToDecimal()
		priceChangeStr = fmt.Sprintf("%.2f → %.2f", oldDecimal, priceDecimal)
	} else {
		// 如果没有旧价格，显示 N/A → 新价格
		priceChangeStr = fmt.Sprintf("N/A → %.2f", priceDecimal)
	}

	// 构建网格层级显示（显示应该触发的层级）
	var gridStr string
	if targetLevel != nil {
		if priceCents == *targetLevel {
			gridStr = fmt.Sprintf("%dc (精确)", *targetLevel)
		} else {
			gridStr = fmt.Sprintf("%dc (触发)", *targetLevel)
		}
	} else {
		gridStr = "N/A (低于所有层级)"
	}

	// 构建区间显示（使用分 cents）
	var intervalStr string
	if lower == nil && upper == nil {
		intervalStr = "[-∞, +∞)"
	} else if lower == nil && upper != nil {
		intervalStr = fmt.Sprintf("[-∞, %d)", *upper)
	} else if lower != nil && upper == nil {
		intervalStr = fmt.Sprintf("[%d, +∞)", *lower)
	} else {
		if *lower == *upper {
			intervalStr = fmt.Sprintf("[%d]", *lower)
		} else {
			intervalStr = fmt.Sprintf("[%d, %d)", *lower, *upper)
		}
	}

	// 格式化输出，对齐格式
	return fmt.Sprintf("%-5s %s (%dc) | Grid: %s | Interval: %s",
		tokenName+":", priceChangeStr, priceCents, gridStr, intervalStr)
}

// formatPositionInfo 格式化仓位信息
func (s *GridStrategy) formatPositionInfo() string {
	if s.activePosition == nil {
		return "💼 仓位: 无"
	}

	pos := s.activePosition
	info := fmt.Sprintf("💼 仓位: %s币 @ %dc, 数量=%.2f", pos.TokenType, pos.EntryPrice.Cents, pos.Size)

	// 计算当前利润/亏损
	if s.currentPriceUp > 0 && pos.TokenType == domain.TokenTypeUp {
		currentPrice := domain.Price{Cents: s.currentPriceUp}
		profit := pos.CalculateProfit(currentPrice)
		if profit > 0 {
			info += fmt.Sprintf(" | 利润: +%dc", profit)
		} else if profit < 0 {
			info += fmt.Sprintf(" | 亏损: %dc", profit)
		} else {
			info += " | 盈亏: 0"
		}
	} else if s.currentPriceDown > 0 && pos.TokenType == domain.TokenTypeDown {
		currentPrice := domain.Price{Cents: s.currentPriceDown}
		profit := pos.CalculateProfit(currentPrice)
		if profit > 0 {
			info += fmt.Sprintf(" | 利润: +%dc", profit)
		} else if profit < 0 {
			info += fmt.Sprintf(" | 亏损: %dc", profit)
		} else {
			info += " | 盈亏: 0"
		}
	}

	// 显示是否已对冲
	if pos.IsHedged() {
		info += " ✅ 已对冲"
	} else {
		info += " ⚠️ 未对冲"
	}

	return info
}

// formatOrdersInfo 格式化待成交订单信息

// calculateOrderSize 根据配置计算订单金额和share数量
// 使用 OrderSize（share数量）下单，确保金额 >= MinOrderSize USDC（交易所最小要求）

// handleGridLevelReached 处理网格层级到达
// 网格交易逻辑：
// 1. 不论 UP 还是 DOWN，只要价格达到 62分（网格层级）就买入该币
// 2. 因为只有涨的币（价格高的币），代表周期结束后才大概率胜出
// 3. 如果买了 UP 币，对冲买入 DOWN 币
// 4. 如果买了 DOWN 币，对冲买入 UP 币

// OnOrderUpdate 处理订单更新事件（实现 OrderHandler 接口）
// 将订单更新转换为 OrderFilledEvent 并调用 OnOrderFilled

// OnOrderFilled 处理订单成交事件

// CanOpenPosition 检查是否可以开仓

// CalculateEntry 计算入场价格和数量
// 网格策略入场规则：
// - 只买入 UP 币（YES token）
// - 价格必须是网格层级价格
// - 价格必须 >= 62分（最小交易价格）

// CalculateHedge 计算对冲订单
// 对冲订单规则：
// - 入场订单买入 UP 币 @ 网格层级价格
// - 对冲订单买入 DOWN 币 @ (100 - UP币价格)
// - 两个订单都是 BUY，但买入不同的代币
//
// 注意：此方法需要 market 参数来获取 DOWN 币资产 ID，建议使用 handleGridLevelReached 方法

// CheckStopLoss 检查止损
// 止损规则：
// - 目标是对冲锁定利润，如果对冲锁定不了，则止损
// - 止损：卖出持有的代币
// - 注意：只有止损才是卖出操作，入场和对冲都是买入

// CheckTakeProfitStopLoss 检查止盈止损（接口方法）
// 注意：已禁用止损功能，改用智能对冲算法管理风险敞口
// 只下买单，不卖，用对冲方案来解决风险敞口

// 注意：已移除定时器检查，改为在每次价格变化时实时检查
// 这样更及时，能更快响应市场变化并补充对冲订单

// checkAndSupplementHedge 检查并补充对冲订单（智能对冲算法）
// 分析风险敞口，如果对冲单没有成交，及时补充对向侧的订单

// calculateOptimalHedgePrice 计算最优对冲价格
// 策略：
// 1. 优先使用理想对冲价格（确保利润目标）
// 2. 如果理想价格无法成交，动态调整到市场价格附近（确保能成交）
// 3. 确保总成本 <= 100，利润目标尽量满足

// checkAndAutoHedge 实时检查利润并自动对冲
// 如果发现某个方向利润为负（未锁定），自动补充对冲订单

// Cleanup 清理资源
func (s *GridStrategy) Cleanup(ctx context.Context) error {
	log.Infof("网格策略: 开始清理资源...")
	// 策略已改为单线程事件循环，清理无需使用“带超时抢锁”的并发技巧
	s.mu.Lock()
	defer s.mu.Unlock()

	// 注意：智能对冲检查在每次价格变化时实时触发，无需清理定时器

	// 重构后：activeOrders 已移除，现在由 OrderEngine 管理
	s.activePosition = nil
	s.roundsThisPeriod = 0

	log.Infof("网格策略: 资源清理完成")
	return nil
}

// Subscribe 订阅会话事件（BBGO 风格）
func (s *GridStrategy) Subscribe(session *bbgo.ExchangeSession) {
	// 检查策略配置是否还在
	if s.config == nil {
		log.Errorf("❌ [周期切换] 错误：策略配置丢失！策略实例可能被重置！")
		return
	}
	if s.grid == nil {
		log.Errorf("❌ [周期切换] 错误：网格配置丢失！策略实例可能被重置！")
		return
	}
	log.Infof("✅ [周期切换] 策略配置正常：网格层级数量=%d, 订单大小=%.2f",
		len(s.grid.Levels), s.config.OrderSize)

	// 确保 map 已初始化（防止 nil map panic）
	s.mu.Lock()
	// 重构后：activeOrders 已移除，现在由 OrderEngine 管理
	if false {
		// 重构后：activeOrders 已移除，现在由 OrderEngine 管理
	}
	if s.pendingHedgeOrders == nil {
		s.pendingHedgeOrders = make(map[string]*domain.Order)
	}
	s.mu.Unlock()

	// 检测周期切换：如果会话的市场 Slug 与当前不同，说明切换到新周期
	market := session.Market()
	if market != nil {
		s.mu.Lock()
		oldSlug := s.currentMarketSlug
		if oldSlug != "" && oldSlug != market.Slug {
			s.mu.Unlock()
			log.Infof("🔄 [周期切换] Subscribe 检测到新周期: %s → %s", oldSlug, market.Slug)
			// 重置所有状态，与上一个周期完全无关
			// 使用 defer recover 确保即使 ResetStateForNewCycle 出错，后续代码也能执行
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Errorf("❌ [周期切换] ResetStateForNewCycle panic: %v", r)
					}
				}()
				s.ResetStateForNewCycle()
			}()
			s.mu.Lock()
		}
		s.currentMarketSlug = market.Slug
		s.currentMarket = market // 保存市场引用，用于订单更新处理
		s.mu.Unlock()
		log.Infof("✅ [周期切换] 周期切换检测完成，准备注册回调")
	} else {
		log.Warnf("⚠️ [周期切换] Session.Market() 返回 nil，无法获取市场信息")
	}

	// 注册订单更新回调（策略自己管理订阅，符合 BBGO 设计理念）
	log.Infof("🔄 [周期切换] 准备注册订单更新回调到 Session (session=%s)", session.Name)
	session.OnOrderUpdate(s)
	log.Infof("✅ [周期切换] 网格策略已订阅订单更新事件")

	// 注册价格变化回调
	log.Infof("🔄 [周期切换] 准备注册价格变化回调到 Session")
	session.OnPriceChanged(s)
	handlerCount := session.PriceChangeHandlerCount()
	log.Infof("✅ [周期切换] 网格策略已订阅价格变化事件 (Session handlers=%d)", handlerCount)
	if handlerCount == 0 {
		log.Warnf("⚠️ [周期切换] 警告：注册后 Session priceChangeHandlers 仍为空！")
	} else {
		log.Infof("✅ [周期切换] Session priceChangeHandlers 已成功注册，数量=%d", handlerCount)
	}

	// 调试：检查 session 的 MarketDataStream 是否已设置
	if session.MarketDataStream == nil {
		log.Warnf("⚠️ [周期切换] Session 的 MarketDataStream 为 nil，价格变化事件可能无法传递")
	} else {
		log.Debugf("✅ [周期切换] Session 的 MarketDataStream 已设置，可以接收价格变化事件")
	}
}

// Run 运行策略（BBGO 风格）
func (s *GridStrategy) Run(ctx context.Context, orderExecutor bbgo.OrderExecutor, session *bbgo.ExchangeSession) error {
	log.Infof("网格策略已启动")
	// 启动策略内部单线程事件循环（只启动一次）
	s.startLoop(ctx)
	return nil
}

// Shutdown 优雅关闭（BBGO 风格）
// 注意：wg 参数由 shutdown.Manager 统一管理，策略的 Shutdown 方法不应该调用 wg.Done()
// 除非策略启动了新的 goroutine 并需要等待它们完成
func (s *GridStrategy) Shutdown(ctx context.Context, wg *sync.WaitGroup) {
	log.Infof("网格策略: 开始优雅关闭...")

	// 停止内部事件循环（不关闭 channel，避免并发发送 panic）
	if s.loopCancel != nil {
		s.loopCancel()
	}

	// 清理资源
	if err := s.Cleanup(ctx); err != nil {
		log.Errorf("网格策略清理失败: %v", err)
	}

	log.Infof("网格策略: 优雅关闭完成")
}
