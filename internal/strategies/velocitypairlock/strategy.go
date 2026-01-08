package velocitypairlock

import (
	"context"
	"fmt"
	"math"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/common"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/orderutil"
	"github.com/betbot/gobet/internal/ports"
	"github.com/betbot/gobet/internal/services"
	gcfg "github.com/betbot/gobet/pkg/config"
	"github.com/betbot/gobet/pkg/bbgo"
)

func init() {
	bbgo.RegisterStrategy("velocitypairlock", &Strategy{})
}

// Strategy：BTC 15m Up/Down 速度触发对冲策略（双向限价 + 自动 merge 释放资金）。
//
// 设计原则：
// - 事件驱动：只在价格事件到来时做轻量计算；下单/合并放入 goroutine，避免阻塞行情分发
// - 单对单：同一时刻最多允许一对（UP+DOWN）在途，资金有限时更安全、更可控
// - 可维护：信号/定价/状态机/合并逻辑独立文件，便于后续扩展（盘口质量、止盈止损、重下/FAK 等）
type Strategy struct {
	// ===== 注入（由 Trader 注入）=====
	TradingService *services.TradingService `json:"-" yaml:"-"`

	// ===== 配置（由 exchangeStrategies 注入到 struct）=====
	Config `json:",inline" yaml:",inline"`

	// ===== 运行期 =====
	orderExecutor bbgo.OrderExecutor
	log          *logrus.Entry

	st state

	// 仅用于 Run 启动确认日志的 once（无锁）
	started atomic.Bool
}

func (s *Strategy) ID() string { return "velocitypairlock" }

// Name 兼容旧接口（如果有旧注册表使用）
func (s *Strategy) Name() string { return s.ID() }

func (s *Strategy) Defaults() error {
	s.Config.Defaults()
	if s.log == nil {
		s.log = logrus.WithField("strategy", s.ID())
	}
	s.st.cfg = s.Config
	if s.st.upVel == nil {
		s.st.upVel = NewVelocityTracker(s.Config.WindowSeconds)
	}
	if s.st.downVel == nil {
		s.st.downVel = NewVelocityTracker(s.Config.WindowSeconds)
	}
	return nil
}

func (s *Strategy) Validate() error {
	s.Config.Defaults()
	return s.Config.Validate()
}

// Subscribe 注册回调（价格事件 + 订单更新）。
func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
	if s.log == nil {
		s.log = logrus.WithField("strategy", s.ID())
	}
	if session == nil {
		return
	}
	session.OnPriceChanged(s)

	// BestBook 透传给 TradingService（如果上层尚未注入）
	if s.TradingService != nil && session.BestBook() != nil {
		s.TradingService.SetBestBook(session.BestBook())
	}

	// 订单更新：优先注册到 TradingService（OrderEngine 会统一回调），并兼容注册到 UserWebSocket（如果存在）
	if s.TradingService != nil {
		s.TradingService.OnOrderUpdate(s)
	}
	if session.UserDataStream != nil {
		session.UserDataStream.OnOrderUpdate(s)
	}
}

// OnCycle 在周期切换时重置状态（避免跨周期污染）。
func (s *Strategy) OnCycle(ctx context.Context, oldMarket *domain.Market, newMarket *domain.Market) {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()

	// 停止盯盘协程
	s.stopMonitorLocked()
	// 停止收敛 sweeper（随后会在 Run 中/新周期再启动）
	s.stopSweeperLocked()

	if s.st.upVel != nil {
		s.st.upVel.Reset()
	}
	if s.st.downVel != nil {
		s.st.downVel.Reset()
	}
	
	// 如果有旧周期，启动 goroutine 合并上一周期的持仓
	if oldMarket != nil && s.st.cfg.AutoMerge.Enabled && s.TradingService != nil {
		cfg := s.st.cfg.AutoMerge
		tradingService := s.TradingService
		log := s.log
		
		// 在独立的 goroutine 中运行，不阻塞周期切换
		go func() {
			s.mergePreviousCyclePositions(ctx, oldMarket, cfg, tradingService, log)
		}()
	}
	
	s.st.rt.market = newMarket
	s.st.rt.tradesThisCycle = 0
	s.resetPairLocked("cycle_switch")
	// 给一点保护：刚切换时盘口/WS 可能还在同步
	s.st.rt.cooldownUntil = time.Now().Add(800 * time.Millisecond)
}

func (s *Strategy) Run(ctx context.Context, orderExecutor bbgo.OrderExecutor, session *bbgo.ExchangeSession) error {
	s.orderExecutor = orderExecutor
	if s.log == nil {
		s.log = logrus.WithField("strategy", s.ID())
	}
	if !s.started.Swap(true) {
		s.log.Infof("✅ 策略启动：%s enabled=%v", s.ID(), s.Config.Enabled)
	}

	// 初始 market（若 session 已就绪）
	if session != nil {
		s.st.mu.Lock()
		s.st.rt.market = session.Market()
		s.st.mu.Unlock()
	}

	// 启动后台收敛 sweeper（防止挂单堆积占用资金）
	s.startSweeperIfNeeded()
	
	// 启动 autoMerge 定期轮询（检查持仓并触发 merge）
	s.startAutoMergePollerIfNeeded()

	<-ctx.Done()
	
	// 清理后台 goroutine（确保程序可以正常退出）
	s.st.mu.Lock()
	s.stopSweeperLocked()
	s.stopMonitorLocked()
	s.stopAutoMergePollerLocked()
	s.st.mu.Unlock()
	
	return ctx.Err()
}

