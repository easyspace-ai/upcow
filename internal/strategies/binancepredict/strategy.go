package binancepredict

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/services"
	"github.com/betbot/gobet/internal/strategies/common"
	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/betbot/gobet/pkg/config"
	"github.com/sirupsen/logrus"
)

var log = logrus.WithField("strategy", ID)

func init() { bbgo.RegisterStrategy(ID, &Strategy{}) }

// Strategy：基于 Binance 秒级 K 线预测的镜像套利策略
type Strategy struct {
	TradingService       *services.TradingService
	BinanceFuturesKlines *services.BinanceFuturesKlines
	Config               `yaml:",inline" json:",inline"`

	autoMerge common.AutoMergeController

	mu sync.Mutex

	// 周期状态
	firstSeenAt          time.Time
	lastTriggerAt        time.Time
	tradesCountThisCycle int

	// 预测器和订单管理器
	predictor    *Predictor
	orderManager *OrderManager

	// 市场过滤
	marketSlugPrefix string

	// 全局约束
	minOrderSize float64 // USDC
	minShareSize float64 // GTC 最小 shares
}

func (s *Strategy) ID() string   { return ID }
func (s *Strategy) Name() string { return ID }

func (s *Strategy) Defaults() error { return nil }
func (s *Strategy) Validate() error { return s.Config.Validate() }

func (s *Strategy) Initialize() error {
	gc := config.Get()
	if gc == nil {
		return fmt.Errorf("[%s] 全局配置未加载：拒绝启动（避免误交易）", ID)
	}
	sp, err := gc.Market.Spec()
	if err != nil {
		return fmt.Errorf("[%s] 读取 market 配置失败：%w（拒绝启动，避免误交易）", ID, err)
	}

	prefix := strings.TrimSpace(gc.Market.SlugPrefix)
	if prefix == "" {
		prefix = sp.SlugPrefix()
	}
	s.marketSlugPrefix = strings.ToLower(strings.TrimSpace(prefix))
	if s.marketSlugPrefix == "" {
		return fmt.Errorf("[%s] marketSlugPrefix 为空：拒绝启动（避免误交易）", ID)
	}

	s.minOrderSize = gc.MinOrderSize
	s.minShareSize = gc.MinShareSize
	if s.minOrderSize <= 0 {
		s.minOrderSize = 1.1
	}
	if s.minShareSize <= 0 {
		s.minShareSize = 5.0
	}

	// 初始化预测器和订单管理器
	if s.BinanceFuturesKlines != nil {
		s.predictor = NewPredictor(s.BinanceFuturesKlines, s.Config)
		log.Infof("✅ [%s] Binance 预测器已初始化", ID)
	} else {
		log.Warnf("⚠️ [%s] BinanceFuturesKlines 未设置，预测功能将不可用", ID)
	}

	if s.TradingService != nil {
		s.orderManager = NewOrderManager(s.TradingService, s.Config)
		log.Infof("✅ [%s] 订单管理器已初始化", ID)
	}

	return nil
}

func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
	session.OnPriceChanged(s)
	session.OnOrderUpdate(s)
	log.Infof("✅ [%s] 策略已订阅价格变化和订单更新事件", ID)

	// 注册 TradingService 的订单更新回调（通过 Strategy 的 OnOrderUpdate 转发给 orderManager）
	if s.TradingService != nil {
		handler := services.OrderUpdateHandlerFunc(s.OnOrderUpdate)
		s.TradingService.OnOrderUpdate(handler)
		log.Infof("✅ [%s] 已注册 TradingService 订单更新回调", ID)
	}
}

func (s *Strategy) Run(ctx context.Context, _ bbgo.OrderExecutor, _ *bbgo.ExchangeSession) error {
	<-ctx.Done()
	return ctx.Err()
}

func (s *Strategy) OnCycle(ctx context.Context, _ *domain.Market, _ *domain.Market) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.firstSeenAt = time.Now()
	s.tradesCountThisCycle = 0
	log.Infof("🔄 [%s] 周期切换：交易计数器已重置 tradesCount=0", ID)
}

func (s *Strategy) shouldHandleMarketEvent(m *domain.Market) bool {
	if s == nil || m == nil || s.TradingService == nil {
		return false
	}
	if !strings.HasPrefix(strings.ToLower(m.Slug), s.marketSlugPrefix) {
		return false
	}
	currentMarketSlug := s.TradingService.GetCurrentMarket()
	if currentMarketSlug != "" && currentMarketSlug != m.Slug {
		return false
	}
	return true
}

