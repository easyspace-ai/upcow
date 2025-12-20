package services

import (
	"context"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
)

var orderEngineLog = logrus.WithField("component", "order_engine")

// OrderCommand 订单命令接口
type OrderCommand interface {
	CommandType() OrderCommandType
	ID() string // 命令唯一ID，用于追踪和去重
}

// OrderCommandType 命令类型
type OrderCommandType string

const (
	CmdPlaceOrder     OrderCommandType = "place_order"
	CmdCancelOrder    OrderCommandType = "cancel_order"
	CmdUpdateOrder    OrderCommandType = "update_order"
	CmdProcessTrade   OrderCommandType = "process_trade"
	CmdUpdateBalance  OrderCommandType = "update_balance"
	CmdCreatePosition OrderCommandType = "create_position"
	CmdUpdatePosition OrderCommandType = "update_position"
	CmdClosePosition  OrderCommandType = "close_position"
	CmdQueryState     OrderCommandType = "query_state" // 查询状态（只读）
)

// PlaceOrderCommand 下单命令
type PlaceOrderCommand struct {
	id      string
	Order   *domain.Order
	Reply   chan *PlaceOrderResult
	Context context.Context
}

func (c *PlaceOrderCommand) CommandType() OrderCommandType { return CmdPlaceOrder }
func (c *PlaceOrderCommand) ID() string                     { return c.id }

// PlaceOrderResult 下单结果
type PlaceOrderResult struct {
	Order *domain.Order
	Error error
}

// CancelOrderCommand 取消订单命令
type CancelOrderCommand struct {
	id      string
	OrderID string
	Reply   chan error
	Context context.Context
}

func (c *CancelOrderCommand) CommandType() OrderCommandType { return CmdCancelOrder }
func (c *CancelOrderCommand) ID() string                    { return c.id }

// UpdateOrderCommand 更新订单命令
type UpdateOrderCommand struct {
	id    string
	Order *domain.Order
	Error error
}

func (c *UpdateOrderCommand) CommandType() OrderCommandType { return CmdUpdateOrder }
func (c *UpdateOrderCommand) ID() string                       { return c.id }

// ProcessTradeCommand 处理交易命令
type ProcessTradeCommand struct {
	id    string
	Trade *domain.Trade
}

func (c *ProcessTradeCommand) CommandType() OrderCommandType { return CmdProcessTrade }
func (c *ProcessTradeCommand) ID() string                     { return c.id }

// UpdateBalanceCommand 更新余额命令
type UpdateBalanceCommand struct {
	id       string
	Balance  float64
	Currency string
}

func (c *UpdateBalanceCommand) CommandType() OrderCommandType { return CmdUpdateBalance }
func (c *UpdateBalanceCommand) ID() string                     { return c.id }

// CreatePositionCommand 创建仓位命令
type CreatePositionCommand struct {
	id       string
	Position *domain.Position
	Reply    chan error
}

func (c *CreatePositionCommand) CommandType() OrderCommandType { return CmdCreatePosition }
func (c *CreatePositionCommand) ID() string                     { return c.id }

// UpdatePositionCommand 更新仓位命令
type UpdatePositionCommand struct {
	id        string
	PositionID string
	Updater   func(*domain.Position)
	Reply     chan error
}

func (c *UpdatePositionCommand) CommandType() OrderCommandType { return CmdUpdatePosition }
func (c *UpdatePositionCommand) ID() string                    { return c.id }

// ClosePositionCommand 关闭仓位命令
type ClosePositionCommand struct {
	id        string
	PositionID string
	ExitPrice domain.Price
	ExitOrder *domain.Order
	Reply     chan error
}

func (c *ClosePositionCommand) CommandType() OrderCommandType { return CmdClosePosition }
func (c *ClosePositionCommand) ID() string                    { return c.id }

// QueryStateCommand 查询状态命令
type QueryStateCommand struct {
	id      string
	Query   QueryType
	Reply   chan *StateSnapshot
}

func (c *QueryStateCommand) CommandType() OrderCommandType { return CmdQueryState }
func (c *QueryStateCommand) ID() string                     { return c.id }

