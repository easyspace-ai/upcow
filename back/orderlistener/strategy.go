package orderlistener

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/execution"
	"github.com/betbot/gobet/internal/services"
	"github.com/betbot/gobet/internal/strategies/common"
	"github.com/betbot/gobet/internal/strategies/orderutil"
	"github.com/betbot/gobet/pkg/bbgo"
)

var log = logrus.WithField("strategy", ID)

func init() { bbgo.RegisterStrategy(ID, &Strategy{}) }

// Strategy 订单监听策略
// 功能：
// - 只监听订单更新，不下单
// - 当监听到订单成交时，自动挂止盈单（加配置的利润点数）
type Strategy struct {
	TradingService *services.TradingService
	Config         `yaml:",inline" json:",inline"`

	// 订单更新队列
	orderC chan *domain.Order

	// 价格更新队列
	priceC chan *events.PriceChangedEvent

	// 追踪已监听到的订单：orderID -> orderMeta
	tracked   map[string]*orderMeta
	trackedMu sync.RWMutex

	autoMerge common.AutoMergeController
}

type orderMeta struct {
	OrderID          string
	AssetID          string
	TokenType        domain.TokenType
	MarketSlug       string
	Side             types.Side
	EntryPriceCents  int
	TargetPriceCents int // 止盈目标价格
	FilledSize       float64
	ExitPlaced       bool // 是否已挂止盈单（限价单）
	UseMarketOrder   bool // 是否使用市价单止盈（数量 < 5 shares）
	RetryCount       int  // 止盈单重试次数

	// 止盈单完整信息
	ExitOrderID        string                     // 止盈单订单ID
	ExitOrderPrice     domain.Price               // 止盈单价格
	ExitOrderSize      float64                    // 止盈单数量
	ExitOrderType      types.OrderType            // 止盈单类型（GTC/FAK）
	ExitOrderStatus    domain.OrderStatus         // 止盈单状态
	ExitOrderCreatedAt time.Time                  // 止盈单创建时间
	ExitOrderRequest   *execution.MultiLegRequest // 完整的订单请求信息
}

func (s *Strategy) ID() string      { return ID }
func (s *Strategy) Name() string    { return ID }
func (s *Strategy) Defaults() error { return nil }
func (s *Strategy) Validate() error { return s.Config.Validate() }

func (s *Strategy) Initialize() error {
	if s.orderC == nil {
		s.orderC = make(chan *domain.Order, 2048)
	}
	if s.priceC == nil {
		s.priceC = make(chan *events.PriceChangedEvent, 2048)
	}
	if s.tracked == nil {
		s.tracked = make(map[string]*orderMeta)
	}
	return nil
}

func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
	// 订阅订单更新和价格更新（价格更新用于市价单止盈）
	session.OnOrderUpdate(s)
	session.OnPriceChanged(s)
	log.Infof("✅ [orderlistener] 策略已订阅订单更新和价格更新 (session=%s)", session.Name)
}

func (s *Strategy) Run(ctx context.Context, _ bbgo.OrderExecutor, _ *bbgo.ExchangeSession) error {
	// 启动订单处理循环和价格处理循环
	go s.processOrders(ctx)
	go s.processPrices(ctx)
	<-ctx.Done()
	return ctx.Err()
}

func (s *Strategy) OnOrderUpdate(_ context.Context, order *domain.Order) error {
	log.Infof("📥 [orderlistener] OnOrderUpdate 被调用: orderID=%s status=%s filledSize=%.4f marketSlug=%s side=%s assetID=%s tokenType=%s",
		order.OrderID, order.Status, order.FilledSize, order.MarketSlug, order.Side, order.AssetID, order.TokenType)

	if order == nil {
		log.Warnf("⚠️ [orderlistener] 订单为 nil，跳过")
		return nil
	}

	select {
	case s.orderC <- order:
		log.Infof("✅ [orderlistener] 订单已投递到队列: orderID=%s status=%s filledSize=%.4f marketSlug=%s side=%s",
			order.OrderID, order.Status, order.FilledSize, order.MarketSlug, order.Side)
	default:
		log.Warnf("⚠️ [orderlistener] 订单更新队列已满，丢弃: orderID=%s", order.OrderID)
	}
	return nil
}

