package services

import (
	"context"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/betbot/gobet/clob/client"
	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/execution"
	"github.com/betbot/gobet/internal/ports"
	"github.com/betbot/gobet/internal/risk"
	"github.com/betbot/gobet/pkg/cache"
	"github.com/betbot/gobet/pkg/persistence"
)

var log = logrus.WithField("component", "trading_service")

// OrderResult 订单处理结果
type OrderResult struct {
	Order   *domain.Order
	Success bool
	Error   error
}

// TradingService 交易服务（重构后，无锁，使用 OrderEngine）
type TradingService struct {
	orderEngine *OrderEngine
	ioExecutor  *IOExecutor
	clobClient  *client.Client

	// 配置
	funderAddress string
	signatureType types.SignatureType
	dryRun        bool
	minOrderSize  float64

	// 上下文
	ctx    context.Context
	cancel context.CancelFunc

	// 订单状态缓存（只读，可以保留）
	orderStatusCache *cache.OrderStatusCache

	// 订单状态同步配置
	orderStatusSyncIntervalWithOrders    int
	orderStatusSyncIntervalWithoutOrders int

	// 执行层保护（防重复/风控）
	inFlightDeduper *execution.InFlightDeduper
	circuitBreaker  *risk.CircuitBreaker

	// 重启恢复/快照
	persistence   persistence.Service
	persistenceID string
}

// NewTradingService 创建新的交易服务（使用 OrderEngine）
func NewTradingService(clobClient *client.Client, dryRun bool) *TradingService {
	ctx, cancel := context.WithCancel(context.Background())

	minOrderSize := 1.1 // 默认最小订单金额

	// 创建 IO 执行器
	ioExecutor := NewIOExecutor(clobClient, dryRun)

	// 创建 OrderEngine
	orderEngine := NewOrderEngine(ioExecutor, minOrderSize, dryRun)

	service := &TradingService{
		orderEngine:                          orderEngine,
		ioExecutor:                           ioExecutor,
		clobClient:                           clobClient,
		funderAddress:                        "",
		signatureType:                        types.SignatureTypeBrowser,
		dryRun:                               dryRun,
		minOrderSize:                         minOrderSize,
		ctx:                                  ctx,
		cancel:                               cancel,
		orderStatusCache:                     cache.NewOrderStatusCache(),
		orderStatusSyncIntervalWithOrders:    3,  // 默认3秒
		orderStatusSyncIntervalWithoutOrders: 30, // 默认30秒
		inFlightDeduper:                      execution.NewInFlightDeduper(2*time.Second, 64),
		circuitBreaker: risk.NewCircuitBreaker(risk.CircuitBreakerConfig{
			// 默认只启用“连续错误熔断”，避免误伤；当日亏损上限可后续接入完整 PnL 统计后再启用。
			MaxConsecutiveErrors: 10,
			DailyLossLimitCents:  0,
		}),
	}

	if dryRun {
		log.Warnf("📝 纸交易模式已启用：不会进行真实交易，订单信息仅记录在日志中")
	}

	return service
}

// SetOrderStatusSyncConfig 设置订单状态同步配置（无锁版本）
func (s *TradingService) SetOrderStatusSyncConfig(withOrdersSeconds, withoutOrdersSeconds int) {
	if withOrdersSeconds > 0 {
		s.orderStatusSyncIntervalWithOrders = withOrdersSeconds
	}
	if withoutOrdersSeconds > 0 {
		s.orderStatusSyncIntervalWithoutOrders = withoutOrdersSeconds
	}
	log.Infof("订单状态同步配置已更新: 有活跃订单时=%d秒, 无活跃订单时=%d秒", s.orderStatusSyncIntervalWithOrders, s.orderStatusSyncIntervalWithoutOrders)
}

// OnOrderUpdate 注册订单更新回调（通过 OrderEngine）
func (s *TradingService) OnOrderUpdate(handler ports.OrderUpdateHandler) {
	s.orderEngine.OnOrderUpdate(handler)
}

