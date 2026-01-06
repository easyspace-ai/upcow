package velocityfollow

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/services"
	"github.com/betbot/gobet/internal/strategies/velocityfollow/brain"
	"github.com/betbot/gobet/internal/strategies/velocityfollow/capital"
	"github.com/betbot/gobet/internal/strategies/velocityfollow/dashboard"
	"github.com/betbot/gobet/internal/strategies/velocityfollow/oms"
	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/betbot/gobet/pkg/marketspec"
	"github.com/sirupsen/logrus"
)

var log = logrus.WithField("strategy", ID)

func init() {
	bbgo.RegisterStrategy(ID, &Strategy{})
}

// Strategy VelocityFollow 策略
type Strategy struct {
	TradingService *services.TradingService
	Config         `yaml:",inline" json:",inline"`

	mu sync.RWMutex
	// 避免在周期切换/重复 Subscribe 时重复注册 handler
	orderUpdateOnce sync.Once

	// 三个核心模块
	brain   *brain.Brain
	oms     *oms.OMS
	capital *capital.Capital
	dash    *dashboard.Dashboard

	// Dashboard 更新循环的独立 context（不受周期切换影响）
	dashboardCtx      context.Context
	dashboardCancel   context.CancelFunc
	dashboardLoopOnce sync.Once // 确保 dashboardUpdateLoop 只启动一次

	// Dashboard 退出信号（当原生TUI退出时，取消主程序的 context）
	dashboardExitCtx    context.Context
	dashboardExitCancel context.CancelFunc

	// 周期切换标志，用于防止 dashboardUpdateLoop 在周期切换时立即更新
	cycleSwitching  bool
	cycleSwitchTime time.Time
	cycleSwitchMu   sync.RWMutex
	// 周期切换时的新市场信息（用于在切换窗口内接受新市场的价格事件）
	newMarketSlug string
	newMarketMu   sync.RWMutex

	// 周期状态
	cycleStartTime  time.Time
	lastTriggerTime time.Time
	tradesThisCycle int
}

func (s *Strategy) ID() string {
	return ID
}

func (s *Strategy) Name() string {
	return ID
}

func (s *Strategy) Defaults() error {
	return s.Config.Defaults()
}

func (s *Strategy) Validate() error {
	return s.Config.Validate()
}

// Initialize 初始化策略
func (s *Strategy) Initialize() error {
	if s.TradingService == nil {
		return nil // TradingService 会在后续注入
	}

	// 初始化三个模块
	var err error
	s.brain, err = brain.New(s.TradingService, &s.Config)
	if err != nil {
		return err
	}

	s.oms, err = oms.New(s.TradingService, &s.Config)
	if err != nil {
		return err
	}

	s.capital, err = capital.New(s.TradingService, &s.Config)
	if err != nil {
		return err
	}

	// 设置 OMS 对 Capital 的引用，用于在对冲单完成时触发 merge
	if s.oms != nil && s.capital != nil {
		s.oms.SetCapital(s.capital)
	}

	// 初始化 Dashboard
	if s.Config.DashboardEnabled {
		s.dash = dashboard.New(s.TradingService, s.Config.DashboardUseNativeTUI)
		s.dash.SetEnabled(true)
		// 立即应用日志重定向（在启动前），避免日志打印到终端
		s.dash.ReapplyLogRedirect()
		// 创建独立的 context 用于 Dashboard 更新循环（不受周期切换影响）
		s.dashboardCtx, s.dashboardCancel = context.WithCancel(context.Background())
		// 创建 Dashboard 退出信号 context（当原生TUI退出时，取消主程序）
		s.dashboardExitCtx, s.dashboardExitCancel = context.WithCancel(context.Background())
		if s.Config.DashboardUseNativeTUI {
			log.Infof("✅ [%s] Dashboard UI 已启用（使用原生TUI）", ID)
		} else {
			log.Infof("✅ [%s] Dashboard UI 已启用（使用Bubble Tea）", ID)
		}
	} else {
		// 关键修复：即使 Dashboard 未启用，也要初始化 dashboardExitCtx
		// 否则在 Run 方法中访问 s.dashboardExitCtx.Done() 会导致 nil pointer dereference
		// 创建一个永远不会完成的 context（这样 select 语句不会选中它）
		s.dashboardExitCtx, s.dashboardExitCancel = context.WithCancel(context.Background())
		// 不取消这个 context，让它永远不会完成
		log.Debugf("📊 [%s] Dashboard 未启用，但已初始化 dashboardExitCtx 以避免 nil pointer", ID)
	}

	// 注册订单更新回调
	s.orderUpdateOnce.Do(func() {
		handler := services.OrderUpdateHandlerFunc(s.OnOrderUpdate)
		s.TradingService.OnOrderUpdate(handler)
		log.Infof("✅ [%s] 已注册订单更新回调", ID)
	})

	return nil
}

func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
	session.OnPriceChanged(s)
	log.Infof("✅ [%s] 策略已订阅价格变化事件 (session=%s)", ID, session.Name)

	// 兜底：有些部署/注入顺序下 Initialize 时 TradingService 可能尚未注入
	if s.TradingService != nil {
		s.orderUpdateOnce.Do(func() {
			handler := services.OrderUpdateHandlerFunc(s.OnOrderUpdate)
			s.TradingService.OnOrderUpdate(handler)
			log.Infof("✅ [%s] 已注册订单更新回调（Subscribe 兜底）", ID)
		})
	}
}