// OnOrderUpdate implements ports.OrderUpdateHandler.
func (s *Strategy) OnOrderUpdate(ctx context.Context, order *domain.Order) error {
	_ = ctx
	if order == nil {
		return nil
	}
	if !s.Config.Enabled {
		return nil
	}

	s.st.mu.Lock()
	needStartMonitor := false

	// 只关心当前一对相关的订单
	if s.st.rt.phase != phaseOpen &&
		s.st.rt.phase != phasePrimaryOpen &&
		s.st.rt.phase != phaseHedgeOpen &&
		s.st.rt.phase != phaseFilled &&
		s.st.rt.phase != phaseMerging {
		s.st.mu.Unlock()
		return nil
	}
	if order.OrderID == "" {
		s.st.mu.Unlock()
		return nil
	}

	// 顺序模式：主 leg 成交 -> 下对冲 leg
	if s.st.rt.phase == phasePrimaryOpen && s.st.rt.primaryOrderID != "" {
		// 方案1: 严格匹配 OrderID（保持现有逻辑）
		if order.OrderID == s.st.rt.primaryOrderID {
			if order.Status == domain.OrderStatusFilled {
				// 防止重复处理：如果已经标记为成交，跳过
				if s.st.rt.primaryFilled {
					s.st.mu.Unlock()
					return nil
				}
				s.st.rt.primaryFilled = true
				s.st.rt.primaryFillCents = order.Price.ToCents()
				s.st.rt.primaryFillSize = order.ExecutedSize()
				s.log.Infof("✅ 主 leg 成交：orderID=%s token=%s price=%dc size=%.2f", order.OrderID, s.st.rt.primaryToken, s.st.rt.primaryFillCents, s.st.rt.primaryFillSize)
				// 下对冲单放到 goroutine（避免阻塞 WS）
				market := s.st.rt.market
				hedgeToken := s.st.rt.hedgeToken
				hedgeCents := s.st.rt.hedgeTargetCents
				size := s.st.rt.primaryFillSize
				s.st.rt.phase = phasePlacing
				s.st.mu.Unlock()
				go s.placeHedgeAfterPrimaryFilled(market, hedgeToken, hedgeCents, size)
				return nil
			}
			if order.Status == domain.OrderStatusCanceled || order.Status == domain.OrderStatusFailed {
				// 检查订单是否已经成交（可能状态更新顺序不对，先收到 failed 后收到 filled）
				// 如果 filledSize > 0，说明订单实际上已经成交，应该优先处理成交逻辑
				if order.ExecutedSize() > 0 {
					s.log.Warnf("⚠️ 主 leg 收到 %s 状态但已成交（filledSize=%.2f），按成交处理：orderID=%s", 
						order.Status, order.ExecutedSize(), order.OrderID)
					// 按成交处理
					if !s.st.rt.primaryFilled {
						s.st.rt.primaryFilled = true
						s.st.rt.primaryFillCents = order.Price.ToCents()
						s.st.rt.primaryFillSize = order.ExecutedSize()
						s.log.Infof("✅ 主 leg 成交：orderID=%s token=%s price=%dc size=%.2f", 
							order.OrderID, s.st.rt.primaryToken, s.st.rt.primaryFillCents, s.st.rt.primaryFillSize)
						// 下对冲单放到 goroutine（避免阻塞 WS）
						market := s.st.rt.market
						hedgeToken := s.st.rt.hedgeToken
						hedgeCents := s.st.rt.hedgeTargetCents
						size := s.st.rt.primaryFillSize
						s.st.rt.phase = phasePlacing
						s.st.mu.Unlock()
						go s.placeHedgeAfterPrimaryFilled(market, hedgeToken, hedgeCents, size)
						return nil
					}
					s.st.mu.Unlock()
					return nil
				}
				// 订单确实失败且未成交，重置
				s.log.Warnf("⚠️ 主 leg 进入终态但未成交：orderID=%s status=%s，重置本对", order.OrderID, order.Status)
				s.resetPairLocked("primary_terminal")
				s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
				s.st.mu.Unlock()
				return nil
			}
			s.st.mu.Unlock()
			return nil
		}
		
		// 方案2: 如果 OrderID 不匹配，尝试通过属性匹配（处理 trade 消息中 orderID 不同的情况）
		if order.Status == domain.OrderStatusFilled && s.isSamePrimaryOrder(order) {
			// 防止重复处理：如果已经标记为成交，跳过
			if s.st.rt.primaryFilled {
				s.st.mu.Unlock()
				return nil
			}
			// 更新 primaryOrderID 为实际成交的 orderID
			s.st.rt.primaryOrderID = order.OrderID
			s.st.rt.primaryFilled = true
			s.st.rt.primaryFillCents = order.Price.ToCents()
			s.st.rt.primaryFillSize = order.ExecutedSize()
			s.log.Infof("✅ 主 leg 成交（通过属性匹配）: orderID=%s token=%s price=%dc size=%.2f (原始orderID=%s)", 
				order.OrderID, s.st.rt.primaryToken, s.st.rt.primaryFillCents, s.st.rt.primaryFillSize, s.st.rt.primaryOrderID)
			// 下对冲单放到 goroutine（避免阻塞 WS）
			market := s.st.rt.market
			hedgeToken := s.st.rt.hedgeToken
			hedgeCents := s.st.rt.hedgeTargetCents
			size := s.st.rt.primaryFillSize
			s.st.rt.phase = phasePlacing
			s.st.mu.Unlock()
			go s.placeHedgeAfterPrimaryFilled(market, hedgeToken, hedgeCents, size)
			return nil
		}
	}

	// 顺序模式：对冲 leg 状态
	if s.st.rt.phase == phaseHedgeOpen && s.st.rt.hedgeOrderID != "" {
		// 方案1: 严格匹配 OrderID（保持现有逻辑）
		if order.OrderID == s.st.rt.hedgeOrderID {
			if order.Status == domain.OrderStatusFilled {
				// 防止重复处理：如果已经标记为成交，跳过
				if s.st.rt.hedgeFilled {
					s.st.mu.Unlock()
					return nil
				}
				s.st.rt.hedgeFilled = true
				s.log.Infof("✅ 对冲 leg 成交：orderID=%s token=%s", order.OrderID, s.st.rt.hedgeToken)
				s.stopMonitorLocked()
				s.st.rt.phase = phaseFilled
				s.triggerAutoMergeLocked()
				s.st.mu.Unlock()
				return nil
			}
			if order.Status == domain.OrderStatusCanceled || order.Status == domain.OrderStatusFailed {
				s.log.Warnf("⚠️ 对冲 leg 进入终态但未成交：orderID=%s status=%s，重置本对", order.OrderID, order.Status)
				s.stopMonitorLocked()
				s.resetPairLocked("hedge_terminal")
				s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
				s.st.mu.Unlock()
				return nil
			}
			s.st.mu.Unlock()
			return nil
		}
		
		// 方案2: 如果 OrderID 不匹配，尝试通过属性匹配（处理 trade 消息中 orderID 不同的情况）
		if order.Status == domain.OrderStatusFilled && s.isSameHedgeOrder(order) {
			// 防止重复处理：如果已经标记为成交，跳过
			if s.st.rt.hedgeFilled {
				s.st.mu.Unlock()
				return nil
			}
			// 更新 hedgeOrderID 为实际成交的 orderID
			s.st.rt.hedgeOrderID = order.OrderID
			s.st.rt.hedgeFilled = true
			s.log.Infof("✅ 对冲 leg 成交（通过属性匹配）: orderID=%s token=%s (原始orderID=%s)", 
				order.OrderID, s.st.rt.hedgeToken, s.st.rt.hedgeOrderID)
			s.stopMonitorLocked()
			s.st.rt.phase = phaseFilled
			s.triggerAutoMergeLocked()
			s.st.mu.Unlock()
			return nil
		}
	}

	// 并发模式：两边订单状态（维持原逻辑）
	updated := false
	if s.st.rt.upOrderID != "" && order.OrderID == s.st.rt.upOrderID {
		if order.Status == domain.OrderStatusFilled {
			s.st.rt.upFilled = true
			updated = true
		} else if order.Status == domain.OrderStatusCanceled || order.Status == domain.OrderStatusFailed {
			s.log.Warnf("⚠️ UP 订单进入终态但未成交：orderID=%s status=%s，重置本对", order.OrderID, order.Status)
			s.resetPairLocked("up_terminal")
			s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
			s.st.mu.Unlock()
			return nil
		}
	}
	if s.st.rt.downOrderID != "" && order.OrderID == s.st.rt.downOrderID {
		if order.Status == domain.OrderStatusFilled {
			s.st.rt.downFilled = true
			updated = true
		} else if order.Status == domain.OrderStatusCanceled || order.Status == domain.OrderStatusFailed {
			s.log.Warnf("⚠️ DOWN 订单进入终态但未成交：orderID=%s status=%s，重置本对", order.OrderID, order.Status)
			s.resetPairLocked("down_terminal")
			s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
			s.st.mu.Unlock()
			return nil
		}
	}

	if updated && s.st.rt.upFilled && s.st.rt.downFilled {
		if s.st.rt.phase != phaseMerging {
			s.st.rt.phase = phaseFilled
			s.stopMonitorLocked()
			s.triggerAutoMergeLocked()
		}
		s.st.mu.Unlock()
		return nil
	}

	// 并发模式：只成交了一边，另一边未成交 -> 进入 hedge_open 并启动盯盘锁损
	if updated && s.st.rt.phase == phaseOpen && s.st.cfg.PriceStopEnabled {
		oneFilled := (s.st.rt.upFilled && !s.st.rt.downFilled) || (!s.st.rt.upFilled && s.st.rt.downFilled)
		if oneFilled && !s.st.rt.monitorRunning {
			// 注意：这里的 order 可能是 UP 或 DOWN 的更新；只在“刚刚收到 filled 的那条”更新 primaryFill*
			if order.Status == domain.OrderStatusFilled {
				s.st.rt.primaryFillCents = order.Price.ToCents()
				s.st.rt.primaryFillSize = order.ExecutedSize()
			}
			// 进入 hedge_open（盯盘目标：未成交的那条订单）
			s.st.rt.phase = phaseHedgeOpen
			s.st.rt.stopLevel = stopNone
			needStartMonitor = true
		}
	}

	s.st.mu.Unlock()
	if needStartMonitor {
		s.startMonitorIfNeeded()
	}
	return nil
}

func clampCents(v int, min int, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func (s *Strategy) makeBuyOrderForToken(market *domain.Market, token domain.TokenType, targetCents int, bestAskCents int, style string, size float64, bypassRiskOff bool) (domain.Order, error) {
	if market == nil {
		return domain.Order{}, fmt.Errorf("market is nil")
	}
	if token != domain.TokenTypeUp && token != domain.TokenTypeDown {
		return domain.Order{}, fmt.Errorf("invalid token type: %s", token)
	}
	assetID := market.NoAssetID
	if token == domain.TokenTypeUp {
		assetID = market.YesAssetID
	}
	orderType := types.OrderTypeGTC
	priceCents := targetCents
	if style == "taker" {
		// FAK 吃单：bestAsk + offset（buy）
		orderType = types.OrderTypeFAK
		priceCents = clampCents(bestAskCents+s.st.cfg.TakerOffsetCents, 1, 99)
	}
	return domain.Order{
		MarketSlug:    market.Slug,
		AssetID:       assetID,
		Side:          types.SideBuy,
		Price:         priceFromCents(priceCents),
		Size:          size,
		TokenType:     token,
		IsEntryOrder:  true,
		Status:        domain.OrderStatusPending,
		CreatedAt:     time.Now(),
		OrderType:     orderType,
		BypassRiskOff: bypassRiskOff,
	}, nil
}

func (s *Strategy) wsConfirmWait() time.Duration {
	sec := s.st.cfg.WsFillConfirmTimeoutSeconds
	if sec <= 0 {
		return 0
	}
	return time.Duration(sec) * time.Second
}

func (s *Strategy) cancelIfNotFilledAfterConfirm() bool {
	if s.st.cfg.CancelIfNotFilledAfterConfirm == nil {
		return true
	}
	return *s.st.cfg.CancelIfNotFilledAfterConfirm
}

func (s *Strategy) enforceOrderConvergence() bool {
	if s.st.cfg.EnforceOrderConvergence == nil {
		return true
	}
	return *s.st.cfg.EnforceOrderConvergence
}

func (s *Strategy) syncOrderStatusBestEffort(orderID string) {
	if s.TradingService == nil || orderID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.TradingService.SyncOrderStatus(ctx, orderID)
	// 给 OrderEngine/回调一个短暂窗口（避免立刻读到旧状态）
	time.Sleep(120 * time.Millisecond)
}

// cancelOrderResult 撤单结果
type cancelOrderResult struct {
	Canceled bool // 是否成功撤单
	Filled   bool // 订单是否已成交（撤单时发现订单已成交）
}

func (s *Strategy) cancelOrderAndConfirmClosed(orderID string) cancelOrderResult {
	result := cancelOrderResult{}
	if s.TradingService == nil || orderID == "" {
		return result
	}
	if s.st.cfg.DecisionOnly {
		s.log.Warnf("🧪 decisionOnly：跳过撤单+确认：orderID=%s", orderID)
		return result
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = s.TradingService.CancelOrder(ctx, orderID)
	cancel()

	// API 兜底确认：轮询 SyncOrderStatus + 本地状态直到不再 open/partial/pending（或超时）
	timeout := time.Duration(s.st.cfg.CancelConfirmTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 6 * time.Second
	}
	interval := time.Duration(s.st.cfg.CancelConfirmPollIntervalMs) * time.Millisecond
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		s.syncOrderStatusBestEffort(orderID)
		if s.TradingService != nil {
			if o, ok := s.TradingService.GetOrder(orderID); ok && o != nil {
				switch o.Status {
				case domain.OrderStatusFilled:
					result.Filled = true
					return result
				case domain.OrderStatusCanceled, domain.OrderStatusFailed:
					result.Canceled = true
					return result
				}
			}
		}
		time.Sleep(interval)
	}
	// 超时后，再次检查一次订单状态
	if s.TradingService != nil {
		if o, ok := s.TradingService.GetOrder(orderID); ok && o != nil {
			switch o.Status {
			case domain.OrderStatusFilled:
				result.Filled = true
			case domain.OrderStatusCanceled, domain.OrderStatusFailed:
				result.Canceled = true
			}
		}
	}
	return result
}

func (s *Strategy) isOrderInCurrentMarket(order *domain.Order, market *domain.Market) bool {
	if order == nil || market == nil {
		return false
	}
	// 以 assetID 为最可靠的隔离键
	if order.AssetID != "" && (order.AssetID == market.YesAssetID || order.AssetID == market.NoAssetID) {
		return true
	}
	// 兜底用 marketSlug
	if order.MarketSlug != "" && order.MarketSlug == market.Slug {
		return true
	}
	return false
}

func (s *Strategy) countOpenOrdersInMarket(market *domain.Market) int {
	if s.TradingService == nil || market == nil {
		return 0
	}
	orders := s.TradingService.GetActiveOrders()
	n := 0
	for _, o := range orders {
		if s.isOrderInCurrentMarket(o, market) {
			n++
		}
	}
	return n
}

func (s *Strategy) snapshotAllowedOrderIDsLocked() map[string]bool {
	allowed := make(map[string]bool, 4)
	if s.st.rt.upOrderID != "" {
		allowed[s.st.rt.upOrderID] = true
	}
	if s.st.rt.downOrderID != "" {
		allowed[s.st.rt.downOrderID] = true
	}
	if s.st.rt.primaryOrderID != "" {
		allowed[s.st.rt.primaryOrderID] = true
	}
	if s.st.rt.hedgeOrderID != "" {
		allowed[s.st.rt.hedgeOrderID] = true
	}
	return allowed
}

func (s *Strategy) sweepOnce() {
	if !s.enforceOrderConvergence() || s.TradingService == nil {
		return
	}
	s.st.mu.Lock()
	market := s.st.rt.market
	allowed := s.snapshotAllowedOrderIDsLocked()
	phase := s.st.rt.phase
	primaryFilled := s.st.rt.primaryFilled
	hedgeFilled := s.st.rt.hedgeFilled
	upFilled := s.st.rt.upFilled
	downFilled := s.st.rt.downFilled
	s.st.mu.Unlock()
	if market == nil {
		return
	}
	
	// 持仓检测兜底机制：如果订单都已成交但策略状态未更新，通过持仓检测触发 merge
	if (phase == phaseHedgeOpen || phase == phasePrimaryOpen || phase == phaseOpen) && 
	   s.st.cfg.AutoMerge.Enabled {
		s.checkPositionsAndTriggerMergeIfNeeded(market, phase, primaryFilled, hedgeFilled, upFilled, downFilled)
	}
	
	// 订单收敛扫单
	orders := s.TradingService.GetActiveOrders()
	for _, o := range orders {
		if o == nil || o.OrderID == "" {
			continue
		}
		if !s.isOrderInCurrentMarket(o, market) {
			continue
		}
		if allowed[o.OrderID] {
			continue
		}
		if s.st.cfg.DecisionOnly {
			s.log.Warnf("🧪 decisionOnly：收敛扫单发现非当前 pair 订单（不撤单）：orderID=%s status=%s", o.OrderID, o.Status)
			continue
		}
		s.log.Warnf("🧹 收敛扫单：发现非当前 pair 订单，撤单：orderID=%s status=%s", o.OrderID, o.Status)
		s.cancelOrderAndConfirmClosed(o.OrderID)
	}
}

func (s *Strategy) startSweeperIfNeeded() {
	if !s.enforceOrderConvergence() {
		return
	}
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	if s.st.rt.sweeperRunning {
		return
	}
	interval := time.Duration(s.st.cfg.ConvergeIntervalMs) * time.Millisecond
	if interval <= 0 {
		interval = 2 * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.st.rt.sweeperCancel = cancel
	s.st.rt.sweeperRunning = true
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.sweepOnce()
			}
		}
	}()
}

