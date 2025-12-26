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

	// 下单资金地址（代理钱包 / funder / proxy_address）与签名类型
	// - funderAddress 非空时，订单 maker 将使用该地址（signer 仍为 EOA）
	// - signatureType 用于 CLOB 的签名类型（Browser/GnosisSafe 等）
	funderAddress string
	signatureType types.SignatureType
}

// NewIOExecutor 创建 IO 执行器
func NewIOExecutor(clobClient *client.Client, dryRun bool) *IOExecutor {
	return &IOExecutor{
		clobClient:    clobClient,
		dryRun:        dryRun,
		funderAddress: "",
		signatureType: types.SignatureTypeBrowser,
	}
}

// SetFunderAddress 设置下单资金地址（proxy_address）与签名类型。
// 注意：这里不会校验地址合法性，调用方应保证传入的 funderAddress 正确。
func (e *IOExecutor) SetFunderAddress(funderAddress string, signatureType types.SignatureType) {
	e.funderAddress = funderAddress
	e.signatureType = signatureType
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
			
			// ✅ 修复：FAK 订单在纸交易模式下立即"成交"
			// FAK (Fill-And-Kill) 订单要么立即成交，要么立即取消
			// 在纸交易模式下，我们模拟立即成交
			if order.OrderType == types.OrderTypeFAK {
				result.Order.Status = domain.OrderStatusFilled
				result.Order.FilledSize = order.Size // 完全成交
			}
			
			// 保持原始订单ID，不生成新的
			if result.Order.OrderID == "" {
				result.Order.OrderID = fmt.Sprintf("dry_run_%d", time.Now().UnixNano())
			}
			ioExecutorLog.Infof("📝 [纸交易] 模拟下单: orderID=%s, assetID=%s, side=%s, price=%.4f, size=%.4f, status=%s",
				result.Order.OrderID, order.AssetID, order.Side, order.Price.ToDecimal(), order.Size, result.Order.Status)
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

	// 创建订单选项（优先使用订单中指定的精度信息）
	options := &types.CreateOrderOptions{
		TickSize: types.TickSize0001, // 默认值
		NegRisk:  boolPtr(false),     // 默认值
	}
	
	// 如果订单中指定了 TickSize，使用订单的值
	if order.TickSize != "" {
		options.TickSize = order.TickSize
	}
	
	// 如果订单中指定了 NegRisk，使用订单的值
	if order.NegRisk != nil {
		options.NegRisk = order.NegRisk
	}

	// 构建用户订单
	userOrder := &types.UserOrder{
		TokenID: order.AssetID,
		Price:   order.Price.ToDecimal(),
		Size:    order.Size,
		Side:    order.Side,
	}

	// 创建签名订单
	var signedOrder *types.SignedOrder
	var err error
	if e.funderAddress != "" {
		// 使用 proxy_address 作为 maker（资金地址），signer 仍为 EOA 私钥地址
		signedOrder, err = e.clobClient.CreateOrderWithFunder(ctx, userOrder, options, e.funderAddress, e.signatureType)
	} else {
		signedOrder, err = e.clobClient.CreateOrder(ctx, userOrder, options)
	}
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