func (s *Strategy) Run(ctx context.Context, _ bbgo.OrderExecutor, session *bbgo.ExchangeSession) error {
	// 启动 Brain 子模块（ArbitrageBrain等）
	if s.brain != nil {
		s.brain.Start(ctx)
	}

	// 启动 OMS 子模块（RiskManager等）
	if s.oms != nil {
		s.oms.Start(ctx)
	}

	// 启动 Dashboard UI（如果启用）
	// 注意：Dashboard 只启动一次，周期切换时不停止
	if s.Config.DashboardEnabled && s.dash != nil {
		// 设置 Dashboard 退出回调：当原生TUI退出时，取消主程序的 context
		// 关键修复：不仅要取消 dashboardExitCtx，还要取消传入的 ctx（主程序的 context）
		// 这样 Strategy.Run 会退出，进而导致整个程序退出
		s.dash.SetExitCallback(func() {
			log.Infof("🛑 [%s] Dashboard 退出，取消主程序 context", ID)
			// 取消 dashboardExitCtx，让 Strategy.Run 退出
			if s.dashboardExitCancel != nil {
				s.dashboardExitCancel()
			}
			// 注意：我们不能直接取消传入的 ctx，因为它是外部的
			// 但 Strategy.Run 退出后，主程序应该能够检测到并退出
		})

		// Start 方法内部会检查是否已启动，避免重复启动
		if err := s.dash.Start(ctx); err != nil {
			log.Warnf("⚠️ [%s] Dashboard 启动失败: %v", ID, err)
		} else {
			log.Infof("✅ [%s] Dashboard UI 已启动", ID)
		}
		// 启动数据更新循环（使用独立的 context，不受周期切换影响）
		// 使用 sync.Once 确保只启动一次
		// 注意：session 参数在 updateDashboard 中未使用，传 nil 即可
		s.dashboardLoopOnce.Do(func() {
			if s.dashboardCtx != nil {
				go s.dashboardUpdateLoop(s.dashboardCtx, nil)
				log.Infof("✅ [%s] Dashboard 更新循环已启动（使用独立 context，不受周期切换影响）", ID)
			} else {
				log.Warnf("⚠️ [%s] Dashboard context 未初始化，无法启动更新循环", ID)
			}
		})
	}

	// 等待主程序 context 或 Dashboard 退出信号
	// 关键修复：检查 dashboardExitCtx 是否为 nil，避免 nil pointer dereference
	// 当 dashboardEnabled: false 时，dashboardExitCtx 可能为 nil
	if s.dashboardExitCtx == nil {
		// Dashboard 未启用，只等待主程序 context
		<-ctx.Done()
		log.Debugf("📊 [%s] 主程序 context 已取消", ID)
	} else {
		// Dashboard 已启用，等待主程序 context 或 Dashboard 退出信号
		select {
		case <-ctx.Done():
			// 主程序 context 已取消（正常退出）
			log.Debugf("📊 [%s] 主程序 context 已取消", ID)
		case <-s.dashboardExitCtx.Done():
			// Dashboard 退出（原生TUI收到 Ctrl+C）
			log.Infof("🛑 [%s] Dashboard 已退出，Strategy.Run 退出", ID)
			// 关键修复：当 Dashboard 退出时，返回一个明确的错误，让主程序知道策略已退出
			// 这样主程序可以检测到策略退出并执行清理
			return fmt.Errorf("Dashboard 已退出（用户按 Ctrl+C）")
		}
	}

	// 停止 Brain 子模块
	if s.brain != nil {
		s.brain.Stop()
	}

	// 停止 OMS 子模块
	if s.oms != nil {
		s.oms.Stop()
	}

	// 停止 Dashboard UI（完全关闭时）
	if s.dash != nil {
		s.dash.Stop()
		log.Infof("✅ [%s] Dashboard UI 已停止", ID)
	}

	return ctx.Err()
}

// dashboardUpdateLoop Dashboard 数据更新循环（bubbletea 负责渲染）
func (s *Strategy) dashboardUpdateLoop(ctx context.Context, session *bbgo.ExchangeSession) {
	// 使用更短的刷新间隔来实现实时 UI 更新（类似 go-polymarket-watcher）
	refreshTicker := time.NewTicker(time.Duration(s.Config.DashboardRefreshIntervalMs) * time.Millisecond)
	defer refreshTicker.Stop()

	// 持仓同步使用较长的间隔
	reconcileTicker := time.NewTicker(time.Duration(s.Config.DashboardPositionReconcileIntervalSeconds) * time.Second)
	defer reconcileTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-refreshTicker.C:
			// 快速刷新数据（bubbletea 会自动渲染）
			s.updateDashboard(ctx, session, nil)
		case <-reconcileTicker.C:
			// 定期同步持仓数据（使用完整更新）
			s.updateDashboard(ctx, session, nil)
		}
	}
}

