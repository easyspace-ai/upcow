package services

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/ports"
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
	CmdResetCycle     OrderCommandType = "reset_cycle" // 周期切换：清空订单/仓位等运行时状态
)

// PlaceOrderCommand 下单命令
type PlaceOrderCommand struct {
	id      string
	Gen     int64 // 周期代号：用于防止周期切换后旧命令/旧 IO 回流污染状态
	Order   *domain.Order
	Reply   chan *PlaceOrderResult
	Context context.Context
}

func (c *PlaceOrderCommand) CommandType() OrderCommandType { return CmdPlaceOrder }
func (c *PlaceOrderCommand) ID() string                    { return c.id }

// PlaceOrderResult 下单结果
type PlaceOrderResult struct {
	Order *domain.Order
	Error error
}

// CancelOrderCommand 取消订单命令
type CancelOrderCommand struct {
	id      string
	Gen     int64 // 周期代号
	OrderID string
	Reply   chan error
	Context context.Context
}

func (c *CancelOrderCommand) CommandType() OrderCommandType { return CmdCancelOrder }
func (c *CancelOrderCommand) ID() string                    { return c.id }

// UpdateOrderCommand 更新订单命令
type UpdateOrderCommand struct {
	id              string
	Gen             int64 // 周期代号（必须与引擎当前一致，否则丢弃）
	Order           *domain.Order
	Error           error
	OriginalOrderID string // 本地 orderID（用于 server orderID 回写时重键）
}

func (c *UpdateOrderCommand) CommandType() OrderCommandType { return CmdUpdateOrder }
func (c *UpdateOrderCommand) ID() string                    { return c.id }

// ProcessTradeCommand 处理交易命令
type ProcessTradeCommand struct {
	id    string
	Gen   int64 // 周期代号
	Trade *domain.Trade
}

func (c *ProcessTradeCommand) CommandType() OrderCommandType { return CmdProcessTrade }
func (c *ProcessTradeCommand) ID() string                    { return c.id }

// UpdateBalanceCommand 更新余额命令
type UpdateBalanceCommand struct {
	id       string
	Balance  float64
	Currency string
}

func (c *UpdateBalanceCommand) CommandType() OrderCommandType { return CmdUpdateBalance }
func (c *UpdateBalanceCommand) ID() string                    { return c.id }

// CreatePositionCommand 创建仓位命令
type CreatePositionCommand struct {
	id       string
	Gen      int64 // 周期代号
	Position *domain.Position
	Reply    chan error
}

func (c *CreatePositionCommand) CommandType() OrderCommandType { return CmdCreatePosition }
func (c *CreatePositionCommand) ID() string                    { return c.id }

// UpdatePositionCommand 更新仓位命令
type UpdatePositionCommand struct {
	id         string
	Gen        int64 // 周期代号
	PositionID string
	Updater    func(*domain.Position)
	Reply      chan error
}

func (c *UpdatePositionCommand) CommandType() OrderCommandType { return CmdUpdatePosition }
func (c *UpdatePositionCommand) ID() string                    { return c.id }

// ClosePositionCommand 关闭仓位命令
type ClosePositionCommand struct {
	id         string
	Gen        int64 // 周期代号
	PositionID string
	ExitPrice  domain.Price
	ExitOrder  *domain.Order
	Reply      chan error
}

func (c *ClosePositionCommand) CommandType() OrderCommandType { return CmdClosePosition }
func (c *ClosePositionCommand) ID() string                    { return c.id }

// QueryStateCommand 查询状态命令
type QueryStateCommand struct {
	id    string
	Query QueryType
	// 可选参数：用于 QueryOrder / QueryPosition 等需要 ID 的查询
	OrderID    string
	PositionID string
	Reply      chan *StateSnapshot
}

func (c *QueryStateCommand) CommandType() OrderCommandType { return CmdQueryState }
func (c *QueryStateCommand) ID() string                    { return c.id }

// QueryType 查询类型
type QueryType string

const (
	QueryAllOrders     QueryType = "all_orders"
	QueryOpenOrders    QueryType = "open_orders"
	QueryAllPositions  QueryType = "all_positions"
	QueryOpenPositions QueryType = "open_positions"
	QueryBalance       QueryType = "balance"
	QueryOrder         QueryType = "order"
	QueryPosition      QueryType = "position"
)

// ResetCycleCommand 周期切换重置命令：
// - 清空订单/仓位/待处理交易等所有“与周期相关”的内存状态
// - 保留余额（余额属于账户，不属于周期）
type ResetCycleCommand struct {
	id            string
	NewMarketSlug string
	Reason        string
	NewGeneration int64 // 新周期代号（必须单调递增，避免旧回流污染）
	Reply         chan error
}

