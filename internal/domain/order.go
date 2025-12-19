package domain

import (
	"time"
	"github.com/betbot/gobet/clob/types"
)

// Order 订单领域模型
type Order struct {
	OrderID      string          // 订单 ID
	AssetID      string          // 资产 ID
	Side         types.Side      // 订单方向
	Price        Price           // 订单价格
	Size         float64         // 订单数量
	GridLevel    int             // 网格层级（分）
	TokenType    TokenType       // Token 类型
	HedgeOrderID *string        // 对冲订单 ID（可选）
	CreatedAt    time.Time       // 创建时间
	FilledAt     *time.Time      // 成交时间（可选）
	IsEntryOrder bool            // 是否为入场订单
	PairOrderID  *string         // 配对订单 ID（entry <-> hedge）
	Status       OrderStatus     // 订单状态
	OrderType    types.OrderType // 订单类型（GTC/FAK/FOK，默认GTC）
}

// OrderStatus 订单状态
type OrderStatus string

const (
	OrderStatusPending   OrderStatus = "pending"   // 待处理
	OrderStatusOpen      OrderStatus = "open"     // 开放中
	OrderStatusFilled    OrderStatus = "filled"   // 已成交
	OrderStatusCanceled  OrderStatus = "canceled" // 已取消
	OrderStatusFailed    OrderStatus = "failed"   // 失败
)

// IsFilled 检查订单是否已成交
func (o *Order) IsFilled() bool {
	return o.Status == OrderStatusFilled && o.FilledAt != nil
}

// IsOpen 检查订单是否开放中
func (o *Order) IsOpen() bool {
	return o.Status == OrderStatusOpen
}

// Price 价格值对象（以分为单位）
type Price struct {
	Cents int // 价格（分）
}

// ToDecimal 转换为小数（例如 60 分 = 0.60）
func (p Price) ToDecimal() float64 {
	return float64(p.Cents) / 100.0
}

// FromDecimal 从小数创建价格
func PriceFromDecimal(decimal float64) Price {
	return Price{
		Cents: int(decimal * 100),
	}
}

// Add 价格相加
func (p Price) Add(other Price) Price {
	return Price{Cents: p.Cents + other.Cents}
}

// Subtract 价格相减
func (p Price) Subtract(other Price) Price {
	return Price{Cents: p.Cents - other.Cents}
}

// GreaterThan 检查是否大于
func (p Price) GreaterThan(other Price) bool {
	return p.Cents > other.Cents
}

// LessThan 检查是否小于
func (p Price) LessThan(other Price) bool {
	return p.Cents < other.Cents
}

// GreaterThanOrEqual 检查是否大于等于
func (p Price) GreaterThanOrEqual(other Price) bool {
	return p.Cents >= other.Cents
}

// LessThanOrEqual 检查是否小于等于
func (p Price) LessThanOrEqual(other Price) bool {
	return p.Cents <= other.Cents
}

