package adaptive

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/betbot/gobet/clob/rtds"
	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/execution"
	"github.com/betbot/gobet/internal/services"
	"github.com/betbot/gobet/internal/strategies/common"
	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/betbot/gobet/pkg/config"
	"github.com/betbot/gobet/pkg/marketspec"
	"github.com/sirupsen/logrus"
)

var log = logrus.WithField("strategy", ID)

func init() {
	bbgo.RegisterStrategy(ID, &Strategy{})
}

// Strategy 自适应定价策略（Bot v5.1 复刻）
type Strategy struct {
	TradingService *services.TradingService
	Config         `yaml:",inline" json:",inline"`

	// RTDS 客户端
	rtdsClient *rtds.Client

	autoMerge common.AutoMergeController

	// 价格数据
	binanceFutPrice float64 // Binance 期货价格
	chainlinkPrice  float64 // Chainlink 价格
	priceMu         sync.RWMutex

	// market spec（用于订阅标的 & 周期长度）
	marketSpec      marketspec.MarketSpec
	underlyingUpper string // e.g. BTC
	binanceSymbol   string // e.g. btcusdt
	chainlinkSymbol string // e.g. btc/usd

	// 市场信息
	marketInfo struct {
		slug       string
		startTime  int64  // Unix 秒时间戳
		endDate    *time.Time
		strikePrice float64
	}
	marketMu sync.RWMutex

	// Maker 订单跟踪
	makerOrders map[domain.TokenType]map[string]string // tokenType -> priceKey -> orderID
	makerMu     sync.RWMutex

	// 日志时间戳
	priceLogTs time.Time

	// 限流
	lastTradeTs time.Time
	tradeMu     sync.Mutex
}

func (s *Strategy) ID() string   { return ID }
func (s *Strategy) Name() string { return ID }

func (s *Strategy) Defaults() error {
	if s.Config.K == 0 {
		s.Config.K = 0.08
	}
	if s.Config.C == 0 {
		s.Config.C = 0.10
	}
	if s.Config.SizePerTrade == 0 {
		s.Config.SizePerTrade = 12
	}
	if s.Config.InventorySkewFactor == 0 {
		s.Config.InventorySkewFactor = 0.005 / 100
	}
	if s.Config.BaseMinEdgeMaker == 0 {
		s.Config.BaseMinEdgeMaker = 0.0005
	}
	if s.Config.BaseMinEdgeTaker == 0 {
		s.Config.BaseMinEdgeTaker = 0.003
	}
	if s.Config.MarketWeight == 0 {
		s.Config.MarketWeight = 0.7
	}
	if s.Config.DecayStartTime == 0 {
		s.Config.DecayStartTime = 300
	}
	if s.Config.ReduceOnlyTime == 0 {
		s.Config.ReduceOnlyTime = 300
	}
	if s.Config.ForceCloseTime == 0 {
		s.Config.ForceCloseTime = 180
	}
	if s.Config.MaxEdgeAtZero == 0 {
		s.Config.MaxEdgeAtZero = 0.02
	}
	if s.Config.HedgeThreshold == 0 {
		s.Config.HedgeThreshold = 80
	}
	if s.Config.StopQuoteThreshold == 0 {
		s.Config.StopQuoteThreshold = 60
	}
	if s.Config.HedgeSizeMultiplier == 0 {
		s.Config.HedgeSizeMultiplier = 1.5
	}
	if s.Config.MinOrderSize == 0 {
		s.Config.MinOrderSize = 1.1
	}
	if s.Config.MarketIntervalSeconds == 0 {
		// 默认从全局 market 配置推导；如果不可用则退回 15m
		if gc := config.Get(); gc != nil {
			if sp, err := gc.Market.Spec(); err == nil {
				s.Config.MarketIntervalSeconds = int(sp.Duration().Seconds())
			}
		}
		if s.Config.MarketIntervalSeconds == 0 {
			s.Config.MarketIntervalSeconds = 900 // 默认15分钟
		}
	}
	return nil
}

