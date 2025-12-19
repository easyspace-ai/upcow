package events

import (
	"time"
	"github.com/betbot/gobet/internal/domain"
)

// PriceChangedEvent 价格变化事件
type PriceChangedEvent struct {
	Market      *domain.Market
	TokenType   domain.TokenType
	OldPrice    *domain.Price
	NewPrice    domain.Price
	Timestamp   time.Time
}

// OrderPlacedEvent 订单下单事件
type OrderPlacedEvent struct {
	Order     *domain.Order
	Market    *domain.Market
	Timestamp time.Time
}

// OrderFilledEvent 订单成交事件
type OrderFilledEvent struct {
	Order     *domain.Order
	Market    *domain.Market
	Timestamp time.Time
}

// OrderCanceledEvent 订单取消事件
type OrderCanceledEvent struct {
	Order     *domain.Order
	Market    *domain.Market
	Timestamp time.Time
}

// PositionOpenedEvent 仓位开启事件
type PositionOpenedEvent struct {
	Position  *domain.Position
	Market    *domain.Market
	Timestamp time.Time
}

// PositionClosedEvent 仓位关闭事件
type PositionClosedEvent struct {
	Position  *domain.Position
	Market    *domain.Market
	Profit    int // 利润（分）
	Timestamp time.Time
}

// GridLevelReachedEvent 网格层级到达事件
type GridLevelReachedEvent struct {
	Market      *domain.Market
	TokenType   domain.TokenType
	GridLevel   int
	Price       domain.Price
	Direction   string // "up" 或 "down"
	Timestamp   time.Time
}

// CriticalErrorEvent 严重错误事件（触发机器人停止）
type CriticalErrorEvent struct {
	Strategy    string    // 策略名称
	Error       string    // 错误信息
	Reason      string    // 错误原因
	Timestamp   time.Time
}