// QueryType 查询类型
type QueryType string

const (
	QueryAllOrders    QueryType = "all_orders"
	QueryOpenOrders   QueryType = "open_orders"
	QueryAllPositions QueryType = "all_positions"
	QueryOpenPositions QueryType = "open_positions"
	QueryBalance      QueryType = "balance"
	QueryOrder        QueryType = "order"
	QueryPosition     QueryType = "position"
)

// StateSnapshot 状态快照
type StateSnapshot struct {
	Balance    float64
	Orders     []*domain.Order
	Positions  []*domain.Position
	OpenOrders []*domain.Order
	Order      *domain.Order
	Position   *domain.Position
	Error      error
}

// EngineStats 引擎统计
type EngineStats struct {
	TotalCommands   int64
	ProcessedOrders int64
	ProcessedTrades int64
	Errors          int64
}

// OrderEngine 订单引擎（Actor 模型）
type OrderEngine struct {
	// 命令通道（唯一入口，缓冲1000避免阻塞）
	cmdChan chan OrderCommand

	// 状态（在单一 goroutine 中维护，无锁）
	balance       float64                      // 可用资金（USDC）
	positions     map[string]*domain.Position // 当前仓位
	openOrders    map[string]*domain.Order    // 未完成订单
	orderStore    map[string]*domain.Order    // 所有订单（包括已成交的）
	pendingTrades map[string]*domain.Trade    // 待处理的交易（订单还未创建时）

	// 配置
	MinOrderSize float64 // 导出以便 TradingService 访问
	dryRun       bool

	// 外部依赖（IO 操作，异步执行）
	ioExecutor *IOExecutor

	// 回调
	orderHandlers []OrderUpdateHandler

	// 上下文
	ctx    context.Context
	cancel context.CancelFunc

	// 统计
	stats *EngineStats
}

// NewOrderEngine 创建新的订单引擎
func NewOrderEngine(ioExecutor *IOExecutor, minOrderSize float64, dryRun bool) *OrderEngine {
	return &OrderEngine{
		cmdChan:       make(chan OrderCommand, 1000), // 缓冲1000避免阻塞
		balance:       0,
		positions:     make(map[string]*domain.Position),
		openOrders:    make(map[string]*domain.Order),
		orderStore:    make(map[string]*domain.Order),
		pendingTrades: make(map[string]*domain.Trade),
		MinOrderSize:  minOrderSize,
		dryRun:        dryRun,
		ioExecutor:    ioExecutor,
		orderHandlers: make([]OrderUpdateHandler, 0),
		stats:         &EngineStats{},
	}
}

// SubmitCommand 提交命令到引擎（线程安全）
func (e *OrderEngine) SubmitCommand(cmd OrderCommand) {
	select {
	case e.cmdChan <- cmd:
		// 命令已提交
	default:
		orderEngineLog.Errorf("命令通道已满，命令被丢弃: %s, ID: %s", cmd.CommandType(), cmd.ID())
	}
}

// OnOrderUpdate 注册订单更新回调
func (e *OrderEngine) OnOrderUpdate(handler OrderUpdateHandler) {
	// 通过命令注册回调（确保线程安全）
	cmd := &RegisterHandlerCommand{
		id:      fmt.Sprintf("register_handler_%d", time.Now().UnixNano()),
		Handler: handler,
	}
	e.SubmitCommand(cmd)
}

// Run 启动订单引擎主循环（必须在独立 goroutine 中运行）
func (e *OrderEngine) Run(ctx context.Context) {
	e.ctx, e.cancel = context.WithCancel(ctx)
	defer e.cancel()

	orderEngineLog.Info("🚀 OrderEngine 启动")

	for {
		select {
		case cmd := <-e.cmdChan:
			e.stats.TotalCommands++
			e.handleCommand(cmd)

		case <-e.ctx.Done():
			orderEngineLog.Info("🛑 OrderEngine 停止")
			return
		}
	}
}

