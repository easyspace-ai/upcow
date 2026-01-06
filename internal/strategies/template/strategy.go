package template

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/execution"
	"github.com/betbot/gobet/internal/services"
	"github.com/betbot/gobet/internal/strategies/common"
	"github.com/betbot/gobet/internal/strategies/orderutil"
	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/sirupsen/logrus"
)

var log = logrus.WithField("strategy", ID)

func init() { bbgo.RegisterStrategy(ID, &Strategy{}) }

// Strategy: 新架构模板（完整示例）
//
// 新架构特性：
// 1. 订单更新回调：通过 TradingService.OnOrderUpdate() 注册，实时跟踪订单状态
// 2. 成本基础跟踪：Position 支持多次成交累加，自动计算平均价格和盈亏
// 3. 订单跟踪：可以跟踪订单状态，处理订单失败等情况
// 4. 周期管理：OnCycle() 统一处理周期切换，无需手动对比 slug
//
// 使用 ExecuteMultiLeg 下单（单腿或多腿都一样）：
// - 支持并发下单（parallel）或顺序下单（sequential）
// - 自动 in-flight 去重，防止重复下单
// - 支持自动对冲（如果配置了 Hedge）
type Strategy struct {
	TradingService *services.TradingService
	Config         `yaml:",inline" json:",inline"`

	mu sync.Mutex
	// 避免在周期切换/重复 Subscribe 时重复注册 handler（OrderEngine handler 列表不去重）
	orderUpdateOnce sync.Once

	// 周期状态
	fired bool

	// 订单跟踪（可选）：利用本地订单状态管理
	lastOrderID   string
	pendingOrders map[string]*domain.Order // 待确认的订单

	autoMerge common.AutoMergeController
}

func (s *Strategy) ID() string      { return ID }
func (s *Strategy) Name() string    { return ID }
func (s *Strategy) Defaults() error { return nil }
func (s *Strategy) Validate() error { return s.Config.Validate() }

// Initialize 初始化策略
// 在这里可以：
// - 读取全局配置
// - 注册订单更新回调（推荐）
// - 初始化内部状态
func (s *Strategy) Initialize() error {
	// 初始化订单跟踪（可选）
	if s.pendingOrders == nil {
		s.pendingOrders = make(map[string]*domain.Order)
	}

	// 注册订单更新回调（推荐）：利用本地订单状态管理
	// 当订单状态更新时（通过 WebSocket 或 API 同步），立即更新本地状态
	if s.TradingService != nil {
		s.orderUpdateOnce.Do(func() {
			// 使用 OrderUpdateHandlerFunc 包装方法
			handler := services.OrderUpdateHandlerFunc(s.OnOrderUpdate)
			s.TradingService.OnOrderUpdate(handler)
			log.Infof("✅ [%s] 已注册订单更新回调（利用本地订单状态管理）", ID)
		})
	}

	return nil
}

func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
	session.OnPriceChanged(s)
	log.Infof("✅ [%s] 策略已订阅价格变化事件 (session=%s)", ID, session.Name)

	// 兜底：有些部署/注入顺序下 Initialize 时 TradingService 可能尚未注入；
	// 这里用 once 保证最多注册一次，且不会因为周期切换重复注册。
	if s.TradingService != nil {
		s.orderUpdateOnce.Do(func() {
			handler := services.OrderUpdateHandlerFunc(s.OnOrderUpdate)
			s.TradingService.OnOrderUpdate(handler)
			log.Infof("✅ [%s] 已注册订单更新回调（Subscribe 兜底）", ID)
		})
	}
}

func (s *Strategy) Run(ctx context.Context, _ bbgo.OrderExecutor, _ *bbgo.ExchangeSession) error {
	<-ctx.Done()
	return ctx.Err()
}

// OnCycle 周期切换回调（框架层统一调用）
// 无需手动对比 slug，框架会自动处理周期切换
// 在这里重置周期相关的状态
func (s *Strategy) OnCycle(_ context.Context, _ *domain.Market, _ *domain.Market) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fired = false
	// 重置订单跟踪（周期切换时清理）
	s.lastOrderID = ""
	s.pendingOrders = make(map[string]*domain.Order)
}

// OnOrderUpdate 订单更新回调（可选但推荐）
// 当订单状态更新时（通过 WebSocket 或 API 同步），立即更新本地状态
// 可以用于：
// - 跟踪订单状态变化
// - 处理订单失败/取消
// - 更新仓位成本基础（如果需要）
func (s *Strategy) OnOrderUpdate(ctx context.Context, order *domain.Order) error {
	if order == nil || order.OrderID == "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// 更新订单跟踪
	s.lastOrderID = order.OrderID
	log.Debugf("📊 [%s] 订单状态更新: orderID=%s status=%s filledSize=%.4f",
		ID, order.OrderID, order.Status, order.FilledSize)

	// 更新待确认订单列表
	if order.Status == domain.OrderStatusFilled ||
		order.Status == domain.OrderStatusCanceled ||
		order.Status == domain.OrderStatusFailed {
		delete(s.pendingOrders, order.OrderID)
	} else if order.Status == domain.OrderStatusOpen || order.Status == domain.OrderStatusPending {
		s.pendingOrders[order.OrderID] = order
	}

	// 示例：订单失败时记录日志
	if order.Status == domain.OrderStatusFailed {
		log.Warnf("⚠️ [%s] 订单失败: orderID=%s", ID, order.OrderID)
	}

	return nil
}