func (s *Strategy) stopSweeperLocked() {
	if s.st.rt.sweeperCancel != nil {
		s.st.rt.sweeperCancel()
		s.st.rt.sweeperCancel = nil
	}
	s.st.rt.sweeperRunning = false
}

func (s *Strategy) schedulePrimaryConfirm(primaryID string) {
	wait := s.wsConfirmWait()
	if wait <= 0 || primaryID == "" {
		return
	}
	time.AfterFunc(wait, func() {
		// 仍在等主单成交才执行兜底确认
		s.st.mu.Lock()
		if s.st.rt.phase != phasePrimaryOpen || s.st.rt.primaryOrderID != primaryID || s.st.rt.primaryFilled {
			s.st.mu.Unlock()
			return
		}
		s.st.mu.Unlock()

		s.syncOrderStatusBestEffort(primaryID)

		s.st.mu.Lock()
		if s.st.rt.phase != phasePrimaryOpen || s.st.rt.primaryOrderID != primaryID || s.st.rt.primaryFilled {
			s.st.mu.Unlock()
			return
		}
		shouldCancel := s.cancelIfNotFilledAfterConfirm()
		s.st.mu.Unlock()

		if !shouldCancel {
			s.log.Warnf("⏳ 主单 WS 未确认且 API 仍未成交（按配置不撤单）：orderID=%s", primaryID)
			return
		}
		s.cancelOrderAndConfirmClosed(primaryID)

		s.st.mu.Lock()
		if s.st.rt.phase == phasePrimaryOpen && s.st.rt.primaryOrderID == primaryID && !s.st.rt.primaryFilled {
			s.log.Warnf("⏳ 主单 WS 未确认且 API 仍未成交，已撤单并重置：orderID=%s", primaryID)
			s.resetPairLocked("primary_ws_timeout_cancel")
			s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
		}
		s.st.mu.Unlock()
	})
}

func (s *Strategy) scheduleParallelConfirm(upID string, downID string) {
	wait := s.wsConfirmWait()
	if wait <= 0 || upID == "" || downID == "" {
		return
	}
	time.AfterFunc(wait, func() {
		s.st.mu.Lock()
		// 仅当仍处于并发 open 且“完全没收到成交确认”时才兜底（否则交给盯盘/后续流程）
		if s.st.rt.phase != phaseOpen || s.st.rt.upOrderID != upID || s.st.rt.downOrderID != downID || s.st.rt.upFilled || s.st.rt.downFilled {
			s.st.mu.Unlock()
			return
		}
		s.st.mu.Unlock()

		s.syncOrderStatusBestEffort(upID)
		s.syncOrderStatusBestEffort(downID)

		s.st.mu.Lock()
		if s.st.rt.phase != phaseOpen || s.st.rt.upOrderID != upID || s.st.rt.downOrderID != downID || s.st.rt.upFilled || s.st.rt.downFilled {
			s.st.mu.Unlock()
			return
		}
		shouldCancel := s.cancelIfNotFilledAfterConfirm()
		s.st.mu.Unlock()

		if !shouldCancel {
			s.log.Warnf("⏳ 并发下单 WS 未确认且 API 仍未成交（按配置不撤单）：upID=%s downID=%s", upID, downID)
			return
		}

		s.cancelOrderAndConfirmClosed(upID)
		s.cancelOrderAndConfirmClosed(downID)

		s.st.mu.Lock()
		if s.st.rt.phase == phaseOpen && s.st.rt.upOrderID == upID && s.st.rt.downOrderID == downID && !s.st.rt.upFilled && !s.st.rt.downFilled {
			s.log.Warnf("⏳ 并发下单 WS 未确认且 API 仍未成交，已撤单并重置：upID=%s downID=%s", upID, downID)
			s.resetPairLocked("parallel_ws_timeout_cancel")
			s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
		}
		s.st.mu.Unlock()
	})
}

// OnPriceChanged implements stream.PriceChangeHandler.
func (s *Strategy) OnPriceChanged(ctx context.Context, ev *events.PriceChangedEvent) error {
	if !s.Config.Enabled {
		return nil
	}
	if ev == nil {
		return nil
	}
	mkt := ev.Market
	if mkt == nil {
		return nil
	}

	// 热路径：先更新速度 tracker（持锁时间很短）
	var shouldTrigger bool
	var primaryToken domain.TokenType
	now := ev.Timestamp
	if now.IsZero() {
		now = time.Now()
	}
	newCents := ev.NewPrice.ToCents()

	s.st.mu.Lock()
	if s.st.upVel == nil || s.st.downVel == nil {
		s.st.upVel = NewVelocityTracker(s.Config.WindowSeconds)
		s.st.downVel = NewVelocityTracker(s.Config.WindowSeconds)
	}
	s.st.rt.market = mkt

	// 更新对应 token 的速度序列
	switch ev.TokenType {
	case domain.TokenTypeUp:
		s.st.upVel.Add(now, newCents)
	case domain.TokenTypeDown:
		s.st.downVel.Add(now, newCents)
	default:
		s.st.mu.Unlock()
		return nil
	}

	// 基于“速度方向 + 大小”选择主方向
	primaryToken, shouldTrigger = s.pickPrimaryByVelocityLocked()

	// 状态门禁：同一时刻只允许一对在途
	if s.st.rt.phase != phaseIdle {
		s.st.mu.Unlock()
		return nil
	}
	if !s.st.rt.cooldownUntil.IsZero() && time.Now().Before(s.st.rt.cooldownUntil) {
		s.st.mu.Unlock()
		return nil
	}
	if s.st.cfg.MaxTradesPerCycle > 0 && s.st.rt.tradesThisCycle >= s.st.cfg.MaxTradesPerCycle {
		s.st.mu.Unlock()
		return nil
	}
	if s.isInCycleEndProtectionLocked(time.Now()) {
		s.st.mu.Unlock()
		return nil
	}
	if !shouldTrigger {
		s.st.mu.Unlock()
		return nil
	}

	// 标记为 placing（立刻占位，防止并发触发）
	s.st.rt.phase = phasePlacing
	s.st.mu.Unlock()

	// 下单放到 goroutine（避免阻塞行情线程）
	go s.placePairAsync(primaryToken, mkt)
	return nil
}