func (c *ResetCycleCommand) CommandType() OrderCommandType { return CmdResetCycle }
func (c *ResetCycleCommand) ID() string                    { return c.id }

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
	balance       float64                     // 可用资金（USDC）
	positions     map[string]*domain.Position // 当前仓位
	openOrders    map[string]*domain.Order    // 未完成订单
	orderStore    map[string]*domain.Order    // 所有订单（包括已成交的）
	pendingTrades map[string]*domain.Trade    // 待处理的交易（订单还未创建时）
	seenTrades    map[string]struct{}         // 已处理/已接收 tradeID 去重（周期内有效，reset 时清空）

	// 配置
	MinOrderSize float64 // 导出以便 TradingService 访问
	dryRun       bool

	// 外部依赖（IO 操作，异步执行）
	ioExecutor *ioExecutor

	// 回调
	orderHandlers []ports.OrderUpdateHandler

	// 上下文
	ctx    context.Context
	cancel context.CancelFunc

	// 统计
	stats *EngineStats

	// 周期代号（generation）：每次周期切换递增，用于丢弃旧周期的异步回流命令
	generation int64
}

// NewOrderEngine 创建新的订单引擎
func NewOrderEngine(ioExecutor *ioExecutor, minOrderSize float64, dryRun bool) *OrderEngine {
	return &OrderEngine{
		cmdChan:       make(chan OrderCommand, 1000), // 缓冲1000避免阻塞
		balance:       0,
		positions:     make(map[string]*domain.Position),
		openOrders:    make(map[string]*domain.Order),
		orderStore:    make(map[string]*domain.Order),
		pendingTrades: make(map[string]*domain.Trade),
		seenTrades:    make(map[string]struct{}),
		MinOrderSize:  minOrderSize,
		dryRun:        dryRun,
		ioExecutor:    ioExecutor,
		orderHandlers: make([]ports.OrderUpdateHandler, 0),
		stats:         &EngineStats{},
		generation:    1,
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

// ResetForNewCycle 在周期切换时清空引擎内的“周期状态”。
// 注意：这是非阻塞触发（通过命令进入 engine goroutine），避免外部加锁/竞态。
func (e *OrderEngine) ResetForNewCycle(newMarketSlug, reason string, newGeneration int64) {
	if e == nil {
		return
	}
	e.SubmitCommand(&ResetCycleCommand{
		id:            fmt.Sprintf("reset_cycle_%d", time.Now().UnixNano()),
		NewMarketSlug: newMarketSlug,
		Reason:        reason,
		NewGeneration: newGeneration,
		Reply:         nil,
	})
}

// OnOrderUpdate 注册订单更新回调
func (e *OrderEngine) OnOrderUpdate(handler ports.OrderUpdateHandler) {
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
	case CmdResetCycle:
		e.handleResetCycle(cmd.(*ResetCycleCommand))
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
	Handler ports.OrderUpdateHandler
}

func (c *RegisterHandlerCommand) CommandType() OrderCommandType { return CmdRegisterHandler }
func (c *RegisterHandlerCommand) ID() string                    { return c.id }

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
	// 周期隔离：旧周期命令直接拒绝（避免切周期后仍下单/回流）
	if cmd.Gen != e.generation {
		e.stats.Errors++
		// 使用非阻塞发送，避免阻塞 OrderEngine 主循环
		select {
		case cmd.Reply <- &PlaceOrderResult{
			Error: fmt.Errorf("stale cycle command: place order dropped (cmdGen=%d engineGen=%d)", cmd.Gen, e.generation),
		}:
		case <-cmd.Context.Done():
			// Context 已取消，接收端可能已经超时退出
		case <-time.After(100 * time.Millisecond):
			// 超时保护：如果 100ms 内无法发送，记录警告但不阻塞
			orderEngineLog.Warnf("回复 stale cycle 命令超时: cmdGen=%d engineGen=%d", cmd.Gen, e.generation)
		}
		return
	}
	// 1. 风控校验（在状态循环中同步执行）
	if err := e.validatePlaceOrder(cmd.Order); err != nil {
		// 使用非阻塞发送，避免阻塞 OrderEngine 主循环
		select {
		case cmd.Reply <- &PlaceOrderResult{Error: err}:
		case <-cmd.Context.Done():
			// Context 已取消，接收端可能已经超时退出
		case <-time.After(100 * time.Millisecond):
			// 超时保护：如果 100ms 内无法发送，记录警告但不阻塞
			orderEngineLog.Warnf("回复验证错误命令超时: orderID=%s", cmd.Order.OrderID)
		}
		return
	}

	// 2. 更新状态（预留资金）
	requiredAmount := cmd.Order.Price.ToDecimal() * cmd.Order.Size
	// 在纸模式下跳过余额检查，或者设置一个很大的初始余额
	if !e.dryRun && e.balance < requiredAmount {
		// 使用非阻塞发送，避免阻塞 OrderEngine 主循环
		select {
		case cmd.Reply <- &PlaceOrderResult{
			Error: fmt.Errorf("余额不足: 需要 %.2f USDC，当前余额 %.2f USDC",
				requiredAmount, e.balance),
		}:
		case <-cmd.Context.Done():
			// Context 已取消，接收端可能已经超时退出
		case <-time.After(100 * time.Millisecond):
			// 超时保护：如果 100ms 内无法发送，记录警告但不阻塞
			orderEngineLog.Warnf("回复余额不足命令超时: orderID=%s", cmd.Order.OrderID)
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
			Gen:   cmd.Gen,
			Order: orderToUpdate,
			Error: result.Error,
			// 关键：携带本地 orderID，用于 server orderID 回写时迁移 map key
			OriginalOrderID: cmd.Order.OrderID,
		}
		e.SubmitCommand(updateCmd)

		// 回复原始命令（使用非阻塞发送，避免阻塞回调 goroutine）
		// 如果接收端已经超时退出，这里不应该阻塞
		select {
		case cmd.Reply <- result:
			// 成功发送
		case <-cmd.Context.Done():
			// Context 已取消，接收端可能已经超时退出，不阻塞
			orderEngineLog.Debugf("回复命令时 context 已取消，接收端可能已超时: orderID=%s", cmd.Order.OrderID)
		case <-time.After(100 * time.Millisecond):
			// 超时保护：如果 100ms 内无法发送，记录警告但不阻塞
			orderEngineLog.Warnf("回复命令超时（接收端可能已退出）: orderID=%s, 命令类型=%s", cmd.Order.OrderID, cmd.CommandType())
		}
	})

	// 注意：不再立即返回本地 pending 订单。
	// 统一等待 IO 返回真实 server orderID，避免上层拿到错误 orderID 导致无法关联后续更新。
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
	if order.Price.Pips <= 0 {
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
	// 周期隔离：旧周期命令直接拒绝
	if cmd.Gen != e.generation {
		e.stats.Errors++
		select {
		case cmd.Reply <- fmt.Errorf("stale cycle command: cancel dropped (cmdGen=%d engineGen=%d)", cmd.Gen, e.generation):
		default:
		}
		return
	}
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
	order.Status = domain.OrderStatusCanceling
	e.orderStore[order.OrderID] = order
	e.emitOrderUpdate(order)

	// 异步执行 IO 操作（结果回流到状态循环）
	go e.ioExecutor.CancelOrderAsync(cmd.Context, cmd.OrderID, func(err error) {
		// 只回流“取消结果”（不要把 engine 内部指针直接跨 goroutine 传回）
		// 具体状态落地由 handleUpdateOrder 根据 err/当前订单状态单调合并后决定。
		updateCmd := &UpdateOrderCommand{
			id:    fmt.Sprintf("cancel_result_%s", cmd.OrderID),
			Gen:   cmd.Gen,
			Order: &domain.Order{OrderID: cmd.OrderID, Status: domain.OrderStatusCanceling},
			Error: err,
		}
		e.SubmitCommand(updateCmd)

		// 使用非阻塞发送，避免阻塞回调 goroutine
		select {
		case cmd.Reply <- err:
			// 成功发送
		case <-cmd.Context.Done():
			// Context 已取消，接收端可能已经超时退出，不阻塞
			orderEngineLog.Debugf("回复取消命令时 context 已取消: orderID=%s", cmd.OrderID)
		case <-time.After(100 * time.Millisecond):
			// 超时保护：如果 100ms 内无法发送，记录警告但不阻塞
			orderEngineLog.Warnf("回复取消命令超时（接收端可能已退出）: orderID=%s", cmd.OrderID)
		}
	})
}

func isNonCancelableCancelError(err error) bool {
	if err == nil {
		return false
	}
	// Polymarket CLOB 常见：HTTP 400 {"error":"Invalid order payload"}（订单不可撤/已终态/参数不匹配）
	// 此类错误在撤单语义上更接近“幂等完成”，不应把本地状态回滚为 open。
	msg := err.Error()
	return strings.Contains(msg, "Invalid order payload") ||
		strings.Contains(msg, "HTTP 错误 400") ||
		strings.Contains(strings.ToLower(msg), "invalid order payload")
}

func cloneOrder(o *domain.Order) *domain.Order {
	if o == nil {
		return nil
	}
	cp := *o
	// 注意：FilledPrice 等指针字段需要深拷贝（避免被上游复用/修改）
	if o.FilledPrice != nil {
		fp := *o.FilledPrice
		cp.FilledPrice = &fp
	}
	if o.FilledAt != nil {
		t := *o.FilledAt
		cp.FilledAt = &t
	}
	if o.CanceledAt != nil {
		t := *o.CanceledAt
		cp.CanceledAt = &t
	}
	if o.HedgeOrderID != nil {
		id := *o.HedgeOrderID
		cp.HedgeOrderID = &id
	}
	if o.PairOrderID != nil {
		id := *o.PairOrderID
		cp.PairOrderID = &id
	}
	if o.NegRisk != nil {
		b := *o.NegRisk
		cp.NegRisk = &b
	}
	return &cp
}

func mergeOrderInPlace(dst *domain.Order, src *domain.Order) {
	if dst == nil || src == nil {
		return
	}

	// 不允许终态被中间态覆盖
	if dst.IsFinalStatus() && !src.IsFinalStatus() {
		// 但允许补齐 FilledSize/FilledAt（如果 src 提供了更“终态化”的信息）
		if src.FilledSize > dst.FilledSize {
			dst.FilledSize = src.FilledSize
		}
		if src.FilledAt != nil && dst.FilledAt == nil {
			t := *src.FilledAt
			dst.FilledAt = &t
		}
		return
	}

	// 基础字段补齐/合并
	if dst.MarketSlug == "" && src.MarketSlug != "" {
		dst.MarketSlug = src.MarketSlug
	}
	if dst.AssetID == "" && src.AssetID != "" {
		dst.AssetID = src.AssetID
	}
	if dst.TokenType == "" && src.TokenType != "" {
		dst.TokenType = src.TokenType
	}
	if dst.Side == "" && src.Side != "" {
		dst.Side = src.Side
	}
	if dst.Price.Pips == 0 && src.Price.Pips != 0 {
		dst.Price = src.Price
	}
	if src.Size > dst.Size {
		dst.Size = src.Size
	}
	// FilledSize 单调递增
	if src.FilledSize > dst.FilledSize {
		dst.FilledSize = src.FilledSize
	}
	// 价格/时间戳补齐
	if src.FilledPrice != nil {
		fp := *src.FilledPrice
		dst.FilledPrice = &fp
	}
	if src.FilledAt != nil && dst.FilledAt == nil {
		t := *src.FilledAt
		dst.FilledAt = &t
	}
	if src.CanceledAt != nil && dst.CanceledAt == nil {
		t := *src.CanceledAt
		dst.CanceledAt = &t
	}

	// 状态收敛（按“成交事实”优先）
	// 1) 若已完全成交 => filled
	if dst.Size > 0 && dst.FilledSize >= dst.Size {
		dst.Status = domain.OrderStatusFilled
		if dst.FilledAt == nil {
			now := time.Now()
			dst.FilledAt = &now
		}
		dst.FilledSize = dst.Size
		return
	}
	// 2) 若有部分成交 => partial（除非已 failed/filled）
	if dst.FilledSize > 0 {
		if dst.Status != domain.OrderStatusFailed && dst.Status != domain.OrderStatusFilled {
			dst.Status = domain.OrderStatusPartial
		}
		return
	}
	// 3) 无成交：按优先级选更“强”的状态
	// filled 已在上面处理；failed/canceled 其次；canceling 再次；open/pending 最弱
	cur := dst.Status
	in := src.Status
	if cur == domain.OrderStatusFilled || in == domain.OrderStatusFilled {
		dst.Status = domain.OrderStatusFilled
		return
	}
	if cur == domain.OrderStatusFailed || in == domain.OrderStatusFailed {
		dst.Status = domain.OrderStatusFailed
		return
	}
	if cur == domain.OrderStatusCanceled || in == domain.OrderStatusCanceled {
		dst.Status = domain.OrderStatusCanceled
		if dst.CanceledAt == nil {
			now := time.Now()
			dst.CanceledAt = &now
		}
		return
	}
	if cur == domain.OrderStatusCanceling || in == domain.OrderStatusCanceling {
		dst.Status = domain.OrderStatusCanceling
		return
	}
	if cur == domain.OrderStatusOpen || in == domain.OrderStatusOpen || in == domain.OrderStatusPartial {
		dst.Status = domain.OrderStatusOpen
		return
	}
	dst.Status = domain.OrderStatusPending
}

// handleUpdateOrder 处理更新订单命令（IO 操作完成后调用）
func (e *OrderEngine) handleUpdateOrder(cmd *UpdateOrderCommand) {
	// 关键防护：丢弃旧周期的 UpdateOrderCommand（包括旧 IO 回流、旧同步回流）
	if cmd.Gen != e.generation {
		orderID := ""
		if cmd.Order != nil {
			orderID = cmd.Order.OrderID
		}
		orderEngineLog.Warnf("⚠️ [周期隔离] 丢弃旧周期 UpdateOrderCommand: cmdGen=%d engineGen=%d orderID=%s",
			cmd.Gen, e.generation, orderID)
		return
	}
	// CancelOrderAsync 也复用 UpdateOrderCommand 回流：撤单失败/不可撤需要特殊处理（幂等 + 单调）
	if cmd.Error != nil && cmd.Order != nil && cmd.Order.Status == domain.OrderStatusCanceling {
		oid := cmd.Order.OrderID
		// 若已不存在于 openOrders，可能已经被 WS 标为 filled/canceled，不做回滚
		if existing, ok := e.openOrders[oid]; ok && existing != nil {
			// “不可撤”类错误：更接近幂等完成（不回滚为 open），先落为 canceled，等待后续 WS/sync 把它推进到 filled（若实际成交）
			if isNonCancelableCancelError(cmd.Error) {
				now := time.Now()
				existing.Status = domain.OrderStatusCanceled
				existing.CanceledAt = &now
				e.orderStore[existing.OrderID] = existing
				delete(e.openOrders, existing.OrderID)
				e.emitOrderUpdate(existing)
			} else if existing.Status == domain.OrderStatusCanceling {
				// 真正撤单失败：回滚到 open
				existing.Status = domain.OrderStatusOpen
				e.orderStore[existing.OrderID] = existing
				e.emitOrderUpdate(existing)
			}
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
	order := cloneOrder(cmd.Order)
	if order == nil {
		return
	}
	// 撤单成功：cancel_result 回流时把 canceling 落为 canceled（避免“永远 canceling”卡死）
	if order.Status == domain.OrderStatusCanceling && strings.HasPrefix(cmd.ID(), "cancel_result_") {
		now := time.Now()
		order.Status = domain.OrderStatusCanceled
		order.CanceledAt = &now
	}
	// 关键：server orderID 回写时，把 openOrders/orderStore 从“本地 ID”迁移到“server ID”
	if order != nil && cmd.OriginalOrderID != "" && cmd.OriginalOrderID != order.OrderID {
		if existingOrder, ok := e.openOrders[cmd.OriginalOrderID]; ok {
			delete(e.openOrders, cmd.OriginalOrderID)
			delete(e.orderStore, cmd.OriginalOrderID)
			existingOrder.OrderID = order.OrderID
			mergeOrderInPlace(existingOrder, order)
			order = existingOrder
		}
	}
	if existingOrder, exists := e.openOrders[order.OrderID]; exists {
		// 更新现有订单
		existingOrder.OrderID = order.OrderID
		mergeOrderInPlace(existingOrder, order)
		order = existingOrder
	} else {
		// 新订单，添加到存储
		e.openOrders[order.OrderID] = order
	}

	// 更新订单存储
	e.orderStore[order.OrderID] = order

	// ✅ 修复：在纸交易模式下，当订单成交时创建 Trade 对象，以便正确创建持仓
	// 这样止盈/止损逻辑才能正常工作
	if order.Status == domain.OrderStatusFilled && e.dryRun {
		// 检查是否已经有对应的 Trade（避免重复创建）
		// 通过检查订单的 FilledSize 是否大于 0 来判断是否需要创建 Trade
		if order.FilledSize > 0 {
			// 检查是否已经处理过这个订单的 Trade
			tradeID := fmt.Sprintf("dry_run_trade_%s", order.OrderID)
			if _, exists := e.seenTrades[tradeID]; !exists {
				// 创建 Trade 对象
				filledAt := time.Now()
				if order.FilledAt != nil {
					filledAt = *order.FilledAt
				}
				trade := &domain.Trade{
					ID:        tradeID,
					OrderID:   order.OrderID,
					AssetID:   order.AssetID,
					Side:      order.Side,
					Price:     order.Price,
					Size:      order.FilledSize,
					TokenType: order.TokenType,
					Market:    nil, // 纸交易模式下 Market 可以为 nil，持仓通过 Order.MarketSlug 标识
					Time:      filledAt,
					Fee:       0, // 纸交易模式下费用为 0
				}

				// 发送 ProcessTradeCommand 到 OrderEngine
				tradeCmd := &ProcessTradeCommand{
					id:    fmt.Sprintf("dry_run_trade_cmd_%d", time.Now().UnixNano()),
					Gen:   cmd.Gen,
					Trade: trade,
				}
				e.SubmitCommand(tradeCmd)

				orderEngineLog.Infof("✅ [纸交易] 已创建 Trade 对象: tradeID=%s orderID=%s size=%.4f price=%.4f",
					tradeID, order.OrderID, trade.Size, trade.Price.ToDecimal())
			}
		}
	}

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
	// 周期隔离：丢弃旧周期 trade（保险起见；上游 session gate 应已隔离）
	if cmd.Gen != e.generation {
		tradeID := ""
		if cmd.Trade != nil {
			tradeID = cmd.Trade.ID
		}
		orderEngineLog.Warnf("⚠️ [周期隔离] 丢弃旧周期 ProcessTradeCommand: cmdGen=%d engineGen=%d tradeID=%s",
			cmd.Gen, e.generation, tradeID)
		return
	}
	trade := cmd.Trade
	if trade == nil {
		return
	}
	// 去重：同一 tradeID 不允许重复影响状态（包含 WS 重放/补偿对账合成 trade）
	if trade.ID != "" {
		if _, ok := e.seenTrades[trade.ID]; ok {
			return
		}
		e.seenTrades[trade.ID] = struct{}{}
	}

	// 1. 检查订单是否存在
	order, exists := e.orderStore[trade.OrderID]
	if !exists {
		// 重要：不能因为 orderID 不匹配就丢弃成交，否则仓位/报表会变成 0（你的日志里正是这种情况）
		// Polymarket 的 trade 消息里 orderID 可能是 taker/maker 的不同 ID，甚至会出现“对手方 ID”。
		// 我们的目标是：只要这是一笔“属于本账户”的成交（User WS 已保证），就必须按 assetID/market 更新仓位。
		order = e.bestEffortMatchOrderForTrade(trade)
		if order == nil {
			order = e.syntheticOrderFromTrade(trade)
		}
		// 把 synthetic/matched order 放进 store，保证同一 trade 之后的累积处理一致
		e.orderStore[order.OrderID] = order
	}

	// 2. 更新订单状态和实际成交价格
	// 支持部分成交：累计 FilledSize，只有 FilledSize >= Size 才标记为 filled
	if trade.Size > 0 {
		order.FilledSize += trade.Size
		// 更新实际成交价格（使用 Trade 的实际成交价格，而不是下单时的价格）
		// 对于部分成交，使用加权平均价格；对于完全成交，使用最后一次成交价格
		if order.FilledPrice == nil {
			// 第一次成交，直接使用 Trade 价格
			order.FilledPrice = &trade.Price
		} else {
			// 部分成交：计算加权平均价格
			// 新价格 = (旧价格 * 旧数量 + 新价格 * 新数量) / 总数量
			oldSize := order.FilledSize - trade.Size
			if oldSize > 0 {
				oldTotalValue := order.FilledPrice.ToDecimal() * oldSize
				newTotalValue := trade.Price.ToDecimal() * trade.Size
				totalValue := oldTotalValue + newTotalValue
				avgPrice := domain.PriceFromDecimal(totalValue / order.FilledSize)
				order.FilledPrice = &avgPrice
			} else {
				order.FilledPrice = &trade.Price
			}
		}
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
	// ⚠️ 修复：不能在部分成交时从 openOrders 删除，否则上层会误判“订单不在活跃列表”
	// 只有在订单完全成交时才从 openOrders 移除
	if order.Status == domain.OrderStatusFilled {
		delete(e.openOrders, order.OrderID)
	}

	// 4. 更新仓位
	e.updatePositionFromTrade(trade, order)

	// 5. 处理待处理的交易（如果有订单创建前的交易）
	e.processPendingTrades()

	// 6. 触发回调
	e.emitOrderUpdate(order)

	e.stats.ProcessedTrades++
	feeStr := ""
	if trade.Fee > 0 {
		feeStr = fmt.Sprintf(", fee=%.6f", trade.Fee)
	}
	orderEngineLog.Infof("✅ 交易已处理: tradeID=%s, orderID=%s, side=%s price=%.4f size=%.4f%s",
		trade.ID, trade.OrderID, trade.Side, trade.Price.ToDecimal(), trade.Size, feeStr)
}

// bestEffortMatchOrderForTrade 尝试把成交匹配到“我们已知的订单”。
// 目标：尽量关联到真实 orderID（便于订单状态/成交均价/日志），但不允许因为匹配失败而丢仓位更新。
func (e *OrderEngine) bestEffortMatchOrderForTrade(trade *domain.Trade) *domain.Order {
	if e == nil || trade == nil {
		return nil
	}
	if trade.AssetID == "" {
		return nil
	}
	// 优先在 openOrders 里找：同 assetID 的订单一般最多 1 个（cyclehedge 的常见模式）
	var candidate *domain.Order
	for _, o := range e.openOrders {
		if o == nil {
			continue
		}
		if o.AssetID != trade.AssetID {
			continue
		}
		// 如果 side 也一致，更可靠
		if trade.Side != "" && o.Side != "" && trade.Side != o.Side {
			continue
		}
		if candidate != nil {
			// 不唯一：放弃，避免误关联
			return nil
		}
		candidate = o
	}
	return candidate
}

// syntheticOrderFromTrade 为“无法关联到已知订单”的成交创建一个最小订单对象，
// 以便 position/报表能够准确反映真实成交。
func (e *OrderEngine) syntheticOrderFromTrade(trade *domain.Trade) *domain.Order {
	if trade == nil {
		return nil
	}
	oid := strings.TrimSpace(trade.OrderID)
	if oid == "" {
		oid = fmt.Sprintf("ws_trade:%s", trade.ID)
	}
	// 如果该 key 已存在，返回现有（避免重复创建）
	if existing, ok := e.orderStore[oid]; ok && existing != nil {
		return existing
	}

	marketSlug := ""
	if trade.Market != nil {
		marketSlug = trade.Market.Slug
	}
	tokenType := trade.TokenType
	if tokenType == "" && trade.Market != nil && trade.AssetID != "" {
		if trade.AssetID == trade.Market.YesAssetID {
			tokenType = domain.TokenTypeUp
		} else if trade.AssetID == trade.Market.NoAssetID {
			tokenType = domain.TokenTypeDown
		}
	}

	// synthetic 订单只用于仓位/报表与成交均价累积：Size 未必等于最终订单 size。
	// 这里把 Size 设为 trade.Size（至少保证 FilledSize 不会被 clamp 掉）。
	return &domain.Order{
		OrderID:    oid,
		MarketSlug: marketSlug,
		AssetID:    trade.AssetID,
		Side:       trade.Side,
		Price:      trade.Price,
		Size:       trade.Size,
		FilledSize: 0,
		TokenType:  tokenType,
		Status:     domain.OrderStatusOpen,
		CreatedAt:  trade.Time,
	}
}

// updatePositionFromTrade 从交易更新仓位
func (e *OrderEngine) updatePositionFromTrade(trade *domain.Trade, order *domain.Order) {
	// 确定 TokenType：优先使用订单的 TokenType（最可靠，来自策略层）
	tokenType := order.TokenType
	if tokenType == "" {
		// 兜底：使用 trade 的 TokenType
		tokenType = trade.TokenType
	}
	// 最后兜底：根据 AssetID 推断
	if tokenType == "" && trade.Market != nil && order.AssetID != "" {
		if order.AssetID == trade.Market.YesAssetID {
			tokenType = domain.TokenTypeUp
		} else if order.AssetID == trade.Market.NoAssetID {
			tokenType = domain.TokenTypeDown
		}
	}

	// 查找或创建仓位
	var position *domain.Position
	positionID := e.getPositionID(order)

	if pos, exists := e.positions[positionID]; exists {
		position = pos
		// 如果已有仓位的 TokenType 为空，更新它
		if position.TokenType == "" && tokenType != "" {
			position.TokenType = tokenType
		}
	} else {
		// 创建新仓位
		position = &domain.Position{
			ID:              positionID,
			MarketSlug:      order.MarketSlug,
			Market:          trade.Market,
			EntryOrder:      order,
			EntryPrice:      trade.Price,
			EntryTime:       trade.Time,
			Size:            0,
			TokenType:       tokenType, // ✅ 使用推断后的 TokenType（优先来自订单）
			Status:          domain.PositionStatusOpen,
			CostBasis:       0,
			AvgPrice:        0,
			TotalFilledSize: 0,
		}
		e.positions[positionID] = position
	}

	// 更新仓位大小和成本基础
	if trade.Side == types.SideBuy {
		// 买入交易：增加仓位
		position.Size += trade.Size
		// 累加成本基础（支持多次成交）
		position.AddFill(trade.Size, trade.Price)
	} else {
		// 卖出交易：减少仓位
		position.Size -= trade.Size
		if position.Size < 0 {
			position.Size = 0
		}
		// 卖出时也累加成本基础（用于计算平均成本）
		// 注意：卖出会减少持仓，但成本基础仍然累加（用于计算盈亏）
		position.AddFill(trade.Size, trade.Price)

		// ✅ 当卖出导致持仓归零时，自动标记仓位已关闭（形成完整闭环）
		if position.Size == 0 && position.Status == domain.PositionStatusOpen {
			exitTime := trade.Time
			position.ExitPrice = &trade.Price
			position.ExitTime = &exitTime
			position.ExitOrder = order
			position.Status = domain.PositionStatusClosed
		}
	}

	// 更新入场订单（如果这是首次成交）
	if position.EntryOrder == nil {
		position.EntryOrder = order
		position.EntryPrice = trade.Price
		position.EntryTime = trade.Time
	}
}

// getPositionID 获取仓位ID
func (e *OrderEngine) getPositionID(order *domain.Order) string {
	// 只管理本周期：positionID 按 MarketSlug 分桶
	return fmt.Sprintf("%s_%s_%s", order.MarketSlug, order.AssetID, order.TokenType)
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
			Gen:   e.generation,
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
	// 周期隔离：旧周期命令直接拒绝
	if cmd.Gen != e.generation {
		select {
		case cmd.Reply <- fmt.Errorf("stale cycle command: create position dropped (cmdGen=%d engineGen=%d)", cmd.Gen, e.generation):
		default:
		}
		return
	}
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
	// 周期隔离：旧周期命令直接拒绝
	if cmd.Gen != e.generation {
		select {
		case cmd.Reply <- fmt.Errorf("stale cycle command: update position dropped (cmdGen=%d engineGen=%d)", cmd.Gen, e.generation):
		default:
		}
		return
	}
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
	// 周期隔离：旧周期命令直接拒绝
	if cmd.Gen != e.generation {
		select {
		case cmd.Reply <- fmt.Errorf("stale cycle command: close position dropped (cmdGen=%d engineGen=%d)", cmd.Gen, e.generation):
		default:
		}
		return
	}
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
		oid := cmd.OrderID
		if oid == "" {
			snapshot.Error = fmt.Errorf("QueryOrder 需要 OrderID")
			break
		}
		// 优先从 orderStore 读（包含已成交/已取消/失败），其次从 openOrders 兜底
		if o, ok := e.orderStore[oid]; ok && o != nil {
			snapshot.Order = o
			break
		}
		if o, ok := e.openOrders[oid]; ok && o != nil {
			snapshot.Order = o
			break
		}
		snapshot.Error = fmt.Errorf("订单不存在: %s", oid)

	case QueryPosition:
		pid := cmd.PositionID
		if pid == "" {
			snapshot.Error = fmt.Errorf("QueryPosition 需要 PositionID")
			break
		}
		if p, ok := e.positions[pid]; ok && p != nil {
			snapshot.Position = p
			break
		}
		snapshot.Error = fmt.Errorf("仓位不存在: %s", pid)
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

// handleResetCycle 清空与周期相关的运行时状态（在 engine goroutine 内执行，无锁）
func (e *OrderEngine) handleResetCycle(cmd *ResetCycleCommand) {
	// 清空“周期相关”的状态（避免旧周期影响新周期）
	e.positions = make(map[string]*domain.Position)
	e.openOrders = make(map[string]*domain.Order)
	e.orderStore = make(map[string]*domain.Order)
	e.pendingTrades = make(map[string]*domain.Trade)
	e.seenTrades = make(map[string]struct{})

	// 更新周期代号（必须单调递增）
	if cmd.NewGeneration > 0 {
		e.generation = cmd.NewGeneration
	} else {
		e.generation++
	}

	orderEngineLog.Warnf("🔄 [周期切换] OrderEngine 已重置运行时状态: newMarket=%s reason=%s gen=%d",
		cmd.NewMarketSlug, cmd.Reason, e.generation)

	if cmd.Reply != nil {
		select {
		case cmd.Reply <- nil:
		default:
		}
	}
}

// emitOrderUpdate 触发订单更新回调
func (e *OrderEngine) emitOrderUpdate(order *domain.Order) {
	handlers := e.orderHandlers
	if len(handlers) == 0 || order == nil {
		return
	}

	orderEngineLog.Debugf("📤 [OrderEngine] 触发订单更新: orderID=%s status=%s marketSlug=%s assetID=%s handlers=%d",
		order.OrderID, order.Status, order.MarketSlug, order.AssetID, len(handlers))

	// 串行执行（确定性优先；避免并发导致策略状态竞态）
	for _, h := range handlers {
		if h == nil {
			continue
		}
		func(handler ports.OrderUpdateHandler) {
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
