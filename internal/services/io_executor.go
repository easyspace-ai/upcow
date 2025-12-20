package services

import (
	"context"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/betbot/gobet/clob/client"
	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
)

var ioExecutorLog = logrus.WithField("component", "io_executor")

// IOExecutor IO 操作执行器（异步执行，不阻塞 OrderEngine）
type IOExecutor struct {
	clobClient *client.Client
	dryRun     bool
}

// NewIOExecutor 创建 IO 执行器
func NewIOExecutor(clobClient *client.Client, dryRun bool) *IOExecutor {
	return &IOExecutor{
		clobClient: clobClient,
		dryRun:     dryRun,
	}
}

// PlaceOrderAsync 异步下单
func (e *IOExecutor) PlaceOrderAsync(
	ctx context.Context,
	order *domain.Order,
	callback func(*PlaceOrderResult),
) {
	go func() {
		result := &PlaceOrderResult{}

		if e.dryRun {
			// 纸交易模式：模拟下单成功
			result.Order = order
			result.Order.Status = domain.OrderStatusOpen
			// 保持原始订单ID，不生成新的
			if result.Order.OrderID == "" {
				result.Order.OrderID = fmt.Sprintf("dry_run_%d", time.Now().UnixNano())
			}
			ioExecutorLog.Infof("📝 [纸交易] 模拟下单: orderID=%s, assetID=%s, side=%s, price=%.4f, size=%.4f",
				result.Order.OrderID, order.AssetID, order.Side, order.Price.ToDecimal(), order.Size)
			callback(result)
			return
		}

		// 真实交易：调用交易所 API
		createdOrder, err := e.placeOrderSync(ctx, order)
		if err != nil {
			result.Error = err
			// 即使失败，也返回原始订单（标记为失败状态），以便状态同步逻辑能正确处理
			result.Order = order
			result.Order.Status = domain.OrderStatusFailed
			ioExecutorLog.Errorf("❌ 下单失败: orderID=%s, error=%v", order.OrderID, err)
		} else {
			result.Order = createdOrder
			ioExecutorLog.Infof("✅ 下单成功: orderID=%s", createdOrder.OrderID)
		}

		callback(result)
	}()
}

// placeOrderSync 同步下单（内部方法）
func (e *IOExecutor) placeOrderSync(ctx context.Context, order *domain.Order) (*domain.Order, error) {
	// 确定订单类型（默认 GTC）
	orderType := order.OrderType
	if orderType == "" {
		orderType = types.OrderTypeGTC
	}

	// 创建订单选项
	options := &types.CreateOrderOptions{
		TickSize: types.TickSize0001,
		NegRisk:  boolPtr(false),
	}

	// 构建用户订单
	userOrder := &types.UserOrder{
		TokenID: order.AssetID,
		Price:   order.Price.ToDecimal(),
		Size:    order.Size,
		Side:    order.Side,
	}

	// 创建签名订单
	signedOrder, err := e.clobClient.CreateOrder(ctx, userOrder, options)
	if err != nil {
		return nil, fmt.Errorf("创建订单失败: %w", err)
	}

	// 提交订单到交易所
	orderResp, err := e.clobClient.PostOrder(ctx, signedOrder, orderType, false)
	if err != nil {
		return nil, fmt.Errorf("提交订单失败: %w", err)
	}

	if orderResp == nil || !orderResp.Success {
		errorMsg := "未知错误"
		if orderResp != nil {
			errorMsg = orderResp.ErrorMsg
		}
		return nil, fmt.Errorf("订单提交失败: %s", errorMsg)
	}

	// 转换为 domain.Order
	createdOrder := convertOrderResponseToDomain(orderResp, order)

	return createdOrder, nil
}

// CancelOrderAsync 异步取消订单
func (e *IOExecutor) CancelOrderAsync(
	ctx context.Context,
	orderID string,
	callback func(error),
) {
	go func() {
		if e.dryRun {
			// 纸交易模式：模拟取消成功
			ioExecutorLog.Infof("📝 [纸交易] 模拟取消订单: orderID=%s", orderID)
			callback(nil)
			return
		}

		// 真实交易：调用交易所 API
		_, err := e.clobClient.CancelOrder(ctx, orderID)
		if err != nil {
			ioExecutorLog.Errorf("❌ 取消订单失败: orderID=%s, error=%v", orderID, err)
		} else {
			ioExecutorLog.Infof("✅ 取消订单成功: orderID=%s", orderID)
		}

		callback(err)
	}()
}

// convertOrderResponseToDomain 将交易所订单响应转换为领域模型
func convertOrderResponseToDomain(orderResp *types.OrderResponse, originalOrder *domain.Order) *domain.Order {
	order := &domain.Order{
		OrderID:      orderResp.OrderID,
		MarketSlug:   originalOrder.MarketSlug,
		AssetID:      originalOrder.AssetID,
		Side:         originalOrder.Side,
		Price:        originalOrder.Price,
		Size:         originalOrder.Size,
		FilledSize:   originalOrder.FilledSize,
		GridLevel:    originalOrder.GridLevel,
		TokenType:    originalOrder.TokenType,
		HedgeOrderID: originalOrder.HedgeOrderID,
		CreatedAt:    time.Now(),
		IsEntryOrder: originalOrder.IsEntryOrder,
		PairOrderID:  originalOrder.PairOrderID,
		OrderType:    originalOrder.OrderType,
	}

	// 根据订单响应设置状态
	switch orderResp.Status {
	case "OPEN", "PENDING":
		order.Status = domain.OrderStatusOpen
	case "PARTIALLY_FILLED":
		order.Status = domain.OrderStatusPartial
	case "FILLED":
		order.Status = domain.OrderStatusFilled
		now := time.Now()
		order.FilledAt = &now
		// 对于已成交，已成交数量等于 size
		order.FilledSize = order.Size
	case "CANCELLED":
		order.Status = domain.OrderStatusCanceled
	default:
		order.Status = domain.OrderStatusPending
	}

	return order
}
