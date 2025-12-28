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

	firstSeenAt time.Time
	cycleStart  time.Time

	sellSequencesDone int
	attemptsThisCycle int
	lastAttemptAt     time.Time

	// ===== 自动编排（新周期开始立刻 split + 持仓校验）=====
	holdingsOK bool
	splitDone  bool
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
	s.holdingsOK = false
	s.splitDone = false

	if newMarket != nil && newMarket.Timestamp > 0 {
		s.cycleStart = time.Unix(newMarket.Timestamp, 0)
	} else {
		s.cycleStart = time.Time{}
	}

	// 新周期开始：立刻 split 本周期（更简单，避免跨周期做事）
	if s.EnableAutoSplitOnCycleStart && newMarket != nil && newMarket.IsValid() {
		go s.splitCurrentCycleAtStart(newMarket)
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

	checkAddr := crypto.PubkeyToAddress(privateKey.PublicKey)
	if useRelayer {
		checkAddr = common.HexToAddress(funder)
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
