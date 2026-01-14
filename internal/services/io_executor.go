package services

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/betbot/gobet/clob/client"
	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
)

var ioExecutorLog = logrus.WithField("component", "io_executor")

// parseTimeframeFromSlug 从 MarketSlug 解析时间周期（15m/1h/4h）
// 支持的格式：
// 1. {symbol}-{kind}-{timeframe}-{timestamp} 例如：btc-updown-15m-1767942900
// 2. {coinName}-up-or-down-{month}-{day}-{hour}{am|pm}-et 例如：bitcoin-up-or-down-january-9-5am-et (1h市场)
func (e *ioExecutor) parseTimeframeFromSlug(slug string) string {
	if slug == "" {
		return ""
	}
	
	slug = strings.ToLower(strings.TrimSpace(slug))
	
	// 格式1: 检查是否包含 -15m- 或 -1h- 或 -4h-
	// 使用正则表达式匹配 -{timeframe}- 格式
	if strings.Contains(slug, "-15m-") {
		return "15m"
	}
	if strings.Contains(slug, "-1h-") {
		return "1h"
	}
	if strings.Contains(slug, "-4h-") {
		return "4h"
	}
	
	// 格式2: 检查是否包含 "up-or-down"（1小时市场通常使用这种格式）
	if strings.Contains(slug, "up-or-down") {
		return "1h"
	}
	
	// 如果无法解析，返回空字符串
	return ""
}

// ioExecutor IO 操作执行器（异步执行，不阻塞 OrderEngine）。
//
// 系统级约束：
// - 必须只被 OrderEngine 调用（统一受 TradingService 的 paused/market gate 管控）。
// - 不对外导出，防止未来误用绕过下单安全门。
type ioExecutor struct {
	clobClient *client.Client
	dryRun     bool

	// 下单资金地址（代理钱包 / funder / proxy_address）与签名类型
	// - funderAddress 非空时，订单 maker 将使用该地址（signer 仍为 EOA）
	// - signatureType 用于 CLOB 的签名类型（Browser/GnosisSafe 等）
	funderAddress string
	signatureType types.SignatureType

	// 默认订单费率（bps）
	// - 0 = maker 费率（限价单通常为 0）
	// - 1000 = 10% taker 费率（市价单通常需要）
	defaultFeeRateBps int
}

// newIOExecutor 创建 IO 执行器（包内私有）。
func newIOExecutor(clobClient *client.Client, dryRun bool) *ioExecutor {
	return &ioExecutor{
		clobClient:    clobClient,
		dryRun:        dryRun,
		funderAddress: "",
		signatureType: types.SignatureTypeBrowser,
	}
}

// SetFunderAddress 设置下单资金地址（proxy_address）与签名类型。
// 注意：这里不会校验地址合法性，调用方应保证传入的 funderAddress 正确。
func (e *ioExecutor) SetFunderAddress(funderAddress string, signatureType types.SignatureType) {
	e.funderAddress = funderAddress
	e.signatureType = signatureType
}

// SetDefaultFeeRateBps 设置默认订单费率（bps）
func (e *ioExecutor) SetDefaultFeeRateBps(feeRateBps int) {
	e.defaultFeeRateBps = feeRateBps
}

