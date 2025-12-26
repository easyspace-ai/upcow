package services

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/pkg/marketmath"
)

// MarketQualityOptions 控制“盘口加工/质量评估”的策略。
//
// 设计目标：
// - 一处实现“盘口健康检查/有效价/可交易性判断”的基础逻辑
// - 策略侧只消费一个结构体，减少重复样板与口径漂移
type MarketQualityOptions struct {
	// MaxBookAge: 仅对 WS bestbook 生效；超过该年龄视为 stale。
	// 默认 3s（与 GetTopOfBook/GetBestPrice 的快路径一致）。
	MaxBookAge time.Duration

	// MaxSpreadPips: 一档价差上限（pips，1pip=0.0001）。
	// 默认 1000 pips (=10c)。
	MaxSpreadPips int

	// PreferWS: 优先使用 WS bestbook（低延迟）。
	PreferWS bool
	// FallbackToREST: 当 WS 不可用/不新鲜/不完整时，是否回退 REST orderbook。
	FallbackToREST bool
	// AllowPartialWS: WS bestbook 即使不完整也允许返回（用于观测/诊断）。
	// 注意：此时 mq.Complete=false，mq.Tradable() 会返回 false。
	AllowPartialWS bool
}

func (o MarketQualityOptions) normalized() MarketQualityOptions {
	if o.MaxBookAge <= 0 {
		o.MaxBookAge = 60 * time.Second  // 放宽默认值：从 3 秒增加到 60 秒，优先使用 WebSocket 数据
	}
	if o.MaxSpreadPips <= 0 {
		o.MaxSpreadPips = 1000 // 10c
	}
	// 默认：优先 WS，必要时回退 REST
	if !o.PreferWS && !o.FallbackToREST {
		o.PreferWS = true
		o.FallbackToREST = true
	}
	if o.PreferWS == false && o.FallbackToREST == false {
		o.PreferWS = true
		o.FallbackToREST = true
	}
	// 默认允许返回 partial（但策略使用 mq.Tradable() 自己做 gate）
	if !o.AllowPartialWS {
		// keep as-is; explicit false is allowed
	}
	return o
}

// MarketQuality 是“策略决策所需的最小盘口加工对象”。
// 目前以 top-of-book 为核心；后续可扩展到多档深度与滑点估计。
type MarketQuality struct {
	MarketSlug string
	ObservedAt time.Time

	// Source: "ws.bestbook" / "rest.orderbook" / "ws.bestbook(partial)" 等
	Source string

	// BookUpdatedAt: WS bestbook 的更新时间；REST 源时为零值（未知）。
	BookUpdatedAt time.Time
	Age          time.Duration

	Top       marketmath.TopOfBook
	Effective marketmath.EffectivePrices

	// 一档价差（pips）
	YesSpreadPips int
	NoSpreadPips  int

	// 镜像偏差（越大表示 YES/NO 两边越不一致，常见于脏快照/断档）
	MirrorGapBuyYesPips int // |YES.ask - (1 - NO.bid)|
	MirrorGapBuyNoPips  int // |NO.ask  - (1 - YES.bid)|

	Complete bool // YES/NO 双边 bid/ask 都存在
	Fresh    bool // 对 WS 源：Age<=MaxBookAge；对 REST 源：true（未知 age）

	Score    int      // 0..100 的粗略质量分
	Problems []string // 机器可读的原因码（用于观测/策略 gate）

	Arbitrage *marketmath.ArbitrageOpportunity // 若存在 complete-set 套利机会则给出
}

// Tradable 给策略一个“默认可交易 gate”（保守）。
func (mq *MarketQuality) Tradable() bool {
	if mq == nil {
		return false
	}
	if !mq.Complete {
		return false
	}
	if !mq.Fresh {
		return false
	}
	// 60 作为默认门槛：允许轻微异常但不允许严重脏数据
	return mq.Score >= 60
}