// handleCommand 处理命令（顺序执行，无锁）
func (e *OrderEngine) handleCommand(cmd OrderCommand) {
	defer func() {
		if r := recover(); r != nil {
			e.stats.Errors++
			orderEngineLog.Errorf("❌ OrderEngine 处理命令时发生 panic: %v, 命令类型: %s, ID: %s",
				r, cmd.CommandType(), cmd.ID())
		}
	}()

	switch cmd.CommandType() {
	case CmdPlaceOrder:
		e.handlePlaceOrder(cmd.(*PlaceOrderCommand))
	case CmdCancelOrder:
		e.handleCancelOrder(cmd.(*CancelOrderCommand))
	case CmdUpdateOrder:
		e.handleUpdateOrder(cmd.(*UpdateOrderCommand))
	case CmdProcessTrade:
		e.handleProcessTrade(cmd.(*ProcessTradeCommand))
	case CmdUpdateBalance:
		e.handleUpdateBalance(cmd.(*UpdateBalanceCommand))
	case CmdCreatePosition:
		e.handleCreatePosition(cmd.(*CreatePositionCommand))
	case CmdUpdatePosition:
		e.handleUpdatePosition(cmd.(*UpdatePositionCommand))
	case CmdClosePosition:
		e.handleClosePosition(cmd.(*ClosePositionCommand))
	case CmdQueryState:
		e.handleQueryState(cmd.(*QueryStateCommand))
	case CmdRegisterHandler:
		e.handleRegisterHandler(cmd.(*RegisterHandlerCommand))
	case CmdQueryStats:
		e.handleQueryStats(cmd.(*QueryStatsCommand))
	default:
		orderEngineLog.Errorf("未知命令类型: %s", cmd.CommandType())
	}
}

// RegisterHandlerCommand 注册处理器命令
type RegisterHandlerCommand struct {
	id      string
	Handler OrderUpdateHandler
}

func (c *RegisterHandlerCommand) CommandType() OrderCommandType { return CmdRegisterHandler }
func (c *RegisterHandlerCommand) ID() string                     { return c.id }

const CmdRegisterHandler OrderCommandType = "register_handler"

// GetStats 获取引擎统计信息（线程安全，返回快照）
func (e *OrderEngine) GetStats() *EngineStats {
	reply := make(chan *EngineStats, 1)
	cmd := &QueryStatsCommand{
		id:    fmt.Sprintf("query_stats_%d", time.Now().UnixNano()),
		Reply: reply,
	}
	e.SubmitCommand(cmd)

	select {
	case stats := <-reply:
		return stats
	case <-time.After(5 * time.Second):
		return &EngineStats{} // 超时返回空统计
	}
}

// QueryStatsCommand 查询统计命令
type QueryStatsCommand struct {
	id    string
	Reply chan *EngineStats
}

func (c *QueryStatsCommand) CommandType() OrderCommandType { return CmdQueryStats }
func (c *QueryStatsCommand) ID() string                    { return c.id }

const CmdQueryStats OrderCommandType = "query_stats"