// PlaceOrderAsync 异步下单
func (e *ioExecutor) PlaceOrderAsync(
	ctx context.Context,
	order *domain.Order,
	callback func(*PlaceOrderResult),
) {
	go func() {
		result := &PlaceOrderResult{}

		if e.dryRun {
			// 纸交易模式：模拟下单
			result.Order = order

			// 保持原始订单ID，不生成新的
			if result.Order.OrderID == "" {
				result.Order.OrderID = fmt.Sprintf("dry_run_%d", time.Now().UnixNano())
			}

			// 根据订单类型决定成交逻辑
			orderType := order.OrderType
			if orderType == "" {
				orderType = types.OrderTypeGTC
			}

			if orderType == types.OrderTypeFAK {
				// FAK 订单：立即成交（FAK 是立即成交或取消）
				result.Order.Status = domain.OrderStatusFilled
				result.Order.FilledSize = order.Size
				now := time.Now()
				result.Order.FilledAt = &now
				ioExecutorLog.Debugf("📝 [纸交易] FAK订单立即成交: orderID=%s, assetID=%s, side=%s, price=%.4f, size=%.4f",
					result.Order.OrderID, order.AssetID, order.Side, order.Price.ToDecimal(), order.Size)
			} else {
				// GTC 订单：根据订单簿价格判断是否可以成交
				// 获取订单簿价格
				book, err := e.clobClient.GetOrderBook(ctx, order.AssetID, nil)
				if err != nil {
					// 如果无法获取订单簿，默认保持 OPEN 状态（更保守）
					result.Order.Status = domain.OrderStatusOpen
					ioExecutorLog.Warnf("⚠️ [纸交易] 无法获取订单簿，GTC订单保持OPEN: orderID=%s, assetID=%s, err=%v",
						result.Order.OrderID, order.AssetID, err)
				} else {
					orderPrice := order.Price.ToDecimal()
					var canFill bool

					if order.Side == types.SideBuy {
						// 买单：只有当市场ask价格 <= 订单价格时才能成交
						// 限价买单：我们愿意以orderPrice或更低的价格买入，如果ask <= orderPrice，可以成交
						if len(book.Asks) > 0 {
							askPrice, err := strconv.ParseFloat(book.Asks[0].Price, 64)
							if err == nil && askPrice <= orderPrice {
								canFill = true
							}
						}
					} else {
						// 卖单：只有当市场bid价格 >= 订单价格时才能成交
						// 限价卖单：我们愿意以orderPrice或更高的价格卖出，如果bid >= orderPrice，可以成交
						if len(book.Bids) > 0 {
							bidPrice, err := strconv.ParseFloat(book.Bids[0].Price, 64)
							if err == nil && bidPrice >= orderPrice {
								canFill = true
							}
						}
					}

					// 在 dry run 模式下，使用真实市场价格验证对冲单能否成交
					// 为了测试调价功能，Hedge订单必须严格低于市场ask价格（买单）才能成交
					// 如果订单价格等于或高于ask，说明价格被调整过，应该保持OPEN触发调价
					if canFill && !order.IsEntryOrder {
						// Hedge订单：使用真实市场价格验证，但要求严格价格匹配
						// 限价买单：如果ask <= orderPrice，可以成交；但如果orderPrice <= ask（等于），保持OPEN用于测试
						var marketPrice float64
						var shouldFill bool
						
						if order.Side == types.SideBuy {
							// 买单：市场ask价格必须严格小于订单价格才能成交（不能等于）
							// 如果ask == orderPrice，说明订单价格被调整为ask价，应该保持OPEN触发调价
							if len(book.Asks) > 0 {
								askPrice, _ := strconv.ParseFloat(book.Asks[0].Price, 64)
								marketPrice = askPrice
								// 严格检查：ask价格必须 < 订单价格（不能等于）
								shouldFill = askPrice < orderPrice
							}
						} else {
							// 卖单：市场bid价格必须严格大于订单价格才能成交（不能等于）
							if len(book.Bids) > 0 {
								bidPrice, _ := strconv.ParseFloat(book.Bids[0].Price, 64)
								marketPrice = bidPrice
								// 严格检查：bid价格必须 > 订单价格（不能等于）
								shouldFill = bidPrice > orderPrice
							}
						}
						
						if shouldFill {
							// 价格严格匹配，立即成交（使用真实市场价格）
							result.Order.Status = domain.OrderStatusFilled
							result.Order.FilledSize = order.Size
							now := time.Now()
							result.Order.FilledAt = &now
							ioExecutorLog.Infof("✅ [纸交易] Hedge订单已成交（价格严格匹配真实市场）: orderID=%s, assetID=%s, side=%s, orderPrice=%.4f, marketPrice=%.4f, size=%.4f",
								result.Order.OrderID, order.AssetID, order.Side, orderPrice, marketPrice, order.Size)
						} else {
							// 价格不严格匹配（订单价格等于市场价），保持OPEN状态（用于测试调价功能）
							result.Order.Status = domain.OrderStatusOpen
							ioExecutorLog.Infof("⏸️ [纸交易] Hedge订单保持OPEN（价格等于市场价，用于测试调价）: orderID=%s, assetID=%s, side=%s, orderPrice=%.4f, marketPrice=%.4f, size=%.4f",
								result.Order.OrderID, order.AssetID, order.Side, orderPrice, marketPrice, order.Size)
						}
					} else if canFill {
						// Entry订单：价格匹配立即成交
						result.Order.Status = domain.OrderStatusFilled
						result.Order.FilledSize = order.Size
						now := time.Now()
						result.Order.FilledAt = &now
						ioExecutorLog.Debugf("📝 [纸交易] GTC订单已成交（价格匹配）: orderID=%s, assetID=%s, side=%s, price=%.4f, size=%.4f",
							result.Order.OrderID, order.AssetID, order.Side, order.Price.ToDecimal(), order.Size)
					} else {
						// 无法成交，保持 OPEN 状态
						result.Order.Status = domain.OrderStatusOpen
						ioExecutorLog.Debugf("📝 [纸交易] GTC订单保持OPEN（价格未匹配）: orderID=%s, assetID=%s, side=%s, price=%.4f, size=%.4f",
							result.Order.OrderID, order.AssetID, order.Side, order.Price.ToDecimal(), order.Size)
					}
				}
			}

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
func (e *ioExecutor) placeOrderSync(ctx context.Context, order *domain.Order) (*domain.Order, error) {
	// 确定订单类型（默认 GTC）
	orderType := order.OrderType
	if orderType == "" {
		orderType = types.OrderTypeGTC
	}

	// 创建订单选项（优先使用订单中指定的精度信息）
	options := &types.CreateOrderOptions{
		TickSize:  types.TickSize0001, // 默认值
		NegRisk:   boolPtr(false),     // 默认值
		OrderType: &orderType,         // 传递订单类型，用于精度计算
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

	// ✅ 对于所有订单类型，如果未设置费率，根据市场时间周期和配置设置费率
	// 费率设置规则：
	// - 如果配置了非 0 值，使用配置值
	// - 如果配置为 0，根据市场时间周期决定：
	//   - 15m 市场：必须使用 1000 bps（taker 费率）
	//   - 1h 市场：必须使用 0 bps（taker 费率为 0）
	//   - 其他市场：使用配置值或 0
	if userOrder.FeeRateBps == nil {
		defaultFeeRateBps := e.defaultFeeRateBps
		
		// 如果配置为 0，根据市场时间周期决定费率
		if defaultFeeRateBps == 0 {
			// 从 MarketSlug 解析时间周期
			timeframe := e.parseTimeframeFromSlug(order.MarketSlug)
			
			switch timeframe {
			case "15m":
				// 15分钟市场必须使用 1000 bps
				defaultFeeRateBps = 1000
				ioExecutorLog.Debugf("📝 [IOExecutor] 15m 市场，使用 1000 bps: orderID=%s marketSlug=%s", order.OrderID, order.MarketSlug)
			case "1h":
				// 1小时市场必须使用 0 bps
				defaultFeeRateBps = 0
				ioExecutorLog.Debugf("📝 [IOExecutor] 1h 市场，使用 0 bps: orderID=%s marketSlug=%s", order.OrderID, order.MarketSlug)
			default:
				// 其他市场或无法解析：根据订单类型决定
				if orderType == types.OrderTypeFAK || orderType == types.OrderTypeFOK {
					// taker 订单：默认使用 0（如果市场 taker fee 是 0）
					defaultFeeRateBps = 0
					ioExecutorLog.Debugf("📝 [IOExecutor] %s 订单（taker），无法确定时间周期，使用 0 bps: orderID=%s marketSlug=%s", orderType, order.OrderID, order.MarketSlug)
				} else {
					// maker 订单：使用 0
					defaultFeeRateBps = 0
					ioExecutorLog.Debugf("📝 [IOExecutor] %s 订单（maker），无法确定时间周期，使用 0 bps: orderID=%s marketSlug=%s", orderType, order.OrderID, order.MarketSlug)
				}
			}
		}
		
		userOrder.FeeRateBps = &defaultFeeRateBps
		ioExecutorLog.Debugf("📝 [IOExecutor] %s 订单使用费率: orderID=%s feeRateBps=%d marketSlug=%s", orderType, order.OrderID, defaultFeeRateBps, order.MarketSlug)
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
func (e *ioExecutor) CancelOrderAsync(
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
			// 检查是否是 "Invalid order payload" 错误（订单可能已不存在/已取消）
			errMsg := err.Error()
			if strings.Contains(errMsg, "Invalid order payload") ||
				strings.Contains(errMsg, "HTTP 错误 400") {
				// 将此类错误视为成功（幂等）：订单可能已经被取消或不存在
				ioExecutorLog.Infof("ℹ️ 取消订单返回 400（订单可能已不存在/已取消），视为成功：orderID=%s", orderID)
				callback(nil) // 视为成功，不传递错误
				return
			}
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
