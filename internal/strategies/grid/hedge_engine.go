package grid

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
)

// hedgeLockWindowSeconds 周期末进入“强对冲”窗口：优先把 minProfit 拉回 >= 0
const hedgeLockWindowSeconds = 90

// ensureMinProfitLocked 基于 minProfit 目标驱动的动态对冲：
// 目标：min(P_up, P_down) >= target，其中
// P_up   = upHoldings - upTotalCost - downTotalCost
// P_down = downHoldings - upTotalCost - downTotalCost
func (s *GridStrategy) ensureMinProfitLocked(ctx context.Context, market *domain.Market) {
	if s.tradingService == nil || market == nil || s.config == nil {
		return
	}

	// 防抖：避免过于频繁地补仓下单
	if !s.lastHedgeOrderSubmitTime.IsZero() && time.Since(s.lastHedgeOrderSubmitTime) < 2*time.Second {
		return
	}

	// 价格未就绪则跳过
	if s.currentPriceUp <= 0 || s.currentPriceDown <= 0 {
		return
	}

	// 如果当前有待提交/待成交的对冲订单，避免重复提交（这里用业务规则兜底）
	if s.hasAnyPendingHedgeOrder() {
		return
	}

	upWin, downWin := s.profitsUSDC()
	target := s.minProfitTargetUSDC()

	// 周期末强对冲：至少保证不亏（target = 0），避免尾盘滑点/延迟导致锁亏
	if s.isInHedgeLockWindow(market) && target < 0 {
		target = 0
	}
	if s.isInHedgeLockWindow(market) && target > 0 {
		// 周期末更保守：只保证 >= 0（减少临近结算时的过度追价）
		target = 0
	}

	// 已满足目标
	if upWin >= target && downWin >= target {
		return
	}

	// 选择更“差”的方向优先补齐
	needUp := target - upWin
	needDown := target - downWin
	if needUp < 0 {
		needUp = 0
	}
	if needDown < 0 {
		needDown = 0
	}

	var tokenType domain.TokenType
	var assetID string
	var priceCents int
	var needed float64
	if needUp >= needDown {
		tokenType = domain.TokenTypeUp
		assetID = market.YesAssetID
		priceCents = s.currentPriceUp
		needed = needUp
	} else {
		tokenType = domain.TokenTypeDown
		assetID = market.NoAssetID
		priceCents = s.currentPriceDown
		needed = needDown
	}

	price := domain.Price{Cents: priceCents}
	priceDec := price.ToDecimal()
	if priceDec <= 0 || priceDec >= 1 {
		return
	}

	// dQ = (target - P) / (1 - p)
	dQ := needed / (1.0 - priceDec)
	if dQ <= 0 || math.IsNaN(dQ) || math.IsInf(dQ, 0) {
		return
	}

	// 下限：最小金额/最小 share（TradingService 会再兜底一遍，这里尽量给合理值）
	minOrderSize := s.config.MinOrderSize
	if minOrderSize <= 0 {
		minOrderSize = 1.1
	}
	if dQ*priceDec < minOrderSize {
		dQ = minOrderSize / priceDec
	}

	// 周期末更激进地收敛，但仍要限制单次补仓上限，避免追价过度
	maxDQ := 0.0
	if s.isInHedgeLockWindow(market) {
		maxDQ = math.Max(50.0, dQ) // 周期末允许更大（仍受 minOrderSize 限制）
	} else {
		maxDQ = 50.0
	}
	if dQ > maxDQ {
		dQ = maxDQ
	}

	// 价格选择：默认用 bestAsk（更容易成交）；周期末更强调成交
	bestPrice := price
	if bid, ask, err := s.tradingService.GetBestPrice(ctx, assetID); err == nil && ask > 0 {
		bestPrice = domain.PriceFromDecimal(ask)
		_ = bid
	}

	order := &domain.Order{
		OrderID:      fmt.Sprintf("hedge-lock-%s-%d-%d", tokenType, bestPrice.Cents, time.Now().UnixNano()),
		AssetID:      assetID,
		Side:         types.SideBuy,
		Price:        bestPrice,
		Size:         dQ,
		TokenType:    tokenType,
		IsEntryOrder: false,
		Status:       domain.OrderStatusPending,
		CreatedAt:    time.Now(),
	}

	orderCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := s.tradingService.PlaceOrder(orderCtx, order); err != nil {
		log.Warnf("🛡️ [对冲] 补仓下单失败: token=%s price=%dc size=%.4f err=%v", tokenType, bestPrice.Cents, dQ, err)
		return
	}

	s.lastHedgeOrderSubmitTime = time.Now()

	log.Infof("🛡️ [对冲] 已提交补仓: token=%s price=%dc size=%.4f | P(up)=%.4f P(down)=%.4f target=%.4f",
		tokenType, bestPrice.Cents, dQ, upWin, downWin, target)
}

func (s *GridStrategy) profitsUSDC() (upWin float64, downWin float64) {
	// 注意：这些字段未来会在单线程 loop 中维护，逐步移除锁
	upWin = s.upHoldings*1.0 - s.upTotalCost - s.downTotalCost
	downWin = s.downHoldings*1.0 - s.upTotalCost - s.downTotalCost
	return
}

// minProfitTargetUSDC 以“已成对的份额”为基准设置目标利润（避免一开始就激进补齐到很高利润）
// target = profitTargetPerShare * min(upHoldings, downHoldings)
func (s *GridStrategy) minProfitTargetUSDC() float64 {
	if s.config == nil {
		return 0
	}
	perShare := float64(s.config.ProfitTarget) / 100.0
	if perShare < 0 {
		perShare = 0
	}
	pairs := math.Min(s.upHoldings, s.downHoldings)
	if pairs <= 0 {
		return 0
	}
	return perShare * pairs
}

func (s *GridStrategy) isInHedgeLockWindow(market *domain.Market) bool {
	if market == nil || market.Timestamp <= 0 {
		return false
	}
	now := time.Now().Unix()
	end := market.Timestamp + 900
	return now >= end-hedgeLockWindowSeconds && now < end
}

func (s *GridStrategy) hasAnyPendingHedgeOrder() bool {
	// 1) pendingHedgeOrders（策略内部待提交）
	if len(s.pendingHedgeOrders) > 0 {
		return true
	}
	// 2) 交易所侧已挂的对冲单（open/pending）
	for _, o := range s.getActiveOrders() {
		if o == nil {
			continue
		}
		if !o.IsEntryOrder && (o.Status == domain.OrderStatusOpen || o.Status == domain.OrderStatusPending) {
			return true
		}
	}
	return false
}

