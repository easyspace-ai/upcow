package capital

import (
	"context"
	"fmt"
	"math"

	"github.com/betbot/gobet/internal/common"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/services"
	"github.com/sirupsen/logrus"
)

var mLog = logrus.WithField("module", "merger")

type Merger struct {
	tradingService *services.TradingService
	config         ConfigInterface
	capital        *Capital
}

func NewMerger(ts *services.TradingService, cfg ConfigInterface) *Merger {
	return &Merger{tradingService: ts, config: cfg}
}

func (m *Merger) SetCapital(capital *Capital) {
	m.capital = capital
}

func (m *Merger) MergePreviousCycle(ctx context.Context, market *domain.Market) (float64, string, error) {
	if market == nil {
		return 0, "", fmt.Errorf("市场信息为空")
	}
	autoMerge := m.config.GetAutoMerge()
	if !autoMerge.Enabled {
		mLog.Debugf("⏸️ [Merger] 自动合并未启用: market=%s", market.Slug)
		return 0, "", nil
	}

	positions := m.tradingService.GetOpenPositionsForMarket(market.Slug)
	if len(positions) == 0 {
		mLog.Debugf("🔍 [Merger] 通过 market.Slug 未获取到持仓，尝试获取所有持仓: market=%s", market.Slug)
		allPositions := m.tradingService.GetAllPositions()
		for _, pos := range allPositions {
			if pos == nil || !pos.IsOpen() || pos.Size <= 0 {
				continue
			}
			if pos.Market != nil && pos.Market.ConditionID == market.ConditionID {
				positions = append(positions, pos)
			} else if pos.EntryOrder != nil && pos.EntryOrder.MarketSlug == market.Slug {
				positions = append(positions, pos)
			}
		}
		mLog.Infof("🔍 [Merger] 通过 ConditionID 匹配到 %d 个持仓: market=%s conditionID=%s",
			len(positions), market.Slug, market.ConditionID)
	}
	return m.mergePositions(ctx, market, positions, autoMerge)
}

func (m *Merger) MergePreviousCycleWithPositions(ctx context.Context, market *domain.Market, positions []*domain.Position) (float64, string, error) {
	if market == nil {
		return 0, "", fmt.Errorf("市场信息为空")
	}
	autoMerge := m.config.GetAutoMerge()
	if !autoMerge.Enabled {
		mLog.Debugf("⏸️ [Merger] 自动合并未启用: market=%s", market.Slug)
		return 0, "", nil
	}
	mLog.Infof("🔍 [Merger] 使用提供的持仓进行合并: market=%s positions=%d", market.Slug, len(positions))
	return m.mergePositions(ctx, market, positions, autoMerge)
}

func (m *Merger) mergePositions(ctx context.Context, market *domain.Market, positions []*domain.Position, autoMerge common.AutoMergeConfig) (float64, string, error) {
	var upSize, downSize float64
	for _, pos := range positions {
		if pos == nil || !pos.IsOpen() || pos.Size <= 0 {
			continue
		}
		if pos.TokenType == domain.TokenTypeUp {
			upSize += pos.Size
		} else if pos.TokenType == domain.TokenTypeDown {
			downSize += pos.Size
		}
	}

	completeSets := math.Min(upSize, downSize)
	mLog.Infof("🔍 [Merger] 检查合并条件: market=%s UP=%.4f DOWN=%.4f complete=%.4f enabled=%v minCompleteSets=%.4f mergeRatio=%.2f onlyIfNoOpenOrders=%v",
		market.Slug, upSize, downSize, completeSets, autoMerge.Enabled, autoMerge.MinCompleteSets, autoMerge.MergeRatio, autoMerge.OnlyIfNoOpenOrders)

	if completeSets <= 0 {
		mLog.Infof("⏸️ [Merger] 无 complete sets 可合并: market=%s UP=%.4f DOWN=%.4f", market.Slug, upSize, downSize)
		return 0, "", nil
	}
	if autoMerge.MinCompleteSets > 0 && completeSets < autoMerge.MinCompleteSets {
		mLog.Infof("⏸️ [Merger] complete sets 不足: market=%s complete=%.4f min=%.4f", market.Slug, completeSets, autoMerge.MinCompleteSets)
		return 0, "", nil
	}

	mergeAmount := completeSets * autoMerge.MergeRatio
	if mergeAmount > completeSets {
		mergeAmount = completeSets
	}
	if autoMerge.MaxCompleteSetsPerRun > 0 && mergeAmount > autoMerge.MaxCompleteSetsPerRun {
		mergeAmount = autoMerge.MaxCompleteSetsPerRun
	}
	if mergeAmount <= 0 {
		mLog.Debugf("⏸️ [Merger] 计算后的合并数量 <= 0: market=%s", market.Slug)
		return 0, "", nil
	}

	if autoMerge.OnlyIfNoOpenOrders {
		allOrders := m.tradingService.GetAllOrders()
		openOrderCount := 0
		for _, order := range allOrders {
			if order != nil && order.MarketSlug == market.Slug && order.IsOpen() {
				openOrderCount++
				mLog.Infof("⏸️ [Merger] 存在活跃订单，跳过合并: market=%s orderID=%s status=%s",
					market.Slug, order.OrderID, order.Status)
			}
		}
		if openOrderCount > 0 {
			mLog.Infof("⏸️ [Merger] 存在 %d 个活跃订单，跳过合并: market=%s", openOrderCount, market.Slug)
			return 0, "", nil
		}
		mLog.Debugf("✅ [Merger] 无活跃订单，可以合并: market=%s", market.Slug)
	}

	mLog.Infof("🔄 [Merger] 开始合并: market=%s amount=%.4f complete=%.4f", market.Slug, mergeAmount, completeSets)
	txHash, err := m.tradingService.MergeCompleteSetsViaRelayer(ctx, market.ConditionID, mergeAmount, autoMerge.Metadata)
	if err != nil {
		return 0, "", fmt.Errorf("合并失败: %w", err)
	}
	mLog.Infof("✅ [Merger] 合并已提交: market=%s amount=%.4f txHash=%s", market.Slug, mergeAmount, txHash)

	if m.capital != nil {
		m.capital.IncrementMergeCount()
	}
	if err := m.tradingService.RefreshBalance(ctx); err != nil {
		mLog.Warnf("⚠️ [Merger] 刷新余额失败: %v (不影响合并结果)", err)
	}
	return mergeAmount, txHash, nil
}

