package ctfendgame

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"os"
	"strings"
	"sync"
	"time"

	clobclient "github.com/betbot/gobet/clob/client"
	"github.com/betbot/gobet/clob/signing"
	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/execution"
	"github.com/betbot/gobet/internal/services"
	strategycommon "github.com/betbot/gobet/internal/strategies/common"
	"github.com/betbot/gobet/internal/strategies/orderutil"
	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/betbot/gobet/pkg/config"
	sdkapi "github.com/betbot/gobet/pkg/sdk/api"
	sdkrelayer "github.com/betbot/gobet/pkg/sdk/relayer"
	relayertypes "github.com/betbot/gobet/pkg/sdk/relayer/types"
	sdktypes "github.com/betbot/gobet/pkg/sdk/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
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

	autoMerge strategycommon.AutoMergeController

	firstSeenAt time.Time
	cycleStart  time.Time

	sellSequencesDone int
	attemptsThisCycle int
	lastAttemptAt     time.Time

	// ===== 自动编排（新周期开始立刻 split + 持仓校验）=====
	holdingsOK bool
	splitDone  bool

	// ===== 强方卖出跟踪（卖出弱方后立即挂强方卖单）=====
	weakSellOrders map[string]*weakSellOrderMeta // 弱方卖出订单跟踪
}

// weakSellOrderMeta 弱方卖出订单元数据
type weakSellOrderMeta struct {
	OrderID        string
	MarketSlug     string
	StrongAssetID  string
	StrongToken    domain.TokenType
	StrongName     string
	BatchIndex     int     // 批次索引（0-based）
	BatchSize      float64 // 弱方卖出批次大小
	FilledSize     float64
	StrongSellDone bool // 是否已挂强方卖单
}

func (s *Strategy) ID() string   { return ID }
func (s *Strategy) Name() string { return ID }

func (s *Strategy) Defaults() error   { return nil }
func (s *Strategy) Validate() error   { return s.Config.Validate() }
func (s *Strategy) Initialize() error { return nil }

func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
	session.OnPriceChanged(s)
	session.OnOrderUpdate(s)
	log.Infof("✅ [%s] 策略已订阅价格变化和订单更新事件 (session=%s)", ID, session.Name)

	// 注册 TradingService 订单更新回调（兜底方案）
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

func (s *Strategy) OnCycle(_ context.Context, oldMarket *domain.Market, newMarket *domain.Market) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.firstSeenAt = time.Now()
	s.sellSequencesDone = 0
	s.attemptsThisCycle = 0
	s.lastAttemptAt = time.Time{}
	s.holdingsOK = false
	s.splitDone = false
	s.weakSellOrders = make(map[string]*weakSellOrderMeta)

	if newMarket != nil && newMarket.Timestamp > 0 {
		s.cycleStart = time.Unix(newMarket.Timestamp, 0)
	} else {
		s.cycleStart = time.Time{}
	}

	// 新周期开始：先检查上一个周期是否需要 merge，然后再 split 本周期
	if s.EnableAutoSplitOnCycleStart && newMarket != nil && newMarket.IsValid() {
		go s.mergePreviousCycleIfNeeded(oldMarket, newMarket)
		return
	}

	// 若不自动 split，则做一次持仓校验（持仓由外部 split/手工保证）
	if newMarket != nil && newMarket.IsValid() && s.HoldingsCheckOnCycleStart != nil && *s.HoldingsCheckOnCycleStart {
		go s.checkHoldingsAtCycleStart(newMarket)
	} else {
		s.holdingsOK = true
	}
}