func (s *Strategy) OnPriceChanged(ctx context.Context, e *events.PriceChangedEvent) error {
	if e == nil {
		return nil
	}
	if s.TradingService != nil && e.Market != nil {
		s.autoMerge.MaybeAutoMerge(ctx, s.TradingService, e.Market, s.AutoMerge, log.Infof)
	}

	select {
	case s.priceC <- e:
		log.Debugf("📥 [orderlistener] 收到价格更新: token=%s price=%.4f", e.TokenType, e.NewPrice.ToDecimal())
	default:
		log.Warnf("⚠️ [orderlistener] 价格更新队列已满，丢弃: token=%s", e.TokenType)
	}
	return nil
}

func (s *Strategy) processOrders(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case o := <-s.orderC:
			log.Infof("🔄 [orderlistener] 从队列取出订单: orderID=%s status=%s filledSize=%.4f", o.OrderID, o.Status, o.FilledSize)
			if o == nil || o.OrderID == "" {
				log.Warnf("⚠️ [orderlistener] 队列中的订单无效，跳过: orderID=%s", o.OrderID)
				continue
			}

			s.trackedMu.Lock()
			meta, exists := s.tracked[o.OrderID]
			if !exists {
				// 新订单：记录到 tracked
				targetPriceCents := o.Price.ToCents() + s.ProfitTargetCents
				if targetPriceCents > 100 {
					targetPriceCents = 100
				}
				meta = &orderMeta{
					OrderID:          o.OrderID,
					AssetID:          o.AssetID,
					TokenType:        o.TokenType,
					MarketSlug:       o.MarketSlug,
					Side:             o.Side,
					EntryPriceCents:  o.Price.ToCents(),
					TargetPriceCents: targetPriceCents,
					FilledSize:       o.FilledSize,
					ExitPlaced:       false,
					UseMarketOrder:   false,                     // 初始化为 false，后续根据数量判断
					RetryCount:       0,                         // 初始重试次数为 0
					ExitOrderID:      "",                        // 初始化为空
					ExitOrderStatus:  domain.OrderStatusPending, // 初始状态为 pending
				}
				s.tracked[o.OrderID] = meta
				log.Infof("📌 [orderlistener] 监听到新订单: orderID=%s token=%s side=%s price=%.4f size=%.4f market=%s",
					o.OrderID, o.TokenType, o.Side, o.Price.ToDecimal(), o.Size, o.MarketSlug)
			} else {
				// 更新已存在的订单
				if o.FilledSize > meta.FilledSize {
					meta.FilledSize = o.FilledSize
				}
			}
			s.trackedMu.Unlock()

			// 检查是否需要挂止盈单
			// 条件：订单有成交，且是买单（BUY），且尚未挂止盈单，且订单状态不是已取消或失败
			if o.Side == types.SideBuy && o.FilledSize > 0 && !meta.ExitPlaced &&
				o.Status != domain.OrderStatusCanceled && o.Status != domain.OrderStatusFailed {
				log.Infof("✅ [orderlistener] 满足挂止盈条件: orderID=%s side=%s filledSize=%.4f exitPlaced=%v status=%s",
					o.OrderID, o.Side, o.FilledSize, meta.ExitPlaced, o.Status)
				s.placeTakeProfit(ctx, meta, o)
			} else {
				// 记录不满足条件的原因
				if o.Side != types.SideBuy {
					log.Debugf("⏭️ [orderlistener] 跳过挂止盈（非买单）: orderID=%s side=%s", o.OrderID, o.Side)
				} else if o.FilledSize <= 0 {
					log.Debugf("⏭️ [orderlistener] 跳过挂止盈（无成交）: orderID=%s filledSize=%.4f", o.OrderID, o.FilledSize)
				} else if meta.ExitPlaced {
					log.Debugf("⏭️ [orderlistener] 跳过挂止盈（已挂单）: orderID=%s", o.OrderID)
				} else if o.Status == domain.OrderStatusCanceled || o.Status == domain.OrderStatusFailed {
					log.Debugf("⏭️ [orderlistener] 跳过挂止盈（订单已取消/失败）: orderID=%s status=%s", o.OrderID, o.Status)
				}
			}

			// 清理已完成的订单（只有在已挂止盈单或使用市价单且已止盈后才清理）
			if o.Status == domain.OrderStatusFilled || o.Status == domain.OrderStatusCanceled || o.Status == domain.OrderStatusFailed {
				// 限价单：必须已挂止盈单
				// 市价单：必须已执行止盈
				if meta.ExitPlaced {
					s.trackedMu.Lock()
					delete(s.tracked, o.OrderID)
					s.trackedMu.Unlock()
					log.Debugf("🗑️ [orderlistener] 清理已完成的订单: orderID=%s status=%s", o.OrderID, o.Status)
				}
			}
		}
	}
}