// handlePlaceOrder 处理下单命令
func (e *OrderEngine) handlePlaceOrder(cmd *PlaceOrderCommand) {
	// 1. 风控校验（在状态循环中同步执行）
	if err := e.validatePlaceOrder(cmd.Order); err != nil {
		select {
		case cmd.Reply <- &PlaceOrderResult{Error: err}:
		default:
		}
		return
	}

	// 2. 更新状态（预留资金）
	requiredAmount := cmd.Order.Price.ToDecimal() * cmd.Order.Size
	// 在纸模式下跳过余额检查，或者设置一个很大的初始余额
	if !e.dryRun && e.balance < requiredAmount {
		select {
		case cmd.Reply <- &PlaceOrderResult{
			Error: fmt.Errorf("余额不足: 需要 %.2f USDC，当前余额 %.2f USDC",
				requiredAmount, e.balance),
		}:
		default:
		}
		return
	}

	// 预留资金（纸模式下不实际扣除）
	if !e.dryRun {
		e.balance -= requiredAmount
	}

	// 3. 添加到订单列表
	if cmd.Order.OrderID == "" {
		cmd.Order.OrderID = fmt.Sprintf("order_%d", time.Now().UnixNano())
	}
	cmd.Order.Status = domain.OrderStatusPending
	cmd.Order.CreatedAt = time.Now()
	e.openOrders[cmd.Order.OrderID] = cmd.Order
	e.orderStore[cmd.Order.OrderID] = cmd.Order

	// 4. 异步执行 IO 操作（不阻塞状态循环）
	go e.ioExecutor.PlaceOrderAsync(cmd.Context, cmd.Order, func(result *PlaceOrderResult) {
		// IO 完成后，发送 UpdateOrderCommand 更新状态
		// 确保 Order 不为 nil（即使失败也会返回原始订单）
		orderToUpdate := result.Order
		if orderToUpdate == nil {
			// 如果 result.Order 为 nil，使用原始订单
			orderToUpdate = cmd.Order
		}
		updateCmd := &UpdateOrderCommand{
			id:    fmt.Sprintf("update_%s", cmd.Order.OrderID),
			Order: orderToUpdate,
			Error: result.Error,
		}
		e.SubmitCommand(updateCmd)

		// 回复原始命令
		select {
		case cmd.Reply <- result:
		default:
		}
	})

	// 5. 立即返回（不等待 IO）
	select {
	case cmd.Reply <- &PlaceOrderResult{Order: cmd.Order}:
	default:
	}
}

// validatePlaceOrder 验证下单请求
func (e *OrderEngine) validatePlaceOrder(order *domain.Order) error {
	if order == nil {
		return fmt.Errorf("订单不能为空")
	}
	if order.AssetID == "" {
		return fmt.Errorf("资产ID不能为空")
	}
	if order.Size <= 0 {
		return fmt.Errorf("订单数量必须大于0")
	}
	if order.Price.Cents <= 0 {
		return fmt.Errorf("订单价格必须大于0")
	}

	// 检查最小订单金额
	orderAmount := order.Price.ToDecimal() * order.Size
	if orderAmount < e.MinOrderSize {
		return fmt.Errorf("订单金额 %.2f USDC 小于最小要求 %.2f USDC", orderAmount, e.MinOrderSize)
	}

	return nil
}

// handleCancelOrder 处理取消订单命令
func (e *OrderEngine) handleCancelOrder(cmd *CancelOrderCommand) {
	// 检查订单是否存在（先检查活跃订单，再检查订单存储）
	order, exists := e.openOrders[cmd.OrderID]
	if !exists {
		// 检查订单存储（可能订单已成交或已取消）
		if storedOrder, storedExists := e.orderStore[cmd.OrderID]; storedExists {
			if storedOrder.Status == domain.OrderStatusFilled {
				select {
				case cmd.Reply <- fmt.Errorf("订单已成交，无法取消: %s", cmd.OrderID):
				default:
				}
				return
			}
			if storedOrder.Status == domain.OrderStatusCanceled {
				select {
				case cmd.Reply <- nil: // 订单已取消，返回成功
				default:
				}
				return
			}
		}
		select {
		case cmd.Reply <- fmt.Errorf("订单不存在: %s", cmd.OrderID):
		default:
		}
		return
	}

	// 更新状态：标记为取消中
	order.Status = domain.OrderStatusCanceled
	e.orderStore[order.OrderID] = order
	e.emitOrderUpdate(order)

	// 异步执行 IO 操作（结果回流到状态循环）
	go e.ioExecutor.CancelOrderAsync(cmd.Context, cmd.OrderID, func(err error) {
		updateCmd := &UpdateOrderCommand{
			id:    fmt.Sprintf("cancel_result_%s", cmd.OrderID),
			Order: order,
			Error: err,
		}
		e.SubmitCommand(updateCmd)

		select {
		case cmd.Reply <- err:
		default:
		}
	})
}

