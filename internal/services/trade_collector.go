package services

import (
	"fmt"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/betbot/gobet/internal/domain"
)

var tradeCollectorLog = logrus.WithField("component", "trade_collector")

// TradeCollector 交易收集器（重构后：只负责发送命令到 OrderEngine）
// 所有交易处理逻辑已移至 OrderEngine.handleProcessTrade
type TradeCollector struct {
	orderEngine *OrderEngine // OrderEngine 引用，用于发送命令
	genProvider func() int64 // 周期代号提供者（可选；不提供则默认 1）
}

// NewTradeCollector 创建交易收集器（重构后：只需要 OrderEngine）
func NewTradeCollector(orderEngine *OrderEngine) *TradeCollector {
	return &TradeCollector{
		orderEngine: orderEngine,
		genProvider: func() int64 { return 1 },
	}
}

// SetGenerationProvider 设置周期代号提供者（建议由 TradingService 注入）。
func (c *TradeCollector) SetGenerationProvider(p func() int64) {
	if c == nil || p == nil {
		return
	}
	c.genProvider = p
}

// ProcessTrade 处理交易（发送命令到 OrderEngine）
// 所有处理逻辑已移至 OrderEngine.handleProcessTrade
func (c *TradeCollector) ProcessTrade(trade *domain.Trade) bool {
	if c.orderEngine == nil {
		tradeCollectorLog.Errorf("OrderEngine 未设置，无法处理交易")
		return false
	}

	// 发送 ProcessTradeCommand 到 OrderEngine
	cmd := &ProcessTradeCommand{
		id:    fmt.Sprintf("process_trade_%d", time.Now().UnixNano()),
		Gen:   c.genProvider(),
		Trade: trade,
	}
	c.orderEngine.SubmitCommand(cmd)

	tradeCollectorLog.Debugf("交易已发送到 OrderEngine: tradeID=%s, orderID=%s", trade.ID, trade.OrderID)
	return true
}

// Process 处理待处理的交易（已废弃，现在由 OrderEngine 自动处理）
// 保留此方法用于向后兼容
func (c *TradeCollector) Process() bool {
	// 此方法已废弃，OrderEngine 会自动处理待处理的交易
	// 在 handleProcessTrade 中会调用 processPendingTrades()
	tradeCollectorLog.Debugf("Process() 已废弃，现在由 OrderEngine 自动处理待处理的交易")
	return false
}

// OrderStore 订单存储（保留用于向后兼容，但已由 OrderEngine 管理）
// 注意：OrderEngine 内部已有 orderStore，此类型保留仅用于兼容
type OrderStore struct {
	// 已废弃：订单现在由 OrderEngine 管理
	// 保留此类型仅用于向后兼容
}

// NewOrderStore 创建订单存储（已废弃，保留用于向后兼容）
func NewOrderStore() *OrderStore {
	return &OrderStore{}
}

// Add 添加订单（已废弃，保留用于向后兼容）
func (s *OrderStore) Add(order *domain.Order) {
	// 已废弃：订单现在由 OrderEngine 管理
}

// Exists 检查订单是否存在（已废弃，保留用于向后兼容）
func (s *OrderStore) Exists(orderID string) bool {
	// 已废弃：订单现在由 OrderEngine 管理
	return false
}

// Get 获取订单（已废弃，保留用于向后兼容）
func (s *OrderStore) Get(orderID string) (*domain.Order, bool) {
	// 已废弃：订单现在由 OrderEngine 管理
	return nil, false
}

// TradeStore 交易存储（已废弃，现在由 OrderEngine 管理）
type TradeStore struct {
	// 已废弃：待处理的交易现在由 OrderEngine.pendingTrades 管理
}

// NewTradeStore 创建交易存储（已废弃，保留用于向后兼容）
func NewTradeStore() *TradeStore {
	return &TradeStore{}
}

// Add 添加交易（已废弃，保留用于向后兼容）
func (s *TradeStore) Add(trade *domain.Trade) {
	// 已废弃：待处理的交易现在由 OrderEngine 管理
}

// Remove 移除交易（已废弃，保留用于向后兼容）
func (s *TradeStore) Remove(tradeID string) {
	// 已废弃：待处理的交易现在由 OrderEngine 管理
}