func (s *Strategy) velocityHitLocked(t *VelocityTracker) (vel float64, ok bool) {
	if t == nil {
		return 0, false
	}
	vel, move, _, ok := t.VelocityCentsPerSec()
	if !ok {
		return 0, false
	}
	if s.st.cfg.MinMoveCents > 0 && int(math.Abs(float64(move))) < s.st.cfg.MinMoveCents {
		return 0, false
	}
	switch s.st.cfg.VelocityDirectionMode {
	case "abs":
		if math.Abs(vel) < s.st.cfg.MinVelocityCentsPerSec {
			return 0, false
		}
		return vel, true
	default: // "positive"
		if vel < s.st.cfg.MinVelocityCentsPerSec {
			return 0, false
		}
		return vel, true
	}
}

// pickPrimaryByVelocityLocked：在持锁状态下选择主方向。
// 规则：
// - positive 模式：只允许 vel >= threshold 的 token 触发
// - abs 模式：允许 |vel| >= threshold 触发（兼容）
// - 当两边都满足时，选择“vel 更大”的一侧作为主 leg（max_velocity）
func (s *Strategy) pickPrimaryByVelocityLocked() (primary domain.TokenType, trigger bool) {
	upVel, upOK := s.velocityHitLocked(s.st.upVel)
	downVel, downOK := s.velocityHitLocked(s.st.downVel)

	if !upOK && !downOK {
		return "", false
	}
	if upOK && !downOK {
		return domain.TokenTypeUp, true
	}
	if downOK && !upOK {
		return domain.TokenTypeDown, true
	}
	// both OK
	switch s.st.cfg.PrimaryPickMode {
	default: // "max_velocity"
		// abs 模式下可能出现负数 vel，这里用“更大”的那个；如果你想 abs 模式也用 |vel| 比较，可再加一个选项
		if upVel >= downVel {
			return domain.TokenTypeUp, true
		}
		return domain.TokenTypeDown, true
	}
}

func (s *Strategy) isInCycleEndProtectionLocked(now time.Time) bool {
	if s.st.cfg.CycleEndProtectionMinutes <= 0 {
		return false
	}
	if s.st.rt.market == nil || s.st.rt.market.Timestamp <= 0 {
		return false
	}

	// 尝试从全局 market spec 读取周期时长；失败则默认 15m
	cycleDur := 15 * time.Minute
	if gc := gcfg.Get(); gc != nil {
		if sp, err := gc.Market.Spec(); err == nil {
			if d := sp.Duration(); d > 0 {
				cycleDur = d
			}
		}
	}

	start := time.Unix(s.st.rt.market.Timestamp, 0)
	end := start.Add(cycleDur)
	protect := time.Duration(s.st.cfg.CycleEndProtectionMinutes) * time.Minute
	return end.Sub(now) <= protect
}