// emitOrderUpdate 触发订单更新回调（已移至 OrderEngine，保留此方法用于向后兼容）
func (s *TradingService) emitOrderUpdate(ctx context.Context, order *domain.Order) {
	// 此方法已废弃，回调现在由 OrderEngine 处理
	log.Debugf("emitOrderUpdate 已废弃，请使用 OrderEngine 的回调机制")
}

// Start 启动交易服务（使用 OrderEngine）
func (s *TradingService) Start(ctx context.Context) error {
	// 取消旧的 context（如果存在）
	if s.cancel != nil {
		s.cancel()
	}
	// 创建新的 context 和 cancel 函数
	s.ctx, s.cancel = context.WithCancel(ctx)

	log.Info("✅ 交易服务已启动（使用 OrderEngine）")

	// 启动 OrderEngine 主循环
	go s.orderEngine.Run(s.ctx)

	// 重启恢复：先加载快照（热启动），后续再用交易所 open orders 对账纠偏
	s.loadSnapshot()
	go func() {
		// 等待 OrderEngine 就绪
		time.Sleep(200 * time.Millisecond)
		s.bootstrapOpenOrdersFromExchange(s.ctx)
	}()

	// 快照持久化：订单/仓位有变化时做一次 debounce 保存
	if s.persistence != nil {
		s.startSnapshotLoop(s.ctx)
	}

	// 初始化余额（从 API 获取）
	if !s.dryRun {
		go s.initializeBalance(ctx)
	} else {
		// 纸交易模式：设置一个很大的初始余额
		updateCmd := &UpdateBalanceCommand{
			id:       fmt.Sprintf("init_balance_%d", time.Now().UnixNano()),
			Balance:  1000000.0, // 纸交易模式使用很大的余额
			Currency: "USDC",
		}
		s.orderEngine.SubmitCommand(updateCmd)
		log.Infof("📊 [余额初始化] 纸交易模式：设置初始余额为 %.2f USDC", 1000000.0)
	}

	// 启动定期订单状态同步（如果需要）
	go s.startOrderStatusSync(s.ctx)

	return nil
}

// isLocalGeneratedOrderID 检查是否是本地生成的订单ID
// 本地生成的订单ID通常以 "entry-", "hedge-", "smart-" 开头
func isLocalGeneratedOrderID(orderID string) bool {
	if orderID == "" {
		return false
	}
	// 检查是否是本地生成的ID格式
	if len(orderID) > 10 && orderID[:10] == "entry-up-" {
		return true
	}
	if len(orderID) > 12 && orderID[:12] == "hedge-down-" {
		return true
	}
	if len(orderID) > 5 && orderID[:5] == "smart" {
		return true
	}
	if len(orderID) > 6 && orderID[:6] == "entry-" {
		return true
	}
	if len(orderID) > 6 && orderID[:6] == "hedge-" {
		return true
	}
	return false
}

// Stop 停止交易服务
func (s *TradingService) Stop() {
	log.Info("正在停止交易服务...")

	// 先取消context，通知所有goroutine停止
	if s.cancel != nil {
		s.cancel()
	}

	log.Info("交易服务已停止")
}

// SetFunderAddress 设置 funder 地址和签名类型（无锁版本）
func (s *TradingService) SetFunderAddress(funderAddress string, signatureType types.SignatureType) {
	s.funderAddress = funderAddress
	s.signatureType = signatureType
}

// SetMinOrderSize 设置最小订单金额（USDC）（无锁版本）
func (s *TradingService) SetMinOrderSize(minOrderSize float64) {
	if minOrderSize < 1.0 {
		minOrderSize = 1.0 // 交易所要求不能小于 1.0
	}
	s.minOrderSize = minOrderSize
	// 更新 OrderEngine 的最小订单金额
	s.orderEngine.MinOrderSize = minOrderSize
	log.Infof("✅ 已设置最小订单金额: %.2f USDC", minOrderSize)
}

