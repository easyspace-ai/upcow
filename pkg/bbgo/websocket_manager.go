package bbgo

import (
	"context"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/infrastructure/websocket"
)

// WebSocketManager 统一管理 WS 连接生命周期（对齐 poly-sdk 的 WebSocketManager 思路）。
//
// 设计目标：
// - 由框架/调度器在“一处”创建与管理 WS（market/user），策略只订阅事件
// - MarketStream/UserWebSocket 内部继续负责：心跳、重连、订阅恢复（market stream）
// - 这里负责：对象创建、注入到 session、统一的接入点
type WebSocketManager struct {
	proxyURL  string
	userCreds *websocket.UserCredentials
}

func NewWebSocketManager(proxyURL string, userCreds *websocket.UserCredentials) *WebSocketManager {
	return &WebSocketManager{
		proxyURL:  proxyURL,
		userCreds: userCreds,
	}
}

// AttachToSession 为 session 注入并连接 WS（market/user）。
// 注意：market stream 的 Connect 在 session.Connect 中触发；
// user stream 在这里异步 Connect（与现有架构一致）。
func (m *WebSocketManager) AttachToSession(ctx context.Context, session *ExchangeSession, market *domain.Market) error {
	if session == nil {
		return nil
	}
	if market != nil {
		session.SetMarket(market)
	}

	// Market WS：每个 session 绑定一个 market stream（market 级别订阅）
	marketStream := websocket.NewMarketStream()
	marketStream.SetProxyURL(m.proxyURL)
	session.SetMarketDataStream(marketStream)

	// User WS：默认每个 session 创建一个（周期切换会关闭旧 session，避免泄漏）
	if m.userCreds != nil {
		userWS := websocket.NewUserWebSocket()
		session.SetUserDataStream(userWS)
		go func() {
			_ = userWS.Connect(ctx, m.userCreds, m.proxyURL)
		}()
	}
	return nil
}