// handleUpdateOrder 处理更新订单命令（IO 操作完成后调用）
func (e *OrderEngine) handleUpdateOrder(cmd *UpdateOrderCommand) {
	// CancelOrderAsync 也复用 UpdateOrderCommand 回流：这里区分“取消失败”与“下单失败”
	if cmd.Error != nil && cmd.Order != nil && cmd.Order.Status == domain.OrderStatusCanceled {
		// 取消失败：恢复为 open，并保留在 openOrders
		if existing, ok := e.openOrders[cmd.Order.OrderID]; ok {
			existing.Status = domain.OrderStatusOpen
			e.orderStore[existing.OrderID] = existing
			e.emitOrderUpdate(existing)
		}
		e.stats.Errors++
		return
	}

	if cmd.Error != nil {
		// IO 操作失败，标记订单为失败状态
		order := cmd.Order
		if order == nil {
			orderEngineLog.Errorf("订单IO操作失败，但订单为nil: %v", cmd.Error)
			return
		}
		
		// 标记订单为失败状态
		order.Status = domain.OrderStatusFailed
		
		// 从活跃订单中查找并更新
		if existingOrder, exists := e.openOrders[order.OrderID]; exists {
			existingOrder.Status = domain.OrderStatusFailed
			// 释放预留资金
			requiredAmount := existingOrder.Price.ToDecimal() * existingOrder.Size
			e.balance += requiredAmount
			// 从活跃订单中移除
			delete(e.openOrders, order.OrderID)
			order = existingOrder
		}
		
		// 更新订单存储（保存失败状态）
		e.orderStore[order.OrderID] = order
		
		// 触发回调，通知策略订单已失败
		e.emitOrderUpdate(order)
		
		orderEngineLog.Errorf("订单IO操作失败: orderID=%s, error=%v", order.OrderID, cmd.Error)
		return
	}

	// IO 操作成功，更新订单状态
	order := cmd.Order
	if existingOrder, exists := e.openOrders[order.OrderID]; exists {
		// 更新现有订单
		existingOrder.Status = order.Status
		existingOrder.OrderID = order.OrderID
		if order.FilledSize > 0 {
			existingOrder.FilledSize = order.FilledSize
		}
		if order.FilledAt != nil {
			existingOrder.FilledAt = order.FilledAt
		}
		order = existingOrder
	} else {
		// 新订单，添加到存储
		e.openOrders[order.OrderID] = order
	}

	// 更新订单存储
	e.orderStore[order.OrderID] = order

	// 如果订单已成交/已取消，从活跃订单中移除
	if order.Status == domain.OrderStatusFilled || order.Status == domain.OrderStatusCanceled {
		delete(e.openOrders, order.OrderID)
	}

	// 触发回调
	e.emitOrderUpdate(order)

	e.stats.ProcessedOrders++
}

// handleProcessTrade 处理交易命令
func (e *OrderEngine) handleProcessTrade(cmd *ProcessTradeCommand) {
	trade := cmd.Trade

	// 1. 检查订单是否存在
	order, exists := e.orderStore[trade.OrderID]
	if !exists {
		// 订单不存在，保存交易等待订单
		e.pendingTrades[trade.ID] = trade
		orderEngineLog.Debugf("订单不存在，保存交易等待订单: tradeID=%s, orderID=%s", trade.ID, trade.OrderID)
		return
	}

	// 2. 更新订单状态
	// 支持部分成交：累计 FilledSize，只有 FilledSize >= Size 才标记为 filled
	if trade.Size > 0 {
		order.FilledSize += trade.Size
		if order.FilledSize >= order.Size && order.Size > 0 {
			order.Status = domain.OrderStatusFilled
			now := time.Now()
			order.FilledAt = &now
			order.FilledSize = order.Size
		} else {
			// 仍未完全成交
			if order.Status != domain.OrderStatusFilled {
				order.Status = domain.OrderStatusPartial
			}
		}
	}

	// 3. 从活跃订单中移除
	delete(e.openOrders, order.OrderID)

	// 4. 更新仓位
	e.updatePositionFromTrade(trade, order)

	// 5. 处理待处理的交易（如果有订单创建前的交易）
	e.processPendingTrades()

	// 6. 触发回调
	e.emitOrderUpdate(order)

	e.stats.ProcessedTrades++
	orderEngineLog.Infof("✅ 交易已处理: tradeID=%s, orderID=%s, size=%.2f", trade.ID, trade.OrderID, trade.Size)
}