func (s *Strategy) OnPriceChanged(ctx context.Context, e *events.PriceChangedEvent) error {
	if e == nil || e.Market == nil || s.TradingService == nil {
		return nil
	}

	s.autoMerge.MaybeAutoMerge(ctx, s.TradingService, e.Market, s.AutoMerge, log.Infof)

	if !s.shouldHandleMarketEvent(e.Market) {
		return nil
	}

	now := e.Timestamp
	if now.IsZero() {
		now = time.Now()
	}

	s.mu.Lock()
	if s.firstSeenAt.IsZero() {
		s.firstSeenAt = now
	}

	// 预热检查
	warmupMs := 1000 // 默认 1 秒预热
	if warmupMs > 0 && now.Sub(s.firstSeenAt) < time.Duration(warmupMs)*time.Millisecond {
		s.mu.Unlock()
		return nil
	}

	// 周期尾部保护
	if s.CycleEndProtectionMinutes > 0 && e.Market.Timestamp > 0 {
		cycleDuration := 15 * time.Minute
		if cfg := config.Get(); cfg != nil {
			if spec, err := cfg.Market.Spec(); err == nil {
				cycleDuration = spec.Duration()
			}
		}
		cycleStartTime := time.Unix(e.Market.Timestamp, 0)
		cycleEndTime := cycleStartTime.Add(cycleDuration)
		if now.After(cycleEndTime.Add(-time.Duration(s.CycleEndProtectionMinutes) * time.Minute)) {
			s.mu.Unlock()
			return nil
		}
	}

	// 冷却时间检查
	if !s.lastTriggerAt.IsZero() && now.Sub(s.lastTriggerAt) < time.Duration(s.PredictionCooldownMs)*time.Millisecond {
		s.mu.Unlock()
		return nil
	}

	// 检查总资金限制
	if s.MaxTotalCapitalUSDC > 0 {
		positions := s.TradingService.GetOpenPositionsForMarket(e.Market.Slug)
		totalCapital := 0.0
		for _, pos := range positions {
			if pos == nil || !pos.IsOpen() || pos.Size <= 0 {
				continue
			}
			price := 0.0
			if pos.AvgPrice > 0 {
				price = pos.AvgPrice
			} else if pos.EntryPrice.Pips > 0 {
				price = pos.EntryPrice.ToDecimal()
			}
			if price > 0 {
				totalCapital += pos.Size * price
			}
		}
		if totalCapital >= s.MaxTotalCapitalUSDC {
			log.Warnf("🚫 [%s] 总资金限制：当前总持仓价值 %.2f USDC >= 限制 %.2f USDC，禁止开新单",
				ID, totalCapital, s.MaxTotalCapitalUSDC)
			s.mu.Unlock()
			return nil
		}
	}

	// 检查是否要求完全对冲后才能开新单
	if s.RequireFullyHedgedBeforeNewEntry {
		orders := s.TradingService.GetActiveOrders()
		hasPendingHedgeOrder := false
		for _, o := range orders {
			if o == nil || o.OrderID == "" {
				continue
			}
			if o.MarketSlug != e.Market.Slug {
				continue
			}
			if o.OrderType != types.OrderTypeGTC {
				continue
			}
			if !o.IsFinalStatus() && o.Status != domain.OrderStatusCanceling {
				hasPendingHedgeOrder = true
				break
			}
		}
		if hasPendingHedgeOrder {
			log.Debugf("🚫 [%s] 有未成交的对冲订单且 RequireFullyHedgedBeforeNewEntry=true，禁止开新单", ID)
			s.mu.Unlock()
			return nil
		}
	}

	s.mu.Unlock()

	// 获取订单薄价格
	orderCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	yesBid, yesAsk, noBid, noAsk, source, err := s.TradingService.GetTopOfBook(orderCtx, e.Market)
	if err != nil {
		log.Debugf("⚠️ [%s] 获取订单薄失败: %v", ID, err)
		return nil
	}

	log.Debugf("📊 [%s] 订单薄价格 (source=%s): UP bid=%dc ask=%dc DOWN bid=%dc ask=%dc",
		ID, source, yesBid.ToCents(), yesAsk.ToCents(), noBid.ToCents(), noAsk.ToCents())

	// 市场质量检查
	if s.EnableMarketQualityGate != nil && *s.EnableMarketQualityGate {
		mq, mqErr := s.TradingService.GetMarketQuality(orderCtx, e.Market, &services.MarketQualityOptions{
			MaxBookAge:    time.Duration(s.MarketQualityMaxBookAgeMs) * time.Millisecond,
			MaxSpreadPips: s.MarketQualityMaxSpreadCents * 100,
			PreferWS:      true,
			FallbackToREST: true,
		})
		if mqErr != nil || mq.Score < s.MarketQualityMinScore {
			log.Debugf("⏸️ [%s] 市场质量检查未通过: score=%d (要求>=%d) err=%v",
				ID, mq.Score, s.MarketQualityMinScore, mqErr)
			return nil
		}
	}

	// 调用预测器获取方向
	if s.predictor == nil {
		log.Debugf("⏸️ [%s] 预测器未初始化，跳过", ID)
		return nil
	}

	direction, reason := s.predictor.Predict(now)
	if direction == DirectionNeutral {
		log.Debugf("⏸️ [%s] 预测结果为中性: reason=%s", ID, reason)
		return nil
	}

	// 记录预测结果
	priceChangeBps, hasBps := s.predictor.GetPriceChangeBps(now)
	if hasBps {
		log.Infof("🔮 [%s] Binance 预测: direction=%s reason=%s priceChange=%d bps window=%ds",
			ID, direction, reason, priceChangeBps, s.PredictionWindowSeconds)
	} else {
		log.Debugf("🔮 [%s] Binance 预测: direction=%s reason=%s (无法获取价格变化)",
			ID, direction, reason)
	}

	// 更新触发时间
	s.mu.Lock()
	s.lastTriggerAt = now
	s.tradesCountThisCycle++
	s.mu.Unlock()

	// 执行交易
	if s.orderManager == nil {
		log.Errorf("❌ [%s] 订单管理器未初始化", ID)
		return nil
	}

	err = s.orderManager.ExecuteTrade(orderCtx, e.Market, direction, yesBid, yesAsk, noBid, noAsk)
	if err != nil {
		log.Errorf("❌ [%s] 执行交易失败: %v", ID, err)
		return nil
	}

	log.Infof("✅ [%s] 交易已执行: direction=%s market=%s", ID, direction, e.Market.Slug)
	return nil
}

func (s *Strategy) OnOrderUpdate(ctx context.Context, order *domain.Order) error {
	if order == nil {
		return nil
	}
	log.Debugf("📝 [%s] 订单更新: orderID=%s status=%s filledSize=%.4f",
		ID, order.OrderID, order.Status, order.FilledSize)
	
	// 转发给 orderManager
	if s.orderManager != nil {
		s.orderManager.OnOrderUpdate(order)
	}
	return nil
}