// OnPriceChanged 处理价格变化事件
// 这是策略的核心逻辑，当价格变化时触发
func (s *Strategy) OnPriceChanged(ctx context.Context, e *events.PriceChangedEvent) error {
	if e == nil || e.Market == nil || s.TradingService == nil {
		return nil
	}
	s.autoMerge.MaybeAutoMerge(ctx, s.TradingService, e.Market, s.AutoMerge, log.Infof)
	// 系统级安全兜底：仅处理当前周期 market 的事件（即使框架层已有过滤，这里仍做防御）
	cur := s.TradingService.GetCurrentMarket()
	if cur != "" && cur != e.Market.Slug {
		log.Debugf("🔄 [%s] 跳过非当前周期价格事件: eventMarket=%s currentMarket=%s", ID, e.Market.Slug, cur)
		return nil
	}

	s.mu.Lock()
	// 示例：简单的去重逻辑（每周期只触发一次）
	if s.fired {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	// 示例：买 YES 一次（用于验证链路）
	orderCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	// 获取最佳买入价格（使用 orderutil 工具函数）
	price, err := orderutil.QuoteBuyPrice(orderCtx, s.TradingService, e.Market.YesAssetID, 0)
	if err != nil {
		log.Warnf("⚠️ [%s] 获取价格失败: %v", ID, err)
		return nil
	}

	// 构建多腿请求（单腿或多腿都可以）
	req := execution.MultiLegRequest{
		Name:       "template_buy_yes",
		MarketSlug: e.Market.Slug,
		Legs: []execution.LegIntent{{
			Name:      "buy_yes",
			AssetID:   e.Market.YesAssetID,
			TokenType: domain.TokenTypeUp,
			Side:      types.SideBuy,
			Price:     price,
			Size:      s.OrderSize,
			OrderType: types.OrderTypeFAK, // FAK: 立即成交或取消
		}},
		Hedge: execution.AutoHedgeConfig{Enabled: false}, // 不启用自动对冲
	}

	// 执行多腿订单（支持并发或顺序执行）
	createdOrders, err := s.TradingService.ExecuteMultiLeg(orderCtx, req)
	if err != nil {
		// fail-safe：系统暂停/市场不一致时属于“预期拒绝”，不应污染策略状态
		estr := strings.ToLower(err.Error())
		if strings.Contains(estr, "trading paused") || strings.Contains(estr, "market mismatch") {
			log.Warnf("⏸️ [%s] 系统拒绝下单（fail-safe，预期行为）: %v", ID, err)
			return nil
		}
		log.Warnf("⚠️ [%s] 下单失败: %v", ID, err)
		return nil
	}

	// 更新状态
	s.mu.Lock()
	s.fired = true
	if len(createdOrders) > 0 {
		s.lastOrderID = createdOrders[0].OrderID
		// 添加到待确认订单列表
		for _, order := range createdOrders {
			s.pendingOrders[order.OrderID] = order
		}
	}
	s.mu.Unlock()

	log.Infof("✅ [%s] 已下单: yes @ %.4f size=%.4f market=%s orders=%d",
		ID, price.ToDecimal(), s.OrderSize, e.Market.Slug, len(createdOrders))

	// 注意：订单状态更新会通过 OnOrderUpdate() 回调自动处理
	// 仓位成本基础会通过 OrderEngine 自动更新（Position.AddFill()）

	return nil
}

// 示例：多腿订单（Entry + Hedge）
// func (s *Strategy) executeMultiLegExample(ctx context.Context, market *domain.Market) error {
// 	orderCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
// 	defer cancel()
//
// 	// 获取 Entry 和 Hedge 价格
// 	entryPrice, err := orderutil.QuoteBuyPrice(orderCtx, s.TradingService, market.YesAssetID, 0)
// 	if err != nil {
// 		return err
// 	}
// 	hedgePrice, err := orderutil.QuoteBuyPrice(orderCtx, s.TradingService, market.NoAssetID, 0)
// 	if err != nil {
// 		return err
// 	}
//
// 	req := execution.MultiLegRequest{
// 		Name:       "template_entry_hedge",
// 		MarketSlug: market.Slug,
// 		Legs: []execution.LegIntent{
// 			{
// 				Name:      "entry",
// 				AssetID:   market.YesAssetID,
// 				TokenType: domain.TokenTypeUp,
// 				Side:      types.SideBuy,
// 				Price:     entryPrice,
// 				Size:      s.OrderSize,
// 				OrderType: types.OrderTypeFAK, // Entry: FAK（立即成交）
// 			},
// 			{
// 				Name:      "hedge",
// 				AssetID:   market.NoAssetID,
// 				TokenType: domain.TokenTypeDown,
// 				Side:      types.SideBuy,
// 				Price:     hedgePrice,
// 				Size:      s.OrderSize,
// 				OrderType: types.OrderTypeGTC, // Hedge: GTC（限价单）
// 			},
// 		},
// 		Hedge: execution.AutoHedgeConfig{Enabled: false},
// 	}
//
// 	createdOrders, err := s.TradingService.ExecuteMultiLeg(orderCtx, req)
// 	if err != nil {
// 		return err
// 	}
//
// 	log.Infof("✅ [%s] 多腿订单已提交: orders=%d", ID, len(createdOrders))
// 	return nil
// }