// updateDashboard 更新 Dashboard 数据
// market 参数可选：如果提供了 market，直接使用它；否则从 TradingService 获取当前市场
func (s *Strategy) updateDashboard(ctx context.Context, session *bbgo.ExchangeSession, market *domain.Market) {
	if s.dash == nil || s.TradingService == nil {
		return
	}

	// 如果正在周期切换，且 market 参数为 nil（来自 dashboardUpdateLoop），则跳过更新
	// 让 OnCycle 中的更新先完成
	if market == nil {
		s.cycleSwitchMu.RLock()
		switching := s.cycleSwitching
		switchTime := s.cycleSwitchTime
		s.cycleSwitchMu.RUnlock()

		if switching {
			// 如果周期切换刚刚发生（2秒内），跳过更新，让 OnCycle 中的更新先完成
			timeSinceSwitch := time.Since(switchTime)
			if timeSinceSwitch < 2*time.Second {
				log.Debugf("⏸️ [%s] 周期切换中，跳过 dashboardUpdateLoop 更新: timeSinceSwitch=%v", ID, timeSinceSwitch)
				return
			} else {
				// 周期切换窗口已过，但标志还未清除，记录警告
				log.Warnf("⚠️ [%s] 周期切换标志仍为 true，但已超过 2 秒: timeSinceSwitch=%v，继续更新", ID, timeSinceSwitch)
			}
		}
	}

	// 如果没有提供 market 参数，从 TradingService 获取当前市场
	var currentMarketSlug string
	if market == nil {
		currentMarketSlug = s.TradingService.GetCurrentMarket()
		if currentMarketSlug == "" {
			log.Debugf("⏸️ [%s] updateDashboard 跳过：TradingService 当前市场为空", ID)
			return
		}
		market = s.TradingService.GetCurrentMarketInfo()
		if market == nil {
			log.Debugf("⏸️ [%s] updateDashboard 跳过：无法获取市场信息 marketSlug=%s", ID, currentMarketSlug)
			return // 没有市场信息，无法更新
		}
		currentMarketSlug = market.Slug
	} else {
		currentMarketSlug = market.Slug
	}

	log.Debugf("📊 [%s] updateDashboard 开始更新: market=%s", ID, currentMarketSlug)

	// 关键修复：检测市场切换
	// 注意：如果是从 OnCycle 调用的（market 参数不为 nil），不应该在这里重置快照
	// 因为 OnCycle 已经调用了 ResetSnapshot，这里再重置会导致数据丢失
	// 只有在 dashboardUpdateLoop 检测到市场切换时，才需要重置
	if s.dash != nil && market != nil && session == nil {
		// 只有从 dashboardUpdateLoop 调用时才检查市场切换
		// OnCycle 已经处理了市场切换，不需要再次检查
		marketChanged := s.dash.CheckAndResetOnMarketChange(market)
		if marketChanged {
			// dashboardUpdateLoop 检测到市场切换，设置周期切换标志
			s.cycleSwitchMu.Lock()
			s.cycleSwitching = true
			s.cycleSwitchTime = time.Now()
			s.cycleSwitchMu.Unlock()
			log.Debugf("🔄 [%s] dashboardUpdateLoop 检测到市场切换，设置周期切换标志", ID)
		}
	}

	// 获取价格信息
	var yesPrice, noPrice, yesBid, yesAsk, noBid, noAsk float64
	if s.TradingService != nil {
		// 使用 GetTopOfBook 获取一档价格
		yesBidPrice, yesAskPrice, noBidPrice, noAskPrice, _, err := s.TradingService.GetTopOfBook(ctx, market)
		if err == nil {
			yesBid = yesBidPrice.ToDecimal()
			yesAsk = yesAskPrice.ToDecimal()
			yesPrice = (yesBid + yesAsk) / 2
			noBid = noBidPrice.ToDecimal()
			noAsk = noAskPrice.ToDecimal()
			noPrice = (noBid + noAsk) / 2
			log.Debugf("📊 [%s] updateDashboard 获取价格成功: UP=%.4f (bid=%.4f ask=%.4f) DOWN=%.4f (bid=%.4f ask=%.4f) market=%s",
				ID, yesPrice, yesBid, yesAsk, noPrice, noBid, noAsk, market.Slug)
		} else {
			// GetTopOfBook 失败，尝试单独获取 bid/ask
			log.Warnf("⚠️ [%s] GetTopOfBook 失败，尝试单独获取价格: market=%s err=%v", ID, market.Slug, err)
			// 尝试获取 UP 价格
			if yesBidPrice, yesAskPrice, err := s.TradingService.GetBestPrice(ctx, market.YesAssetID); err == nil {
				yesBid = yesBidPrice
				yesAsk = yesAskPrice
				yesPrice = (yesBid + yesAsk) / 2
				log.Debugf("📊 [%s] 单独获取 UP 价格成功: price=%.4f (bid=%.4f ask=%.4f)", ID, yesPrice, yesBid, yesAsk)
			} else {
				log.Debugf("⚠️ [%s] 获取 UP 价格失败: %v", ID, err)
			}
			// 尝试获取 DOWN 价格
			if noBidPrice, noAskPrice, err := s.TradingService.GetBestPrice(ctx, market.NoAssetID); err == nil {
				noBid = noBidPrice
				noAsk = noAskPrice
				noPrice = (noBid + noAsk) / 2
				log.Debugf("📊 [%s] 单独获取 DOWN 价格成功: price=%.4f (bid=%.4f ask=%.4f)", ID, noPrice, noBid, noAsk)
			} else {
				log.Debugf("⚠️ [%s] 获取 DOWN 价格失败: %v", ID, err)
			}
		}
	}

	// 从 Brain 获取速度信息
	var upVelocity, downVelocity float64
	var upMove, downMove int
	var direction string
	if s.brain != nil && market != nil {
		velocityInfo := s.brain.GetVelocityInfo(ctx, market)
		if velocityInfo != nil {
			upVelocity = velocityInfo.UpVelocity
			downVelocity = velocityInfo.DownVelocity
			upMove = velocityInfo.UpMove
			downMove = velocityInfo.DownMove
			direction = velocityInfo.Direction
		}
	}

	// 从 Brain 获取持仓状态（先更新持仓）
	var positionState *dashboard.PositionState
	if s.brain != nil && market != nil {
		// 先更新持仓状态（确保获取最新数据）
		s.brain.UpdatePositionState(ctx, market)

		// 获取更新后的持仓状态
		ps := s.brain.GetPositionState(currentMarketSlug)
		if ps != nil {
			positionState = &dashboard.PositionState{
				UpSize:       ps.UpSize,
				DownSize:     ps.DownSize,
				UpCost:       ps.UpCost,
				DownCost:     ps.DownCost,
				UpAvgPrice:   ps.UpAvgPrice,
				DownAvgPrice: ps.DownAvgPrice,
				IsHedged:     ps.IsHedged,
			}
		}
	}

	// 计算盈利信息
	var profitIfUpWin, profitIfDownWin, totalCost float64
	var isProfitLocked bool
	if positionState != nil {
		totalCost = positionState.UpCost + positionState.DownCost
		profitIfUpWin = positionState.UpSize*1.0 - positionState.UpCost - positionState.DownCost
		profitIfDownWin = positionState.DownSize*1.0 - positionState.UpCost - positionState.DownCost
		isProfitLocked = profitIfUpWin > 0 && profitIfDownWin > 0
	}

	// 获取交易统计
	s.mu.RLock()
	tradesThisCycle := s.tradesThisCycle
	lastTriggerTime := s.lastTriggerTime
	s.mu.RUnlock()

	// 获取订单状态
	var pendingHedges, openOrders int
	if s.oms != nil {
		pendingHedgesMap := s.oms.GetPendingHedges()
		pendingHedges = len(pendingHedgesMap)
	}
	if s.TradingService != nil {
		activeOrders := s.TradingService.GetActiveOrders()
		openOrders = len(activeOrders)
	}

	// 获取风控状态
	var riskManagement *dashboard.RiskManagementStatus
	if s.oms != nil {
		omsRiskStatus := s.oms.GetRiskManagementStatus()
		if omsRiskStatus != nil {
			// 转换为 dashboard 类型
			riskExposures := make([]dashboard.RiskExposureInfo, 0, len(omsRiskStatus.RiskExposures))
			for _, exp := range omsRiskStatus.RiskExposures {
				riskExposures = append(riskExposures, dashboard.RiskExposureInfo{
					EntryOrderID:            exp.EntryOrderID,
					EntryTokenType:          exp.EntryTokenType,
					EntrySize:               exp.EntrySize,
					EntryPriceCents:         exp.EntryPriceCents,
					HedgeOrderID:            exp.HedgeOrderID,
					HedgeStatus:             exp.HedgeStatus,
					ExposureSeconds:         exp.ExposureSeconds,
					MaxLossCents:            exp.MaxLossCents,
					OriginalHedgePriceCents: exp.OriginalHedgePriceCents,
					NewHedgePriceCents:      exp.NewHedgePriceCents,
					CountdownSeconds:        exp.CountdownSeconds,
				})
			}
			riskManagement = &dashboard.RiskManagementStatus{
				RiskExposuresCount:    omsRiskStatus.RiskExposuresCount,
				RiskExposures:         riskExposures,
				CurrentAction:         omsRiskStatus.CurrentAction,
				CurrentActionEntry:    omsRiskStatus.CurrentActionEntry,
				CurrentActionHedge:    omsRiskStatus.CurrentActionHedge,
				CurrentActionTime:     omsRiskStatus.CurrentActionTime,
				CurrentActionDesc:     omsRiskStatus.CurrentActionDesc,
				TotalReorders:         omsRiskStatus.TotalReorders,
				TotalAggressiveHedges: omsRiskStatus.TotalAggressiveHedges,
				TotalFakEats:          omsRiskStatus.TotalFakEats,
				// 调价详情
				RepriceOldPriceCents:    omsRiskStatus.RepriceOldPriceCents,
				RepriceNewPriceCents:    omsRiskStatus.RepriceNewPriceCents,
				RepricePriceChangeCents: omsRiskStatus.RepricePriceChangeCents,
				RepriceStrategy:         omsRiskStatus.RepriceStrategy,
				RepriceEntryCostCents:   omsRiskStatus.RepriceEntryCostCents,
				RepriceMarketAskCents:   omsRiskStatus.RepriceMarketAskCents,
				RepriceIdealPriceCents:  omsRiskStatus.RepriceIdealPriceCents,
				RepriceTotalCostCents:   omsRiskStatus.RepriceTotalCostCents,
				RepriceProfitCents:      omsRiskStatus.RepriceProfitCents,
			}
		}
	}

	// 获取 merge 状态和次数
	var mergeCount int
	var mergeStatus string
	var mergeAmount float64
	var mergeTxHash string
	var lastMergeTime time.Time
	if s.capital != nil {
		mergeCount = s.capital.GetMergeCount()
		mergeStatus, mergeAmount, mergeTxHash, lastMergeTime = s.capital.GetMergeStatus()
	}

	// 计算周期结束时间和剩余时间
	var cycleEndTime time.Time
	var cycleRemainingSec float64
	if market != nil && market.Timestamp > 0 {
		// 从市场信息动态获取周期时长（支持 15m/1h/4h）
		cycleDuration := s.getCycleDuration(market)
		cycleStartTime := time.Unix(market.Timestamp, 0)
		cycleEndTime = cycleStartTime.Add(cycleDuration)
		now := time.Now()
		if now.Before(cycleEndTime) {
			cycleRemainingSec = cycleEndTime.Sub(now).Seconds()
		} else {
			cycleRemainingSec = 0
		}
	}

	// 获取决策条件（用于调试）
	var decisionConditions *dashboard.DecisionConditions
	if s.brain != nil && market != nil {
		// 创建一个模拟的 PriceChangedEvent 用于获取决策条件
		priceEvent := &events.PriceChangedEvent{
			Market:    market,
			TokenType: domain.TokenTypeUp, // 默认值，实际会从市场获取
			NewPrice:  domain.PriceFromDecimal(yesPrice),
		}

		// 计算策略信息
		s.mu.RLock()
		cooldownRemaining := 0.0
		if !s.lastTriggerTime.IsZero() {
			cooldownDuration := time.Duration(s.Config.CooldownMs) * time.Millisecond
			elapsed := time.Since(s.lastTriggerTime)
			if elapsed < cooldownDuration {
				cooldownRemaining = (cooldownDuration - elapsed).Seconds()
			}
		}

		warmupRemaining := 0.0
		if !s.cycleStartTime.IsZero() {
			warmupDuration := time.Duration(s.Config.WarmupMs) * time.Millisecond
			elapsed := time.Since(s.cycleStartTime)
			if elapsed < warmupDuration {
				warmupRemaining = (warmupDuration - elapsed).Seconds()
			}
		}
		s.mu.RUnlock()

		strategyInfo := &brain.StrategyInfo{
			CooldownRemaining: cooldownRemaining,
			WarmupRemaining:   warmupRemaining,
			TradesThisCycle:   tradesThisCycle,
			HasPendingHedge:   pendingHedges > 0,
		}

		// 获取决策条件
		dc := s.brain.GetDecisionConditions(ctx, priceEvent, strategyInfo)
		if dc != nil {
			// 转换为 dashboard.DecisionConditions
			decisionConditions = &dashboard.DecisionConditions{
				UpVelocityOK:      dc.UpVelocityOK,
				UpVelocityValue:   dc.UpVelocityValue,
				UpMoveOK:          dc.UpMoveOK,
				UpMoveValue:       dc.UpMoveValue,
				DownVelocityOK:    dc.DownVelocityOK,
				DownVelocityValue: dc.DownVelocityValue,
				DownMoveOK:        dc.DownMoveOK,
				DownMoveValue:     dc.DownMoveValue,
				Direction:         dc.Direction,
				EntryPriceOK:      dc.EntryPriceOK,
				EntryPriceValue:   dc.EntryPriceValue,
				EntryPriceMin:     dc.EntryPriceMin,
				EntryPriceMax:     dc.EntryPriceMax,
				TotalCostOK:       dc.TotalCostOK,
				TotalCostValue:    dc.TotalCostValue,
				HedgePriceOK:      dc.HedgePriceOK,
				HedgePriceValue:   dc.HedgePriceValue,
				HasUnhedgedRisk:   dc.HasUnhedgedRisk,
				IsProfitLocked:    dc.IsProfitLocked,
				ProfitIfUpWin:     dc.ProfitIfUpWin,
				ProfitIfDownWin:   dc.ProfitIfDownWin,
				CooldownOK:        dc.CooldownOK,
				CooldownRemaining: dc.CooldownRemaining,
				WarmupOK:          dc.WarmupOK,
				WarmupRemaining:   dc.WarmupRemaining,
				TradesLimitOK:     dc.TradesLimitOK,
				TradesThisCycle:   dc.TradesThisCycle,
				MaxTradesPerCycle: dc.MaxTradesPerCycle,
				MarketValid:       dc.MarketValid,
				HasPendingHedge:   dc.HasPendingHedge,
				CanTrade:          dc.CanTrade,
				BlockReason:       dc.BlockReason,
			}
		}
	}

	// 更新 Dashboard
	updateData := &dashboard.UpdateData{
		YesPrice:           yesPrice,
		NoPrice:            noPrice,
		YesBid:             yesBid,
		YesAsk:             yesAsk,
		NoBid:              noBid,
		NoAsk:              noAsk,
		UpVelocity:         upVelocity,
		DownVelocity:       downVelocity,
		UpMove:             upMove,
		DownMove:           downMove,
		Direction:          direction,
		PositionState:      positionState,
		ProfitIfUpWin:      profitIfUpWin,
		ProfitIfDownWin:    profitIfDownWin,
		TotalCost:          totalCost,
		IsProfitLocked:     isProfitLocked,
		TradesThisCycle:    tradesThisCycle,
		LastTriggerTime:    lastTriggerTime,
		PendingHedges:      pendingHedges,
		OpenOrders:         openOrders,
		RiskManagement:     riskManagement,
		DecisionConditions: decisionConditions,
		MergeCount:         mergeCount,
		MergeStatus:        mergeStatus,
		MergeAmount:        mergeAmount,
		MergeTxHash:        mergeTxHash,
		LastMergeTime:      lastMergeTime,
		CycleEndTime:       cycleEndTime,
		CycleRemainingSec:  cycleRemainingSec,
	}

	// 更新 Dashboard（即使某些数据获取失败，也要更新，至少显示市场信息和周期信息）
	s.dash.UpdateSnapshot(ctx, market, updateData)
	s.dash.Render()
	log.Debugf("✅ [%s] updateDashboard 完成更新: market=%s prices=(UP=%.4f DOWN=%.4f) velocity=(UP=%.3f DOWN=%.3f)",
		ID, currentMarketSlug, yesPrice, noPrice, upVelocity, downVelocity)
}

