package grid

import (
	"time"

	"github.com/betbot/gobet/internal/domain"
)

// ResetHoldings 重置双向持仓跟踪
func (s *GridStrategy) ResetHoldings() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resetHoldingsLocked()
}

func (s *GridStrategy) resetHoldingsLocked() {
	s.upTotalCost = 0
	s.upHoldings = 0
	s.downTotalCost = 0
	s.downHoldings = 0

	log.Infof("🔄 双向持仓跟踪已清空（新市场周期开始）")
}

// ResetStateForNewCycle 重置策略状态（用于新周期开始）
// 清空所有仓位、订单和状态，与上一个周期完全无关
func (s *GridStrategy) ResetStateForNewCycle() {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 清理 HedgePlan（避免跨周期复用）
	if s.plan != nil {
		log.Infof("🔄 [周期切换] 取消 HedgePlan: id=%s state=%s", s.plan.ID, s.plan.State)
		s.plan = nil
	}

	// 清空仓位
	if s.activePosition != nil {
		log.Infof("🔄 [周期切换] 清空仓位: %s币 @ %dc, 数量=%.2f",
			s.activePosition.TokenType, s.activePosition.EntryPrice.Cents, s.activePosition.Size)
		s.activePosition = nil
	}

	// 重构后：activeOrders 已移除，现在由 OrderEngine 管理
	// 查询活跃订单并记录（用于日志）
	activeOrders := s.getActiveOrders()
	if len(activeOrders) > 0 {
		log.Infof("🔄 [周期切换] 检测到 %d 个活跃订单（由 OrderEngine 管理）", len(activeOrders))
		for _, order := range activeOrders {
			log.Debugf("   活跃订单: %s (ID=%s, %s币 @ %dc, 状态=%s)",
				order.OrderID[:8], order.OrderID, order.TokenType, order.Price.Cents, string(order.Status))
		}
		// 注意：订单由 OrderEngine 管理，这里只记录，不直接清空
	}

	// 确保 pendingHedgeOrders 已初始化，然后清空待提交的对冲订单
	if s.pendingHedgeOrders == nil {
		s.pendingHedgeOrders = make(map[string]*domain.Order)
	} else if len(s.pendingHedgeOrders) > 0 {
		log.Infof("🔄 [周期切换] 清空 %d 个待提交的对冲订单", len(s.pendingHedgeOrders))
		for entryOrderID, hedgeOrder := range s.pendingHedgeOrders {
			log.Debugf("   待提交对冲订单: 主单ID=%s, 对冲订单ID=%s, %s币 @ %dc",
				entryOrderID[:8], hedgeOrder.OrderID[:8], hedgeOrder.TokenType, hedgeOrder.Price.Cents)
		}
		s.pendingHedgeOrders = make(map[string]*domain.Order)
	}

	// 清空已处理的网格层级（允许新周期重新触发）
	if s.processedGridLevels == nil {
		s.processedGridLevels = make(map[string]time.Time)
	} else if len(s.processedGridLevels) > 0 {
		log.Infof("🔄 [周期切换] 清空 %d 个已处理的网格层级", len(s.processedGridLevels))
		s.processedGridLevels = make(map[string]time.Time)
	}

	// 清空已处理的订单成交事件
	if s.processedFilledOrders == nil {
		s.processedFilledOrders = make(map[string]time.Time)
	} else if len(s.processedFilledOrders) > 0 {
		log.Infof("🔄 [周期切换] 清空 %d 个已处理的订单成交事件", len(s.processedFilledOrders))
		s.processedFilledOrders = make(map[string]time.Time)
	}

	// 重置轮数
	s.roundsThisPeriod = 0
	log.Infof("🔄 [周期切换] 轮数已重置: 0")

	// 重置显示时间（确保新周期第一次价格更新能显示）
	s.lastDisplayTime = time.Time{}
	log.Debugf("🔄 [周期切换] 显示时间已重置，确保首次价格更新能显示")

	// 重置双向持仓跟踪
	s.resetHoldingsLocked()

	log.Infof("✅ [周期切换] 策略状态已重置，准备开始新周期")
}