func (s *Strategy) Validate() error {
	return s.Config.Validate()
}

func (s *Strategy) Initialize() error {
	// market spec（默认 btc/15m/updown；如果全局配置存在则以全局 market 为准）
	spec, _ := marketspec.New("btc", "15m", "updown")
	if gc := config.Get(); gc != nil {
		if sp, err := gc.Market.Spec(); err == nil {
			spec = sp
		}
	}
	s.marketSpec = spec
	s.underlyingUpper = strings.ToUpper(spec.Symbol)
	s.binanceSymbol = strings.ToLower(s.underlyingUpper + "usdt")
	s.chainlinkSymbol = strings.ToLower(spec.Symbol) + "/usd"

	// 初始化 RTDS 客户端
	config := rtds.DefaultClientConfig()
	// 使用环境变量中的代理配置
	proxyURL := os.Getenv("HTTP_PROXY")
	if proxyURL == "" {
		proxyURL = os.Getenv("HTTPS_PROXY")
	}
	if proxyURL != "" {
		config.ProxyURL = proxyURL
	}
	s.rtdsClient = rtds.NewClientWithConfig(config)

	// 初始化 Maker 订单跟踪
	s.makerOrders = make(map[domain.TokenType]map[string]string)
	s.makerOrders[domain.TokenTypeUp] = make(map[string]string)
	s.makerOrders[domain.TokenTypeDown] = make(map[string]string)

	// 连接 RTDS
	if err := s.rtdsClient.Connect(); err != nil {
		return fmt.Errorf("连接 RTDS 失败: %w", err)
	}

	// 注册价格处理器
	binanceHandler := rtds.CreateCryptoPriceHandler(func(price *rtds.CryptoPrice) error {
		if strings.ToLower(strings.TrimSpace(price.Symbol)) == s.binanceSymbol {
			val := price.Value.Float64()
			if val > 0 {
				s.priceMu.Lock()
				s.binanceFutPrice = val
				s.priceMu.Unlock()
			}
		}
		return nil
	})

	chainlinkHandler := rtds.CreateCryptoPriceHandler(func(price *rtds.CryptoPrice) error {
		if strings.ToLower(strings.TrimSpace(price.Symbol)) == s.chainlinkSymbol {
			val := price.Value.Float64()
			if val > 0 {
				s.priceMu.Lock()
				s.chainlinkPrice = val
				s.priceMu.Unlock()
			}
		}
		return nil
	})

	s.rtdsClient.RegisterHandler("crypto_prices", binanceHandler)
	s.rtdsClient.RegisterHandler("crypto_prices_chainlink", chainlinkHandler)

	// 订阅价格
	if err := s.rtdsClient.SubscribeToCryptoPrices("binance", s.binanceSymbol); err != nil {
		return fmt.Errorf("订阅 Binance 价格失败: %w", err)
	}
	if err := s.rtdsClient.SubscribeToCryptoPrices("chainlink", s.chainlinkSymbol); err != nil {
		return fmt.Errorf("订阅 Chainlink 价格失败: %w", err)
	}

	log.Infof("✅ [adaptive] 策略初始化完成，RTDS 已连接")
	return nil
}

func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
	session.OnPriceChanged(s)
	session.OnOrderUpdate(s)
	log.Infof("✅ [adaptive] 策略已订阅价格变化和订单更新事件 (session=%s)", session.Name)
}

func (s *Strategy) Run(ctx context.Context, _ bbgo.OrderExecutor, _ *bbgo.ExchangeSession) error {
	<-ctx.Done()
	return ctx.Err()
}

// OnCycle 框架层周期切换回调：统一在这里处理市场切换（策略内部不再基于 slug 对比做周期判断）。
func (s *Strategy) OnCycle(_ context.Context, _ *domain.Market, newMarket *domain.Market) {
	if newMarket == nil || newMarket.Slug == "" {
		return
	}
	log.Infof("🔄 [adaptive] 周期切换: %s", newMarket.Slug)
	s.onMarketSwitch(newMarket)
}