func (m *Merger) MergeCurrentCycle(ctx context.Context, market *domain.Market) (float64, string, error) {
	if market == nil {
		return 0, "", fmt.Errorf("市场信息为空")
	}
	autoMerge := m.config.GetAutoMerge()
	if !autoMerge.Enabled {
		mLog.Debugf("⏸️ [Merger] 自动合并未启用: market=%s", market.Slug)
		return 0, "", nil
	}

	positions := m.tradingService.GetOpenPositionsForMarket(market.Slug)
	mLog.Infof("🔍 [Merger] GetOpenPositionsForMarket 返回 %d 个持仓: market=%s", len(positions), market.Slug)
	
	// 详细记录每个匹配的持仓
	for i, pos := range positions {
		if pos != nil {
			mLog.Infof("🔍 [Merger] 匹配持仓[%d]: positionID=%s marketSlug=%s tokenType=%s size=%.4f status=%s",
				i, pos.ID, pos.MarketSlug, pos.TokenType, pos.Size, pos.Status)
		}
	}
	
	if len(positions) == 0 {
		mLog.Infof("🔍 [Merger] 通过 market.Slug 未获取到持仓，尝试获取所有持仓: market=%s", market.Slug)
		allPositions := m.tradingService.GetAllPositions()
		mLog.Infof("🔍 [Merger] GetAllPositions 返回 %d 个持仓（总计）", len(allPositions))
		
		// 详细记录所有持仓的信息
		for i, pos := range allPositions {
			if pos == nil {
				mLog.Debugf("🔍 [Merger] 持仓[%d] 为 nil", i)
				continue
			}
			
			// 记录持仓的详细信息
			positionMarketSlug := pos.MarketSlug
			if positionMarketSlug == "" && pos.Market != nil {
				positionMarketSlug = pos.Market.Slug
			}
			if positionMarketSlug == "" && pos.EntryOrder != nil {
				positionMarketSlug = pos.EntryOrder.MarketSlug
			}
			
			positionConditionID := ""
			if pos.Market != nil {
				positionConditionID = pos.Market.ConditionID
			}
			
			entryOrderMarketSlug := ""
			if pos.EntryOrder != nil {
				entryOrderMarketSlug = pos.EntryOrder.MarketSlug
			}
			
			mLog.Infof("🔍 [Merger] 持仓[%d] 详细信息: positionID=%s marketSlug=%s conditionID=%s entryOrderMarketSlug=%s tokenType=%s size=%.4f status=%s isOpen=%v targetMarketSlug=%s targetConditionID=%s",
				i, pos.ID, positionMarketSlug, positionConditionID, entryOrderMarketSlug,
				pos.TokenType, pos.Size, pos.Status, pos.IsOpen(), market.Slug, market.ConditionID)
			
			if !pos.IsOpen() {
				mLog.Debugf("🔍 [Merger] 持仓[%d] 被跳过: 状态不是 open (status=%s)", i, pos.Status)
				continue
			}
			if pos.Size <= 0 {
				mLog.Debugf("🔍 [Merger] 持仓[%d] 被跳过: 数量 <= 0 (size=%.4f)", i, pos.Size)
				continue
			}
			
			matched := false
			if pos.Market != nil && pos.Market.ConditionID == market.ConditionID {
				positions = append(positions, pos)
				matched = true
				mLog.Infof("✅ [Merger] 持仓[%d] 通过 ConditionID 匹配: positionID=%s conditionID=%s",
					i, pos.ID, pos.Market.ConditionID)
			} else if pos.EntryOrder != nil && pos.EntryOrder.MarketSlug == market.Slug {
				positions = append(positions, pos)
				matched = true
				mLog.Infof("✅ [Merger] 持仓[%d] 通过 EntryOrder.MarketSlug 匹配: positionID=%s entryOrderMarketSlug=%s",
					i, pos.ID, pos.EntryOrder.MarketSlug)
			} else if positionMarketSlug == market.Slug {
				// 额外检查：通过 position 的 MarketSlug 匹配
				positions = append(positions, pos)
				matched = true
				mLog.Infof("✅ [Merger] 持仓[%d] 通过 Position.MarketSlug 匹配: positionID=%s positionMarketSlug=%s",
					i, pos.ID, positionMarketSlug)
			}
			
			if !matched {
				mLog.Debugf("❌ [Merger] 持仓[%d] 未匹配: positionID=%s", i, pos.ID)
			}
		}
		mLog.Infof("🔍 [Merger] 通过 ConditionID/EntryOrder/PositionMarketSlug 匹配到 %d 个持仓: market=%s conditionID=%s",
			len(positions), market.Slug, market.ConditionID)
	}

	var upSize, downSize float64
	for _, pos := range positions {
		if pos == nil || !pos.IsOpen() || pos.Size <= 0 {
			continue
		}
		if pos.TokenType == domain.TokenTypeUp {
			upSize += pos.Size
		} else if pos.TokenType == domain.TokenTypeDown {
			downSize += pos.Size
		}
	}
	completeSets := math.Min(upSize, downSize)
	mLog.Infof("🔍 [Merger] 检查当前周期合并条件: market=%s UP=%.4f DOWN=%.4f complete=%.4f enabled=%v minCompleteSets=%.4f mergeRatio=%.2f onlyIfNoOpenOrders=%v",
		market.Slug, upSize, downSize, completeSets, autoMerge.Enabled, autoMerge.MinCompleteSets, autoMerge.MergeRatio, autoMerge.OnlyIfNoOpenOrders)

	if completeSets <= 0 {
		mLog.Debugf("⏸️ [Merger] 当前周期无 complete sets 可合并: market=%s UP=%.4f DOWN=%.4f", market.Slug, upSize, downSize)
		return 0, "", nil
	}
	if autoMerge.MinCompleteSets > 0 && completeSets < autoMerge.MinCompleteSets {
		mLog.Debugf("⏸️ [Merger] 当前周期 complete sets 不足: market=%s complete=%.4f min=%.4f", market.Slug, completeSets, autoMerge.MinCompleteSets)
		return 0, "", nil
	}

	mergeAmount := completeSets * autoMerge.MergeRatio
	if mergeAmount > completeSets {
		mergeAmount = completeSets
	}
	if autoMerge.MaxCompleteSetsPerRun > 0 && mergeAmount > autoMerge.MaxCompleteSetsPerRun {
		mergeAmount = autoMerge.MaxCompleteSetsPerRun
	}
	if mergeAmount <= 0 {
		mLog.Debugf("⏸️ [Merger] 计算后的合并数量 <= 0: market=%s", market.Slug)
		return 0, "", nil
	}

	if autoMerge.OnlyIfNoOpenOrders {
		allOrders := m.tradingService.GetAllOrders()
		openOrderCount := 0
		for _, order := range allOrders {
			if order != nil && order.MarketSlug == market.Slug && order.IsOpen() {
				openOrderCount++
				mLog.Debugf("⏸️ [Merger] 存在活跃订单，跳过合并: market=%s orderID=%s status=%s",
					market.Slug, order.OrderID, order.Status)
			}
		}
		if openOrderCount > 0 {
			mLog.Debugf("⏸️ [Merger] 存在 %d 个活跃订单，跳过合并: market=%s", openOrderCount, market.Slug)
			return 0, "", nil
		}
		mLog.Debugf("✅ [Merger] 无活跃订单，可以合并: market=%s", market.Slug)
	}

	mLog.Infof("🔄 [Merger] 开始合并当前周期: market=%s amount=%.4f complete=%.4f", market.Slug, mergeAmount, completeSets)
	txHash, err := m.tradingService.MergeCompleteSetsViaRelayer(ctx, market.ConditionID, mergeAmount, autoMerge.Metadata)
	if err != nil {
		return 0, "", fmt.Errorf("合并失败: %w", err)
	}
	mLog.Infof("✅ [Merger] 当前周期合并已提交: market=%s amount=%.4f txHash=%s", market.Slug, mergeAmount, txHash)

	if m.capital != nil {
		m.capital.IncrementMergeCount()
	}
	if err := m.tradingService.RefreshBalance(ctx); err != nil {
		mLog.Warnf("⚠️ [Merger] 刷新余额失败: %v (不影响合并结果)", err)
	}
	return mergeAmount, txHash, nil
}