func (s *Strategy) placeTakeProfit(ctx context.Context, meta *orderMeta, order *domain.Order) {
	if meta.ExitPlaced {
		return
	}

	// 计算止盈价格：入场价 + 利润点数
	targetPriceCents := meta.EntryPriceCents + s.ProfitTargetCents
	if targetPriceCents > 100 {
		log.Warnf("⚠️ [orderlistener] 止盈价格超过100分，跳过: orderID=%s entry=%dc target=%dc",
			order.OrderID, meta.EntryPriceCents, targetPriceCents)
		return
	}

	// 只对已成交的部分挂止盈单
	exitSize := order.FilledSize
	if exitSize <= 0 {
		return
	}

	// 检查限价单最小 share 数量要求（GTC 限价单必须 >= 5 shares）
	minShareSize := 5.0 // Polymarket 限价单最小要求

	if exitSize < minShareSize {
		// 数量 < 5 shares，使用市价单止盈（等待价格达到止盈价格时触发）
		meta.UseMarketOrder = true
		log.Infof("📊 [orderlistener] 成交数量 %.4f < %.0f shares，将使用市价单止盈（价格达到 %dc 时触发）: orderID=%s entry=%dc filledSize=%.4f",
			exitSize, minShareSize, targetPriceCents, order.OrderID, meta.EntryPriceCents, exitSize)
		return // 不立即挂单，等待价格达到止盈价格
	}

	// 数量 >= 5 shares，使用限价单（GTC）
	target := domain.Price{Pips: targetPriceCents * 100} // 1 cent = 100 pips

	// 记录详细的止盈单信息
	log.Infof("📋 [orderlistener] 准备挂限价止盈单: orderID=%s entryPrice=%dc targetPrice=%dc exitSize=%.4f shares orderType=GTC market=%s assetID=%s tokenType=%s",
		order.OrderID, meta.EntryPriceCents, targetPriceCents, exitSize, order.MarketSlug, meta.AssetID, meta.TokenType)

	req := execution.MultiLegRequest{
		Name:       fmt.Sprintf("orderlistener_tp_%s", order.OrderID),
		MarketSlug: order.MarketSlug,
		Legs: []execution.LegIntent{{
			Name:      "sell_tp",
			AssetID:   meta.AssetID,
			TokenType: meta.TokenType,
			Side:      types.SideSell,
			Price:     target,
			Size:      exitSize,
			OrderType: types.OrderTypeGTC,
		}},
		Hedge: execution.AutoHedgeConfig{Enabled: false},
	}

	// 保存完整的订单请求信息
	meta.ExitOrderPrice = target
	meta.ExitOrderSize = exitSize
	meta.ExitOrderType = types.OrderTypeGTC
	meta.ExitOrderRequest = &req
	meta.ExitOrderCreatedAt = time.Now()

	orderCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	created, err := s.TradingService.ExecuteMultiLeg(orderCtx, req)
	cancel()

	if err != nil {
		// 记录完整的失败订单信息
		log.Errorf("❌ [orderlistener] 挂止盈失败: entryOrderID=%s entryPrice=%dc targetPrice=%dc exitSize=%.4f shares exitOrderType=GTC exitOrderPrice=%dc market=%s assetID=%s tokenType=%s retryCount=%d error=%v",
			order.OrderID, meta.EntryPriceCents, targetPriceCents, exitSize, targetPriceCents, order.MarketSlug, meta.AssetID, meta.TokenType, meta.RetryCount, err)

		// 重试逻辑：最多重试3次，每次间隔5秒
		maxRetries := 3
		if meta.RetryCount < maxRetries {
			meta.RetryCount++
			log.Infof("🔄 [orderlistener] 准备重试挂止盈单: orderID=%s retryCount=%d/%d 将在5秒后重试",
				order.OrderID, meta.RetryCount, maxRetries)

			// 异步重试，避免阻塞
			go func() {
				time.Sleep(5 * time.Second)
				// 检查订单是否仍然有效（未取消、未失败、仍有成交）
				s.trackedMu.RLock()
				currentMeta, exists := s.tracked[order.OrderID]
				s.trackedMu.RUnlock()

				if !exists || currentMeta.ExitPlaced {
					log.Debugf("⏭️ [orderlistener] 订单已处理或不存在，跳过重试: orderID=%s", order.OrderID)
					return
				}

				// 重新获取订单状态
				retryCtx, retryCancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer retryCancel()

				// 重新调用 placeTakeProfit
				s.placeTakeProfit(retryCtx, currentMeta, order)
			}()
		} else {
			log.Errorf("❌ [orderlistener] 挂止盈单已达到最大重试次数，放弃: orderID=%s retryCount=%d",
				order.OrderID, meta.RetryCount)
		}
		return
	}

	if len(created) > 0 && created[0] != nil && created[0].OrderID != "" {
		meta.ExitPlaced = true
		meta.ExitOrderID = created[0].OrderID
		meta.ExitOrderStatus = created[0].Status

		// 记录完整的订单信息
		if meta.RetryCount > 0 {
			log.Infof("🎯 [orderlistener] 挂限价止盈成功（重试后）: entryOrderID=%s exitOrderID=%s token=%s entryPrice=%dc exitPrice=%dc exitSize=%.4f shares exitOrderType=GTC market=%s retryCount=%d exitOrderStatus=%s",
				order.OrderID, meta.ExitOrderID, meta.TokenType, meta.EntryPriceCents, targetPriceCents, exitSize, order.MarketSlug, meta.RetryCount, meta.ExitOrderStatus)
		} else {
			log.Infof("🎯 [orderlistener] 挂限价止盈成功: entryOrderID=%s exitOrderID=%s token=%s entryPrice=%dc exitPrice=%dc exitSize=%.4f shares exitOrderType=GTC market=%s exitOrderStatus=%s",
				order.OrderID, meta.ExitOrderID, meta.TokenType, meta.EntryPriceCents, targetPriceCents, exitSize, order.MarketSlug, meta.ExitOrderStatus)
		}
	} else {
		log.Warnf("⚠️ [orderlistener] 挂止盈返回空订单: orderID=%s", order.OrderID)
	}
}