// OnPriceChanged 处理价格变化事件
func (s *Strategy) OnPriceChanged(ctx context.Context, e *events.PriceChangedEvent) error {
	if e == nil || e.Market == nil {
		return nil
	}
	if s.TradingService != nil {
		s.autoMerge.MaybeAutoMerge(ctx, s.TradingService, e.Market, s.AutoMerge, log.Infof)
	}

	// 兜底：如果框架的 OnCycle 尚未来得及初始化 marketInfo（极端竞态），这里做一次性初始化
	s.marketMu.RLock()
	inited := s.marketInfo.slug != ""
	s.marketMu.RUnlock()
	if !inited && e.Market.Slug != "" {
		s.onMarketSwitch(e.Market)
	}

	// 执行策略逻辑
	return s.onTick(ctx, e)
}

// OnOrderUpdate 处理订单更新事件
func (s *Strategy) OnOrderUpdate(_ context.Context, order *domain.Order) error {
	if order == nil {
		return nil
	}

	// 检查 Maker 订单成交
	s.makerMu.Lock()
	defer s.makerMu.Unlock()

	for tokenType, orders := range s.makerOrders {
		for priceKey, orderID := range orders {
			if orderID == order.OrderID {
				if order.IsFilled() {
					log.Infof("✅ [adaptive] MAKER 订单成交: %s @ %s (OrderID: %s)", tokenType, priceKey, orderID)
					delete(s.makerOrders[tokenType], priceKey)
				}
				break
			}
		}
	}

	return nil
}

// onMarketSwitch 处理市场切换
func (s *Strategy) onMarketSwitch(market *domain.Market) {
	s.marketMu.Lock()
	defer s.marketMu.Unlock()

	// 重置市场信息
	s.marketInfo.slug = market.Slug
	s.marketInfo.startTime = market.Timestamp
	s.marketInfo.strikePrice = 0

	// 清空 Maker 订单
	s.makerMu.Lock()
	s.makerOrders[domain.TokenTypeUp] = make(map[string]string)
	s.makerOrders[domain.TokenTypeDown] = make(map[string]string)
	s.makerMu.Unlock()

	// 异步获取 Strike Price
	go s.ensureStrikePrice(market)
}

// ensureStrikePrice 异步获取行权价
func (s *Strategy) ensureStrikePrice(market *domain.Market) {
	_ = market
	// 为了支持多币种/多周期，这里不再调用“固定 BTC + fifteen 变体”的 polymarket crypto-price API。
	// 直接等待 Chainlink 实时报价可用后，作为 strike 的近似/兜底。
	maxAttempts := 30
	for i := 0; i < maxAttempts; i++ {
		s.priceMu.RLock()
		cl := s.chainlinkPrice
		s.priceMu.RUnlock()
		if cl > 0 {
			s.marketMu.Lock()
			s.marketInfo.strikePrice = cl
			s.marketMu.Unlock()
			log.Infof("🎯 [adaptive] 使用 Chainlink 作为行权价兜底: %.2f (symbol=%s)", cl, s.chainlinkSymbol)
			return
		}
		time.Sleep(1 * time.Second)
	}
	log.Warnf("⚠️ [adaptive] 无法获取行权价（Chainlink 仍未就绪），继续等待 onTick 兜底逻辑")
}

