package domain

import (
	"time"
	"github.com/betbot/gobet/clob/types"
)

// Trade 交易领域模型（BBGO风格）
// Trade 代表一次实际的交易执行，与 Order 分离
// Order 是订单（可能未成交），Trade 是交易（已执行）
type Trade struct {
	ID            string     // 交易 ID（从 WebSocket 或 API 获取）
	OrderID       string     // 关联的订单 ID（优先匹配到的订单ID，可能是 taker 或 maker）
	TakerOrderID  string     // Taker 订单 ID（从 trade 消息中获取）
	MakerOrderIDs []string   // Maker 订单 ID 列表（从 trade 消息中的 maker_orders 获取）
	AssetID       string     // 资产 ID
	Side          types.Side // 交易方向
	Price         Price      // 成交价格
	Size          float64    // 成交数量
	TokenType     TokenType  // Token 类型
	Market        *Market    // 市场信息
	Time          time.Time  // 成交时间
	Fee           float64    // 手续费（可选）
}

// Key 返回交易的唯一键（用于去重）
func (t *Trade) Key() string {
	return t.ID
}