// updatePositionFromTrade 从交易更新仓位
func (e *OrderEngine) updatePositionFromTrade(trade *domain.Trade, order *domain.Order) {
	// 查找或创建仓位
	var position *domain.Position
	positionID := e.getPositionID(order)

	if pos, exists := e.positions[positionID]; exists {
		position = pos
	} else {
		// 创建新仓位
		position = &domain.Position{
			ID:        positionID,
			Market:    trade.Market,
			EntryOrder: order,
			EntryPrice: trade.Price,
			EntryTime:  trade.Time,
			Size:      0,
			TokenType: trade.TokenType,
			Status:    domain.PositionStatusOpen,
		}
		e.positions[positionID] = position
	}

	// 更新仓位大小
	if trade.Side == types.SideBuy {
		// 买入交易：增加仓位
		position.Size += trade.Size
	} else {
		// 卖出交易：减少仓位
		position.Size -= trade.Size
		if position.Size < 0 {
			position.Size = 0
		}
	}

	// 更新入场订单
	if position.EntryOrder == nil {
		position.EntryOrder = order
		position.EntryPrice = trade.Price
		position.EntryTime = trade.Time
	}
}

// getPositionID 获取仓位ID
func (e *OrderEngine) getPositionID(order *domain.Order) string {
	return fmt.Sprintf("%s_%s", order.AssetID, order.TokenType)
}

// processPendingTrades 处理待处理的交易
func (e *OrderEngine) processPendingTrades() {
	var tradesToProcess []*domain.Trade
	for _, trade := range e.pendingTrades {
		if _, exists := e.orderStore[trade.OrderID]; exists {
			tradesToProcess = append(tradesToProcess, trade)
		}
	}

	for _, trade := range tradesToProcess {
		delete(e.pendingTrades, trade.ID)
		// 重新处理交易
		cmd := &ProcessTradeCommand{
			id:    fmt.Sprintf("process_trade_%d", time.Now().UnixNano()),
			Trade: trade,
		}
		e.handleProcessTrade(cmd)
	}
}

// handleUpdateBalance 处理更新余额命令
func (e *OrderEngine) handleUpdateBalance(cmd *UpdateBalanceCommand) {
	if cmd.Currency == "USDC" || cmd.Currency == "" {
		e.balance = cmd.Balance
		orderEngineLog.Debugf("余额已更新: %.2f USDC", e.balance)
	}
}

// handleCreatePosition 处理创建仓位命令
func (e *OrderEngine) handleCreatePosition(cmd *CreatePositionCommand) {
	if cmd.Position.ID == "" {
		select {
		case cmd.Reply <- fmt.Errorf("仓位ID不能为空"):
		default:
		}
		return
	}

	if _, exists := e.positions[cmd.Position.ID]; exists {
		select {
		case cmd.Reply <- fmt.Errorf("仓位已存在: %s", cmd.Position.ID):
		default:
		}
		return
	}

	cmd.Position.Status = domain.PositionStatusOpen
	e.positions[cmd.Position.ID] = cmd.Position

	orderEngineLog.Infof("创建仓位: positionID=%s", cmd.Position.ID)

	select {
	case cmd.Reply <- nil:
	default:
	}
}

// handleUpdatePosition 处理更新仓位命令
func (e *OrderEngine) handleUpdatePosition(cmd *UpdatePositionCommand) {
	position, exists := e.positions[cmd.PositionID]
	if !exists {
		select {
		case cmd.Reply <- fmt.Errorf("仓位不存在: %s", cmd.PositionID):
		default:
		}
		return
	}

	if cmd.Updater != nil {
		cmd.Updater(position)
	}

	orderEngineLog.Debugf("更新仓位: positionID=%s", cmd.PositionID)

	select {
	case cmd.Reply <- nil:
	default:
	}
}