func (s *Strategy) placePairAsync(primaryToken domain.TokenType, market *domain.Market) {
	if market == nil {
		s.st.mu.Lock()
		s.resetPairLocked("nil_market")
		s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
		s.st.mu.Unlock()
		return
	}
	if s.orderExecutor == nil {
		s.st.mu.Lock()
		s.resetPairLocked("nil_order_executor")
		s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
		s.st.mu.Unlock()
		return
	}
	if s.TradingService == nil {
		s.st.mu.Lock()
		s.resetPairLocked("nil_trading_service")
		s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
		s.st.mu.Unlock()
		return
	}

	// open orders 上限门禁：避免“挂一堆单占用资金”
	if s.st.cfg.MaxOpenOrdersInMarket > 0 {
		openN := s.countOpenOrdersInMarket(market)
		if openN > s.st.cfg.MaxOpenOrdersInMarket {
			s.log.Warnf("🛑 当前 market open orders 过多，禁止开新仓：open=%d max=%d market=%s（触发收敛）",
				openN, s.st.cfg.MaxOpenOrdersInMarket, market.Slug)
			// 异步收敛一次
			go s.sweepOnce()
			s.st.mu.Lock()
			s.resetPairLocked("too_many_open_orders")
			s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
			s.st.mu.Unlock()
			return
		}
	}

	// 取两边当前 bestAsk 作为“挂单参考价”（买单）。
	// 注意：这里取 bestAsk 是为了提高成交率；如果你想更偏 maker，可改为 bestBid。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	upAsk, upErr := orderutil.QuoteBuyPrice(ctx, s.TradingService, market.YesAssetID, s.st.cfg.MaxEntryPriceCents)
	downAsk, downErr := orderutil.QuoteBuyPrice(ctx, s.TradingService, market.NoAssetID, s.st.cfg.MaxEntryPriceCents)

	// 选主边：优先用触发侧；若因边界/缺价失败，则回退另一侧
	type sidePlan struct {
		primaryToken domain.TokenType
		primaryCents int
		hedgeCents   int
	}

	plans := make([]sidePlan, 0, 2)
	if primaryToken == domain.TokenTypeUp && upErr == nil {
		if pp, err := PricePairLock(upAsk.ToCents(), s.st.cfg.ProfitCents, s.st.cfg.MinEntryPriceCents, s.st.cfg.MaxEntryPriceCents); err == nil {
			plans = append(plans, sidePlan{primaryToken: domain.TokenTypeUp, primaryCents: pp.PrimaryCents, hedgeCents: pp.HedgeCents})
		}
	}
	if primaryToken == domain.TokenTypeDown && downErr == nil {
		if pp, err := PricePairLock(downAsk.ToCents(), s.st.cfg.ProfitCents, s.st.cfg.MinEntryPriceCents, s.st.cfg.MaxEntryPriceCents); err == nil {
			plans = append(plans, sidePlan{primaryToken: domain.TokenTypeDown, primaryCents: pp.PrimaryCents, hedgeCents: pp.HedgeCents})
		}
	}
	// fallback：另一边
	if primaryToken != domain.TokenTypeUp && upErr == nil {
		if pp, err := PricePairLock(upAsk.ToCents(), s.st.cfg.ProfitCents, s.st.cfg.MinEntryPriceCents, s.st.cfg.MaxEntryPriceCents); err == nil {
			plans = append(plans, sidePlan{primaryToken: domain.TokenTypeUp, primaryCents: pp.PrimaryCents, hedgeCents: pp.HedgeCents})
		}
	}
	if primaryToken != domain.TokenTypeDown && downErr == nil {
		if pp, err := PricePairLock(downAsk.ToCents(), s.st.cfg.ProfitCents, s.st.cfg.MinEntryPriceCents, s.st.cfg.MaxEntryPriceCents); err == nil {
			plans = append(plans, sidePlan{primaryToken: domain.TokenTypeDown, primaryCents: pp.PrimaryCents, hedgeCents: pp.HedgeCents})
		}
	}

	if len(plans) == 0 {
		s.log.Warnf("⏸️ 触发后无法计算可用挂单价格：upErr=%v downErr=%v", upErr, downErr)
		s.st.mu.Lock()
		s.resetPairLocked("no_valid_plan")
		s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
		s.st.mu.Unlock()
		return
	}
	plan := plans[0]

	// 构造订单（两边都是 BUY + GTC）
	upPriceCents := 0
	downPriceCents := 0
	if plan.primaryToken == domain.TokenTypeUp {
		upPriceCents = plan.primaryCents
		downPriceCents = plan.hedgeCents
	} else {
		downPriceCents = plan.primaryCents
		upPriceCents = plan.hedgeCents
	}

	// 顺序模式 gate：只允许主 leg 价格在区间内时走 sequential
	if s.st.cfg.OrderExecutionMode == "sequential" {
		primaryCents := plan.primaryCents
		if primaryCents < s.st.cfg.SequentialPrimaryMinCents || primaryCents > s.st.cfg.SequentialPrimaryMaxCents {
			s.log.Infof("⏸️ sequential gate：主 leg 价格不在区间内，跳过：primary=%dc range=[%d,%d]",
				primaryCents, s.st.cfg.SequentialPrimaryMinCents, s.st.cfg.SequentialPrimaryMaxCents)
			s.st.mu.Lock()
			s.resetPairLocked("sequential_gate")
			s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
			s.st.mu.Unlock()
			return
		}
	}

	// 最小金额检查（不做 size 自动放大，避免破坏“一对一”对冲；用户可自行调大 orderSize）
	if float64(upPriceCents)/100.0*s.st.cfg.OrderSize < s.st.cfg.MinOrderUSDC ||
		float64(downPriceCents)/100.0*s.st.cfg.OrderSize < s.st.cfg.MinOrderUSDC {
		s.log.Warnf("⏸️ 订单金额不足最小要求：orderSize=%.4f up=%dc down=%dc minUSDC=%.2f",
			s.st.cfg.OrderSize, upPriceCents, downPriceCents, s.st.cfg.MinOrderUSDC)
		s.st.mu.Lock()
		s.resetPairLocked("min_order_usdc")
		s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
		s.st.mu.Unlock()
		return
	}

	if s.st.cfg.OrderExecutionMode == "sequential" {
		// 顺序：只下主 leg，等待成交后再下对冲
		primaryToken := plan.primaryToken
		primaryCents := plan.primaryCents
		hedgeCents := plan.hedgeCents

		primaryAsset := market.YesAssetID
		hedgeAsset := market.NoAssetID
		hedgeToken := domain.TokenTypeDown
		if primaryToken == domain.TokenTypeDown {
			primaryAsset = market.NoAssetID
			hedgeAsset = market.YesAssetID
			hedgeToken = domain.TokenTypeUp
		}

		bestAskCents := upAsk.ToCents()
		if primaryToken == domain.TokenTypeDown {
			bestAskCents = downAsk.ToCents()
		}
		primaryOrder, err := s.makeBuyOrderForToken(market, primaryToken, primaryCents, bestAskCents, s.st.cfg.PrimaryOrderStyle, s.st.cfg.OrderSize, false)
		if err != nil {
			s.log.Warnf("❌ sequential 主 leg 构造订单失败：err=%v", err)
			s.st.mu.Lock()
			s.resetPairLocked("sequential_primary_build_failed")
			s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
			s.st.mu.Unlock()
			return
		}
		primaryOrder.AssetID = primaryAsset

		if s.st.cfg.DecisionOnly {
			s.log.Warnf("🧪 decisionOnly：将下主 leg（不真实下单）｜token=%s style=%s priceTarget=%dc bestAsk=%dc size=%.2f",
				primaryToken, s.st.cfg.PrimaryOrderStyle, primaryCents, bestAskCents, s.st.cfg.OrderSize)
			s.st.mu.Lock()
			s.resetPairLocked("decision_only_primary")
			s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
			s.st.mu.Unlock()
			return
		}

		submitCtx, submitCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer submitCancel()
		created, err := s.orderExecutor.SubmitOrders(submitCtx, primaryOrder)
		if err != nil || len(created) == 0 || created[0] == nil || created[0].OrderID == "" {
			s.log.Warnf("❌ sequential 主 leg 下单失败：err=%v primary=%dc", err, primaryCents)
			s.st.mu.Lock()
			s.resetPairLocked("sequential_primary_submit_failed")
			s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
			s.st.mu.Unlock()
			return
		}

		primaryID := created[0].OrderID
		s.st.mu.Lock()
		s.st.rt.market = market
		s.st.rt.primaryToken = primaryToken
		s.st.rt.primaryOrderID = primaryID
		s.st.rt.primaryFilled = false
		s.st.rt.primaryFillCents = 0
		s.st.rt.hedgeToken = hedgeToken
		s.st.rt.hedgeOrderID = ""
		s.st.rt.hedgeFilled = false
		s.st.rt.hedgeTargetCents = hedgeCents
		s.st.rt.stopLevel = stopNone
		s.st.rt.phase = phasePrimaryOpen
		s.st.rt.tradesThisCycle++
		s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
		s.st.mu.Unlock()

		s.log.Infof("✅ sequential：主 leg 已下单｜token=%s price=%dc orderID=%s hedgeTarget=%dc profit=%dc size=%.2f",
			primaryToken, primaryCents, primaryID, hedgeCents, s.st.cfg.ProfitCents, s.st.cfg.OrderSize)

		// WS -> API 兜底成交确认（5 秒默认）
		s.schedulePrimaryConfirm(primaryID)

		// 额外硬超时（避免极端情况下长时间卡住）
		hard := time.Duration(s.st.cfg.SequentialPrimaryMaxWaitMs) * time.Millisecond
		if hard > 0 {
			time.AfterFunc(hard, func() {
				s.st.mu.Lock()
				if s.st.rt.phase != phasePrimaryOpen || s.st.rt.primaryOrderID != primaryID || s.st.rt.primaryFilled {
					s.st.mu.Unlock()
					return
				}
				s.st.mu.Unlock()
				s.cancelOrderAndConfirmClosed(primaryID)
				s.st.mu.Lock()
				if s.st.rt.phase == phasePrimaryOpen && s.st.rt.primaryOrderID == primaryID && !s.st.rt.primaryFilled {
					s.log.Warnf("⏱️ 主单硬超时，已撤单并重置：orderID=%s", primaryID)
					s.resetPairLocked("primary_hard_timeout_cancel")
					s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
				}
				s.st.mu.Unlock()
			})
		}
		_ = hedgeAsset
		return
	}

	// parallel：并发提交 UP+DOWN（主/对冲可分别配置下单类型）
	upStyle := s.st.cfg.HedgeOrderStyle
	downStyle := s.st.cfg.HedgeOrderStyle
	if plan.primaryToken == domain.TokenTypeUp {
		upStyle = s.st.cfg.PrimaryOrderStyle
	} else {
		downStyle = s.st.cfg.PrimaryOrderStyle
	}
	upOrder, err := s.makeBuyOrderForToken(market, domain.TokenTypeUp, upPriceCents, upAsk.ToCents(), upStyle, s.st.cfg.OrderSize, false)
	if err != nil {
		s.log.Warnf("❌ parallel 构造 UP 订单失败：err=%v", err)
		s.st.mu.Lock()
		s.resetPairLocked("parallel_up_build_failed")
		s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
		s.st.mu.Unlock()
		return
	}
	downOrder, err := s.makeBuyOrderForToken(market, domain.TokenTypeDown, downPriceCents, downAsk.ToCents(), downStyle, s.st.cfg.OrderSize, false)
	if err != nil {
		s.log.Warnf("❌ parallel 构造 DOWN 订单失败：err=%v", err)
		s.st.mu.Lock()
		s.resetPairLocked("parallel_down_build_failed")
		s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
		s.st.mu.Unlock()
		return
	}

	if s.st.cfg.DecisionOnly {
		s.log.Warnf("🧪 decisionOnly：将并发下单（不真实下单）｜primary=%s profit=%dc upStyle=%s upTarget=%dc upBestAsk=%dc downStyle=%s downTarget=%dc downBestAsk=%dc size=%.2f",
			plan.primaryToken, s.st.cfg.ProfitCents,
			upStyle, upPriceCents, upAsk.ToCents(),
			downStyle, downPriceCents, downAsk.ToCents(),
			s.st.cfg.OrderSize,
		)
		s.st.mu.Lock()
		s.resetPairLocked("decision_only_parallel")
		s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
		s.st.mu.Unlock()
		return
	}

	submitCtx, submitCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer submitCancel()

	created, err := s.orderExecutor.SubmitOrders(submitCtx, upOrder, downOrder)
	if err != nil {
		// 失败回滚：尽量撤掉已创建的订单，避免单边裸奔
		if len(created) > 0 {
			_ = s.orderExecutor.CancelOrders(context.Background(), created...)
		}
		s.log.Warnf("❌ 双边挂单失败：err=%v (up=%dc down=%dc profit=%dc)", err, upPriceCents, downPriceCents, s.st.cfg.ProfitCents)
		s.st.mu.Lock()
		s.resetPairLocked("submit_failed")
		s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
		s.st.mu.Unlock()
		return
	}

	var upID, downID string
	for _, o := range created {
		if o == nil {
			continue
		}
		if o.AssetID == market.YesAssetID {
			upID = o.OrderID
		} else if o.AssetID == market.NoAssetID {
			downID = o.OrderID
		}
	}
	if upID == "" || downID == "" {
		// 极端情况：创建成功但回包异常，直接重置并进入冷却
		s.log.Warnf("⚠️ 双边挂单回包缺少 orderID：upID=%q downID=%q，进入冷却", upID, downID)
		s.st.mu.Lock()
		s.resetPairLocked("missing_order_id")
		s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
		s.st.mu.Unlock()
		return
	}

	s.st.mu.Lock()
	s.st.rt.market = market
	s.st.rt.upOrderID = upID
	s.st.rt.downOrderID = downID
	s.st.rt.upFilled = false
	s.st.rt.downFilled = false
	// 记录本次 primary/hedge（用于并发模式下一边成交后的盯盘锁损）
	s.st.rt.primaryToken = plan.primaryToken
	if plan.primaryToken == domain.TokenTypeUp {
		s.st.rt.primaryOrderID = upID
		s.st.rt.hedgeToken = domain.TokenTypeDown
		s.st.rt.hedgeOrderID = downID
	} else {
		s.st.rt.primaryOrderID = downID
		s.st.rt.hedgeToken = domain.TokenTypeUp
		s.st.rt.hedgeOrderID = upID
	}
	s.st.rt.phase = phaseOpen
	s.st.rt.tradesThisCycle++
	s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
	s.st.mu.Unlock()

	// WS -> API 兜底确认（避免 WS 延迟导致“看起来没成交”）
	s.scheduleParallelConfirm(upID, downID)

	s.log.Infof("✅ 速度触发：双边挂单已创建｜UP=%dc(%s) DOWN=%dc(%s) profit=%dc size=%.2f",
		upPriceCents, upID, downPriceCents, downID, s.st.cfg.ProfitCents, s.st.cfg.OrderSize)
}