// processPrices 处理价格更新，检查是否需要触发市价单止盈
func (s *Strategy) processPrices(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-s.priceC:
			if e == nil || e.Market == nil {
				continue
			}

			s.trackedMu.RLock()
			// 查找所有使用市价单止盈且价格达到止盈目标的订单
			for _, meta := range s.tracked {
				if !meta.UseMarketOrder || meta.ExitPlaced {
					continue
				}
				// 只处理匹配的 token 类型
				if meta.TokenType != e.TokenType {
					continue
				}
				// 检查价格是否达到止盈目标
				if e.NewPrice.ToCents() >= meta.TargetPriceCents {
					// 价格达到止盈目标，使用市价单止盈
					log.Infof("📊 [orderlistener] 价格达到止盈目标: orderID=%s token=%s currentPrice=%.4f targetPrice=%dc",
						meta.OrderID, meta.TokenType, e.NewPrice.ToDecimal(), meta.TargetPriceCents)
					s.trackedMu.RUnlock()
					s.executeMarketOrderTakeProfit(ctx, meta, e.Market)
					s.trackedMu.RLock()
				}
			}
			s.trackedMu.RUnlock()
		}
	}
}

// executeMarketOrderTakeProfit 执行市价单止盈
func (s *Strategy) executeMarketOrderTakeProfit(ctx context.Context, meta *orderMeta, market *domain.Market) {
	if meta.ExitPlaced {
		return
	}

	// 检查最小订单金额要求（市价单 >= 1 USDC）
	exitSize := meta.FilledSize
	if exitSize <= 0 {
		return
	}

	// 获取当前最优卖价（bestBid）用于市价单
	quoteCtx, quoteCancel := context.WithTimeout(ctx, 10*time.Second)
	bestBidPrice, err := orderutil.QuoteSellPrice(quoteCtx, s.TradingService, meta.AssetID, 0)
	quoteCancel()
	if err != nil {
		log.Errorf("❌ [orderlistener] 获取最优卖价失败: orderID=%s error=%v", meta.OrderID, err)
		return
	}

	// 检查最小订单金额要求（市价单 >= 1 USDC）
	minOrderAmount := 1.0 // 最小订单金额 1 USDC
	estimatedAmount := bestBidPrice.ToDecimal() * exitSize

	if estimatedAmount < minOrderAmount {
		log.Debugf("⏳ [orderlistener] 市价单金额估算 %.2f USDC < %.2f USDC，等待更多成交: orderID=%s",
			estimatedAmount, minOrderAmount, meta.OrderID)
		return
	}

	// 记录详细的市价单止盈信息
	log.Infof("📋 [orderlistener] 准备挂市价止盈单: orderID=%s entryPrice=%dc targetPrice=%dc exitSize=%.4f shares orderType=FAK bestBidPrice=%.4f estimatedAmount=%.2f USDC market=%s assetID=%s tokenType=%s",
		meta.OrderID, meta.EntryPriceCents, meta.TargetPriceCents, exitSize, bestBidPrice.ToDecimal(), estimatedAmount, meta.MarketSlug, meta.AssetID, meta.TokenType)

	// 使用 FAK 市价单（Fill-And-Kill），使用当前最优卖价
	req := execution.MultiLegRequest{
		Name:       fmt.Sprintf("orderlistener_tp_market_%s", meta.OrderID),
		MarketSlug: meta.MarketSlug,
		Legs: []execution.LegIntent{{
			Name:      "sell_tp_market",
			AssetID:   meta.AssetID,
			TokenType: meta.TokenType,
			Side:      types.SideSell,
			Price:     bestBidPrice, // 使用当前最优卖价
			Size:      exitSize,
			OrderType: types.OrderTypeFAK, // 使用 FAK 市价单
		}},
		Hedge: execution.AutoHedgeConfig{Enabled: false},
	}

	// 保存完整的订单请求信息
	meta.ExitOrderPrice = bestBidPrice
	meta.ExitOrderSize = exitSize
	meta.ExitOrderType = types.OrderTypeFAK
	meta.ExitOrderRequest = &req
	meta.ExitOrderCreatedAt = time.Now()

	orderCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	created, err := s.TradingService.ExecuteMultiLeg(orderCtx, req)
	cancel()

	if err != nil {
		// 记录完整的失败订单信息
		log.Errorf("❌ [orderlistener] 市价单止盈失败: entryOrderID=%s entryPrice=%dc targetPrice=%dc exitSize=%.4f shares exitOrderType=FAK exitOrderPrice=%.4f bestBidPrice=%.4f estimatedAmount=%.2f USDC market=%s assetID=%s tokenType=%s retryCount=%d error=%v",
			meta.OrderID, meta.EntryPriceCents, meta.TargetPriceCents, exitSize, bestBidPrice.ToDecimal(), bestBidPrice.ToDecimal(), estimatedAmount, meta.MarketSlug, meta.AssetID, meta.TokenType, meta.RetryCount, err)

		// 重试逻辑：最多重试3次，每次间隔5秒
		maxRetries := 3
		if meta.RetryCount < maxRetries {
			meta.RetryCount++
			log.Infof("🔄 [orderlistener] 准备重试挂市价止盈单: orderID=%s retryCount=%d/%d 将在5秒后重试",
				meta.OrderID, meta.RetryCount, maxRetries)

			// 异步重试，避免阻塞
			go func() {
				time.Sleep(5 * time.Second)
				// 检查订单是否仍然有效（未取消、未失败、仍有成交）
				s.trackedMu.RLock()
				currentMeta, exists := s.tracked[meta.OrderID]
				s.trackedMu.RUnlock()

				if !exists || currentMeta.ExitPlaced {
					log.Debugf("⏭️ [orderlistener] 订单已处理或不存在，跳过重试: orderID=%s", meta.OrderID)
					return
				}

				// 重新执行市价单止盈
				retryCtx, retryCancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer retryCancel()

				// 需要重新获取 market，这里从 TradingService 获取或使用 meta.MarketSlug
				// 简化处理：如果 market 为 nil，executeMarketOrderTakeProfit 会处理
				s.executeMarketOrderTakeProfit(retryCtx, currentMeta, market)
			}()
		} else {
			log.Errorf("❌ [orderlistener] 市价单止盈已达到最大重试次数，放弃: orderID=%s retryCount=%d",
				meta.OrderID, meta.RetryCount)
		}
		return
	}

	if len(created) > 0 && created[0] != nil && created[0].OrderID != "" {
		meta.ExitPlaced = true
		meta.ExitOrderID = created[0].OrderID
		meta.ExitOrderStatus = created[0].Status

		// 记录完整的订单信息
		if meta.RetryCount > 0 {
			log.Infof("🎯 [orderlistener] 市价单止盈成功（重试后）: entryOrderID=%s exitOrderID=%s token=%s entryPrice=%dc exitPrice=%.4f exitSize=%.4f shares exitOrderType=FAK market=%s retryCount=%d exitOrderStatus=%s bestBidPrice=%.4f estimatedAmount=%.2f USDC",
				meta.OrderID, meta.ExitOrderID, meta.TokenType, meta.EntryPriceCents, bestBidPrice.ToDecimal(), exitSize, meta.MarketSlug, meta.RetryCount, meta.ExitOrderStatus, bestBidPrice.ToDecimal(), estimatedAmount)
		} else {
			log.Infof("🎯 [orderlistener] 市价单止盈成功: entryOrderID=%s exitOrderID=%s token=%s entryPrice=%dc exitPrice=%.4f exitSize=%.4f shares exitOrderType=FAK market=%s exitOrderStatus=%s bestBidPrice=%.4f estimatedAmount=%.2f USDC",
				meta.OrderID, meta.ExitOrderID, meta.TokenType, meta.EntryPriceCents, bestBidPrice.ToDecimal(), exitSize, meta.MarketSlug, meta.ExitOrderStatus, bestBidPrice.ToDecimal(), estimatedAmount)
		}
	} else {
		log.Warnf("⚠️ [orderlistener] 市价单止盈返回空订单: orderID=%s", meta.OrderID)
	}
}