// WaitOrderResult 等待订单处理结果（已废弃，现在通过 OrderEngine 处理）
// 保留此方法用于向后兼容，但不再使用
func (s *TradingService) WaitOrderResult(ctx context.Context, orderID string, timeout time.Duration) (*OrderResult, error) {
	// 通过 OrderEngine 查询订单状态
	reply := make(chan *StateSnapshot, 1)
	cmd := &QueryStateCommand{
		id:    fmt.Sprintf("query_order_%s", orderID),
		Query: QueryOrder,
		Reply: reply,
	}

	s.orderEngine.SubmitCommand(cmd)

	select {
	case snapshot := <-reply:
		if snapshot.Order != nil && snapshot.Order.OrderID == orderID {
			return &OrderResult{
				Order:   snapshot.Order,
				Success: snapshot.Order.Status != domain.OrderStatusFailed,
				Error:   snapshot.Error,
			}, nil
		}
		return nil, fmt.Errorf("订单不存在: %s", orderID)
	case <-time.After(timeout):
		return nil, fmt.Errorf("等待订单结果超时: %s", orderID)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// 注意：processOrderQueue 和 processOrderAsync 方法已移除
// 订单提交现在改为同步提交，不再使用异步队列

// 注意：processOrderAsync 方法已完全移除
// 订单提交现在在 PlaceOrder 中同步完成，不再需要异步处理

// handleOrderPlaced 处理订单下单事件（通过 OrderEngine）
func (s *TradingService) handleOrderPlaced(order *domain.Order, market *domain.Market) error {
	log.Debugf("📥 [WebSocket] 订单已下单: orderID=%s, status=%s", order.OrderID, order.Status)

	// 发送 UpdateOrderCommand 到 OrderEngine
	updateCmd := &UpdateOrderCommand{
		id:    fmt.Sprintf("websocket_placed_%s", order.OrderID),
		Order: order,
	}
	s.orderEngine.SubmitCommand(updateCmd)

	// 更新缓存
	if order.Status == domain.OrderStatusOpen {
		s.orderStatusCache.Set(order.OrderID, true)
	}

	// 如果订单状态是 open，检查价格偏差
	if order.Status == domain.OrderStatusOpen && market != nil {
		// 在 goroutine 中异步检查价格偏差，避免阻塞
		go s.checkAndCorrectOrderPrice(context.Background(), order, market)
	}

	return nil
}

// checkAndCorrectOrderPrice 检查订单价格偏差并自动修正
func (s *TradingService) checkAndCorrectOrderPrice(ctx context.Context, order *domain.Order, market *domain.Market) {
	// 获取当前订单簿最佳价格
	var currentBestPrice float64
	var err error

	if order.Side == types.SideBuy {
		// 买入订单：使用最佳卖价（best ask）
		_, currentBestPrice, err = s.GetBestPrice(ctx, order.AssetID)
	} else {
		// 卖出订单：使用最佳买价（best bid）
		currentBestPrice, _, err = s.GetBestPrice(ctx, order.AssetID)
	}

	if err != nil {
		log.Warnf("⚠️ 无法获取订单簿价格，跳过价格偏差检查: orderID=%s, error=%v", order.OrderID, err)
		return
	}

	if currentBestPrice <= 0 {
		log.Warnf("⚠️ 订单簿价格无效，跳过价格偏差检查: orderID=%s", order.OrderID)
		return
	}

	// 计算价格偏差（分）
	expectedPrice := order.Price.ToDecimal()
	priceDeviationCents := int((currentBestPrice - expectedPrice) * 100)
	if priceDeviationCents < 0 {
		priceDeviationCents = -priceDeviationCents
	}

	// 价格偏差阈值：默认 2 cents
	// 注意：对于网格策略，如果订单价格与订单簿价格偏差超过 2 cents，说明价格已经变化，需要重新下单
	deviationThreshold := 2

	// 如果价格偏差超过阈值，撤单并重新下单
	if priceDeviationCents > deviationThreshold {
		log.Warnf("⚠️ 订单价格偏差过大: orderID=%s, 预期价格=%.4f, 当前最佳价格=%.4f, 偏差=%dc (阈值=%dc)",
			order.OrderID, expectedPrice, currentBestPrice, priceDeviationCents, deviationThreshold)

		// 检查订单是否仍然存在且状态为 open（通过 OrderEngine 查询）
		openOrders := s.GetActiveOrders()
		var existingOrder *domain.Order
		for _, o := range openOrders {
			if o.OrderID == order.OrderID {
				existingOrder = o
				break
			}
		}

		if existingOrder == nil || existingOrder.Status != domain.OrderStatusOpen {
			log.Debugf("订单状态已变化，跳过价格修正: orderID=%s", order.OrderID)
			return
		}

		// 撤单
		if err := s.CancelOrder(ctx, order.OrderID); err != nil {
			log.Errorf("❌ 撤单失败: orderID=%s, error=%v", order.OrderID, err)
			return
		}

		log.Infof("✅ 已撤单: orderID=%s (价格偏差过大: %dc)", order.OrderID, priceDeviationCents)

		// 等待一小段时间，确保撤单完成
		time.Sleep(500 * time.Millisecond)

		// 使用最新价格重新下单
		newPrice := domain.PriceFromDecimal(currentBestPrice)

		// 创建新的订单（使用新的订单 ID）
		newOrder := &domain.Order{
			OrderID:      fmt.Sprintf("%s-corrected-%d", order.OrderID, time.Now().UnixNano()),
			MarketSlug:   order.MarketSlug,
			AssetID:      order.AssetID,
			Side:         order.Side,
			Price:        newPrice,
			Size:         order.Size,
			GridLevel:    order.GridLevel,
			TokenType:    order.TokenType,
			HedgeOrderID: order.HedgeOrderID,
			IsEntryOrder: order.IsEntryOrder,
			PairOrderID:  order.PairOrderID,
			Status:       domain.OrderStatusPending,
			CreatedAt:    time.Now(),
		}

		// 如果是配对订单（entry/hedge），需要同时处理对冲订单
		if order.PairOrderID != nil {
			// 通过 OrderEngine 查询配对订单
			openOrders := s.GetActiveOrders()
			var pairOrder *domain.Order
			for _, o := range openOrders {
				if o.OrderID == *order.PairOrderID {
					pairOrder = o
					break
				}
			}

			if pairOrder != nil && pairOrder.Status == domain.OrderStatusOpen {
				// 获取对冲订单的最佳价格
				var hedgeBestPrice float64
				if pairOrder.Side == types.SideBuy {
					_, hedgeBestPrice, err = s.GetBestPrice(ctx, pairOrder.AssetID)
				} else {
					hedgeBestPrice, _, err = s.GetBestPrice(ctx, pairOrder.AssetID)
				}

				if err == nil && hedgeBestPrice > 0 {
					// 计算对冲订单的价格偏差
					hedgeExpectedPrice := pairOrder.Price.ToDecimal()
					hedgeDeviationCents := int((hedgeBestPrice - hedgeExpectedPrice) * 100)
					if hedgeDeviationCents < 0 {
						hedgeDeviationCents = -hedgeDeviationCents
					}

					// 如果对冲订单价格偏差也超过阈值，同时撤单并重新下单
					if hedgeDeviationCents > deviationThreshold {
						log.Warnf("⚠️ 对冲订单价格偏差过大: orderID=%s, 预期价格=%.4f, 当前最佳价格=%.4f, 偏差=%dc (阈值=%dc)",
							pairOrder.OrderID, hedgeExpectedPrice, hedgeBestPrice, hedgeDeviationCents, deviationThreshold)

						// 撤单对冲订单
						if err := s.CancelOrder(ctx, pairOrder.OrderID); err != nil {
							log.Errorf("❌ 撤单对冲订单失败: orderID=%s, error=%v", pairOrder.OrderID, err)
						} else {
							log.Infof("✅ 已撤单对冲订单: orderID=%s (价格偏差过大: %dc)", pairOrder.OrderID, hedgeDeviationCents)

							// 等待撤单完成
							time.Sleep(500 * time.Millisecond)

							// 创建新的对冲订单（使用最新价格）
							hedgeNewPrice := domain.PriceFromDecimal(hedgeBestPrice)
							newHedgeOrder := &domain.Order{
								OrderID:      fmt.Sprintf("%s-corrected-%d", pairOrder.OrderID, time.Now().UnixNano()),
								MarketSlug:   pairOrder.MarketSlug,
								AssetID:      pairOrder.AssetID,
								Side:         pairOrder.Side,
								Price:        hedgeNewPrice,
								Size:         pairOrder.Size,
								GridLevel:    pairOrder.GridLevel,
								TokenType:    pairOrder.TokenType,
								HedgeOrderID: pairOrder.HedgeOrderID,
								IsEntryOrder: pairOrder.IsEntryOrder,
								PairOrderID:  &newOrder.OrderID, // 更新配对订单 ID
								Status:       domain.OrderStatusPending,
								CreatedAt:    time.Now(),
							}

							// 更新配对关系
							newOrder.PairOrderID = &newHedgeOrder.OrderID
							newOrder.HedgeOrderID = &newHedgeOrder.OrderID
							newHedgeOrder.HedgeOrderID = &newOrder.OrderID

							// 先重新下单对冲订单
							_, err := s.PlaceOrder(ctx, newHedgeOrder)
							if err != nil {
								log.Errorf("❌ 重新下单对冲订单失败: error=%v", err)
							} else {
								log.Infof("✅ 已重新下单对冲订单: orderID=%s, 新价格=%.4f (原价格=%.4f, 偏差=%dc)",
									newHedgeOrder.OrderID, hedgeBestPrice, hedgeExpectedPrice, hedgeDeviationCents)
							}
						}
					} else {
						// 对冲订单价格正常，但需要更新配对关系
						newOrder.PairOrderID = &pairOrder.OrderID
						newOrder.HedgeOrderID = &pairOrder.OrderID
						log.Debugf("对冲订单价格正常，保持配对关系: pairOrderID=%s, 偏差=%dc (阈值=%dc)",
							pairOrder.OrderID, hedgeDeviationCents, deviationThreshold)
					}
				}
			}
		}

		// 重新下单
		_, err := s.PlaceOrder(ctx, newOrder)
		if err != nil {
			log.Errorf("❌ 重新下单失败: error=%v", err)
		} else {
			log.Infof("✅ 已重新下单: orderID=%s, 新价格=%.4f (原价格=%.4f, 偏差=%dc)",
				newOrder.OrderID, currentBestPrice, expectedPrice, priceDeviationCents)
		}
	} else {
		log.Debugf("✅ 订单价格正常: orderID=%s, 预期价格=%.4f, 当前最佳价格=%.4f, 偏差=%dc (阈值=%dc)",
			order.OrderID, expectedPrice, currentBestPrice, priceDeviationCents, deviationThreshold)
	}
}

// handleOrderFilled 处理订单成交事件（通过 OrderEngine）
func (s *TradingService) handleOrderFilled(order *domain.Order, market *domain.Market) error {
	// 确保 FilledAt 已设置
	if order.FilledAt == nil {
		now := time.Now()
		order.FilledAt = &now
	}
	if order.MarketSlug == "" && market != nil {
		order.MarketSlug = market.Slug
	}

	// 更新订单状态
	order.Status = domain.OrderStatusFilled
	order.FilledSize = order.Size

	// 发送 UpdateOrderCommand 到 OrderEngine
	updateCmd := &UpdateOrderCommand{
		id:    fmt.Sprintf("websocket_filled_%s", order.OrderID),
		Order: order,
	}
	s.orderEngine.SubmitCommand(updateCmd)

	// 更新缓存（标记为已关闭）
	s.orderStatusCache.Set(order.OrderID, false)

	log.Infof("✅ [WebSocket] 订单已成交: orderID=%s, size=%.2f", order.OrderID, order.Size)

	return nil
}

// HandleTrade 处理交易事件（通过 OrderEngine）
func (s *TradingService) HandleTrade(ctx context.Context, trade *domain.Trade) {
	log.Debugf("📥 [WebSocket] 收到交易事件: tradeID=%s, orderID=%s, size=%.2f", trade.ID, trade.OrderID, trade.Size)

	// 发送 ProcessTradeCommand 到 OrderEngine
	cmd := &ProcessTradeCommand{
		id:    fmt.Sprintf("process_trade_%d", time.Now().UnixNano()),
		Trade: trade,
	}
	s.orderEngine.SubmitCommand(cmd)
}

// handleOrderCanceled 处理订单取消事件（通过 OrderEngine）
func (s *TradingService) handleOrderCanceled(order *domain.Order) error {
	// 更新订单状态
	order.Status = domain.OrderStatusCanceled
	// 尽量补齐 market slug，避免跨周期串单
	if order.MarketSlug == "" {
		// 这里无法可靠拿到 market，只能保留为空
	}

	// 发送 UpdateOrderCommand 到 OrderEngine
	updateCmd := &UpdateOrderCommand{
		id:    fmt.Sprintf("websocket_canceled_%s", order.OrderID),
		Order: order,
	}
	s.orderEngine.SubmitCommand(updateCmd)

	log.Infof("❌ [WebSocket] 订单已取消: orderID=%s", order.OrderID)

	return nil
}

// 注意：updatePositionFromOrder 方法已移除
// 仓位更新现在通过 TradeCollector 处理交易事件，而不是直接根据订单更新

// CancelOrdersNotInMarket 只管理本周期：取消所有 MarketSlug != currentSlug 的活跃订单（MarketSlug 为空也会取消）
func (s *TradingService) CancelOrdersNotInMarket(ctx context.Context, currentSlug string) {
	orders := s.GetActiveOrders()
	for _, o := range orders {
		if o == nil || o.OrderID == "" {
			continue
		}
		if currentSlug == "" {
			_ = s.CancelOrder(ctx, o.OrderID)
			continue
		}
		if o.MarketSlug == "" || o.MarketSlug != currentSlug {
			_ = s.CancelOrder(ctx, o.OrderID)
		}
	}
}

// CancelOrdersForMarket 取消指定 marketSlug 的活跃订单
func (s *TradingService) CancelOrdersForMarket(ctx context.Context, marketSlug string) {
	if marketSlug == "" {
		return
	}
	orders := s.GetActiveOrders()
	for _, o := range orders {
		if o == nil || o.OrderID == "" {
			continue
		}
		if o.MarketSlug == marketSlug {
			_ = s.CancelOrder(ctx, o.OrderID)
		}
	}
}

// GetActiveOrders 获取活跃订单（通过 OrderEngine 查询）
func (s *TradingService) GetActiveOrders() []*domain.Order {
	reply := make(chan *StateSnapshot, 1)
	cmd := &QueryStateCommand{
		id:    fmt.Sprintf("query_open_orders_%d", time.Now().UnixNano()),
		Query: QueryOpenOrders,
		Reply: reply,
	}

	s.orderEngine.SubmitCommand(cmd)

	select {
	case snapshot := <-reply:
		return snapshot.OpenOrders
	case <-time.After(5 * time.Second):
		return []*domain.Order{} // 超时返回空列表
	}
}

// GetPosition 获取仓位（通过 OrderEngine 查询）
func (s *TradingService) GetPosition(positionID string) (*domain.Position, error) {
	reply := make(chan *StateSnapshot, 1)
	cmd := &QueryStateCommand{
		id:    fmt.Sprintf("query_position_%d", time.Now().UnixNano()),
		Query: QueryPosition,
		Reply: reply,
	}

	s.orderEngine.SubmitCommand(cmd)

	select {
	case snapshot := <-reply:
		if snapshot.Position != nil && snapshot.Position.ID == positionID {
			return snapshot.Position, nil
		}
		return nil, fmt.Errorf("仓位不存在: %s", positionID)
	case <-time.After(5 * time.Second):
		return nil, fmt.Errorf("查询仓位超时: %s", positionID)
	}
}

// CreatePosition 创建仓位（通过 OrderEngine）
func (s *TradingService) CreatePosition(ctx context.Context, position *domain.Position) error {
	reply := make(chan error, 1)
	cmd := &CreatePositionCommand{
		id:       fmt.Sprintf("create_position_%d", time.Now().UnixNano()),
		Position: position,
		Reply:    reply,
	}

	s.orderEngine.SubmitCommand(cmd)

	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// UpdatePosition 更新仓位（通过 OrderEngine）
func (s *TradingService) UpdatePosition(ctx context.Context, positionID string, updater func(*domain.Position)) error {
	reply := make(chan error, 1)
	cmd := &UpdatePositionCommand{
		id:         fmt.Sprintf("update_position_%d", time.Now().UnixNano()),
		PositionID: positionID,
		Updater:    updater,
		Reply:      reply,
	}

	s.orderEngine.SubmitCommand(cmd)

	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ClosePosition 关闭仓位（通过 OrderEngine）
func (s *TradingService) ClosePosition(ctx context.Context, positionID string, exitPrice domain.Price, exitOrder *domain.Order) error {
	reply := make(chan error, 1)
	cmd := &ClosePositionCommand{
		id:         fmt.Sprintf("close_position_%d", time.Now().UnixNano()),
		PositionID: positionID,
		ExitPrice:  exitPrice,
		ExitOrder:  exitOrder,
		Reply:      reply,
	}

	s.orderEngine.SubmitCommand(cmd)

	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// GetAllPositions 获取所有仓位（通过 OrderEngine 查询）
func (s *TradingService) GetAllPositions() []*domain.Position {
	reply := make(chan *StateSnapshot, 1)
	cmd := &QueryStateCommand{
		id:    fmt.Sprintf("query_all_positions_%d", time.Now().UnixNano()),
		Query: QueryAllPositions,
		Reply: reply,
	}

	s.orderEngine.SubmitCommand(cmd)

	select {
	case snapshot := <-reply:
		return snapshot.Positions
	case <-time.After(5 * time.Second):
		return []*domain.Position{} // 超时返回空列表
	}
}

// GetOpenPositions 获取开放仓位（通过 OrderEngine 查询）
func (s *TradingService) GetOpenPositions() []*domain.Position {
	reply := make(chan *StateSnapshot, 1)
	cmd := &QueryStateCommand{
		id:    fmt.Sprintf("query_open_positions_%d", time.Now().UnixNano()),
		Query: QueryOpenPositions,
		Reply: reply,
	}

	s.orderEngine.SubmitCommand(cmd)

	select {
	case snapshot := <-reply:
		return snapshot.Positions
	case <-time.After(5 * time.Second):
		return []*domain.Position{} // 超时返回空列表
	}
}

// GetOpenPositionsForMarket 只返回指定 marketSlug 的开放仓位
func (s *TradingService) GetOpenPositionsForMarket(marketSlug string) []*domain.Position {
	positions := s.GetOpenPositions()
	if marketSlug == "" {
		return positions
	}
	out := make([]*domain.Position, 0, len(positions))
	for _, p := range positions {
		if p == nil {
			continue
		}
		slug := p.MarketSlug
		if slug == "" && p.Market != nil {
			slug = p.Market.Slug
		}
		if slug == "" && p.EntryOrder != nil {
			slug = p.EntryOrder.MarketSlug
		}
		if slug == marketSlug {
			out = append(out, p)
		}
	}
	return out
}
