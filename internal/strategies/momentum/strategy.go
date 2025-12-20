package momentum

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/strategies/common"
	"github.com/betbot/gobet/internal/strategies/orderutil"
	strategyports "github.com/betbot/gobet/internal/strategies/ports"
	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/sirupsen/logrus"
)

const ID = "momentum"

var log = logrus.WithField("strategy", ID)

func init() {
	// bbgo main 风格：注册策略 struct，用于直接从 YAML/JSON 反序列化配置
	bbgo.RegisterStrategy(ID, &MomentumStrategy{})
}

type Direction int

const (
	DirectionUp Direction = iota + 1
	DirectionDown
)

type MomentumSignal struct {
	Asset     string
	MoveBps   int
	Dir       Direction
	FiredAt   time.Time
	Source    string
	WindowS   int
	Threshold int
}

// MomentumStrategy 动量策略（基于外部快行情信号触发快速下单）。
//
// 说明：
// - 当前项目是“单市场会话”模型：策略以 Session 当前 market 作为交易标的；
// - 外部信号只用于决定买 UP 还是买 DOWN，以及是否满足“边际优势”；
// - 执行使用 FAK，尽量减少挂单风险。
type MomentumStrategy struct {
	Executor       bbgo.CommandExecutor
	tradingService strategyports.MomentumTradingService

	// 配置字段（bbgo main 风格：直接反序列化到策略 struct）
	MomentumStrategyConfig `yaml:",inline" json:",inline"`
	config                *MomentumStrategyConfig `json:"-" yaml:"-"`

	loopOnce   sync.Once
	loopCancel context.CancelFunc
	signalC    chan MomentumSignal

	// 当前周期 market（用于 MarketSlug/assetID）
	mu            sync.RWMutex
	currentMarket *domain.Market
	marketGuard   common.MarketSlugGuard
	tradeCooldown *common.Debouncer
}

func (s *MomentumStrategy) ID() string   { return ID }
func (s *MomentumStrategy) Name() string { return ID }

func (s *MomentumStrategy) SetTradingService(ts strategyports.MomentumTradingService) {
	s.mu.Lock()
	s.tradingService = ts
	s.mu.Unlock()
}

func (s *MomentumStrategy) Defaults() error { return nil }

func (s *MomentumStrategy) Validate() error {
	s.config = &s.MomentumStrategyConfig
	return s.MomentumStrategyConfig.Validate()
}

func (s *MomentumStrategy) Initialize() error {
	s.config = &s.MomentumStrategyConfig
	if err := s.MomentumStrategyConfig.Validate(); err != nil {
		return err
	}
	if s.tradeCooldown == nil {
		s.tradeCooldown = common.NewDebouncer(time.Duration(s.CooldownSecs) * time.Second)
	} else {
		s.tradeCooldown.SetInterval(time.Duration(s.CooldownSecs) * time.Second)
		s.tradeCooldown.Reset()
	}
	log.Infof("动量策略初始化: asset=%s size=$%.2f threshold=%dbps window=%ds edge=%dc cooldown=%ds polygon=%v",
		s.Asset, s.SizeUSDC, s.ThresholdBps, s.WindowSecs, s.MinEdgeCents, s.CooldownSecs, s.UsePolygonFeed)
	return nil
}

// Subscribe 订阅会话事件：这里只用于更新当前 market（周期切换时自动跟随）。
func (s *MomentumStrategy) Subscribe(session *bbgo.ExchangeSession) {
	session.OnPriceChanged(s)
	log.Infof("动量策略已订阅价格变化事件（用于同步当前 market/cycle）")
}

// OnPriceChanged 快路径：只保存当前 market 引用。
func (s *MomentumStrategy) OnPriceChanged(ctx context.Context, event *events.PriceChangedEvent) error {
	if event == nil || event.Market == nil {
		return nil
	}
	s.mu.Lock()
	if s.marketGuard.Update(event.Market.Slug) {
		// 周期切换：重置冷却，避免新周期被旧周期的 cooldown 误伤
		if s.tradeCooldown != nil {
			s.tradeCooldown.Reset()
		}
	}
	s.currentMarket = event.Market
	s.mu.Unlock()
	return nil
}

func (s *MomentumStrategy) Run(ctx context.Context, orderExecutor bbgo.OrderExecutor, session *bbgo.ExchangeSession) error {
	s.startLoop(ctx)

	// 尽量在启动时就拿到 market（不依赖第一条 price_change）
	if session != nil {
		if m := session.Market(); m != nil {
			s.mu.Lock()
			if s.marketGuard.Update(m.Slug) {
				if s.tradeCooldown != nil {
					s.tradeCooldown.Reset()
				}
			}
			s.currentMarket = m
			s.mu.Unlock()
		}
	}

	log.Infof("动量策略已启动")
	return nil
}

func (s *MomentumStrategy) Shutdown(ctx context.Context, wg *sync.WaitGroup) {
	if s.loopCancel != nil {
		s.loopCancel()
	}
}