// handleClosePosition 处理关闭仓位命令
func (e *OrderEngine) handleClosePosition(cmd *ClosePositionCommand) {
	position, exists := e.positions[cmd.PositionID]
	if !exists {
		select {
		case cmd.Reply <- fmt.Errorf("仓位不存在: %s", cmd.PositionID):
		default:
		}
		return
	}

	if !position.IsOpen() {
		select {
		case cmd.Reply <- fmt.Errorf("仓位已关闭: %s", cmd.PositionID):
		default:
		}
		return
	}

	now := time.Now()
	position.ExitPrice = &cmd.ExitPrice
	position.ExitTime = &now
	position.ExitOrder = cmd.ExitOrder
	position.Status = domain.PositionStatusClosed

	orderEngineLog.Infof("关闭仓位: positionID=%s, exitPrice=%.4f",
		cmd.PositionID, cmd.ExitPrice.ToDecimal())

	select {
	case cmd.Reply <- nil:
	default:
	}
}

// handleQueryState 处理查询状态命令
func (e *OrderEngine) handleQueryState(cmd *QueryStateCommand) {
	snapshot := &StateSnapshot{
		Balance: e.balance,
	}

	switch cmd.Query {
	case QueryAllOrders:
		orders := make([]*domain.Order, 0, len(e.orderStore))
		for _, order := range e.orderStore {
			orders = append(orders, order)
		}
		snapshot.Orders = orders

	case QueryOpenOrders:
		orders := make([]*domain.Order, 0, len(e.openOrders))
		for _, order := range e.openOrders {
			orders = append(orders, order)
		}
		snapshot.OpenOrders = orders

	case QueryAllPositions:
		positions := make([]*domain.Position, 0, len(e.positions))
		for _, position := range e.positions {
			positions = append(positions, position)
		}
		snapshot.Positions = positions

	case QueryOpenPositions:
		positions := make([]*domain.Position, 0)
		for _, position := range e.positions {
			if position.IsOpen() {
				positions = append(positions, position)
			}
		}
		snapshot.Positions = positions

	case QueryBalance:
		// Balance already set

	case QueryOrder:
		// 需要额外的参数，这里简化处理
		snapshot.Error = fmt.Errorf("QueryOrder 需要额外的订单ID参数")

	case QueryPosition:
		// 需要额外的参数，这里简化处理
		snapshot.Error = fmt.Errorf("QueryPosition 需要额外的仓位ID参数")
	}

	select {
	case cmd.Reply <- snapshot:
	default:
	}
}

// handleRegisterHandler 处理注册处理器命令
func (e *OrderEngine) handleRegisterHandler(cmd *RegisterHandlerCommand) {
	e.orderHandlers = append(e.orderHandlers, cmd.Handler)
	orderEngineLog.Debugf("注册订单更新处理器: %d", len(e.orderHandlers))
}

// handleQueryStats 处理查询统计命令
func (e *OrderEngine) handleQueryStats(cmd *QueryStatsCommand) {
	// 创建统计快照
	stats := &EngineStats{
		TotalCommands:   e.stats.TotalCommands,
		ProcessedOrders: e.stats.ProcessedOrders,
		ProcessedTrades: e.stats.ProcessedTrades,
		Errors:          e.stats.Errors,
	}

	select {
	case cmd.Reply <- stats:
	default:
	}
}

// emitOrderUpdate 触发订单更新回调
func (e *OrderEngine) emitOrderUpdate(order *domain.Order) {
	handlers := e.orderHandlers
	if len(handlers) == 0 || order == nil {
		return
	}

	// 串行执行（确定性优先；避免并发导致策略状态竞态）
	for _, h := range handlers {
		if h == nil {
			continue
		}
		func(handler OrderUpdateHandler) {
			defer func() {
				if r := recover(); r != nil {
					orderEngineLog.Errorf("订单更新回调 panic: %v", r)
				}
			}()
			if err := handler.OnOrderUpdate(context.Background(), order); err != nil {
				orderEngineLog.Errorf("订单更新回调执行失败: %v", err)
			}
		}(h)
	}
}