func (s *Strategy) placeHedgeAfterPrimaryFilled(market *domain.Market, hedgeToken domain.TokenType, hedgeCents int, size float64) {
	if market == nil || s.TradingService == nil || s.orderExecutor == nil {
		s.st.mu.Lock()
		s.resetPairLocked("hedge_missing_deps")
		s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
		s.st.mu.Unlock()
		return
	}
	
	// 状态检查：防止重复下单
	// 如果状态已经不是 phasePlacing 或 phasePrimaryOpen，说明已经有对冲单在途或已成交，不需要再下
	s.st.mu.Lock()
	if s.st.rt.phase != phasePlacing && s.st.rt.phase != phasePrimaryOpen {
		s.st.mu.Unlock()
		s.log.Warnf("⚠️ 跳过对冲单下单：状态已变化 phase=%s（可能已有对冲单在途或已成交）", s.st.rt.phase)
		return
	}
	// 如果已经有对冲单在途，不需要再下
	if s.st.rt.hedgeOrderID != "" {
		s.st.mu.Unlock()
		s.log.Warnf("⚠️ 跳过对冲单下单：已有对冲单在途 hedgeOrderID=%s", s.st.rt.hedgeOrderID)
		return
	}
	// 如果主 leg 未成交，不应该下对冲单
	if !s.st.rt.primaryFilled {
		s.st.mu.Unlock()
		s.log.Warnf("⚠️ 跳过对冲单下单：主 leg 未成交 primaryFilled=false")
		return
	}
	s.st.mu.Unlock()
	assetID := market.NoAssetID
	if hedgeToken == domain.TokenTypeUp {
		assetID = market.YesAssetID
	}

	// 对冲单下单方式（limit/taker）
	bestAskCents := 0
	if s.st.cfg.HedgeOrderStyle == "taker" {
		cctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		p, err := orderutil.QuoteBuyPrice(cctx, s.TradingService, assetID, 0)
		if err != nil {
			s.log.Warnf("❌ 对冲单吃单模式获取 bestAsk 失败：err=%v", err)
			s.st.mu.Lock()
			s.resetPairLocked("hedge_taker_quote_failed")
			s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
			s.st.mu.Unlock()
			return
		}
		bestAskCents = p.ToCents()
	}
	hedgeOrder, err := s.makeBuyOrderForToken(market, hedgeToken, hedgeCents, bestAskCents, s.st.cfg.HedgeOrderStyle, size, true)
	if err != nil {
		s.log.Warnf("❌ 对冲单构造失败：err=%v", err)
		s.st.mu.Lock()
		s.resetPairLocked("hedge_build_failed")
		s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
		s.st.mu.Unlock()
		return
	}
	hedgeOrder.AssetID = assetID

	if s.st.cfg.DecisionOnly {
		s.log.Warnf("🧪 decisionOnly：将下对冲 leg（不真实下单）｜token=%s style=%s hedgeTarget=%dc bestAsk=%dc size=%.2f",
			hedgeToken, s.st.cfg.HedgeOrderStyle, hedgeCents, bestAskCents, size)
		s.st.mu.Lock()
		s.resetPairLocked("decision_only_hedge")
		s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
		s.st.mu.Unlock()
		return
	}

	submitCtx, submitCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer submitCancel()
	created, err := s.orderExecutor.SubmitOrders(submitCtx, hedgeOrder)
	if err != nil || len(created) == 0 || created[0] == nil || created[0].OrderID == "" {
		s.log.Warnf("❌ 对冲 leg 下单失败：err=%v hedge=%dc token=%s", err, hedgeCents, hedgeToken)
		s.st.mu.Lock()
		s.resetPairLocked("hedge_submit_failed")
		s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
		s.st.mu.Unlock()
		return
	}

	hedgeID := created[0].OrderID
	s.st.mu.Lock()
	s.st.rt.hedgeOrderID = hedgeID
	s.st.rt.hedgeFilled = false
	s.st.rt.phase = phaseHedgeOpen
	s.st.mu.Unlock()

	s.log.Infof("✅ 对冲 leg 已下单｜token=%s price=%dc orderID=%s size=%.2f", hedgeToken, hedgeCents, hedgeID, size)

	// 启动盯盘止损
	if s.st.cfg.PriceStopEnabled {
		s.startMonitorIfNeeded()
	}
}

func (s *Strategy) startMonitorIfNeeded() {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	// 仅在 hedgeOpen 才盯盘
	if s.st.rt.monitorRunning || s.st.rt.phase != phaseHedgeOpen || s.st.rt.hedgeOrderID == "" {
		return
	}
	interval := time.Duration(s.st.cfg.PriceStopCheckIntervalMs) * time.Millisecond
	if interval <= 0 {
		interval = 200 * time.Millisecond
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.st.rt.monitorCancel = cancel
	s.st.rt.monitorRunning = true
	hedgeOrderID := s.st.rt.hedgeOrderID
	s.log.Infof("👀 启动盯盘止损：interval=%s hedgeOrderID=%s", interval, hedgeOrderID)
	go s.monitorLoop(ctx, interval, hedgeOrderID)
}

func (s *Strategy) stopMonitorLocked() {
	if s.st.rt.monitorCancel != nil {
		s.st.rt.monitorCancel()
		s.st.rt.monitorCancel = nil
	}
	s.st.rt.monitorRunning = false
	s.st.rt.stopLevel = stopNone
}

func (s *Strategy) monitorLoop(ctx context.Context, interval time.Duration, hedgeOrderID string) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.checkStopLossOnce(ctx, hedgeOrderID)
		}
	}
}

func (s *Strategy) checkStopLossOnce(ctx context.Context, hedgeOrderID string) {
	_ = ctx
	if s.TradingService == nil {
		return
	}

	s.st.mu.Lock()
	if s.st.rt.phase != phaseHedgeOpen || s.st.rt.hedgeOrderID != hedgeOrderID || s.st.rt.hedgeFilled {
		s.st.mu.Unlock()
		return
	}
	market := s.st.rt.market
	primaryFill := s.st.rt.primaryFillCents
	hedgeToken := s.st.rt.hedgeToken
	soft := s.st.cfg.PriceStopSoftLossCents
	hard := s.st.cfg.PriceStopHardLossCents
	maxLoss := s.st.cfg.MaxAcceptableLossCents
	currentLevel := s.st.rt.stopLevel
	s.st.mu.Unlock()

	if market == nil || primaryFill <= 0 {
		return
	}
	assetID := market.NoAssetID
	if hedgeToken == domain.TokenTypeUp {
		assetID = market.YesAssetID
	}
	// 取当前对冲侧 bestAsk（买单）
	cctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	p, err := orderutil.QuoteBuyPrice(cctx, s.TradingService, assetID, 0)
	if err != nil {
		return
	}
	hedgeAsk := p.ToCents()
	// 预计锁定收益（分）：100 - (primary + hedgeAsk)
	pnl := 100 - (primaryFill + hedgeAsk)

	// pnl 为负：亏损
	if pnl >= 0 {
		return
	}

	// 超过最大可接受亏损：不自动锁损（避免“为了对冲”吃得太贵）
	if -pnl > maxLoss {
		s.log.Warnf("🛑 预计锁损亏损过大，拒绝自动锁损：pnl=%dc maxLoss=%dc primary=%dc hedgeAsk=%dc",
			pnl, maxLoss, primaryFill, hedgeAsk)
		// 风控降频：短时间不再开新仓
		s.TradingService.TriggerRiskOff(5*time.Second, "velocitypairlock_stoploss_too_large")
		return
	}

	// 达到 hard：撤旧对冲单 -> FAK 吃单
	if pnl <= hard && currentLevel != stopHard {
		s.log.Warnf("🔻 触发硬锁损：pnl=%dc (<=%dc) 先撤对冲单再 FAK 锁损", pnl, hard)
		go s.executeStopLoss(hedgeOrderID, hedgeToken, market, hedgeAsk+s.st.cfg.TakerOffsetCents, types.OrderTypeFAK)
		s.st.mu.Lock()
		if s.st.rt.hedgeOrderID == hedgeOrderID && s.st.rt.phase == phaseHedgeOpen {
			s.st.rt.stopLevel = stopHard
		}
		s.st.mu.Unlock()
		return
	}

	// 达到 soft：撤旧对冲单 -> GTC@bestAsk（更激进，尽量成交，但不强制）
	if pnl <= soft && currentLevel == stopNone {
		s.log.Warnf("🔸 触发软锁损：pnl=%dc (<=%dc) 先撤对冲单再提价对冲", pnl, soft)
		ot := types.OrderTypeGTC
		price := hedgeAsk
		if s.st.cfg.HedgeOrderStyle == "taker" {
			ot = types.OrderTypeFAK
			price = hedgeAsk + s.st.cfg.TakerOffsetCents
		}
		go s.executeStopLoss(hedgeOrderID, hedgeToken, market, price, ot)
		s.st.mu.Lock()
		if s.st.rt.hedgeOrderID == hedgeOrderID && s.st.rt.phase == phaseHedgeOpen {
			s.st.rt.stopLevel = stopSoft
		}
		s.st.mu.Unlock()
		return
	}
}

func (s *Strategy) executeStopLoss(oldHedgeOrderID string, hedgeToken domain.TokenType, market *domain.Market, newPriceCents int, orderType types.OrderType) {
	if s.TradingService == nil || s.orderExecutor == nil || market == nil {
		return
	}
	if s.st.cfg.DecisionOnly {
		s.log.Warnf("🧪 decisionOnly：将锁损（不真实撤单/下单）｜oldHedge=%s token=%s orderType=%s newPrice=%dc",
			oldHedgeOrderID, hedgeToken, orderType, newPriceCents)
		return
	}
	// 1) 撤掉旧对冲单
	cancelResult := s.cancelOrderAndConfirmClosed(oldHedgeOrderID)
	
	// 如果订单在撤单过程中已成交，不需要下新单
	if cancelResult.Filled {
		s.log.Infof("✅ 止损撤单时发现订单已成交：orderID=%s，无需下新单", oldHedgeOrderID)
		s.st.mu.Lock()
		// 更新状态，确保策略知道订单已成交
		if s.st.rt.phase == phaseHedgeOpen && s.st.rt.hedgeOrderID == oldHedgeOrderID {
			s.st.rt.hedgeFilled = true
			s.st.rt.phase = phaseFilled
			s.stopMonitorLocked()
			s.triggerAutoMergeLocked()
		}
		s.st.mu.Unlock()
		return
	}
	
	// 如果撤单失败且订单未成交，记录警告但继续尝试下新单（可能是网络问题）
	if !cancelResult.Canceled && !cancelResult.Filled {
		s.log.Warnf("⚠️ 止损撤单未确认：orderID=%s（可能仍在挂单中），继续下新单", oldHedgeOrderID)
	}

	// 2) 新建锁损对冲单（更激进）
	s.st.mu.Lock()
	// 若状态已变化（比如已经成交/重置），退出
	if s.st.rt.phase != phaseHedgeOpen || s.st.rt.hedgeOrderID != oldHedgeOrderID || s.st.rt.hedgeFilled {
		s.st.mu.Unlock()
		return
	}
	size := s.st.rt.primaryFillSize
	if size <= 0 {
		size = s.st.cfg.OrderSize
	}
	s.st.mu.Unlock()

	assetID := market.NoAssetID
	if hedgeToken == domain.TokenTypeUp {
		assetID = market.YesAssetID
	}
	newOrder := domain.Order{
		MarketSlug:    market.Slug,
		AssetID:       assetID,
		Side:          types.SideBuy,
		Price:         priceFromCents(clampCents(newPriceCents, 1, 99)),
		Size:          size,
		TokenType:     hedgeToken,
		IsEntryOrder:  true,
		Status:        domain.OrderStatusPending,
		CreatedAt:     time.Now(),
		OrderType:     orderType,
		BypassRiskOff: true,
	}
	submitCtx, submitCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer submitCancel()
	created, err := s.orderExecutor.SubmitOrders(submitCtx, newOrder)
	if err != nil || len(created) == 0 || created[0] == nil || created[0].OrderID == "" {
		s.log.Warnf("❌ 锁损对冲下单失败：err=%v type=%s price=%dc", err, orderType, newPriceCents)
		return
	}
	newID := created[0].OrderID
	s.st.mu.Lock()
	if s.st.rt.phase == phaseHedgeOpen && s.st.rt.hedgeOrderID == oldHedgeOrderID {
		s.st.rt.hedgeOrderID = newID
		s.st.rt.hedgeFilled = false
		// 盯盘协程继续盯（但需要同步 hedgeOrderID）
		s.log.Warnf("✅ 已替换对冲单：old=%s new=%s type=%s price=%dc", oldHedgeOrderID, newID, orderType, newPriceCents)
	}
	s.st.mu.Unlock()
}

