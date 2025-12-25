package common

import (
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/services"
)

// InventoryCalculator 库存计算器
// 用于计算净持仓，支持库存偏斜机制
type InventoryCalculator struct {
	tradingService *services.TradingService
}

// NetPositionResult 净持仓计算结果
type NetPositionResult struct {
	UpInventory   float64 // UP 方向持仓（shares）
	DownInventory float64 // DOWN 方向持仓（shares）
	NetPosition   float64 // 净持仓 = UP - DOWN
}

// NewInventoryCalculator 创建库存计算器
func NewInventoryCalculator(tradingService *services.TradingService) *InventoryCalculator {
	return &InventoryCalculator{
		tradingService: tradingService,
	}
}

// CalculateNetPosition 计算净持仓
// marketSlug: 市场周期标识，如果为空则计算所有周期的持仓
// 返回：净持仓计算结果
//
// 计算逻辑：
// 1. UP 持仓 = sum(已成交的 Entry UP 订单) - sum(已成交的 Hedge UP 订单)
// 2. DOWN 持仓 = sum(已成交的 Entry DOWN 订单) - sum(已成交的 Hedge DOWN 订单)
// 3. 净持仓 = UP 持仓 - DOWN 持仓
//
// 说明：
// - Entry 订单：增加对应方向的持仓（IsEntryOrder = true）
// - Hedge 订单：减少对应方向的持仓（IsEntryOrder = false）
// - 只计算已成交的订单（Status = Filled）
func (ic *InventoryCalculator) CalculateNetPosition(marketSlug string) NetPositionResult {
	if ic.tradingService == nil {
		return NetPositionResult{}
	}

	// 获取所有活跃订单
	activeOrders := ic.tradingService.GetActiveOrders()

	var upInventory, downInventory float64

	// 遍历所有订单，计算持仓
	for _, order := range activeOrders {
		if order == nil {
			continue
		}

		// 只计算已成交的订单
		if order.Status != domain.OrderStatusFilled {
			continue
		}

		// 如果指定了 marketSlug，只计算当前周期的订单
		if marketSlug != "" && order.MarketSlug != marketSlug {
			continue
		}

		// 获取订单的实际成交数量
		filledSize := order.FilledSize
		if filledSize <= 0 {
			continue
		}

		// 判断订单类型和方向
		isEntryOrder := order.IsEntryOrder
		tokenType := order.TokenType

		// 计算持仓
		// Entry 订单：增加对应方向的持仓
		// Hedge 订单：减少对应方向的持仓（对冲）
		if tokenType == domain.TokenTypeUp {
			if isEntryOrder {
				// Entry UP 订单：增加 UP 持仓
				upInventory += filledSize
			} else {
				// Hedge UP 订单：减少 UP 持仓（对冲）
				// 注意：Hedge UP 订单通常不会出现，因为 Hedge 通常是 DOWN 方向
				// 但为了完整性，仍然处理这种情况
				upInventory -= filledSize
			}
		} else if tokenType == domain.TokenTypeDown {
			if isEntryOrder {
				// Entry DOWN 订单：增加 DOWN 持仓
				downInventory += filledSize
			} else {
				// Hedge DOWN 订单：减少 DOWN 持仓（对冲）
				// 注意：Hedge DOWN 订单通常不会出现，因为 Hedge 通常是 UP 方向
				// 但为了完整性，仍然处理这种情况
				downInventory -= filledSize
			}
		}
	}

	// 计算净持仓
	netPosition := upInventory - downInventory

	return NetPositionResult{
		UpInventory:   upInventory,
		DownInventory: downInventory,
		NetPosition:   netPosition,
	}
}

// ShouldSkipDirection 判断是否应该跳过某个方向的交易
// netPosition: 净持仓
// threshold: 阈值（正数）
// direction: 交易方向（UP 或 DOWN）
// 返回：true 表示应该跳过，false 表示允许交易
//
// 逻辑：
// - 如果 netPosition > threshold && direction == UP：跳过 UP 方向的交易
// - 如果 netPosition < -threshold && direction == DOWN：跳过 DOWN 方向的交易
func ShouldSkipDirection(netPosition, threshold float64, direction domain.TokenType) bool {
	if threshold <= 0 {
		return false // 如果阈值 <= 0，不跳过任何方向
	}

	if direction == domain.TokenTypeUp {
		// 如果净持仓偏向 UP 方向（> threshold），跳过 UP 方向的交易
		return netPosition > threshold
	} else if direction == domain.TokenTypeDown {
		// 如果净持仓偏向 DOWN 方向（< -threshold），跳过 DOWN 方向的交易
		return netPosition < -threshold
	}

	return false
}

// CheckInventorySkew 检查库存偏斜（库存偏斜机制的完整检查）
// marketSlug: 市场周期标识
// threshold: 净持仓阈值
// direction: 交易方向
// 返回：true 表示应该跳过交易（库存偏斜），false 表示允许交易
func (ic *InventoryCalculator) CheckInventorySkew(marketSlug string, threshold float64, direction domain.TokenType) bool {
	if ic.tradingService == nil {
		return false
	}

	// 计算净持仓
	result := ic.CalculateNetPosition(marketSlug)

	// 检查是否应该跳过该方向
	return ShouldSkipDirection(result.NetPosition, threshold, direction)
}