// OnCycle 周期切换回调
func (s *Strategy) OnCycle(ctx context.Context, oldMarket *domain.Market, newMarket *domain.Market) {
	// 周期切换后，立即重新应用日志重定向（防止日志系统覆盖重定向设置）
	// 必须在任何日志输出之前执行，避免日志打印到终端
	if s.dash != nil && s.Config.DashboardEnabled {
		s.dash.ReapplyLogRedirect()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// 现在可以安全地输出日志（已重定向到文件）
	log.Infof("🔄 [%s] 周期切换开始: %s -> %s", ID,
		getMarketSlug(oldMarket), getMarketSlug(newMarket))

	// 记录 TradingService 的当前市场状态（用于调试）
	if s.TradingService != nil {
		currentMarketBeforeSwitch := s.TradingService.GetCurrentMarket()
		log.Debugf("📊 [%s] 周期切换前 TradingService 当前市场: %s", ID, currentMarketBeforeSwitch)
	}

	// 重置周期状态
	s.cycleStartTime = time.Now()
	s.lastTriggerTime = time.Time{}
	s.tradesThisCycle = 0

	// 关键修复：在周期切换时，先保存旧周期的持仓（在 ResetForNewCycle 清空之前）
	// 因为 MergePreviousCycle 需要这些持仓，但 ResetForNewCycle 会清空 OrderEngine 中的持仓
	var oldCyclePositions []*domain.Position
	if oldMarket != nil && s.TradingService != nil {
		// 在 ResetForNewCycle 执行之前，先获取旧周期的持仓
		oldCyclePositions = s.TradingService.GetOpenPositionsForMarket(oldMarket.Slug)
		if len(oldCyclePositions) == 0 {
			// 如果通过 marketSlug 获取不到，尝试获取所有持仓并过滤
			allPositions := s.TradingService.GetAllPositions()
			for _, pos := range allPositions {
				if pos == nil || !pos.IsOpen() || pos.Size <= 0 {
					continue
				}
				// 检查持仓是否属于旧周期（通过 ConditionID 匹配）
				if pos.Market != nil && pos.Market.ConditionID == oldMarket.ConditionID {
					oldCyclePositions = append(oldCyclePositions, pos)
				} else if pos.EntryOrder != nil && pos.EntryOrder.MarketSlug == oldMarket.Slug {
					// 或者通过 EntryOrder 的 MarketSlug 匹配
					oldCyclePositions = append(oldCyclePositions, pos)
				}
			}
		}
		if len(oldCyclePositions) > 0 {
			log.Infof("📊 [%s] 周期切换前保存旧周期持仓: oldMarket=%s positions=%d", ID, oldMarket.Slug, len(oldCyclePositions))
		}
	}

	// 通知模块周期切换
	if s.brain != nil {
		s.brain.OnCycle(ctx, oldMarket, newMarket)
	}
	if s.oms != nil {
		s.oms.OnCycle(ctx, oldMarket, newMarket)
	}
	if s.capital != nil {
		// 传递旧周期持仓给 Capital，用于合并
		if oldMarket != nil && len(oldCyclePositions) > 0 {
			s.capital.OnCycleWithPositions(ctx, oldMarket, newMarket, oldCyclePositions)
		} else {
			s.capital.OnCycle(ctx, oldMarket, newMarket)
		}
	}

	// 更新 Dashboard UI（异步执行，避免阻塞周期切换回调）
	if s.dash != nil && s.Config.DashboardEnabled && newMarket != nil {
		// 设置周期切换标志，防止 dashboardUpdateLoop 立即更新
		s.cycleSwitchMu.Lock()
		s.cycleSwitching = true
		s.cycleSwitchTime = time.Now()
		s.cycleSwitchMu.Unlock()

		// 保存新市场信息，用于在周期切换窗口内接受新市场的价格事件
		s.newMarketMu.Lock()
		if newMarket != nil {
			s.newMarketSlug = newMarket.Slug
		} else {
			s.newMarketSlug = ""
		}
		s.newMarketMu.Unlock()
		log.Infof("🔄 [%s] 已保存周期切换新市场信息: newMarket=%s (用于接受周期切换窗口内的价格事件)", ID, s.newMarketSlug)

		// 记录 TradingService 的当前市场状态（用于调试）
		if s.TradingService != nil {
			currentMarketAfterSwitch := s.TradingService.GetCurrentMarket()
			log.Debugf("📊 [%s] 周期切换后 TradingService 当前市场: %s (期望: %s)", ID, currentMarketAfterSwitch, newMarket.Slug)
		}

		// 重置 Dashboard 快照，重建 UI 状态（完全清空旧数据）
		s.dash.ResetSnapshot(newMarket)
		log.Debugf("🔄 [%s] Dashboard UI 已重置: market=%s", ID, newMarket.Slug)

		// 立即使用 program.Send() 强制发送更新，确保UI立即更新（显示新市场信息）
		// 这比 ForceRender() 更可靠，因为它直接发送消息到 Bubble Tea，不依赖 channel
		s.dash.SendUpdate()
		log.Debugf("🔄 [%s] 已通过 program.Send() 强制更新 UI: market=%s", ID, newMarket.Slug)

		// 立即同步更新 Dashboard（使用新市场信息），确保UI立即反映新周期状态
		// 注意：这里必须传入 newMarket，确保使用新市场信息而不是从 TradingService 获取（可能还是旧市场）
		go func() {
			// 稍微延迟一下，确保 TradingService 已经更新为新市场
			time.Sleep(100 * time.Millisecond)
			updateCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			// 关键：传入 newMarket，确保使用新市场信息
			log.Infof("🔄 [%s] 周期切换后第一次更新 Dashboard: market=%s", ID, newMarket.Slug)
			s.updateDashboard(updateCtx, nil, newMarket)
			// 更新后立即使用 program.Send() 强制发送更新
			s.dash.SendUpdate()
			log.Debugf("🔄 [%s] 第一次更新后通过 program.Send() 强制更新 UI: market=%s", ID, newMarket.Slug)
		}()

		// 异步再次更新一次（延迟后），确保所有数据都已更新（包括价格、速度等）
		// 因为周期切换后，价格事件可能还没到达，所以延迟更新确保获取最新数据
		go func() {
			time.Sleep(800 * time.Millisecond)
			updateCtx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel2()
			// 关键：传入 newMarket，确保使用新市场信息
			log.Infof("🔄 [%s] 周期切换后第二次更新 Dashboard: market=%s", ID, newMarket.Slug)
			s.updateDashboard(updateCtx2, nil, newMarket)
			// 更新后立即使用 program.Send() 强制发送更新
			s.dash.SendUpdate()
			log.Debugf("🔄 [%s] 第二次更新后通过 program.Send() 强制更新 UI: market=%s", ID, newMarket.Slug)
		}()

		// 再次延迟更新一次，确保UI完全刷新（处理可能的竞态条件）
		go func() {
			time.Sleep(1 * time.Second)
			// 最后一次更新，使用当前市场（此时 TradingService 应该已经更新为新市场）
			updateCtx3, cancel3 := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel3()
			log.Infof("🔄 [%s] 周期切换后最后一次更新 Dashboard: market=%s", ID, newMarket.Slug)
			s.updateDashboard(updateCtx3, nil, newMarket)
			// 更新后立即使用 program.Send() 强制发送更新
			s.dash.SendUpdate()
			log.Debugf("🔄 [%s] 第三次更新后通过 program.Send() 强制更新 UI: market=%s", ID, newMarket.Slug)

			// 清除周期切换标志，允许 dashboardUpdateLoop 继续更新
			s.cycleSwitchMu.Lock()
			s.cycleSwitching = false
			s.cycleSwitchTime = time.Time{} // 重置切换时间
			s.cycleSwitchMu.Unlock()

			// 清除新市场信息（周期切换窗口已过）
			s.newMarketMu.Lock()
			s.newMarketSlug = ""
			s.newMarketMu.Unlock()

			log.Infof("✅ [%s] 周期切换完成，允许 Dashboard 更新循环继续更新", ID)
		}()

		// 额外：在周期切换后 3 秒再次更新一次，确保价格数据已经到达
		go func() {
			time.Sleep(3 * time.Second)
			updateCtx4, cancel4 := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel4()
			log.Infof("🔄 [%s] 周期切换后 3 秒再次更新 Dashboard（确保价格数据已到达）: market=%s", ID, newMarket.Slug)
			s.updateDashboard(updateCtx4, nil, newMarket)
			// 更新后立即使用 program.Send() 强制发送更新
			s.dash.SendUpdate()
			log.Debugf("🔄 [%s] 第四次更新后通过 program.Send() 强制更新 UI: market=%s", ID, newMarket.Slug)
		}()
	}
}

// OnOrderUpdate 订单更新回调
func (s *Strategy) OnOrderUpdate(ctx context.Context, order *domain.Order) error {
	if order == nil || order.OrderID == "" {
		return nil
	}

	// 转发给 OMS 模块处理
	if s.oms != nil {
		if err := s.oms.OnOrderUpdate(ctx, order); err != nil {
			log.Warnf("⚠️ [%s] OMS 处理订单更新失败: %v", ID, err)
		}
	}

	return nil
}

// Shutdown 策略关闭回调（实现 StrategyShutdown 接口）
// 在策略完全关闭时调用，用于清理资源
// 注意：wg 参数由 shutdown.Manager 管理，不需要在此方法中调用 wg.Done()
func (s *Strategy) Shutdown(ctx context.Context, wg *sync.WaitGroup) {
	log.Infof("🛑 [%s] 开始关闭策略...", ID)

	// 停止 Brain 子模块
	if s.brain != nil {
		s.brain.Stop()
	}

	// 停止 OMS 子模块
	if s.oms != nil {
		s.oms.Stop()
	}

	// 停止 Dashboard UI
	if s.dash != nil {
		s.dash.Stop()
	}

	// 取消 Dashboard 更新循环的 context
	if s.dashboardCancel != nil {
		s.dashboardCancel()
		log.Infof("✅ [%s] Dashboard 更新循环 context 已取消", ID)
	}

	log.Infof("✅ [%s] 策略关闭完成", ID)
}

// OnPriceChanged 处理价格变化事件
func (s *Strategy) OnPriceChanged(ctx context.Context, e *events.PriceChangedEvent) error {
	if e == nil || e.Market == nil || s.TradingService == nil {
		return nil
	}

	// ⚠️ 关键修复：无论是否触发交易，都要更新 Brain 的样本（用于 Dashboard 显示速度）
	// 这样 Dashboard 才能获取到实时的速度数据
	if s.brain != nil {
		s.brain.UpdateSamplesFromPriceEvent(ctx, e)
	}

	// 实时更新 Dashboard（如果启用）- 在任何条件检查之前先更新价格显示
	// 注意：价格事件已经通过了 session 层的检查，应该属于当前 session 的市场
	if s.Config.DashboardEnabled && s.dash != nil {
		// 记录价格事件信息（用于调试）
		log.Debugf("📊 [%s] 收到价格事件，准备更新 Dashboard: token=%s price=%.4f market=%s",
			ID, e.TokenType, e.NewPrice.ToDecimal(), e.Market.Slug)
		go func() {
			// 使用新的 context，避免阻塞主流程
			updateCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			s.updateDashboardFromPriceEvent(updateCtx, e)
		}()
	} else {
		// 关键修复：即使 Dashboard 未启用，也记录价格事件（使用 Info 级别，确保用户能看到）
		log.Infof("📊 [%s] 收到价格事件: token=%s price=%.4f market=%s (Dashboard 已禁用)",
			ID, e.TokenType, e.NewPrice.ToDecimal(), e.Market.Slug)
	}

	// 关键修复：移除策略层的市场匹配检查
	// 原因：
	// 1. session 层（sessionPriceHandler）已经做了严格的市场匹配检查
	// 2. 如果价格事件到达策略层，说明它已经通过了 session 层的检查，属于当前 session 的市场
	// 3. 在周期切换时，session 的 market 已经更新，但 TradingService.GetCurrentMarket() 可能还未更新
	// 4. 如果在这里再次检查，可能会错误地过滤掉新市场的价格事件
	//
	// 保留调试日志，但不阻止处理
	cur := s.TradingService.GetCurrentMarket()
	eventMarketSlug := e.Market.Slug
	if cur != "" && cur != eventMarketSlug {
		log.Debugf("📊 [%s] 价格事件市场与 TradingService 当前市场不匹配（但已通过 session 层检查）: eventMarket=%s currentMarket=%s",
			ID, eventMarketSlug, cur)
		// 不返回，继续处理，因为 session 层已经验证了事件属于当前 session 的市场
	}

	// 检查周期状态
	s.mu.Lock()
	now := time.Now()

	// 检查预热窗口
	if !s.cycleStartTime.IsZero() {
		warmupDuration := time.Duration(s.Config.WarmupMs) * time.Millisecond
		if now.Sub(s.cycleStartTime) < warmupDuration {
			s.mu.Unlock()
			return nil
		}
	}

	// 检查冷却时间
	if !s.lastTriggerTime.IsZero() {
		cooldownDuration := time.Duration(s.Config.CooldownMs) * time.Millisecond
		if now.Sub(s.lastTriggerTime) < cooldownDuration {
			s.mu.Unlock()
			return nil
		}
	}

	// 检查交易次数限制
	if s.Config.MaxTradesPerCycle > 0 && s.tradesThisCycle >= s.Config.MaxTradesPerCycle {
		s.mu.Unlock()
		log.Debugf("⏸️ [%s] 已达到本周期最大交易次数: %d", ID, s.tradesThisCycle)
		return nil
	}

	// 检查周期结束保护
	// 注意：这里使用固定的 15 分钟作为周期时长（默认值）
	// 如果需要动态获取，可以从 MarketDataService 或配置中获取
	if !s.cycleStartTime.IsZero() {
		// 动态获取周期时长（支持 15m/1h/4h）
		cycleDuration := s.getCycleDuration(e.Market)
		protectionDuration := time.Duration(s.Config.CycleEndProtectionMinutes) * time.Minute
		elapsed := now.Sub(s.cycleStartTime)
		if elapsed > cycleDuration-protectionDuration {
			s.mu.Unlock()
			log.Debugf("⏸️ [%s] 周期结束保护，不再开新单", ID)
			return nil
		}
	}

	s.mu.Unlock()

	// 调用 Brain 模块进行决策
	if s.brain == nil {
		log.Warnf("⚠️ [%s] Brain 模块未初始化", ID)
		return nil
	}

	// 关键修复：即使 Dashboard 禁用，也记录价格事件和决策过程（使用 Info 级别）
	// 这样用户可以看到策略的活动
	if !s.Config.DashboardEnabled {
		log.Infof("📊 [%s] 处理价格事件: token=%s price=%.4f market=%s",
			ID, e.TokenType, e.NewPrice.ToDecimal(), e.Market.Slug)
	}

	// 检查是否有未对冲风险（通过 OMS）
	if s.oms != nil {
		hasUnhedgedRisk, err := s.oms.HasUnhedgedRisk(e.Market.Slug)
		if err != nil {
			log.Warnf("⚠️ [%s] 检查未对冲风险失败: %v", ID, err)
		} else if hasUnhedgedRisk {
			log.Debugf("⏸️ [%s] 存在未对冲风险，跳过本次下单", ID)
			return nil
		}
	}

	// Brain 决策：是否应该下单
	decision, err := s.brain.MakeDecision(ctx, e)
	if err != nil {
		log.Warnf("⚠️ [%s] Brain 决策失败: %v", ID, err)
		return nil
	}

	if !decision.ShouldTrade {
		return nil // 不满足交易条件
	}

	// 通过 OMS 执行订单
	if s.oms == nil {
		log.Warnf("⚠️ [%s] OMS 模块未初始化", ID)
		return nil
	}

	err = s.oms.ExecuteOrder(ctx, e.Market, decision)
	if err != nil {
		log.Warnf("⚠️ [%s] 订单执行失败: %v", ID, err)
		return nil
	}

	// 更新状态
	s.mu.Lock()
	s.lastTriggerTime = now
	s.tradesThisCycle++
	s.mu.Unlock()

	log.Infof("✅ [%s] 已触发交易: direction=%s market=%s tradesThisCycle=%d",
		ID, decision.Direction, e.Market.Slug, s.tradesThisCycle)

	return nil
}

// updateDashboardFromPriceEvent 从价格事件更新 Dashboard
func (s *Strategy) updateDashboardFromPriceEvent(ctx context.Context, e *events.PriceChangedEvent) {
	if s.dash == nil || e == nil || e.Market == nil {
		log.Debugf("🔍 [%s] updateDashboardFromPriceEvent 跳过: dash=%v e=%v market=%v", ID, s.dash != nil, e != nil, e != nil && e.Market != nil)
		return
	}

	// 记录价格事件信息（用于调试周期切换问题）
	log.Debugf("📊 [%s] updateDashboardFromPriceEvent 开始: token=%s price=%.4f market=%s TradingService.currentMarket=%s",
		ID, e.TokenType, e.NewPrice.ToDecimal(), e.Market.Slug, s.TradingService.GetCurrentMarket())

	// 获取价格信息（使用 GetTopOfBook 获取完整价格）
	var yesPrice, noPrice, yesBid, yesAsk, noBid, noAsk float64
	if s.TradingService != nil {
		yesBidPrice, yesAskPrice, noBidPrice, noAskPrice, _, err := s.TradingService.GetTopOfBook(ctx, e.Market)
		if err == nil {
			yesBid = yesBidPrice.ToDecimal()
			yesAsk = yesAskPrice.ToDecimal()
			yesPrice = (yesBid + yesAsk) / 2
			noBid = noBidPrice.ToDecimal()
			noAsk = noAskPrice.ToDecimal()
			noPrice = (noBid + noAsk) / 2
			log.Debugf("📊 [%s] Dashboard 价格更新: UP=%.4f (bid=%.4f ask=%.4f) DOWN=%.4f (bid=%.4f ask=%.4f)",
				ID, yesPrice, yesBid, yesAsk, noPrice, noBid, noAsk)
		} else {
			// 如果 GetTopOfBook 失败，尝试从事件中获取
			log.Debugf("⚠️ [%s] GetTopOfBook 失败，使用事件价格: %v", ID, err)
			if e.TokenType == domain.TokenTypeUp {
				yesPrice = e.NewPrice.ToDecimal()
				// 尝试单独获取 bid/ask
				if yesBidPrice, yesAskPrice, err := s.TradingService.GetBestPrice(ctx, e.Market.YesAssetID); err == nil {
					yesBid = yesBidPrice
					yesAsk = yesAskPrice
				}
			} else if e.TokenType == domain.TokenTypeDown {
				noPrice = e.NewPrice.ToDecimal()
				// 尝试单独获取 bid/ask
				if noBidPrice, noAskPrice, err := s.TradingService.GetBestPrice(ctx, e.Market.NoAssetID); err == nil {
					noBid = noBidPrice
					noAsk = noAskPrice
				}
			}
		}
	}

	// 获取持仓状态
	var positionState *dashboard.PositionState
	if s.brain != nil {
		ps := s.brain.GetPositionState(e.Market.Slug)
		if ps != nil {
			positionState = &dashboard.PositionState{
				UpSize:       ps.UpSize,
				DownSize:     ps.DownSize,
				UpCost:       ps.UpCost,
				DownCost:     ps.DownCost,
				UpAvgPrice:   ps.UpAvgPrice,
				DownAvgPrice: ps.DownAvgPrice,
				IsHedged:     ps.IsHedged,
			}
		}
	}

	// 计算盈利信息
	var profitIfUpWin, profitIfDownWin, totalCost float64
	var isProfitLocked bool
	if positionState != nil {
		totalCost = positionState.UpCost + positionState.DownCost
		profitIfUpWin = positionState.UpSize*1.0 - positionState.UpCost - positionState.DownCost
		profitIfDownWin = positionState.DownSize*1.0 - positionState.UpCost - positionState.DownCost
		isProfitLocked = profitIfUpWin > 0 && profitIfDownWin > 0
	}

	// 获取交易统计
	s.mu.RLock()
	tradesThisCycle := s.tradesThisCycle
	lastTriggerTime := s.lastTriggerTime
	s.mu.RUnlock()

	// 获取订单状态
	var pendingHedges, openOrders int
	if s.oms != nil {
		pendingHedgesMap := s.oms.GetPendingHedges()
		pendingHedges = len(pendingHedgesMap)
	}
	if s.TradingService != nil {
		activeOrders := s.TradingService.GetActiveOrders()
		openOrders = len(activeOrders)
	}

	// 获取风控状态
	var riskManagement *dashboard.RiskManagementStatus
	if s.oms != nil {
		omsRiskStatus := s.oms.GetRiskManagementStatus()
		if omsRiskStatus != nil {
			// 转换为 dashboard 类型
			riskExposures := make([]dashboard.RiskExposureInfo, 0, len(omsRiskStatus.RiskExposures))
			for _, exp := range omsRiskStatus.RiskExposures {
				riskExposures = append(riskExposures, dashboard.RiskExposureInfo{
					EntryOrderID:            exp.EntryOrderID,
					EntryTokenType:          exp.EntryTokenType,
					EntrySize:               exp.EntrySize,
					EntryPriceCents:         exp.EntryPriceCents,
					HedgeOrderID:            exp.HedgeOrderID,
					HedgeStatus:             exp.HedgeStatus,
					ExposureSeconds:         exp.ExposureSeconds,
					MaxLossCents:            exp.MaxLossCents,
					OriginalHedgePriceCents: exp.OriginalHedgePriceCents,
					NewHedgePriceCents:      exp.NewHedgePriceCents,
					CountdownSeconds:        exp.CountdownSeconds,
				})
			}
			riskManagement = &dashboard.RiskManagementStatus{
				RiskExposuresCount:    omsRiskStatus.RiskExposuresCount,
				RiskExposures:         riskExposures,
				CurrentAction:         omsRiskStatus.CurrentAction,
				CurrentActionEntry:    omsRiskStatus.CurrentActionEntry,
				CurrentActionHedge:    omsRiskStatus.CurrentActionHedge,
				CurrentActionTime:     omsRiskStatus.CurrentActionTime,
				CurrentActionDesc:     omsRiskStatus.CurrentActionDesc,
				TotalReorders:         omsRiskStatus.TotalReorders,
				TotalAggressiveHedges: omsRiskStatus.TotalAggressiveHedges,
				TotalFakEats:          omsRiskStatus.TotalFakEats,
				// 调价详情
				RepriceOldPriceCents:    omsRiskStatus.RepriceOldPriceCents,
				RepriceNewPriceCents:    omsRiskStatus.RepriceNewPriceCents,
				RepricePriceChangeCents: omsRiskStatus.RepricePriceChangeCents,
				RepriceStrategy:         omsRiskStatus.RepriceStrategy,
				RepriceEntryCostCents:   omsRiskStatus.RepriceEntryCostCents,
				RepriceMarketAskCents:   omsRiskStatus.RepriceMarketAskCents,
				RepriceIdealPriceCents:  omsRiskStatus.RepriceIdealPriceCents,
				RepriceTotalCostCents:   omsRiskStatus.RepriceTotalCostCents,
				RepriceProfitCents:      omsRiskStatus.RepriceProfitCents,
			}
		}
	}

	// 获取 merge 状态和次数
	var mergeCount int
	var mergeStatus string
	var mergeAmount float64
	var mergeTxHash string
	var lastMergeTime time.Time
	if s.capital != nil {
		mergeCount = s.capital.GetMergeCount()
		mergeStatus, mergeAmount, mergeTxHash, lastMergeTime = s.capital.GetMergeStatus()
	}

	// 计算周期结束时间和剩余时间
	var cycleEndTime time.Time
	var cycleRemainingSec float64
	if e.Market != nil && e.Market.Timestamp > 0 {
		// 从市场信息动态获取周期时长（支持 15m/1h/4h）
		cycleDuration := s.getCycleDuration(e.Market)
		cycleStartTime := time.Unix(e.Market.Timestamp, 0)
		cycleEndTime = cycleStartTime.Add(cycleDuration)
		now := time.Now()
		if now.Before(cycleEndTime) {
			cycleRemainingSec = cycleEndTime.Sub(now).Seconds()
		} else {
			cycleRemainingSec = 0
		}
	}

	// 更新 Dashboard
	updateData := &dashboard.UpdateData{
		YesPrice:          yesPrice,
		NoPrice:           noPrice,
		YesBid:            yesBid,
		YesAsk:            yesAsk,
		NoBid:             noBid,
		NoAsk:             noAsk,
		PositionState:     positionState,
		ProfitIfUpWin:     profitIfUpWin,
		ProfitIfDownWin:   profitIfDownWin,
		TotalCost:         totalCost,
		IsProfitLocked:    isProfitLocked,
		TradesThisCycle:   tradesThisCycle,
		LastTriggerTime:   lastTriggerTime,
		PendingHedges:     pendingHedges,
		OpenOrders:        openOrders,
		RiskManagement:    riskManagement,
		MergeCount:        mergeCount,
		MergeStatus:       mergeStatus,
		MergeAmount:       mergeAmount,
		MergeTxHash:       mergeTxHash,
		LastMergeTime:     lastMergeTime,
		CycleEndTime:      cycleEndTime,
		CycleRemainingSec: cycleRemainingSec,
	}

	s.dash.UpdateSnapshot(ctx, e.Market, updateData)
	s.dash.Render()
}

// getMarketSlug 获取市场 slug（安全处理 nil）
func getMarketSlug(market *domain.Market) string {
	if market == nil {
		return "<nil>"
	}
	return market.Slug
}

// getCycleDuration 获取周期时长（优先从市场信息获取，支持动态周期）
// 参数：
//   - market: 当前市场信息（如果为 nil，会尝试从 TradingService 获取）
//
// 返回：周期时长（默认 15 分钟）
func (s *Strategy) getCycleDuration(market *domain.Market) time.Duration {
	// 优先从传入的 market 参数获取
	if market != nil && market.Slug != "" {
		return getCycleDurationFromMarket(market)
	}

	// 如果 market 为 nil，尝试从 TradingService 获取当前市场
	if s.TradingService != nil {
		currentMarketSlug := s.TradingService.GetCurrentMarket()
		if currentMarketSlug != "" {
			// 构造一个临时的 Market 对象用于解析
			tempMarket := &domain.Market{Slug: currentMarketSlug}
			return getCycleDurationFromMarket(tempMarket)
		}
	}

	// 兜底：返回默认 15 分钟
	log.Debugf("⚠️ [%s] 无法获取市场信息，使用默认周期 15 分钟", ID)
	return 15 * time.Minute
}

// getCycleDurationFromMarket 从 market slug 解析周期时长
// 支持两种 slug 格式：
//  1. timestamp 格式: {symbol}-{kind}-{timeframe}-{timestamp}
//     例如: eth-updown-1h-1767717000
//  2. hourly ET 格式: {coinName}-up-or-down-{month}-{day}-{hour}{am|pm}-et
//     例如: ethereum-up-or-down-january-6-11am-et
func getCycleDurationFromMarket(market *domain.Market) time.Duration {
	if market == nil || market.Slug == "" {
		// 默认返回 15 分钟（向后兼容）
		return 15 * time.Minute
	}

	slug := market.Slug

	// 方法1: 尝试从 timestamp 格式解析（第三个部分是 timeframe）
	parts := strings.Split(slug, "-")
	if len(parts) >= 3 {
		timeframeStr := parts[2] // 例如 "1h", "15m", "4h"
		tf, err := marketspec.ParseTimeframe(timeframeStr)
		if err == nil {
			// 成功解析，返回对应的周期时长
			return tf.Duration()
		}
		// 如果解析失败，继续尝试其他方法
	}

	// 方法2: 检查是否为 hourly ET 格式（包含 "am" 或 "pm"）
	// hourly ET 格式通常是 1 小时市场
	slugLower := strings.ToLower(slug)
	if strings.Contains(slugLower, "am") || strings.Contains(slugLower, "pm") {
		// 检查是否包含 "-et" 后缀（hourly ET 格式的特征）
		if strings.HasSuffix(slugLower, "-et") || strings.Contains(slugLower, "-et-") {
			log.Debugf("✅ [%s] 检测到 hourly ET 格式 slug，使用 1 小时周期: slug=%s", ID, slug)
			return 1 * time.Hour
		}
	}

	// 方法3: 检查是否包含月份名称（hourly ET 格式的另一个特征）
	months := []string{"january", "february", "march", "april", "may", "june",
		"july", "august", "september", "october", "november", "december"}
	for _, month := range months {
		if strings.Contains(slugLower, month) {
			log.Debugf("✅ [%s] 检测到包含月份名称的 slug，推断为 1 小时周期: slug=%s", ID, slug)
			return 1 * time.Hour
		}
	}

	// 无法解析，返回默认 15 分钟
	log.Warnf("⚠️ [%s] 无法从 slug 解析周期时长: slug=%s，使用默认 15 分钟", ID, slug)
	return 15 * time.Minute
}