func (s *Strategy) triggerAutoMergeLocked() {
	if s.st.cfg.DecisionOnly {
		s.log.Warnf("🧪 decisionOnly：跳过 autoMerge（不真实合并），重置回 idle")
		s.resetPairLocked("decision_only_merge_skip")
		s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
		return
	}
	if !s.st.cfg.AutoMerge.Enabled {
		s.log.Infof("ℹ️ 双边已成交，但 autoMerge 未启用：等待结算（不合并释放资金）")
		s.st.rt.phase = phaseCooldown
		s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
		// 不清空订单（保留用于审计）；但允许继续开单会导致资金不足，所以默认仍走 cooldown
		s.resetPairLocked("filled_no_automerge")
		return
	}
	if s.TradingService == nil || s.st.rt.market == nil {
		s.log.Warnf("⚠️ 无法 autoMerge：TradingService/market 为空")
		s.resetPairLocked("automerge_missing_deps")
		s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
		return
	}

	delay := time.Duration(s.st.cfg.AutoMerge.MergeTriggerDelaySeconds) * time.Second
	if delay < 0 {
		delay = 0
	}
	s.st.rt.phase = phaseMerging

	market := s.st.rt.market
	cfg := s.st.cfg.AutoMerge

	s.log.Infof("🔄 双边已成交：%ds 后触发 merge complete sets（释放资金继续开单）", int(delay.Seconds()))

	time.AfterFunc(delay, func() {
		s.st.rt.autoMergeCtl.MaybeAutoMerge(
			context.Background(),
			s.TradingService,
			market,
			cfg,
			func(format string, args ...any) { s.log.Infof(format, args...) },
			func(status string, amount float64, txHash string, err error) {
				// 回调里只做轻量状态更新，避免阻塞 autoMerge goroutine
				if status == "balance_refreshed" || status == "completed" {
					s.st.mu.Lock()
					defer s.st.mu.Unlock()
					s.log.Infof("✅ merge 完成（资金已刷新）：amount=%.6f tx=%s", amount, txHash)
					s.resetPairLocked("merge_done")
					s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
				}
				if status == "failed" && err != nil {
					s.st.mu.Lock()
					defer s.st.mu.Unlock()
					s.log.Warnf("⚠️ merge 失败：amount=%.6f err=%v", amount, err)
					// 失败也允许继续尝试下一次信号（资金可能仍被占用，取决于实际持仓）
					s.resetPairLocked("merge_failed")
					s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
				}
			},
		)
	})
}

func (s *Strategy) resetPairLocked(reason string) {
	s.stopMonitorLocked()
	s.stopSweeperLocked()
	s.st.rt.phase = phaseIdle
	s.st.rt.upOrderID = ""
	s.st.rt.downOrderID = ""
	s.st.rt.upFilled = false
	s.st.rt.downFilled = false
	s.st.rt.primaryToken = ""
	s.st.rt.primaryOrderID = ""
	s.st.rt.primaryFilled = false
	s.st.rt.primaryFillCents = 0
	s.st.rt.primaryFillSize = 0
	s.st.rt.hedgeToken = ""
	s.st.rt.hedgeOrderID = ""
	s.st.rt.hedgeFilled = false
	s.st.rt.hedgeTargetCents = 0
	s.st.rt.stopLevel = stopNone
	_ = reason
}

func priceFromCents(c int) domain.Price {
	// 1 cent = 100 pips
	return domain.Price{Pips: c * 100}
}

// isSamePrimaryOrder 检查订单是否与主 leg 订单匹配（通过属性而非 OrderID）
// 用于处理 trade 消息中 orderID 与下单时返回的 orderID 不同的情况
func (s *Strategy) isSamePrimaryOrder(order *domain.Order) bool {
	if order == nil || s.st.rt.market == nil {
		return false
	}
	
	// 检查 assetID 和 token type
	expectedAssetID := s.st.rt.market.NoAssetID
	if s.st.rt.primaryToken == domain.TokenTypeUp {
		expectedAssetID = s.st.rt.market.YesAssetID
	}
	if order.AssetID != expectedAssetID {
		return false
	}
	
	// 检查 side (应该是 BUY)
	if order.Side != types.SideBuy {
		return false
	}
	
	// 检查价格（如果有对冲目标价，可以估算主 leg 价格范围）
	if s.st.rt.hedgeTargetCents > 0 {
		// 估算主 leg 价格：100 - hedgeTarget - profitCents
		expectedPriceCents := 100 - s.st.rt.hedgeTargetCents - s.st.cfg.ProfitCents
		actualPriceCents := order.Price.ToCents()
		priceDiff := int(math.Abs(float64(actualPriceCents - expectedPriceCents)))
		// 允许 ±10c 的误差（考虑到实际成交价格可能与目标价略有差异）
		if priceDiff > 10 {
			return false
		}
	}
	
	// 检查时间（订单应该是最近创建的，比如 60 秒内）
	if order.CreatedAt.IsZero() {
		return false
	}
	if time.Since(order.CreatedAt) > 60*time.Second {
		return false
	}
	
	// 检查订单数量（应该在合理范围内，比如 ±20%）
	if s.st.cfg.OrderSize > 0 {
		sizeDiff := math.Abs(order.Size - s.st.cfg.OrderSize)
		if sizeDiff > s.st.cfg.OrderSize*0.2 {
			return false
		}
	}
	
	return true
}

// isSameHedgeOrder 检查订单是否与对冲 leg 订单匹配（通过属性而非 OrderID）
// 用于处理 trade 消息中 orderID 与下单时返回的 orderID 不同的情况
func (s *Strategy) isSameHedgeOrder(order *domain.Order) bool {
	if order == nil || s.st.rt.market == nil {
		return false
	}
	
	// 检查 assetID 和 token type
	expectedAssetID := s.st.rt.market.NoAssetID
	if s.st.rt.hedgeToken == domain.TokenTypeUp {
		expectedAssetID = s.st.rt.market.YesAssetID
	}
	if order.AssetID != expectedAssetID {
		return false
	}
	
	// 检查 side (应该是 BUY)
	if order.Side != types.SideBuy {
		return false
	}
	
	// 检查价格（对冲目标价应该在合理范围内）
	if s.st.rt.hedgeTargetCents > 0 {
		actualPriceCents := order.Price.ToCents()
		priceDiff := int(math.Abs(float64(actualPriceCents - s.st.rt.hedgeTargetCents)))
		// 允许 ±10c 的误差（考虑到实际成交价格可能与目标价略有差异）
		if priceDiff > 10 {
			return false
		}
	}
	
	// 检查时间（订单应该是最近创建的，比如 60 秒内）
	if order.CreatedAt.IsZero() {
		return false
	}
	if time.Since(order.CreatedAt) > 60*time.Second {
		return false
	}
	
	// 检查订单数量（应该在合理范围内，比如 ±20%）
	// 对冲单的数量应该与主 leg 的成交数量匹配
	expectedSize := s.st.cfg.OrderSize
	if s.st.rt.primaryFillSize > 0 {
		expectedSize = s.st.rt.primaryFillSize
	}
	if expectedSize > 0 {
		sizeDiff := math.Abs(order.Size - expectedSize)
		if sizeDiff > expectedSize*0.2 {
			return false
		}
	}
	
	return true
}

