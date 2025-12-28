package ctfendgame

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/execution"
	"github.com/betbot/gobet/internal/services"
	"github.com/betbot/gobet/internal/strategies/orderutil"
	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/sirupsen/logrus"
)

var log = logrus.WithField("strategy", ID)

func init() { bbgo.RegisterStrategy(ID, &Strategy{}) }

// Strategy: 尾盘卖弱（V0）
//
// 特点：
// - 默认不动（50 附近摇摆不卖）
// - 仅在尾盘窗口内且强弱明确时，弱方 bestBid 落在 5–15 才卖
// - 分批卖出（sellSplits），每周期最多执行一次卖弱序列
type Strategy struct {
	TradingService *services.TradingService
	Config         `yaml:",inline" json:",inline"`

	mu sync.Mutex

	firstSeenAt time.Time
	cycleStart  time.Time

	sellSequencesDone int
	attemptsThisCycle int
	lastAttemptAt     time.Time
}

func (s *Strategy) ID() string   { return ID }
func (s *Strategy) Name() string { return ID }

func (s *Strategy) Defaults() error   { return nil }
func (s *Strategy) Validate() error   { return s.Config.Validate() }
func (s *Strategy) Initialize() error { return nil }

func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
	session.OnPriceChanged(s)
	log.Infof("✅ [%s] 策略已订阅价格变化事件 (session=%s)", ID, session.Name)
}

func (s *Strategy) Run(ctx context.Context, _ bbgo.OrderExecutor, _ *bbgo.ExchangeSession) error {
	<-ctx.Done()
	return ctx.Err()
}

func (s *Strategy) OnCycle(_ context.Context, _ *domain.Market, newMarket *domain.Market) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.firstSeenAt = time.Now()
	s.sellSequencesDone = 0
	s.attemptsThisCycle = 0
	s.lastAttemptAt = time.Time{}

	if newMarket != nil && newMarket.Timestamp > 0 {
		s.cycleStart = time.Unix(newMarket.Timestamp, 0)
	} else {
		s.cycleStart = time.Time{}
	}
}