// fetchStrikePrice 获取行权价
func (s *Strategy) fetchStrikePrice(startIso, endIso string) (float64, error) {
	apiURL := fmt.Sprintf(
		"https://polymarket.com/api/crypto/crypto-price?symbol=%s&eventStartTime=%s&variant=fifteen&endDate=%s",
		url.QueryEscape(s.underlyingUpper),
		url.QueryEscape(startIso),
		url.QueryEscape(endIso),
	)

	req, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return 0, fmt.Errorf("创建请求失败: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "gobet-adaptive")

	transport := &http.Transport{
		MaxIdleConns:        100,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}

	proxyURL := os.Getenv("HTTP_PROXY")
	if proxyURL == "" {
		proxyURL = os.Getenv("HTTPS_PROXY")
	}
	if proxyURL != "" {
		if parsed, err := url.Parse(proxyURL); err == nil {
			transport.Proxy = http.ProxyURL(parsed)
		}
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   10 * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("API 返回错误状态码 %d", resp.StatusCode)
	}

	var apiResp struct {
		OpenPrice  *float64 `json:"openPrice"`
		ClosePrice *float64 `json:"closePrice"`
		Error      string   `json:"error"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return 0, fmt.Errorf("解析响应失败: %w", err)
	}

	if apiResp.Error != "" {
		return 0, fmt.Errorf("API 返回错误: %s", apiResp.Error)
	}

	if apiResp.OpenPrice == nil || *apiResp.OpenPrice <= 0 {
		return 0, fmt.Errorf("API 返回的 openPrice 无效")
	}

	return *apiResp.OpenPrice, nil
}

// getDynamicMakerEdge 计算动态 Maker Edge
func (s *Strategy) getDynamicMakerEdge(remaining float64) float64 {
	if remaining > s.Config.DecayStartTime {
		return s.Config.BaseMinEdgeMaker
	}
	progress := (s.Config.DecayStartTime - remaining) / s.Config.DecayStartTime
	if progress < 0 {
		progress = 0
	}
	if progress > 1 {
		progress = 1
	}
	return s.Config.BaseMinEdgeMaker + progress*(s.Config.MaxEdgeAtZero-s.Config.BaseMinEdgeMaker)
}

// onTick 核心策略循环
func (s *Strategy) onTick(ctx context.Context, e *events.PriceChangedEvent) error {
	s.marketMu.RLock()
	marketSlug := s.marketInfo.slug
	startTime := s.marketInfo.startTime
	strikePrice := s.marketInfo.strikePrice
	s.marketMu.RUnlock()

	if marketSlug == "" {
		return nil
	}

	// 计算剩余时间
	// 使用事件时间戳（如果可用），否则使用当前时间
	eventTimeSec := e.Timestamp.Unix()
	if eventTimeSec == 0 {
		eventTimeSec = time.Now().Unix()
	}
	remaining := float64(s.Config.MarketIntervalSeconds) - float64(eventTimeSec-startTime)

	// 兜底逻辑：如果开始10s还没拿到官方 Strike，就用 Chainlink 顶替
	if (remaining <= 0 || remaining > float64(s.Config.MarketIntervalSeconds)-10) && strikePrice == 0 {
		s.priceMu.RLock()
		chainlinkPrice := s.chainlinkPrice
		s.priceMu.RUnlock()
		if chainlinkPrice > 0 {
			s.marketMu.Lock()
			s.marketInfo.strikePrice = chainlinkPrice
			strikePrice = chainlinkPrice
			s.marketMu.Unlock()
		}
	}

	// 如果没有 Strike Price，无法计算 Delta，跳过
	if strikePrice == 0 {
		return nil
	}

	// 获取盘口数据
	marketUp := s.getMarketData(ctx, e.Market, domain.TokenTypeUp)
	marketDown := s.getMarketData(ctx, e.Market, domain.TokenTypeDown)

	if marketUp == nil || marketDown == nil {
		return nil
	}

	// 检查 Maker 订单成交
	s.checkMakerFills(marketUp, marketDown)

	// 获取 Binance 期货价格
	s.priceMu.RLock()
	fut := s.binanceFutPrice
	s.priceMu.RUnlock()

	if fut == 0 {
		return nil
	}

	// 获取持仓
	positions := s.TradingService.GetOpenPositionsForMarket(marketSlug)
	netInv := s.calculateNetInventory(positions)

	// 计算定价（需要 netInv 来计算库存偏斜）
	pricing := s.calculatePricing(fut, strikePrice, remaining, marketUp, marketDown, netInv)
	if pricing == nil {
		return nil
	}

	// 状态判定
	currentMakerEdge := s.getDynamicMakerEdge(remaining)
	isReduceOnly := remaining < s.Config.ReduceOnlyTime
	isForceClose := remaining < s.Config.ForceCloseTime

	// 决策逻辑
	action := s.decideAction(pricing, marketUp, marketDown, netInv, currentMakerEdge, isReduceOnly, isForceClose, remaining)

	// 执行交易
	if action != nil {
		if err := s.executeTrade(ctx, action, e.Market, remaining); err != nil {
			log.Errorf("执行交易失败: %v", err)
		}
	}

	// 定时日志（每1秒）
	now := time.Now()
	if now.Sub(s.priceLogTs) >= 1*time.Second {
		s.priceLogTs = now
		s.logStatus(pricing, marketUp, marketDown, netInv, remaining, fut, strikePrice, isForceClose, isReduceOnly, action)
	}

	return nil
}

// MarketData 市场数据
type MarketData struct {
	Bid       float64
	Ask       float64
	MidPrice  float64
	Timestamp time.Time
}

// getMarketData 获取市场数据
func (s *Strategy) getMarketData(ctx context.Context, market *domain.Market, tokenType domain.TokenType) *MarketData {
	assetID := market.GetAssetID(tokenType)
	bestBid, bestAsk, err := s.TradingService.GetBestPrice(ctx, assetID)
	if err != nil || bestBid <= 0 || bestAsk <= 0 {
		return nil
	}

	midPrice := (bestBid + bestAsk) / 2.0
	return &MarketData{
		Bid:       bestBid,
		Ask:       bestAsk,
		MidPrice:  midPrice,
		Timestamp: time.Now(),
	}
}

// Pricing 定价结果
type Pricing struct {
	Delta          float64
	ModelFairUp    float64
	MarketMidUp    float64
	FinalFairUp    float64
	FinalFairDown  float64
	Skew           float64
	ResPriceUp      float64
	ResPriceDown    float64
}

// calculatePricing 计算定价
func (s *Strategy) calculatePricing(fut, strikePrice, remaining float64, marketUp, marketDown *MarketData, netInv float64) *Pricing {
	// Delta = fut - strikePrice
	delta := fut - strikePrice

	// 防止除以0
	timeFactor := 1.0
	if remaining > 1 {
		timeFactor = remaining
	}
	// rawX = delta / sqrt(remaining)
	rawX := delta / sqrt(timeFactor)

	// 模型概率
	z := s.Config.K*rawX + s.Config.C
	modelFairUp := common.NormCdf(z)

	// 市场中枢
	marketMidUp := modelFairUp // 默认
	if marketUp.Bid > 0 && marketUp.Ask > 0 {
		marketMidUp = marketUp.MidPrice
	}

	// 融合概率
	finalFairUp := (1-s.Config.MarketWeight)*modelFairUp + s.Config.MarketWeight*marketMidUp
	finalFairDown := 1.0 - finalFairUp

	// 库存偏斜
	skew := netInv * s.Config.InventorySkewFactor

	return &Pricing{
		Delta:         delta,
		ModelFairUp:   modelFairUp,
		MarketMidUp:   marketMidUp,
		FinalFairUp:   finalFairUp,
		FinalFairDown: finalFairDown,
		Skew:          skew,
		ResPriceUp:     finalFairUp - skew,
		ResPriceDown:   finalFairDown + skew,
	}
}

// calculateNetInventory 计算净持仓
func (s *Strategy) calculateNetInventory(positions []*domain.Position) float64 {
	var upShares, downShares float64
	for _, pos := range positions {
		if pos.Status == domain.PositionStatusOpen {
			if pos.TokenType == domain.TokenTypeUp {
				upShares += pos.Size
			} else {
				downShares += pos.Size
			}
		}
	}
	return upShares - downShares
}

// TradeAction 交易动作
type TradeAction struct {
	Type string // MAKER, TAKER, FORCE_CLOSE, TAKER_HEDGE
	Side domain.TokenType
	Price float64
	Size  float64
}

// decideAction 决策逻辑
func (s *Strategy) decideAction(pricing *Pricing, marketUp, marketDown *MarketData, netInv, currentMakerEdge float64, isReduceOnly, isForceClose bool, remaining float64) *TradeAction {
	// 使用已计算好的价格（已包含库存偏斜）
	resPriceUp := pricing.ResPriceUp
	resPriceDown := pricing.ResPriceDown

	// [A] 强制平仓（最高优先级）
	if isForceClose {
		if abs(netInv) >= 5 {
			if netInv > 0 {
				// 持有净多头，买入 DOWN
				if marketDown.Ask > 0 && marketDown.Ask < 0.99 {
					return &TradeAction{
						Type:  "FORCE_CLOSE",
						Side:  domain.TokenTypeDown,
						Price: marketDown.Ask,
						Size:  s.Config.SizePerTrade,
					}
				}
			} else {
				// 持有净空头，买入 UP
				if marketUp.Ask > 0 && marketUp.Ask < 0.99 {
					return &TradeAction{
						Type:  "FORCE_CLOSE",
						Side:  domain.TokenTypeUp,
						Price: marketUp.Ask,
						Size:  s.Config.SizePerTrade,
					}
				}
			}
		}
		return nil
	}

	// [B] 正常逻辑
	// 风控对冲
	var forceActionSide domain.TokenType
	if netInv > s.Config.HedgeThreshold {
		forceActionSide = domain.TokenTypeDown
	} else if netInv < -s.Config.HedgeThreshold {
		forceActionSide = domain.TokenTypeUp
	}

	if forceActionSide != "" {
		targetBook := marketUp
		fairPrice := pricing.FinalFairUp // 使用未应用库存偏斜的价格
		if forceActionSide == domain.TokenTypeDown {
			targetBook = marketDown
			fairPrice = pricing.FinalFairDown
		}

		if targetBook.Ask > 0 && targetBook.Ask < fairPrice+0.03 {
			return &TradeAction{
				Type:  "TAKER_HEDGE",
				Side:  forceActionSide,
				Price: targetBook.Ask,
				Size:  s.Config.SizePerTrade * s.Config.HedgeSizeMultiplier,
			}
		}
	}

	// 交易逻辑
	allowTradeUp := !isReduceOnly || netInv < 0
	allowTradeDown := !isReduceOnly || netInv > 0

	targetUpBid := marketUp.Bid + 0.001
	targetDownBid := marketDown.Bid + 0.001

	// UP 方向
	if allowTradeUp && netInv < s.Config.StopQuoteThreshold {
		// Taker
		if marketUp.Ask > 0 && marketUp.Ask < resPriceUp-s.Config.BaseMinEdgeTaker {
			return &TradeAction{
				Type:  "TAKER",
				Side:  domain.TokenTypeUp,
				Price: marketUp.Ask,
				Size:  s.Config.SizePerTrade,
			}
		}
		// Maker
		if targetUpBid < resPriceUp-currentMakerEdge {
			return &TradeAction{
				Type:  "MAKER",
				Side:  domain.TokenTypeUp,
				Price: targetUpBid,
				Size:  s.Config.SizePerTrade,
			}
		}
	}

	// DOWN 方向
	if allowTradeDown && netInv > -s.Config.StopQuoteThreshold {
		// Taker
		if marketDown.Ask > 0 && marketDown.Ask < resPriceDown-s.Config.BaseMinEdgeTaker {
			return &TradeAction{
				Type:  "TAKER",
				Side:  domain.TokenTypeDown,
				Price: marketDown.Ask,
				Size:  s.Config.SizePerTrade,
			}
		}
		// Maker
		if targetDownBid < resPriceDown-currentMakerEdge {
			return &TradeAction{
				Type:  "MAKER",
				Side:  domain.TokenTypeDown,
				Price: targetDownBid,
				Size:  s.Config.SizePerTrade,
			}
		}
	}

	return nil
}

// checkMakerFills 检查 Maker 订单成交
func (s *Strategy) checkMakerFills(marketUp, marketDown *MarketData) {
	s.makerMu.Lock()
	defer s.makerMu.Unlock()

	// 检查 UP 方向
	for priceKey, orderID := range s.makerOrders[domain.TokenTypeUp] {
		price := parsePriceKey(priceKey)
		if price > 0 && marketUp.Ask > 0 && marketUp.Ask <= price {
			log.Infof("✅ [adaptive] MAKER 成交检测: Buy UP @ %.4f (OrderID: %s)", price, orderID)
			delete(s.makerOrders[domain.TokenTypeUp], priceKey)
		}
	}

	// 检查 DOWN 方向
	for priceKey, orderID := range s.makerOrders[domain.TokenTypeDown] {
		price := parsePriceKey(priceKey)
		if price > 0 && marketDown.Ask > 0 && marketDown.Ask <= price {
			log.Infof("✅ [adaptive] MAKER 成交检测: Buy DOWN @ %.4f (OrderID: %s)", price, orderID)
			delete(s.makerOrders[domain.TokenTypeDown], priceKey)
		}
	}
}

// parsePriceKey 解析价格键
func parsePriceKey(key string) float64 {
	if len(key) > 1 && key[0] == '@' {
		var price float64
		fmt.Sscanf(key[1:], "%f", &price)
		return price
	}
	return 0
}

// priceKey 生成价格键
func priceKey(price float64) string {
	return fmt.Sprintf("@%.4f", price)
}

// executeTrade 执行交易
func (s *Strategy) executeTrade(ctx context.Context, action *TradeAction, market *domain.Market, remaining float64) error {
	// 限流
	s.tradeMu.Lock()
	if !s.lastTradeTs.IsZero() && time.Since(s.lastTradeTs) < 200*time.Millisecond {
		s.tradeMu.Unlock()
		return nil
	}
	s.lastTradeTs = time.Now()
	s.tradeMu.Unlock()

	assetID := market.GetAssetID(action.Side)
	priceCents := int(action.Price*100 + 0.5)
	if priceCents <= 0 {
		return fmt.Errorf("无效价格: %.4f", action.Price)
	}

price := domain.PriceFromDecimal(action.Price)

	// Maker 订单处理
	if action.Type == "MAKER" {
		priceKey := priceKey(action.Price)

		// 检查是否已挂单
		s.makerMu.Lock()
		if _, exists := s.makerOrders[action.Side][priceKey]; exists {
			s.makerMu.Unlock()
			return nil // 已挂单，跳过
		}

		// 撤掉同方向的旧订单
		for oldPriceKey, oldOrderID := range s.makerOrders[action.Side] {
			log.Infof("❌ [adaptive] 撤单: %s @ %s (OrderID: %s)", action.Side, oldPriceKey, oldOrderID)
			_ = s.TradingService.CancelOrder(ctx, oldOrderID)
			delete(s.makerOrders[action.Side], oldPriceKey)
		}
		s.makerMu.Unlock()

		// 下新订单
		order := &domain.Order{
			MarketSlug: market.Slug,
			AssetID:    assetID,
			Side:       types.SideBuy,
			Price:      price,
			Size:       action.Size,
			TokenType:  action.Side,
			Status:     domain.OrderStatusPending,
			CreatedAt:  time.Now(),
			OrderType:  types.OrderTypeGTC,
		}

		placedOrder, err := s.TradingService.PlaceOrder(ctx, order)
		if err != nil {
			return fmt.Errorf("下单失败: %w", err)
		}

		// 记录 Maker 订单
		s.makerMu.Lock()
		s.makerOrders[action.Side][priceKey] = placedOrder.OrderID
		s.makerMu.Unlock()

		log.Infof("⚡ [adaptive] 挂单 [MAKER] Buy %s @ %.4f (Size: %.2f) | Rem: %.1fs", action.Side, action.Price, action.Size, remaining)
		return nil
	}

	// Taker 订单处理
	orderType := types.OrderTypeFAK
	if action.Type == "FORCE_CLOSE" || action.Type == "TAKER_HEDGE" {
		orderType = types.OrderTypeFAK
	}

	req := execution.MultiLegRequest{
		Name:       fmt.Sprintf("adaptive_%s", action.Type),
		MarketSlug: market.Slug,
		Legs: []execution.LegIntent{
			{
				Name:      "buy",
				AssetID:   assetID,
				TokenType: action.Side,
				Side:      types.SideBuy,
				Price:     price,
				Size:      action.Size,
				OrderType: orderType,
			},
		},
		Hedge: execution.AutoHedgeConfig{Enabled: false},
	}

	modeMap := map[string]string{
		"TAKER":        "吃单",
		"FORCE_CLOSE":  "强制纠偏",
		"TAKER_HEDGE":  "对冲",
	}

	_, err := s.TradingService.ExecuteMultiLeg(ctx, req)
	if err != nil {
		return fmt.Errorf("执行交易失败: %w", err)
	}

	log.Infof("⚡ [adaptive] %s [%s] Buy %s @ %.4f (Size: %.2f) | Rem: %.1fs",
		modeMap[action.Type], action.Type, action.Side, action.Price, action.Size, remaining)

	return nil
}

// logStatus 记录状态
func (s *Strategy) logStatus(pricing *Pricing, marketUp, marketDown *MarketData, netInv, remaining, fut, strikePrice float64, isForceClose, isReduceOnly bool, action *TradeAction) {
	mode := "NORMAL"
	if isForceClose {
		mode = "FORCE"
	} else if isReduceOnly {
		mode = "REDUCE"
	}

	// 计算允许交易条件和信号状态
	allowTradeUp := !isReduceOnly || netInv < 0
	allowTradeDown := !isReduceOnly || netInv > 0
	targetUpBid := marketUp.Bid + 0.001
	targetDownBid := marketDown.Bid + 0.001
	currentMakerEdge := s.getDynamicMakerEdge(remaining)

	isTakerUp := marketUp.Ask > 0 && marketUp.Ask < pricing.ResPriceUp-s.Config.BaseMinEdgeTaker
	isTakerDown := marketDown.Ask > 0 && marketDown.Ask < pricing.ResPriceDown-s.Config.BaseMinEdgeTaker
	isMakerUp := targetUpBid < pricing.ResPriceUp-currentMakerEdge
	isMakerDown := targetDownBid < pricing.ResPriceDown-currentMakerEdge

	log.Infof("[adaptive] UpBid:%.4f DownBid:%.4f UpAsk:%.4f DownAsk:%.4f | PriceUp:%.4f PriceDown:%.4f",
		marketUp.Bid, marketDown.Bid, marketUp.Ask, marketDown.Ask, pricing.ResPriceUp, pricing.ResPriceDown)
	log.Infof("[adaptive] AllowUp:%v AllowDown:%v | MakerUp:%v MakerDown:%v | TakerUp:%v TakerDown:%v",
		allowTradeUp, allowTradeDown, isMakerUp, isMakerDown, isTakerUp, isTakerDown)
	log.Infof("[adaptive] DeltaUp:%.4f DeltaDown:%.4f",
		targetUpBid-pricing.ResPriceUp, targetDownBid-pricing.ResPriceDown)
	log.Infof("[adaptive] [Rem:%.0fs] Fut:%.2f Strike:%.2f Delta:%.2f | FairUP:%.4f Skew:%.4f NetInv:%.2f | Mode: %s",
		remaining, fut, strikePrice, pricing.Delta, pricing.FinalFairUp, pricing.Skew, netInv, mode)
}

// abs 绝对值
func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// sqrt 平方根（包装 math.Sqrt）
func sqrt(x float64) float64 {
	return math.Sqrt(x)
}