// checkPositionsAndTriggerMergeIfNeeded 通过持仓检测兜底机制
// 如果两个订单都已成交（通过持仓判断），但策略状态未更新，则触发 merge
func (s *Strategy) checkPositionsAndTriggerMergeIfNeeded(
	market *domain.Market,
	phase pairPhase,
	primaryFilled, hedgeFilled, upFilled, downFilled bool,
) {
	if s.TradingService == nil || market == nil || !s.st.cfg.AutoMerge.Enabled {
		return
	}
	
	// 顺序模式：检查主 leg 和对冲 leg 是否都已成交
	if phase == phaseHedgeOpen || phase == phasePrimaryOpen {
		// 如果策略状态显示都已成交，不需要检查持仓
		if (phase == phaseHedgeOpen && primaryFilled && hedgeFilled) ||
		   (phase == phasePrimaryOpen && primaryFilled) {
			return
		}
		
		// 检查持仓：如果 UP 和 DOWN 都有持仓，说明两个订单都已成交
		positions := s.TradingService.GetOpenPositionsForMarket(market.Slug)
		var upSize, downSize float64
		for _, p := range positions {
			if p == nil || !p.IsOpen() || p.Size <= 0 {
				continue
			}
			if p.TokenType == domain.TokenTypeUp {
				upSize += p.Size
			} else if p.TokenType == domain.TokenTypeDown {
				downSize += p.Size
			}
		}
		
		// 如果两个仓位都存在且数量匹配（说明两个订单都已成交）
		if upSize > 0 && downSize > 0 {
			// 检查数量是否匹配（允许 ±20% 误差）
			minSize := math.Min(upSize, downSize)
			maxSize := math.Max(upSize, downSize)
			if maxSize > 0 && (maxSize-minSize)/maxSize <= 0.2 {
				s.st.mu.Lock()
				// 再次检查状态（避免并发问题）
				if (s.st.rt.phase == phaseHedgeOpen || s.st.rt.phase == phasePrimaryOpen) &&
				   !s.st.rt.primaryFilled && !s.st.rt.hedgeFilled {
					s.log.Warnf("🔍 [持仓检测] 发现两个仓位都已存在但策略状态未更新：UP=%.2f DOWN=%.2f phase=%s，触发 merge", 
						upSize, downSize, s.st.rt.phase)
					// 更新状态并触发 merge
					s.st.rt.primaryFilled = true
					s.st.rt.hedgeFilled = true
					s.st.rt.phase = phaseFilled
					s.stopMonitorLocked()
					s.triggerAutoMergeLocked()
				}
				s.st.mu.Unlock()
			}
		}
		return
	}
	
	// 并发模式：检查 UP 和 DOWN 是否都已成交
	if phase == phaseOpen {
		// 如果策略状态显示都已成交，不需要检查持仓
		if upFilled && downFilled {
			return
		}
		
		// 检查持仓：如果 UP 和 DOWN 都有持仓，说明两个订单都已成交
		positions := s.TradingService.GetOpenPositionsForMarket(market.Slug)
		var upSize, downSize float64
		for _, p := range positions {
			if p == nil || !p.IsOpen() || p.Size <= 0 {
				continue
			}
			if p.TokenType == domain.TokenTypeUp {
				upSize += p.Size
			} else if p.TokenType == domain.TokenTypeDown {
				downSize += p.Size
			}
		}
		
		// 如果两个仓位都存在且数量匹配（说明两个订单都已成交）
		if upSize > 0 && downSize > 0 {
			// 检查数量是否匹配（允许 ±20% 误差）
			minSize := math.Min(upSize, downSize)
			maxSize := math.Max(upSize, downSize)
			if maxSize > 0 && (maxSize-minSize)/maxSize <= 0.2 {
				s.st.mu.Lock()
				// 再次检查状态（避免并发问题）
				if s.st.rt.phase == phaseOpen && !s.st.rt.upFilled && !s.st.rt.downFilled {
					s.log.Warnf("🔍 [持仓检测] 发现两个仓位都已存在但策略状态未更新：UP=%.2f DOWN=%.2f，触发 merge", 
						upSize, downSize)
					// 更新状态并触发 merge
					s.st.rt.upFilled = true
					s.st.rt.downFilled = true
					s.st.rt.phase = phaseFilled
					s.stopMonitorLocked()
					s.triggerAutoMergeLocked()
				}
				s.st.mu.Unlock()
			}
		}
	}
}

// startAutoMergePollerIfNeeded 启动 autoMerge 定期轮询
// 定期检查持仓，如果发现双向持仓就触发 merge
func (s *Strategy) startAutoMergePollerIfNeeded() {
	if !s.st.cfg.AutoMerge.Enabled {
		return
	}
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	if s.st.rt.mergePollerRunning {
		return
	}
	
	interval := time.Duration(s.st.cfg.AutoMerge.IntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 45 * time.Second // 默认 45 秒
	}
	
	ctx, cancel := context.WithCancel(context.Background())
	s.st.rt.mergePollerCancel = cancel
	s.st.rt.mergePollerRunning = true
	
	s.log.Infof("🔄 启动 autoMerge 定期轮询：interval=%v", interval)
	
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.pollAutoMergeOnce()
			}
		}
	}()
}

// stopAutoMergePollerLocked 停止 autoMerge 定期轮询
func (s *Strategy) stopAutoMergePollerLocked() {
	if s.st.rt.mergePollerCancel != nil {
		s.st.rt.mergePollerCancel()
		s.st.rt.mergePollerCancel = nil
	}
	s.st.rt.mergePollerRunning = false
}

// pollAutoMergeOnce 定期轮询检查持仓并触发 merge
// 只要是在本周期的双向持仓都可以合并
func (s *Strategy) pollAutoMergeOnce() {
	if !s.st.cfg.AutoMerge.Enabled || s.TradingService == nil {
		return
	}
	
	s.st.mu.Lock()
	market := s.st.rt.market
	s.st.mu.Unlock()
	
	if market == nil || !market.IsValid() {
		return
	}
	
	// 检查持仓：获取当前市场的所有开放持仓
	positions := s.TradingService.GetOpenPositionsForMarket(market.Slug)
	if len(positions) == 0 {
		return
	}
	
	var upSize, downSize float64
	for _, p := range positions {
		if p == nil || !p.IsOpen() || p.Size <= 0 {
			continue
		}
		if p.TokenType == domain.TokenTypeUp {
			upSize += p.Size
		} else if p.TokenType == domain.TokenTypeDown {
			downSize += p.Size
		}
	}
	
	// 如果两个仓位都存在，计算可合并的 complete sets
	if upSize > 0 && downSize > 0 {
		complete := math.Min(upSize, downSize)
		
		// 检查是否满足最小合并数量
		if s.st.cfg.AutoMerge.MinCompleteSets > 0 && complete < s.st.cfg.AutoMerge.MinCompleteSets {
			return
		}
		
		// 检查是否正在合并中（避免重复触发）
		s.st.mu.Lock()
		if s.st.rt.phase == phaseMerging {
			s.st.mu.Unlock()
			return
		}
		s.st.mu.Unlock()
		
		// 触发 merge（不更新策略状态，因为这是定期轮询，不是订单成交触发）
		s.log.Infof("🔄 [定期轮询] 发现双向持仓：UP=%.2f DOWN=%.2f complete=%.2f，触发 merge", 
			upSize, downSize, complete)
		
		cfg := s.st.cfg.AutoMerge
		s.st.rt.autoMergeCtl.MaybeAutoMerge(
			context.Background(),
			s.TradingService,
			market,
			cfg,
			func(format string, args ...any) { s.log.Infof(format, args...) },
			func(status string, amount float64, txHash string, err error) {
				// 回调里只做日志记录，不更新策略状态（因为这是定期轮询）
				if status == "balance_refreshed" || status == "completed" {
					s.log.Infof("✅ [定期轮询] merge 完成：amount=%.6f tx=%s", amount, txHash)
				}
				if status == "failed" && err != nil {
					s.log.Warnf("⚠️ [定期轮询] merge 失败：amount=%.6f err=%v", amount, err)
				}
			},
		)
	}
}

// mergePreviousCyclePositions 合并上一周期的持仓
// 在周期切换后，启动独立的 goroutine 来合并上一周期的双向持仓
func (s *Strategy) mergePreviousCyclePositions(
	ctx context.Context,
	oldMarket *domain.Market,
	cfg common.AutoMergeConfig,
	tradingService *services.TradingService,
	log *logrus.Entry,
) {
	if oldMarket == nil || !oldMarket.IsValid() || oldMarket.Slug == "" {
		return
	}
	
	// 等待一小段时间，确保周期切换完成，持仓数据已同步
	time.Sleep(2 * time.Second)
	
	// 检查上一周期的持仓
	positions := tradingService.GetOpenPositionsForMarket(oldMarket.Slug)
	if len(positions) == 0 {
		log.Debugf("🔄 [周期切换] 上一周期 %s 无持仓，跳过 merge", oldMarket.Slug)
		return
	}
	
	var upSize, downSize float64
	for _, p := range positions {
		if p == nil || !p.IsOpen() || p.Size <= 0 {
			continue
		}
		if p.TokenType == domain.TokenTypeUp {
			upSize += p.Size
		} else if p.TokenType == domain.TokenTypeDown {
			downSize += p.Size
		}
	}
	
	// 如果两个仓位都存在，计算可合并的 complete sets
	if upSize > 0 && downSize > 0 {
		complete := math.Min(upSize, downSize)
		
		// 检查是否满足最小合并数量
		if cfg.MinCompleteSets > 0 && complete < cfg.MinCompleteSets {
			log.Infof("🔄 [周期切换] 上一周期 %s 持仓不足：UP=%.2f DOWN=%.2f complete=%.2f < minCompleteSets=%.2f，跳过 merge",
				oldMarket.Slug, upSize, downSize, complete, cfg.MinCompleteSets)
			return
		}
		
		log.Infof("🔄 [周期切换] 发现上一周期 %s 有双向持仓：UP=%.2f DOWN=%.2f complete=%.2f，开始 merge",
			oldMarket.Slug, upSize, downSize, complete)
		
		// 使用独立的 AutoMergeController 实例，避免与新周期的 merge 冲突
		var previousCycleMergeCtl common.AutoMergeController
		
		previousCycleMergeCtl.MaybeAutoMerge(
			ctx,
			tradingService,
			oldMarket,
			cfg,
			func(format string, args ...any) { log.Infof("[上一周期] "+format, args...) },
			func(status string, amount float64, txHash string, err error) {
				// 回调里只做日志记录
				if status == "balance_refreshed" || status == "completed" {
					log.Infof("✅ [周期切换] 上一周期 %s merge 完成：amount=%.6f tx=%s", oldMarket.Slug, amount, txHash)
				}
				if status == "failed" && err != nil {
					log.Warnf("⚠️ [周期切换] 上一周期 %s merge 失败：amount=%.6f err=%v", oldMarket.Slug, amount, err)
				}
			},
		)
	} else {
		log.Debugf("🔄 [周期切换] 上一周期 %s 无双向持仓：UP=%.2f DOWN=%.2f，跳过 merge", oldMarket.Slug, upSize, downSize)
	}
}

// ===== compile-time guard =====
var _ bbgo.SingleExchangeStrategy = (*Strategy)(nil)
var _ bbgo.ExchangeSessionSubscriber = (*Strategy)(nil)
var _ bbgo.StrategyDefaulter = (*Strategy)(nil)
var _ bbgo.StrategyValidator = (*Strategy)(nil)
var _ bbgo.CycleAwareStrategy = (*Strategy)(nil)
var _ ports.OrderUpdateHandler = (*Strategy)(nil)