func (o *OrdersService) GetMarketQuality(ctx context.Context, market *domain.Market, opt *MarketQualityOptions) (*MarketQuality, error) {
	s := o.s
	if market == nil || market.Slug == "" {
		return nil, fmt.Errorf("market invalid")
	}
	if market.YesAssetID == "" || market.NoAssetID == "" {
		return nil, fmt.Errorf("market assetIDs missing")
	}

	opts := MarketQualityOptions{
		PreferWS:       true,
		FallbackToREST: true,
		AllowPartialWS: true,
	}
	if opt != nil {
		opts = (*opt)
	}
	opts = opts.normalized()

	now := time.Now()
	mq := &MarketQuality{
		MarketSlug: market.Slug,
		ObservedAt: now,
		Problems:   make([]string, 0, 8),
		Score:      100,
	}

	// 1) 读取 WS bestbook（允许 partial）
	var wsTop marketmath.TopOfBook
	var wsUpdatedAt time.Time
	wsHasAny := false
	wsComplete := false
	wsFresh := false
	if s != nil && opts.PreferWS {
		book := s.getBestBook()
		cur := s.getCurrentMarketInfo()
		if book != nil && cur != nil && cur.Slug == market.Slug {
			snap := book.Load()
			wsTop = marketmath.TopOfBook{
				YesBidPips: int(snap.YesBidPips),
				YesAskPips: int(snap.YesAskPips),
				NoBidPips:  int(snap.NoBidPips),
				NoAskPips:  int(snap.NoAskPips),
			}
			wsUpdatedAt = snap.UpdatedAt
			wsHasAny = wsTop.YesBidPips > 0 || wsTop.YesAskPips > 0 || wsTop.NoBidPips > 0 || wsTop.NoAskPips > 0
			wsComplete = wsTop.YesBidPips > 0 && wsTop.YesAskPips > 0 && wsTop.NoBidPips > 0 && wsTop.NoAskPips > 0
			if !wsUpdatedAt.IsZero() {
				wsFresh = time.Since(wsUpdatedAt) <= opts.MaxBookAge
			}
		}
	}

	// 2) 决策：选择 WS 或 REST
	chosenTop := marketmath.TopOfBook{}
	chosenSource := ""
	chosenUpdatedAt := time.Time{}

	if opts.PreferWS && wsHasAny && (wsComplete || opts.AllowPartialWS) {
		chosenTop = wsTop
		chosenUpdatedAt = wsUpdatedAt
		if wsComplete {
			chosenSource = "ws.bestbook"
		} else {
			chosenSource = "ws.bestbook(partial)"
			mq.Problems = append(mq.Problems, "ws_partial")
			mq.Score -= 35
		}
		if !wsFresh {
			mq.Problems = append(mq.Problems, "ws_stale")
			mq.Score -= 25
		}
	}

	needRest := opts.FallbackToREST && (chosenSource == "" || !wsFresh || !wsComplete)
	if needRest {
		restTop, err := o.fetchTopFromREST(ctx, market)
		if err == nil {
			chosenTop = restTop
			chosenSource = "rest.orderbook"
			// REST 无法给出“真实更新时间”，这里只能记为 observedAt
			chosenUpdatedAt = time.Time{}
		} else {
			// REST 失败：如果 WS 有任何数据则继续用 WS；否则返回错误
			if chosenSource == "" {
				return nil, err
			}
			mq.Problems = append(mq.Problems, "rest_failed")
			mq.Score -= 15
		}
	}

	mq.Source = chosenSource
	mq.Top = chosenTop
	mq.BookUpdatedAt = chosenUpdatedAt
	if !mq.BookUpdatedAt.IsZero() {
		mq.Age = now.Sub(mq.BookUpdatedAt)
	}

	// 3) 基础完整性 + spread
	mq.Complete = mq.Top.YesBidPips > 0 && mq.Top.YesAskPips > 0 && mq.Top.NoBidPips > 0 && mq.Top.NoAskPips > 0
	if mq.Source == "rest.orderbook" {
		// REST: freshness 未知，保守设 true（由策略的 cooldown/滑点等兜底）
		mq.Fresh = true
	} else {
		mq.Fresh = !mq.BookUpdatedAt.IsZero() && mq.Age <= opts.MaxBookAge
	}
	if !mq.Complete {
		mq.Problems = append(mq.Problems, "incomplete_top")
		mq.Score -= 50
	}

	// yes/no spread
	if mq.Top.YesBidPips > 0 && mq.Top.YesAskPips > 0 {
		mq.YesSpreadPips = mq.Top.YesAskPips - mq.Top.YesBidPips
		if mq.YesSpreadPips < 0 {
			mq.Problems = append(mq.Problems, "crossed_yes")
			mq.Score -= 40
			mq.YesSpreadPips = int(math.Abs(float64(mq.YesSpreadPips)))
		}
		if mq.YesSpreadPips > opts.MaxSpreadPips {
			mq.Problems = append(mq.Problems, "wide_spread_yes")
			mq.Score -= 20
		}
	}
	if mq.Top.NoBidPips > 0 && mq.Top.NoAskPips > 0 {
		mq.NoSpreadPips = mq.Top.NoAskPips - mq.Top.NoBidPips
		if mq.NoSpreadPips < 0 {
			mq.Problems = append(mq.Problems, "crossed_no")
			mq.Score -= 40
			mq.NoSpreadPips = int(math.Abs(float64(mq.NoSpreadPips)))
		}
		if mq.NoSpreadPips > opts.MaxSpreadPips {
			mq.Problems = append(mq.Problems, "wide_spread_no")
			mq.Score -= 20
		}
	}

	// 4) 镜像偏差（用于观测：越大越可能脏）
	mirror := func(pips int) int {
		if pips <= 0 {
			return 0
		}
		return 10000 - pips
	}
	if mq.Top.YesAskPips > 0 && mq.Top.NoBidPips > 0 {
		mq.MirrorGapBuyYesPips = int(math.Abs(float64(mq.Top.YesAskPips - mirror(mq.Top.NoBidPips))))
		if mq.MirrorGapBuyYesPips > 2000 { // 0.20 以上通常很异常
			mq.Problems = append(mq.Problems, "mirror_gap_buy_yes")
			mq.Score -= 10
		}
	}
	if mq.Top.NoAskPips > 0 && mq.Top.YesBidPips > 0 {
		mq.MirrorGapBuyNoPips = int(math.Abs(float64(mq.Top.NoAskPips - mirror(mq.Top.YesBidPips))))
		if mq.MirrorGapBuyNoPips > 2000 {
			mq.Problems = append(mq.Problems, "mirror_gap_buy_no")
			mq.Score -= 10
		}
	}

	// 5) 有效价 + 套利判断（即使 partial 也可计算；内部会做 validate）
	if eff, err := marketmath.GetEffectivePrices(mq.Top); err == nil {
		mq.Effective = eff
		if arb, err := marketmath.CheckArbitrage(mq.Top); err == nil {
			mq.Arbitrage = arb
		}
	} else {
		mq.Problems = append(mq.Problems, "effective_price_failed")
		mq.Score -= 20
	}

	// clamp
	if mq.Score < 0 {
		mq.Score = 0
	}
	if mq.Score > 100 {
		mq.Score = 100
	}
	return mq, nil
}