func (s *Strategy) OnPriceChanged(ctx context.Context, e *events.PriceChangedEvent) error {
	if e == nil || e.Market == nil || s.TradingService == nil {
		return nil
	}
	s.autoMerge.MaybeAutoMerge(ctx, s.TradingService, e.Market, s.AutoMerge, log.Infof)

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
	holdingsOK := s.holdingsOK
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

	// 核心原则：本周期若未确认持仓正常，则不执行尾盘卖弱（避免“没币还卖”）
	if !holdingsOK {
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

	strongAssetID := e.Market.NoAssetID
	strongToken := domain.TokenTypeDown
	strongName := "NO"

	strongMid := yesMid
	weakMid := noMid
	if noMid < yesMid {
		weakAssetID = e.Market.NoAssetID
		weakToken = domain.TokenTypeDown
		weakName = "NO"
		weakBidCents = noBidCents

		strongAssetID = e.Market.YesAssetID
		strongToken = domain.TokenTypeUp
		strongName = "YES"

		strongMid = yesMid
		weakMid = noMid
	} else if yesMid < noMid {
		weakAssetID = e.Market.YesAssetID
		weakToken = domain.TokenTypeUp
		weakName = "YES"
		weakBidCents = yesBidCents

		strongAssetID = e.Market.NoAssetID
		strongToken = domain.TokenTypeDown
		strongName = "NO"

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

	log.Infof("🎯 [%s] 尾盘卖弱候选: market=%s tte=%ds strongMid=%dc weakMid=%dc weak=%s bid=%dc",
		ID, e.Market.Slug, int(timeToEnd.Seconds()), strongMid, weakMid, weakName, weakBidCents)

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

		result, err := s.TradingService.ExecuteMultiLeg(batchCtx, req)
		cancelBatch()

		// 记录尝试（在知道结果后）
		s.mu.Lock()
		shouldCountAttempt := true
		if err != nil {
			estr := strings.ToLower(err.Error())
			// duplicate in-flight 不算真正的尝试（订单已在处理中）
			if strings.Contains(estr, "duplicate in-flight") {
				shouldCountAttempt = false
				// 延长冷却时间，避免频繁触发
				s.lastAttemptAt = time.Now()
			}
		}
		if shouldCountAttempt {
			s.attemptsThisCycle++
			s.lastAttemptAt = time.Now()
		}
		attemptN := s.attemptsThisCycle
		s.mu.Unlock()

		if err != nil {
			// fail-safe：系统暂停/市场不一致时属于“预期拒绝”，不应把本周期标记为完成
			estr := strings.ToLower(err.Error())
			if strings.Contains(estr, "trading paused") || strings.Contains(estr, "market mismatch") {
				log.Warnf("⏸️ [%s] 系统拒绝下单（fail-safe，预期行为）: %v", ID, err)
				return nil
			}
			if strings.Contains(estr, "duplicate in-flight") {
				log.Debugf("🔍 [%s] 订单已在处理中，跳过: weak=%s price=%dc size=%.4f attempt=%d/%d",
					ID, weakName, curBidCents, batchSize, attemptN, s.MaxAttemptsPerCycle)
			} else {
				log.Warnf("⚠️ [%s] 卖弱下单失败: weak=%s price=%dc size=%.4f err=%v attempt=%d/%d",
					ID, weakName, curBidCents, batchSize, err, attemptN, s.MaxAttemptsPerCycle)
			}
			return nil
		}

		// 记录订单信息（用于后续挂强方卖单）
		if s.EnableStrongSellAfterWeak && result != nil && len(result) > 0 {
			orderID := result[0].OrderID
			if orderID != "" {
				s.mu.Lock()
				s.weakSellOrders[orderID] = &weakSellOrderMeta{
					OrderID:        orderID,
					MarketSlug:     e.Market.Slug,
					StrongAssetID:  strongAssetID,
					StrongToken:    strongToken,
					StrongName:     strongName,
					BatchIndex:     i,
					BatchSize:      batchSize,
					FilledSize:     0,
					StrongSellDone: false,
				}
				trackedCount := len(s.weakSellOrders)
				s.mu.Unlock()
				log.Infof("📝 [%s] 已记录弱方卖出订单: orderID=%s batch=%d strong=%s batchSize=%.4f 当前跟踪订单数=%d",
					ID, orderID, i+1, strongName, batchSize, trackedCount)
			} else {
				log.Warnf("⚠️ [%s] ExecuteMultiLeg 返回的订单ID为空，无法跟踪弱方订单", ID)
			}
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

func (s *Strategy) mergePreviousCycleIfNeeded(oldMarket *domain.Market, newMarket *domain.Market) {
	// 先检查上一个周期是否需要 merge
	if oldMarket != nil && oldMarket.IsValid() && strings.TrimSpace(oldMarket.ConditionID) != "" {
		gc := config.Get()
		if gc == nil || strings.TrimSpace(gc.Wallet.PrivateKey) == "" {
			log.Warnf("⚠️ [%s] 检查上一周期 merge 失败：全局 wallet.private_key 不可用", ID)
		} else {
			privateKey, err := signing.PrivateKeyFromHex(gc.Wallet.PrivateKey)
			if err != nil {
				log.Warnf("⚠️ [%s] 检查上一周期 merge 失败：解析私钥失败: %v", ID, err)
			} else {
				checkAddr := crypto.PubkeyToAddress(privateKey.PublicKey)
				if strings.TrimSpace(gc.Wallet.FunderAddress) != "" {
					checkAddr = common.HexToAddress(strings.TrimSpace(gc.Wallet.FunderAddress))
				}

				// 检查上一个周期的 YES/NO 持仓
				_, yesBal, noBal, err := s.checkHoldingsOnce(oldMarket, checkAddr, 0)
				if err == nil && yesBal > 0 && noBal > 0 {
					// 取最小值进行 merge
					mergeAmount := math.Min(yesBal, noBal)
					if mergeAmount > 0 {
						log.Infof("🔄 [%s] 检测到上一周期需要 merge: market=%s yes=%.6f no=%.6f mergeAmount=%.6f",
							ID, oldMarket.Slug, yesBal, noBal, mergeAmount)

						// 执行 merge
						if s.TradingService != nil {
							ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
							defer cancel()

							metadata := fmt.Sprintf("AutoMerge previous cycle %.6f USDC for %s", mergeAmount, oldMarket.Slug)
							txHash, err := s.TradingService.MergeCompleteSetsViaRelayer(ctx, oldMarket.ConditionID, mergeAmount, metadata)
							if err != nil {
								log.Warnf("⚠️ [%s] 上一周期 merge 失败: market=%s amount=%.6f err=%v", ID, oldMarket.Slug, mergeAmount, err)
							} else {
								log.Infof("✅ [%s] 上一周期 merge 已提交: market=%s amount=%.6f tx=%s", ID, oldMarket.Slug, mergeAmount, txHash)
								// 等待一小段时间，让 merge 交易有时间提交
								time.Sleep(2 * time.Second)
							}
						} else {
							log.Warnf("⚠️ [%s] TradingService 不可用，跳过 merge", ID)
						}
					}
				} else if err != nil {
					log.Debugf("🔍 [%s] 检查上一周期持仓失败（可能已清空）: market=%s err=%v", ID, oldMarket.Slug, err)
				}
			}
		}
	}

	// merge 完成后（或无需 merge），继续执行 split
	s.splitCurrentCycleAtStart(newMarket)
}

func (s *Strategy) splitCurrentCycleAtStart(market *domain.Market) {
	if market == nil || strings.TrimSpace(market.ConditionID) == "" {
		return
	}

	// 去重：每周期只做一次 split 尝试
	s.mu.Lock()
	if s.splitDone {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	gc := config.Get()
	if gc == nil || strings.TrimSpace(gc.Wallet.PrivateKey) == "" {
		log.Warnf("⚠️ [%s] 自动 split 失败：全局 wallet.private_key 不可用", ID)
		return
	}

	privateKey, err := signing.PrivateKeyFromHex(gc.Wallet.PrivateKey)
	if err != nil {
		log.Warnf("⚠️ [%s] 自动 split 失败：解析私钥失败: %v", ID, err)
		return
	}

	amount := s.SplitAmount
	if amount <= 0 {
		amount = s.OrderSize
	}

	// 先检查是否已经持有本周期 YES+NO（避免重复 split 导致“越拆越多”）
	checkAddr := crypto.PubkeyToAddress(privateKey.PublicKey)
	if strings.TrimSpace(gc.Wallet.FunderAddress) != "" {
		checkAddr = common.HexToAddress(strings.TrimSpace(gc.Wallet.FunderAddress))
	}
	if ok, yesBal, noBal, err := s.checkHoldingsOnce(market, checkAddr, amount); err == nil && ok {
		s.mu.Lock()
		s.splitDone = true
		s.holdingsOK = true
		s.mu.Unlock()
		log.Infof("✅ [%s] 本周期已持有完整持仓，跳过 split: market=%s addr=%s yes=%.6f no=%.6f",
			ID, market.Slug, checkAddr.Hex(), yesBal, noBal)
		return
	}

	// dry-run：不发链上交易，直接标记持仓 OK（用于演练链路）
	if gc.DryRun {
		log.Warnf("📝 [%s] dry-run：跳过真实 split，仅记录计划: market=%s amount=%.6f", ID, market.Slug, amount)
		s.mu.Lock()
		s.splitDone = true
		s.holdingsOK = true
		s.mu.Unlock()
		return
	}

	builderKey := strings.TrimSpace(os.Getenv("BUILDER_API_KEY"))
	builderSecret := strings.TrimSpace(os.Getenv("BUILDER_SECRET"))
	builderPass := strings.TrimSpace(os.Getenv("BUILDER_PASS_PHRASE"))
	funder := strings.TrimSpace(gc.Wallet.FunderAddress)
	useRelayer := builderKey != "" && builderSecret != "" && builderPass != "" && funder != ""

	if useRelayer {
		checkAddr = common.HexToAddress(funder)
		// relayer 模式下：提前校验代理地址 USDC 余额 + allowance，避免白发链上请求
		ctf, err := clobclient.NewCTFClient(s.RPCURL, types.Chain(s.ChainID), privateKey)
		if err != nil {
			log.Warnf("⚠️ [%s] 自动 split 失败：创建 CTFClient 失败: %v", ID, err)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := ctf.ValidateSplitPositionForAddress(ctx, checkAddr, amount); err != nil {
			log.Warnf("⚠️ [%s] 自动 split（relayer）前置校验失败: market=%s addr=%s err=%v", ID, market.Slug, checkAddr.Hex(), err)
			return
		}
	}

	// 优先走 relayer（gasless）
	if useRelayer {
		if err := s.executeRelayerSplit(privateKey, funder, market.ConditionID, amount, market.Slug); err != nil {
			log.Warnf("⚠️ [%s] 自动 split（relayer）失败: %v", ID, err)
			return
		}
		s.mu.Lock()
		s.splitDone = true
		s.mu.Unlock()
		log.Infof("✅ [%s] 已自动 split 本周期（relayer 已提交）: market=%s amount=%.6f", ID, market.Slug, amount)
		go s.waitForHoldings(market, checkAddr, amount)
		return
	}

	// fallback：直接调用（仅适用于 EOA 自己持仓 + 自己交易；若你依赖 Safe/代理钱包，不建议）
	log.Warnf("⚠️ [%s] 未检测到 relayer 配置（BUILDER_* 或 funder_address 缺失），将尝试 direct split（需要 EOA 有 USDC 授权 + MATIC）", ID)
	ctf, err := clobclient.NewCTFClient(s.RPCURL, types.Chain(s.ChainID), privateKey)
	if err != nil {
		log.Warnf("⚠️ [%s] 自动 split 失败：创建 CTFClient 失败: %v", ID, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tx, err := ctf.SplitPosition(ctx, clobclient.SplitPositionParams{
		ConditionId: market.ConditionID,
		Amount:      amount,
	})
	if err != nil {
		log.Warnf("⚠️ [%s] 自动 split（direct）失败：构建 split tx 失败: %v", ID, err)
		return
	}
	txHash, err := ctf.SendTransaction(ctx, tx)
	if err != nil {
		log.Warnf("⚠️ [%s] 自动 split（direct）失败：发送 split tx 失败: %v", ID, err)
		return
	}

	s.mu.Lock()
	s.splitDone = true
	s.mu.Unlock()
	log.Infof("✅ [%s] 已自动 split 本周期（direct 已发送）: market=%s amount=%.6f tx=%s", ID, market.Slug, amount, txHash.Hex())
	go s.waitForHoldings(market, checkAddr, amount)
}

func (s *Strategy) checkHoldingsAtCycleStart(market *domain.Market) {
	// “不自动 split”的场景：只是确认持仓存在
	if market == nil || strings.TrimSpace(market.ConditionID) == "" {
		return
	}
	gc := config.Get()
	if gc == nil || strings.TrimSpace(gc.Wallet.PrivateKey) == "" {
		log.Warnf("⚠️ [%s] 周期持仓校验失败：全局 wallet.private_key 不可用", ID)
		return
	}
	privateKey, err := signing.PrivateKeyFromHex(gc.Wallet.PrivateKey)
	if err != nil {
		log.Warnf("⚠️ [%s] 周期持仓校验失败：解析私钥失败: %v", ID, err)
		return
	}
	checkAddr := crypto.PubkeyToAddress(privateKey.PublicKey)
	if strings.TrimSpace(gc.Wallet.FunderAddress) != "" {
		checkAddr = common.HexToAddress(strings.TrimSpace(gc.Wallet.FunderAddress))
	}
	expected := s.SplitAmount
	if expected <= 0 {
		expected = s.OrderSize
	}
	go s.waitForHoldings(market, checkAddr, expected)
}

func (s *Strategy) waitForHoldings(market *domain.Market, address common.Address, expected float64) {
	// 轮询等待余额出现（链上确认/索引同步可能有延迟）
	deadline := time.Now().Add(90 * time.Second)
	for {
		ok, yesBal, noBal, err := s.checkHoldingsOnce(market, address, expected)
		if err == nil && ok {
			s.mu.Lock()
			s.holdingsOK = true
			s.mu.Unlock()
			log.Infof("✅ [%s] 持仓校验通过: market=%s addr=%s yes=%.6f no=%.6f expected>=%.6f",
				ID, market.Slug, address.Hex(), yesBal, noBal, expected*s.HoldingsExpectedMinRatio)
			return
		}
		if time.Now().After(deadline) {
			s.mu.Lock()
			s.holdingsOK = false
			s.mu.Unlock()
			if err != nil {
				log.Warnf("🛑 [%s] 持仓校验超时失败: market=%s addr=%s err=%v", ID, market.Slug, address.Hex(), err)
			} else {
				log.Warnf("🛑 [%s] 持仓校验超时失败: market=%s addr=%s yes=%.6f no=%.6f expected>=%.6f",
					ID, market.Slug, address.Hex(), yesBal, noBal, expected*s.HoldingsExpectedMinRatio)
			}
			return
		}
		time.Sleep(5 * time.Second)
	}
}

func (s *Strategy) checkHoldingsOnce(market *domain.Market, address common.Address, expected float64) (ok bool, yesBal float64, noBal float64, err error) {
	if market == nil || strings.TrimSpace(market.ConditionID) == "" {
		return false, 0, 0, fmt.Errorf("market/conditionId invalid")
	}
	gc := config.Get()
	if gc == nil || strings.TrimSpace(gc.Wallet.PrivateKey) == "" {
		return false, 0, 0, fmt.Errorf("wallet.private_key missing")
	}
	privateKey, err := signing.PrivateKeyFromHex(gc.Wallet.PrivateKey)
	if err != nil {
		return false, 0, 0, err
	}
	ctf, err := clobclient.NewCTFClient(s.RPCURL, types.Chain(s.ChainID), privateKey)
	if err != nil {
		return false, 0, 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cond := common.HexToHash(market.ConditionID)
	parent := common.Hash{}

	yesCol, err := ctf.GetCollectionId(parent, cond, big.NewInt(1))
	if err != nil {
		return false, 0, 0, err
	}
	noCol, err := ctf.GetCollectionId(parent, cond, big.NewInt(2))
	if err != nil {
		return false, 0, 0, err
	}
	yesPos, err := ctf.GetPositionId(ctf.GetCollateralToken(), yesCol)
	if err != nil {
		return false, 0, 0, err
	}
	noPos, err := ctf.GetPositionId(ctf.GetCollateralToken(), noCol)
	if err != nil {
		return false, 0, 0, err
	}

	yesBal, err = ctf.GetConditionalTokenBalanceForAddress(ctx, address, yesPos)
	if err != nil {
		return false, 0, 0, err
	}
	noBal, err = ctf.GetConditionalTokenBalanceForAddress(ctx, address, noPos)
	if err != nil {
		return false, 0, 0, err
	}

	minNeed := expected * s.HoldingsExpectedMinRatio
	ok = yesBal >= minNeed && noBal >= minNeed
	return ok, yesBal, noBal, nil
}

func (s *Strategy) executeRelayerSplit(privateKey *ecdsa.PrivateKey, funderAddress string, conditionID string, amount float64, slug string) error {
	builderKey := strings.TrimSpace(os.Getenv("BUILDER_API_KEY"))
	builderSecret := strings.TrimSpace(os.Getenv("BUILDER_SECRET"))
	builderPass := strings.TrimSpace(os.Getenv("BUILDER_PASS_PHRASE"))
	if builderKey == "" || builderSecret == "" || builderPass == "" {
		return fmt.Errorf("builder creds missing")
	}
	if strings.TrimSpace(funderAddress) == "" {
		return fmt.Errorf("funder_address missing")
	}

	// amount -> 6 decimals
	amountBig := new(big.Int)
	amountFloat := new(big.Float).SetFloat64(amount)
	decimals := new(big.Float).SetInt64(1000000)
	amountFloat.Mul(amountFloat, decimals)
	amountBig, _ = amountFloat.Int(nil)

	condHash := common.HexToHash(conditionID)
	apiTx, err := sdkapi.BuildSplitTransaction(condHash, amountBig)
	if err != nil {
		return fmt.Errorf("build split tx failed: %w", err)
	}

	relayerTx := relayertypes.SafeTransaction{
		To:        apiTx.To.Hex(),
		Operation: relayertypes.OperationType(apiTx.Operation),
		Data:      "0x" + hex.EncodeToString(apiTx.Data),
		Value:     apiTx.Value.String(),
	}

	// 签名函数（EIP-191 digest 由 relayer SDK 处理）
	signFn := func(_ string, digest []byte) ([]byte, error) {
		sig, err := crypto.Sign(digest, privateKey)
		if err != nil {
			return nil, err
		}
		if sig[64] < 27 {
			sig[64] += 27
		}
		return sig, nil
	}

	relayerURL := "https://relayer-v2.polymarket.com"
	builderCreds := &sdktypes.BuilderApiKeyCreds{
		Key:        builderKey,
		Secret:     builderSecret,
		Passphrase: builderPass,
	}

	chainID := big.NewInt(s.ChainID)
	rc := sdkrelayer.NewClient(relayerURL, chainID, signFn, builderCreds)

	signer := crypto.PubkeyToAddress(privateKey.PublicKey).Hex()
	auth := &sdktypes.AuthOption{
		SingerAddress: signer,
		FunderAddress: strings.TrimSpace(funderAddress),
	}

	metadata := fmt.Sprintf("AutoSplit %.6f USDC for %s", amount, slug)
	if len(metadata) > 500 {
		metadata = metadata[:497] + "..."
	}

	resp, err := rc.Execute([]relayertypes.SafeTransaction{relayerTx}, metadata, auth)
	if err != nil {
		return err
	}
	txHash := resp.TransactionHash
	if txHash == "" {
		txHash = resp.Hash
	}
	log.Infof("📨 [%s] relayer split submitted: txID=%s txHash=%s state=%s", ID, resp.TransactionID, txHash, resp.State)
	return nil
}

// OnOrderUpdate 订单更新回调：监听弱方卖出订单成交，成交后立即挂强方卖单
func (s *Strategy) OnOrderUpdate(ctx context.Context, order *domain.Order) error {
	if order == nil {
		return nil
	}

	log.Infof("🔍 [%s] OnOrderUpdate 收到订单更新: orderID=%s status=%s filledSize=%.4f marketSlug=%s EnableStrongSellAfterWeak=%v",
		ID, order.OrderID, order.Status, order.FilledSize, order.MarketSlug, s.EnableStrongSellAfterWeak)

	if !s.EnableStrongSellAfterWeak {
		log.Infof("🔍 [%s] EnableStrongSellAfterWeak 为 false，跳过处理", ID)
		return nil
	}

	s.mu.Lock()
	meta, exists := s.weakSellOrders[order.OrderID]
	trackedCount := len(s.weakSellOrders)
	if !exists {
		// 打印所有跟踪的订单ID以便调试
		trackedIDs := make([]string, 0, trackedCount)
		for id := range s.weakSellOrders {
			trackedIDs = append(trackedIDs, id)
		}
		s.mu.Unlock()
		log.Infof("🔍 [%s] 订单不在跟踪列表中: orderID=%s 跟踪列表长度=%d 跟踪的订单IDs=%v (可能是竞态条件，延迟重试)",
			ID, order.OrderID, trackedCount, trackedIDs)
		
		// 延迟重试：可能是竞态条件，订单记录还在进行中
		if order.Status == domain.OrderStatusFilled {
			go func() {
				time.Sleep(200 * time.Millisecond)
				s.mu.Lock()
				meta, exists := s.weakSellOrders[order.OrderID]
				s.mu.Unlock()
				if exists && order.Status == domain.OrderStatusFilled {
					log.Infof("🔄 [%s] 延迟重试：找到跟踪的弱方订单: orderID=%s batch=%d status=%s filledSize=%.4f batchSize=%.4f",
						ID, order.OrderID, meta.BatchIndex+1, order.Status, order.FilledSize, meta.BatchSize)
					
					// 检查是否应该挂强方卖单
					shouldPlaceStrongSell := order.FilledSize >= meta.BatchSize &&
						!meta.StrongSellDone &&
						meta.BatchIndex < len(s.StrongSellPrices)
					
					if shouldPlaceStrongSell {
						s.mu.Lock()
						meta.StrongSellDone = true
						s.mu.Unlock()
						go s.placeStrongSellOrder(context.Background(), meta, order)
					}
				}
			}()
		}
		return nil
	}
	log.Infof("📋 [%s] 找到跟踪的弱方订单: orderID=%s batch=%d status=%s filledSize=%.4f batchSize=%.4f",
		ID, order.OrderID, meta.BatchIndex+1, order.Status, order.FilledSize, meta.BatchSize)

	// 更新已成交数量
	meta.FilledSize = order.FilledSize

	// 检查是否已完全成交且未挂强方卖单
	statusOK := order.Status == domain.OrderStatusFilled
	filledOK := order.FilledSize >= meta.BatchSize
	notDone := !meta.StrongSellDone
	indexOK := meta.BatchIndex < len(s.StrongSellPrices)
	shouldPlaceStrongSell := statusOK && filledOK && notDone && indexOK

	// 详细日志：诊断为什么没有挂单
	log.Infof("🔍 [%s] 强方卖单检查: orderID=%s statusOK=%v filledOK=%v (filledSize=%.4f >= batchSize=%.4f) notDone=%v indexOK=%v (batchIndex=%d < pricesLen=%d) shouldPlace=%v",
		ID, order.OrderID, statusOK, filledOK, order.FilledSize, meta.BatchSize, notDone, indexOK, meta.BatchIndex, len(s.StrongSellPrices), shouldPlaceStrongSell)

	// 如果应该挂单，立即标记为已处理，避免重复触发
	if shouldPlaceStrongSell {
		meta.StrongSellDone = true
		log.Infof("✅ [%s] 准备挂强方卖单: orderID=%s batch=%d", ID, order.OrderID, meta.BatchIndex+1)
	}
	s.mu.Unlock()

	if !shouldPlaceStrongSell {
		log.Infof("⏸️ [%s] 不挂强方卖单: orderID=%s reason=%s", ID, order.OrderID, func() string {
			if !statusOK {
				return "status != filled"
			}
			if !filledOK {
				return fmt.Sprintf("filledSize(%.4f) < batchSize(%.4f)", order.FilledSize, meta.BatchSize)
			}
			if !notDone {
				return "already done"
			}
			if !indexOK {
				return fmt.Sprintf("batchIndex(%d) >= pricesLen(%d)", meta.BatchIndex, len(s.StrongSellPrices))
			}
			return "unknown"
		}())
		return nil
	}

	// 异步挂强方卖单，避免阻塞订单更新回调
	go s.placeStrongSellOrder(ctx, meta, order)
	return nil
}

// placeStrongSellOrder 挂强方卖单
func (s *Strategy) placeStrongSellOrder(ctx context.Context, meta *weakSellOrderMeta, weakOrder *domain.Order) {
	// 双重检查：再次确认未挂单（防御并发）
	s.mu.Lock()
	if meta.StrongSellDone {
		// 检查是否已经有订单ID（说明已经成功挂单）
		s.mu.Unlock()
		log.Debugf("🔍 [%s] 强方卖单已处理，跳过: batch=%d market=%s", ID, meta.BatchIndex+1, meta.MarketSlug)
		return
	}
	s.mu.Unlock()

	if s.TradingService == nil {
		log.Warnf("⚠️ [%s] TradingService 不可用，无法挂强方卖单", ID)
		return
	}

	// 获取强方卖出价格
	if meta.BatchIndex >= len(s.StrongSellPrices) {
		log.Warnf("⚠️ [%s] 批次索引超出价格数组范围: batchIndex=%d pricesLen=%d", ID, meta.BatchIndex, len(s.StrongSellPrices))
		return
	}
	if meta.BatchIndex >= len(s.SellSplits) {
		log.Warnf("⚠️ [%s] 批次索引超出 sellSplits 范围: batchIndex=%d splitsLen=%d", ID, meta.BatchIndex, len(s.SellSplits))
		return
	}
	strongPriceCents := s.StrongSellPrices[meta.BatchIndex]
	strongPrice := domain.PriceFromDecimal(float64(strongPriceCents) / 100.0)

	// 根据 sellSplits 比例动态计算强方卖出数量
	batchSize := s.OrderSize * s.SellSplits[meta.BatchIndex]
	if batchSize <= 0 {
		log.Warnf("⚠️ [%s] 计算出的强方卖出数量无效: batchSize=%.4f orderSize=%.4f split=%.4f",
			ID, batchSize, s.OrderSize, s.SellSplits[meta.BatchIndex])
		return
	}

	// 四舍五入到4位小数，避免浮点数精度问题
	batchSize = math.Round(batchSize*10000) / 10000

	// 验证并修正订单金额精度：确保 price * size 的金额计算正确
	// Polymarket 要求 taker amount 必须精确匹配（例如：0.94 * 5 = 4.7，不能是 4.6999）
	priceDecimal := float64(strongPriceCents) / 100.0
	expectedAmount := priceDecimal * batchSize
	// 计算期望的精确金额（四舍五入到2位小数）
	expectedAmountRounded := math.Round(expectedAmount*100) / 100
	// 如果存在精度误差，重新计算batchSize以确保金额精确
	if math.Abs(expectedAmount-expectedAmountRounded) > 0.0001 {
		// 反向计算：从精确金额反推size
		batchSize = expectedAmountRounded / priceDecimal
		batchSize = math.Round(batchSize*10000) / 10000
		log.Debugf("🔧 [%s] 调整订单大小以确保金额精度: size=%.4f price=%.2f expectedAmount=%.2f",
			ID, batchSize, priceDecimal, expectedAmountRounded)
	}

	// 检查持仓：确保有足够的强方代币可卖
	positions := s.TradingService.GetOpenPositionsForMarket(meta.MarketSlug)
	var strongPosition *domain.Position
	for _, pos := range positions {
		if pos != nil && pos.IsOpen() && pos.TokenType == meta.StrongToken {
			// 通过 Market 获取 AssetID 进行匹配
			if pos.Market != nil && pos.Market.GetAssetID(meta.StrongToken) == meta.StrongAssetID {
				strongPosition = pos
				break
			}
		}
	}

	if strongPosition == nil || strongPosition.Size < batchSize {
		availableSize := 0.0
		if strongPosition != nil {
			availableSize = strongPosition.Size
		}
		log.Warnf("⚠️ [%s] 强方持仓不足: 需要=%.4f 可用=%.4f strong=%s market=%s",
			ID, batchSize, availableSize, meta.StrongName, meta.MarketSlug)
		return
	}

	log.Infof("🎯 [%s] 准备挂强方卖单: strong=%s price=%dc size=%.4f batch=%d market=%s 持仓=%.4f",
		ID, meta.StrongName, strongPriceCents, batchSize, meta.BatchIndex+1, meta.MarketSlug, strongPosition.Size)

	req := execution.MultiLegRequest{
		Name:       fmt.Sprintf("ctfendgame_sellstrong_%d", meta.BatchIndex+1),
		MarketSlug: meta.MarketSlug,
		Legs: []execution.LegIntent{{
			Name:      fmt.Sprintf("sell_strong_%d", meta.BatchIndex+1),
			AssetID:   meta.StrongAssetID,
			TokenType: meta.StrongToken,
			Side:      types.SideSell,
			Price:     strongPrice,
			Size:      batchSize,
			OrderType: types.OrderTypeGTC, // 限价单，等待成交
		}},
		Hedge: execution.AutoHedgeConfig{Enabled: false},
	}

	orderCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	result, err := s.TradingService.ExecuteMultiLeg(orderCtx, req)
	if err != nil {
		// 挂单失败：释放标记，允许重试（但避免立即重试导致重复）
		estr := strings.ToLower(err.Error())
		if strings.Contains(estr, "duplicate in-flight") {
			// 重复挂单错误：可能是并发导致的，不释放标记（避免重复尝试）
			log.Debugf("🔍 [%s] 强方卖单重复提交（已处理）: batch=%d market=%s", ID, meta.BatchIndex+1, meta.MarketSlug)
		} else {
			// 其他错误：释放标记，允许后续重试
			s.mu.Lock()
			meta.StrongSellDone = false
			s.mu.Unlock()
			log.Warnf("⚠️ [%s] 挂强方卖单失败: strong=%s price=%dc size=%.4f err=%v",
				ID, meta.StrongName, strongPriceCents, batchSize, err)
		}
		return
	}

	// 挂单成功：确认标记已挂强方卖单
	s.mu.Lock()
	meta.StrongSellDone = true
	s.mu.Unlock()

	orderID := ""
	if result != nil && len(result) > 0 {
		orderID = result[0].OrderID
	}

	log.Infof("✅ [%s] 已挂强方卖单: strong=%s price=%dc size=%.4f orderID=%s batch=%d market=%s",
		ID, meta.StrongName, strongPriceCents, batchSize, orderID, meta.BatchIndex+1, meta.MarketSlug)
}
