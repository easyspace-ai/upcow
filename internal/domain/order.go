package domain

import (
	"math"
	"time"
	"github.com/betbot/gobet/clob/types"
)

// Order 订单领域模型
type Order struct {
	OrderID      string          // 订单 ID
	MarketSlug   string          // 订单所属市场周期（btc-updown-15m-xxxx），用于只管理本周期
	AssetID      string          // 资产 ID
	Side         types.Side      // 订单方向
	Price        Price           // 订单价格
	Size         float64         // 订单原始数量（requested size）
	FilledSize   float64         // 已成交数量（partial fill 累计）
	FilledPrice  *Price          // 实际成交价格（从 Trade 消息获取，可选）
	GridLevel    int             // 网格层级（分）
	TokenType    TokenType       // Token 类型
	HedgeOrderID *string        // 对冲订单 ID（可选）
	CreatedAt    time.Time       // 创建时间
	FilledAt     *time.Time      // 成交时间（可选）
	CanceledAt   *time.Time      // 取消时间（可选）
	IsEntryOrder bool            // 是否为入场订单
	PairOrderID  *string         // 配对订单 ID（entry <-> hedge）
	Status       OrderStatus     // 订单状态
	OrderType    types.OrderType // 订单类型（GTC/FAK/FOK，默认GTC）
	TickSize     types.TickSize  // 价格精度（可选，如果未设置则使用默认值）
	NegRisk      *bool           // 是否为负风险市场（可选）
}

// OrderStatus 订单状态
type OrderStatus string

const (
	OrderStatusPending   OrderStatus = "pending"   // 待处理
	OrderStatusOpen      OrderStatus = "open"     // 开放中
	OrderStatusPartial   OrderStatus = "partial"  // 部分成交
	OrderStatusFilled    OrderStatus = "filled"   // 已成交
	OrderStatusCanceled  OrderStatus = "canceled" // 已取消
	OrderStatusFailed    OrderStatus = "failed"   // 失败
)

// IsFilled 检查订单是否已成交
func (o *Order) IsFilled() bool {
	return o.Status == OrderStatusFilled && o.FilledAt != nil
}

func (o *Order) IsPartiallyFilled() bool {
	return o.Status == OrderStatusPartial && o.FilledSize > 0 && o.FilledSize < o.Size
}

// ExecutedSize 返回已成交数量（优先 FilledSize）
func (o *Order) ExecutedSize() float64 {
	if o == nil {
		return 0
	}
	if o.FilledSize > 0 {
		return o.FilledSize
	}
	return o.Size
}

// IsOpen 检查订单是否开放中
func (o *Order) IsOpen() bool {
	return o.Status == OrderStatusOpen
}

// IsFinalStatus 检查订单是否为最终状态（filled/canceled/failed）
// 最终状态不应该被中间状态（open/pending）覆盖
func (o *Order) IsFinalStatus() bool {
	return o.Status == OrderStatusFilled || o.Status == OrderStatusCanceled || o.Status == OrderStatusFailed
}

// HasFinalStatusTimestamp 检查订单是否有最终状态的时间戳
// 如果有时间戳，说明已经确认了最终状态，不应该被覆盖
func (o *Order) HasFinalStatusTimestamp() bool {
	if o.Status == OrderStatusFilled {
		return o.FilledAt != nil
	}
	if o.Status == OrderStatusCanceled {
		return o.CanceledAt != nil
	}
	return false
}

// Price 价格值对象（固定精度：1e-4）
//
// Polymarket 的 tick size 可能为 0.1 / 0.01 / 0.001 / 0.0001。
// 为了让策略/执行层不丢精度，这里使用 1e-4 作为内部最小单位（pips）：
//   - 1 pip  = 0.0001
//   - 100 pips = 0.01（旧系统中的 1 cent）
//   - 10000 pips = 1.0
type Price struct {
	// Pips: 价格 * 10000（范围通常 1..9999）
	Pips int
}

// ToDecimal 转换为小数（例如 6000 pips = 0.6000）
func (p Price) ToDecimal() float64 {
	return float64(p.Pips) / 10000.0
}

// ToCents 返回“分（0.01）口径”的整数（用于兼容旧策略阈值/日志）。
// 注意：这不是内部精度，只是展示/阈值换算用。
func (p Price) ToCents() int {
	// 100 pips = 1 cent
	return int(math.Round(float64(p.Pips) / 100.0))
}

// PriceFromDecimal 从小数创建价格（四舍五入到 1e-4）
func PriceFromDecimal(decimal float64) Price {
	return Price{
		Pips: int(math.Round(decimal * 10000)),
	}
}

// Add 价格相加
func (p Price) Add(other Price) Price {
	return Price{Pips: p.Pips + other.Pips}
}

// Subtract 价格相减
func (p Price) Subtract(other Price) Price {
	return Price{Pips: p.Pips - other.Pips}
}

// GreaterThan 检查是否大于
func (p Price) GreaterThan(other Price) bool {
	return p.Pips > other.Pips
}

// LessThan 检查是否小于
func (p Price) LessThan(other Price) bool {
	return p.Pips < other.Pips
}

// GreaterThanOrEqual 检查是否大于等于
func (p Price) GreaterThanOrEqual(other Price) bool {
	return p.Pips >= other.Pips
}

// LessThanOrEqual 检查是否小于等于
func (p Price) LessThanOrEqual(other Price) bool {
	return p.Pips <= other.Pips
}