func (o *OrdersService) fetchTopFromREST(ctx context.Context, market *domain.Market) (marketmath.TopOfBook, error) {
	s := o.s
	if s == nil || s.clobClient == nil {
		return marketmath.TopOfBook{}, fmt.Errorf("clob client not initialized")
	}
	yesBook, err := s.clobClient.GetOrderBook(ctx, market.YesAssetID, nil)
	if err != nil {
		return marketmath.TopOfBook{}, fmt.Errorf("get yes orderbook: %w", err)
	}
	noBook, err := s.clobClient.GetOrderBook(ctx, market.NoAssetID, nil)
	if err != nil {
		return marketmath.TopOfBook{}, fmt.Errorf("get no orderbook: %w", err)
	}

	parse := func(book *types.OrderBookSummary) (bidPips, askPips int, e error) {
		var bestBid, bestAsk float64
		if book != nil && len(book.Bids) > 0 {
			bestBid, _ = strconv.ParseFloat(book.Bids[0].Price, 64)
		}
		if book != nil && len(book.Asks) > 0 {
			bestAsk, _ = strconv.ParseFloat(book.Asks[0].Price, 64)
		}
		if bestBid <= 0 || bestAsk <= 0 {
			return 0, 0, fmt.Errorf("incomplete book: bid=%.6f ask=%.6f", bestBid, bestAsk)
		}
		// 统一到 pips（四舍五入）
		b := domain.PriceFromDecimal(bestBid)
		a := domain.PriceFromDecimal(bestAsk)
		return b.Pips, a.Pips, nil
	}

	yb, ya, err := parse(yesBook)
	if err != nil {
		return marketmath.TopOfBook{}, fmt.Errorf("parse yes book: %w", err)
	}
	nb, na, err := parse(noBook)
	if err != nil {
		return marketmath.TopOfBook{}, fmt.Errorf("parse no book: %w", err)
	}

	return marketmath.TopOfBook{
		YesBidPips: yb,
		YesAskPips: ya,
		NoBidPips:  nb,
		NoAskPips:  na,
	}, nil
}

