package grid

import (
	"context"
	"fmt"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
)

func (s *GridStrategy) CanOpenPosition(ctx context.Context, market *domain.Market) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// 检查是否已有活跃仓位
	if s.activePosition != nil {
		return false, nil
	}

	// 检查周期限制
	if s.roundsThisPeriod >= s.config.MaxRoundsPerPeriod {
		return false, nil
	}

	return true, nil
}
func (s *GridStrategy) CalculateEntry(ctx context.Context, market *domain.Market, price domain.Price) (*domain.Order, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.config == nil {
		return nil, fmt.Errorf("策略未初始化")
	}

	// 检查价格是否在网格层级上
	gridLevel := s.grid.GetLevel(price.Cents)
	if gridLevel == nil {
		return nil, fmt.Errorf("价格 %dc 不在网格层级上", price.Cents)
	}

	// 网格策略只买入 UP 币（YES token）
	assetID := market.YesAssetID

	// 计算订单金额和share数量
	_, share := s.calculateOrderSize(price)

	// 创建入场订单（买入 UP 币）
	order := &domain.Order{
		OrderID:      fmt.Sprintf("entry-up-%d-%d", price.Cents, time.Now().UnixNano()),
		AssetID:      assetID,
		Side:         types.SideBuy, // 买入
		Price:        price,
		Size:         share,
		GridLevel:    price.Cents,
		TokenType:    domain.TokenTypeUp, // UP 币
		IsEntryOrder: true,
		Status:       domain.OrderStatusPending,
		CreatedAt:    time.Now(),
	}

	return order, nil
}
func (s *GridStrategy) CalculateHedge(ctx context.Context, entryOrder *domain.Order) (*domain.Order, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.config == nil {
		return nil, fmt.Errorf("策略未初始化")
	}

	// 确保入场订单是 UP 币
	if entryOrder.TokenType != domain.TokenTypeUp {
		return nil, fmt.Errorf("网格策略只支持 UP 币入场订单")
	}

	// 注意：此方法无法获取 market.NoAssetID，需要传入 market 参数
	// 建议直接使用 handleGridLevelReached 方法，它已经包含了完整的逻辑
	//
	// 对冲价格计算逻辑：
	// DOWN 币价格 = 100 - UP币价格
	// 例如：UP 币 62分 → DOWN 币 38分
	return nil, fmt.Errorf("CalculateHedge 需要 market 参数来获取 DOWN 币资产 ID，请使用 handleGridLevelReached 方法")
}
func (s *GridStrategy) CheckStopLoss(ctx context.Context, position *domain.Position, currentPrice domain.Price) (*domain.Order, error) {
	if position == nil || !position.IsOpen() {
		return nil, nil
	}

	// 如果仓位已对冲，不需要止损（已锁定利润）
	if position.IsHedged() {
		return nil, nil
	}

	// 网格策略持有 UP 币和 DOWN 币的配对仓位
	// 止损时，需要卖出持有的代币

	loss := position.CalculateLoss(currentPrice)

	// 检查硬止损
	if currentPrice.Cents <= s.config.HardStopPrice {
		log.Warnf("触发硬止损: 价格=%dc <= 硬止损价格=%dc", currentPrice.Cents, s.config.HardStopPrice)

		// 止损：卖出 UP 币
		// 获取订单簿的买一价（快速止损）
		if s.tradingService == nil {
			return nil, fmt.Errorf("交易服务未设置")
		}

		// 获取订单簿的最佳买一价
		bestBid, _, err := s.tradingService.GetBestPrice(ctx, position.Market.YesAssetID)
		if err != nil {
			log.Errorf("获取订单簿失败: %v", err)
			// 如果获取失败，使用当前价格
			bestBid = currentPrice.ToDecimal()
		}

		if bestBid <= 0 {
			bestBid = currentPrice.ToDecimal()
		}

		sellPrice := domain.PriceFromDecimal(bestBid)
		log.Warnf("硬止损卖出，使用买一价 %.4f", bestBid)

		// 创建卖出 UP 币的订单
		sellOrder := &domain.Order{
			OrderID:      fmt.Sprintf("stop-loss-up-%d-%d", currentPrice.Cents, time.Now().UnixNano()),
			AssetID:      position.Market.YesAssetID, // UP 币资产 ID
			Side:         types.SideSell,             // 卖出
			Price:        sellPrice,
			Size:         position.Size,
			TokenType:    domain.TokenTypeUp,
			IsEntryOrder: false,
			Status:       domain.OrderStatusPending,
			CreatedAt:    time.Now(),
		}

		log.Warnf("创建硬止损卖出订单: 卖出UP币@%.4f, 数量=%.2f",
			sellPrice.ToDecimal(), position.Size)

		return sellOrder, nil
	}

	// 检查弹性止损
	if currentPrice.Cents > s.config.ElasticStopPrice &&
		currentPrice.Cents <= s.config.HardStopPrice &&
		loss >= s.config.MaxUnhedgedLoss {
		log.Warnf("触发弹性止损: 损失=%dc >= 最大未对冲损失=%dc", loss, s.config.MaxUnhedgedLoss)

		// 止损：卖出 UP 币
		// 获取订单簿的买一价（快速止损）
		if s.tradingService == nil {
			return nil, fmt.Errorf("交易服务未设置")
		}

		bestBid, _, err := s.tradingService.GetBestPrice(ctx, position.Market.YesAssetID)
		if err != nil {
			log.Errorf("获取订单簿失败: %v", err)
			bestBid = currentPrice.ToDecimal()
		}

		if bestBid <= 0 {
			bestBid = currentPrice.ToDecimal()
		}

		sellPrice := domain.PriceFromDecimal(bestBid)
		log.Warnf("弹性止损卖出，使用买一价 %.4f", bestBid)

		sellOrder := &domain.Order{
			OrderID:      fmt.Sprintf("elastic-stop-loss-up-%d-%d", currentPrice.Cents, time.Now().UnixNano()),
			AssetID:      position.Market.YesAssetID,
			Side:         types.SideSell,
			Price:        sellPrice,
			Size:         position.Size,
			TokenType:    domain.TokenTypeUp,
			IsEntryOrder: false,
			Status:       domain.OrderStatusPending,
			CreatedAt:    time.Now(),
		}

		log.Warnf("创建弹性止损卖出订单: 卖出UP币@%.4f, 数量=%.2f",
			sellPrice.ToDecimal(), position.Size)

		return sellOrder, nil
	}

	// 检查常规止损
	if currentPrice.Cents > s.config.HardStopPrice && loss >= s.config.MaxUnhedgedLoss {
		log.Warnf("触发常规止损: 损失=%dc >= 最大未对冲损失=%dc", loss, s.config.MaxUnhedgedLoss)

		// 止损：卖出 UP 币
		// 获取订单簿的买一价（快速止损）
		if s.tradingService == nil {
			return nil, fmt.Errorf("交易服务未设置")
		}

		bestBid, _, err := s.tradingService.GetBestPrice(ctx, position.Market.YesAssetID)
		if err != nil {
			log.Errorf("获取订单簿失败: %v", err)
			bestBid = currentPrice.ToDecimal()
		}

		if bestBid <= 0 {
			bestBid = currentPrice.ToDecimal()
		}

		sellPrice := domain.PriceFromDecimal(bestBid)
		log.Warnf("常规止损卖出，使用买一价 %.4f", bestBid)

		sellOrder := &domain.Order{
			OrderID:      fmt.Sprintf("normal-stop-loss-up-%d-%d", currentPrice.Cents, time.Now().UnixNano()),
			AssetID:      position.Market.YesAssetID,
			Side:         types.SideSell,
			Price:        sellPrice,
			Size:         position.Size,
			TokenType:    domain.TokenTypeUp,
			IsEntryOrder: false,
			Status:       domain.OrderStatusPending,
			CreatedAt:    time.Now(),
		}

		log.Warnf("创建常规止损卖出订单: 卖出UP币@%.4f, 数量=%.2f",
			sellPrice.ToDecimal(), position.Size)

		return sellOrder, nil
	}

	return nil, nil
}
func (s *GridStrategy) CheckTakeProfitStopLoss(ctx context.Context, position *domain.Position, currentPrice domain.Price) (*domain.Order, error) {
	// 已禁用止损，改用智能对冲算法
	// 智能对冲算法会在 checkAndSupplementHedge 中处理风险敞口
	return nil, nil
}