func (s *Strategy) OnPriceChanged(ctx context.Context, e *events.PriceChangedEvent) error {
	if e == nil || e.Market == nil || s.TradingService == nil {
		return nil
	}

	// 防御：只处理当前周期的 market（避免跨周期污染）
	cur := s.TradingService.GetCurrentMarket()
	if cur != "" && cur != e.Market.Slug {
		return nil
	}

	s.mu.Lock()
	if s.firstSeenAt.IsZero() {
		s.firstSeenAt = time.Now()
	}
	// 预热
	if s.WarmupMs > 0 && time.Since(s.firstSeenAt) < time.Duration(s.WarmupMs)*time.Millisecond {
		s.mu.Unlock()
		return nil
	}
	// 每周期最多执行一次卖弱序列
	if s.sellSequencesDone >= s.MaxSellSequencesPerCycle {
		s.mu.Unlock()
		return nil
	}
	// 尝试次数上限（包含失败）
	if s.attemptsThisCycle >= s.MaxAttemptsPerCycle {
		s.mu.Unlock()
		return nil
	}
	// 冷却
	if !s.lastAttemptAt.IsZero() && time.Since(s.lastAttemptAt) < time.Duration(s.CooldownMs)*time.Millisecond {
		s.mu.Unlock()
		return nil
	}

	// 计算尾盘窗口（cycleEnd - now <= endgameWindow）
	cycleStart := s.cycleStart
	s.mu.Unlock()

	if cycleStart.IsZero() && e.Market.Timestamp > 0 {
		cycleStart = time.Unix(e.Market.Timestamp, 0)
	}
	if cycleStart.IsZero() {
		// 兜底：拿不到周期起点就不交易
		return nil
	}

	dur, _ := time.ParseDuration(s.Timeframe) // Validate 已保证可解析
	cycleEnd := cycleStart.Add(dur)
	now := time.Now()
	timeToEnd := cycleEnd.Sub(now)
	if timeToEnd > time.Duration(s.EndgameWindowSecs)*time.Second {
		return nil
	}
	if timeToEnd < -30*time.Second {
		// 已明显过期的 market（避免历史回放/时钟漂移误触发）
		return nil
	}

	orderCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	// 同时读取 YES/NO 盘口
	yesBid, yesAsk, err := s.TradingService.GetBestPrice(orderCtx, e.Market.YesAssetID)
	if err != nil {
		return nil
	}
	noBid, noAsk, err := s.TradingService.GetBestPrice(orderCtx, e.Market.NoAssetID)
	if err != nil {
		return nil
	}
	if yesBid <= 0 || yesAsk <= 0 || noBid <= 0 || noAsk <= 0 {
		return nil
	}

	yesBidCents := int(yesBid*100 + 0.5)
	yesAskCents := int(yesAsk*100 + 0.5)
	noBidCents := int(noBid*100 + 0.5)
	noAskCents := int(noAsk*100 + 0.5)
	if yesBidCents <= 0 || yesAskCents <= 0 || noBidCents <= 0 || noAskCents <= 0 {
		return nil
	}

	yesSpread := yesAskCents - yesBidCents
	if yesSpread < 0 {
		yesSpread = -yesSpread
	}
	noSpread := noAskCents - noBidCents
	if noSpread < 0 {
		noSpread = -noSpread
	}
	if s.MaxSpreadCents > 0 && (yesSpread > s.MaxSpreadCents || noSpread > s.MaxSpreadCents) {
		return nil
	}

	yesMid := (yesBidCents + yesAskCents) / 2
	noMid := (noBidCents + noAskCents) / 2

	// 不确定不动：两边都在 50 附近摇摆
	if yesMid >= s.UncertainMinCents && yesMid <= s.UncertainMaxCents &&
		noMid >= s.UncertainMinCents && noMid <= s.UncertainMaxCents {
		return nil
	}

	// 强弱明确判定（V0：用价格差/强方高度近似）
	diff := int(math.Abs(float64(yesMid - noMid)))
	strongEnough := diff >= s.MinStrongWeakDiffCents || maxInt(yesMid, noMid) >= s.MinStrongSideCents
	if !strongEnough {
		return nil
	}

	// 确定强/弱侧
	weakAssetID := e.Market.YesAssetID
	weakToken := domain.TokenTypeUp
	weakName := "YES"
	weakBidCents := yesBidCents

	strongMid := yesMid
	weakMid := noMid
	if noMid < yesMid {
		weakAssetID = e.Market.NoAssetID
		weakToken = domain.TokenTypeDown
		weakName = "NO"
		weakBidCents = noBidCents

		strongMid = yesMid
		weakMid = noMid
	} else if yesMid < noMid {
		weakAssetID = e.Market.YesAssetID
		weakToken = domain.TokenTypeUp
		weakName = "YES"
		weakBidCents = yesBidCents

		strongMid = noMid
		weakMid = yesMid
	} else {
		// mid 相等：视为不明确
		return nil
	}

	// 弱方价格必须在 5–15（以 bestBid 可成交卖价为准）
	if weakBidCents < s.WeakSellMinCents || weakBidCents > s.WeakSellMaxCents {
		return nil
	}

	// 记录一次尝试（无论后续成功/失败，都计入 attempts）
	s.mu.Lock()
	s.attemptsThisCycle++
	s.lastAttemptAt = time.Now()
	attemptN := s.attemptsThisCycle
	s.mu.Unlock()

	log.Infof("🎯 [%s] 尾盘卖弱候选: market=%s tte=%ds strongMid=%dc weakMid=%dc weak=%s bid=%dc attempt=%d/%d",
		ID, e.Market.Slug, int(timeToEnd.Seconds()), strongMid, weakMid, weakName, weakBidCents, attemptN, s.MaxAttemptsPerCycle)

	// 执行分批卖弱：每批次重新报价，若离开 5–15 则停止
	for i, frac := range s.SellSplits {
		batchSize := s.OrderSize * frac
		if batchSize <= 0 {
			continue
		}

		// 每批次之前做冷却（避免 WS 高频触发/也给盘口更新一点时间）
		if i > 0 && s.CooldownMs > 0 {
			time.Sleep(time.Duration(s.CooldownMs) * time.Millisecond)
		}

		batchCtx, cancelBatch := context.WithTimeout(ctx, 5*time.Second)
		// 重新报价（卖出用 bestBid）
		price, err := orderutil.QuoteSellPrice(batchCtx, s.TradingService, weakAssetID, s.WeakSellMinCents)
		if err != nil {
			cancelBatch()
			return nil
		}
		curBidCents := price.ToCents()
		if curBidCents > s.WeakSellMaxCents {
			cancelBatch()
			return nil
		}

		req := execution.MultiLegRequest{
			Name:       fmt.Sprintf("ctfendgame_sellweak_%d", i+1),
			MarketSlug: e.Market.Slug,
			Legs: []execution.LegIntent{{
				Name:      fmt.Sprintf("sell_weak_%d", i+1),
				AssetID:   weakAssetID,
				TokenType: weakToken,
				Side:      types.SideSell,
				Price:     price,
				Size:      batchSize,
				OrderType: types.OrderTypeFAK,
			}},
			Hedge: execution.AutoHedgeConfig{Enabled: false},
		}

		_, err = s.TradingService.ExecuteMultiLeg(batchCtx, req)
		cancelBatch()
		if err != nil {
			// fail-safe：系统暂停/市场不一致时属于“预期拒绝”，不应把本周期标记为完成
			estr := strings.ToLower(err.Error())
			if strings.Contains(estr, "trading paused") || strings.Contains(estr, "market mismatch") {
				log.Warnf("⏸️ [%s] 系统拒绝下单（fail-safe，预期行为）: %v", ID, err)
				return nil
			}
			log.Warnf("⚠️ [%s] 卖弱下单失败: weak=%s price=%dc size=%.4f err=%v", ID, weakName, curBidCents, batchSize, err)
			return nil
		}

		log.Infof("✅ [%s] 已卖出弱方批次: weak=%s price=%dc size=%.4f market=%s",
			ID, weakName, curBidCents, batchSize, e.Market.Slug)
	}

	// 全部批次完成：标记本周期已执行
	s.mu.Lock()
	s.sellSequencesDone++
	s.mu.Unlock()

	log.Infof("🏁 [%s] 本周期卖弱完成: market=%s sequencesDone=%d/%d",
		ID, e.Market.Slug, s.sellSequencesDone, s.MaxSellSequencesPerCycle)
	return nil
}

func maxInt(a, b int) int {
	if a >= b {
		return a
	}
	return b
}