func (s *MomentumStrategy) startLoop(parent context.Context) {
	common.StartLoopOnce(
		parent,
		&s.loopOnce,
		func(cancel context.CancelFunc) { s.loopCancel = cancel },
		0,
		func(loopCtx context.Context, _ <-chan time.Time) {
			s.signalC = make(chan MomentumSignal, 1024)

			// 外部行情源：Polygon
			if s.config != nil && s.config.UsePolygonFeed {
				go runPolygonFeed(loopCtx, s.config.Asset, s.config.ThresholdBps, s.config.WindowSecs, s.signalC, log)
			}

			s.loop(loopCtx)
		},
	)
}

func (s *MomentumStrategy) loop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case sig := <-s.signalC:
			_ = s.handleSignal(ctx, sig)
		}
	}
}

func (s *MomentumStrategy) handleSignal(ctx context.Context, sig MomentumSignal) error {
	s.mu.RLock()
	ts := s.tradingService
	cfg := s.config
	market := s.currentMarket
	cooldown := s.tradeCooldown
	s.mu.RUnlock()

	if ts == nil || cfg == nil || market == nil {
		return nil
	}

	// 冷却：通过 Debouncer 统一实现；interval=0 等价于不冷却
	if cooldown != nil {
		ready, _ := cooldown.ReadyNow()
		if !ready {
			return nil
		}
	}

	// 决策：Up -> 买 UP（YES），Down -> 买 DOWN（NO）
	tokenType := domain.TokenTypeUp
	if sig.Dir == DirectionDown {
		tokenType = domain.TokenTypeDown
	}
	assetID := market.GetAssetID(tokenType)

	// “估计公平价”：50¢ ± f(move)（与外部示例一致，属于启发式）
	absMove := int(math.Abs(float64(sig.MoveBps)))
	estimatedFair := 50 + absMove/10 // 每 10bps ≈ 1¢
	maxPay := estimatedFair - cfg.MinEdgeCents
	if maxPay < 1 {
		maxPay = 1
	}

	// 将网络 IO 投递到全局串行执行器，避免阻塞策略 loop
	if s.Executor == nil {
		if err := s.placeFAK(ctx, ts, market, tokenType, assetID, cfg.SizeUSDC, maxPay, sig); err != nil {
			return err
		}
		s.mu.Lock()
		if s.tradeCooldown != nil {
			s.tradeCooldown.MarkNow()
		}
		s.mu.Unlock()
		return nil
	}

	ok := s.Executor.Submit(bbgo.Command{
		Name:    fmt.Sprintf("momentum_%s_%s_%dbps", sig.Asset, map[Direction]string{DirectionUp: "up", DirectionDown: "down"}[sig.Dir], absMove),
		Timeout: 25 * time.Second,
		Do: func(runCtx context.Context) {
			_ = s.placeFAK(runCtx, ts, market, tokenType, assetID, cfg.SizeUSDC, maxPay, sig)
		},
	})
	if !ok {
		return fmt.Errorf("执行器队列已满，无法提交动量订单")
	}

	// 成功投递后记录 cooldown（避免同一信号风暴提交大量 command）
	s.mu.Lock()
	if s.tradeCooldown != nil {
		s.tradeCooldown.MarkNow()
	}
	s.mu.Unlock()
	return nil
}

func (s *MomentumStrategy) placeFAK(ctx context.Context, ts strategyports.MomentumTradingService, market *domain.Market, tokenType domain.TokenType, assetID string, sizeUSDC float64, maxPayCents int, sig MomentumSignal) error {
	// 价格取 bestAsk，并用 maxPayCents 做上限保护
	price, err := orderutil.QuoteBuyPrice(ctx, ts, assetID, maxPayCents)
	if err != nil {
		log.Debugf("动量下单跳过：QuoteBuyPrice 失败: %v (assetID=%s max=%dc)", err, assetID, maxPayCents)
		return err
	}

	// 计算 shares 数量
	if price.ToDecimal() <= 0 {
		return fmt.Errorf("无效价格: %v", price)
	}
	shares := sizeUSDC / price.ToDecimal()

	order := orderutil.NewOrder(market.Slug, assetID, types.SideBuy, price, shares, tokenType, true, types.OrderTypeFAK)
	created, err := ts.PlaceOrder(ctx, order)
	if err != nil {
		log.Warnf("动量下单失败: %v (sig=%s %dbps price=%dc shares=%.4f)", err, sig.Asset, sig.MoveBps, price.Cents, shares)
		return err
	}

	if created != nil {
		log.Infof("动量下单成功: orderID=%s market=%s token=%s price=%dc shares=%.4f (sig=%s %dbps source=%s)",
			created.OrderID, market.Slug, tokenType, price.Cents, shares, sig.Asset, sig.MoveBps, sig.Source)
	}
	return nil
}
