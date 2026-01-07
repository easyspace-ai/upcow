package brain

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/services"
	"github.com/sirupsen/logrus"
)

var deLog = logrus.WithField("module", "decision_engine")

// PriceSample 价格样本
type PriceSample struct {
	Timestamp  int64
	PriceCents int
}

// VelocitySample 速度样本
type VelocitySample struct {
	Timestamp int64
	Velocity  float64
}

type VelocityState string

const (
	VelocityStateFast VelocityState = "fast"
	VelocityStateSlow VelocityState = "slow"
)

type DecisionEngine struct {
	config         ConfigInterface
	tradingService *services.TradingService

	mu             sync.RWMutex
	samples        map[domain.TokenType][]PriceSample
	velocityHistory map[domain.TokenType][]VelocitySample
}

func NewDecisionEngine(cfg ConfigInterface) *DecisionEngine {
	return &DecisionEngine{
		config:          cfg,
		samples:         make(map[domain.TokenType][]PriceSample),
		velocityHistory: make(map[domain.TokenType][]VelocitySample),
	}
}

func (de *DecisionEngine) SetTradingService(ts *services.TradingService) {
	de.tradingService = ts
}

func (de *DecisionEngine) OnCycle(ctx context.Context, oldMarket *domain.Market, newMarket *domain.Market) {
	_ = ctx
	de.mu.Lock()
	defer de.mu.Unlock()
	de.samples[domain.TokenTypeUp] = nil
	de.samples[domain.TokenTypeDown] = nil
	de.velocityHistory[domain.TokenTypeUp] = nil
	de.velocityHistory[domain.TokenTypeDown] = nil
}

func (de *DecisionEngine) CalculateVelocityAndDirection(ctx context.Context, e *events.PriceChangedEvent) (domain.TokenType, float64, error) {
	if e == nil || e.Market == nil || de.tradingService == nil {
		return "", 0, nil
	}

	de.mu.Lock()
	defer de.mu.Unlock()

	now := time.Now().UnixMilli()
	_, yesAsk, err := de.tradingService.GetBestPrice(ctx, e.Market.YesAssetID)
	if err != nil {
		return "", 0, err
	}
	_, noAsk, err := de.tradingService.GetBestPrice(ctx, e.Market.NoAssetID)
	if err != nil {
		return "", 0, err
	}

	yesPrice := domain.PriceFromDecimal(yesAsk)
	noPrice := domain.PriceFromDecimal(noAsk)
	de.updateSamples(domain.TokenTypeUp, e.Market.YesAssetID, yesPrice, now)
	de.updateSamples(domain.TokenTypeDown, e.Market.NoAssetID, noPrice, now)

	upVelocity, upMove := de.calculateVelocity(domain.TokenTypeUp, now)
	downVelocity, downMove := de.calculateVelocity(domain.TokenTypeDown, now)

	de.updateVelocityHistory(domain.TokenTypeUp, upVelocity, now)
	de.updateVelocityHistory(domain.TokenTypeDown, downVelocity, now)

	deLog.Debugf("📈 [DecisionEngine] 速度计算: UP=%.3f (move=%d) DOWN=%.3f (move=%d)",
		upVelocity, upMove, downVelocity, downMove)

	var winner domain.TokenType
	var winnerVelocity float64

	upSatisfied := upMove >= de.config.GetMinMoveCents() &&
		upVelocity >= de.config.GetMinVelocityCentsPerSec()
	downSatisfied := downMove >= de.config.GetMinMoveCents() &&
		downVelocity >= de.config.GetMinVelocityCentsPerSec()

	if !upSatisfied && !downSatisfied {
		return "", 0, nil
	}

	if upSatisfied && downSatisfied {
		if upVelocity > downVelocity {
			winner = domain.TokenTypeUp
			winnerVelocity = upVelocity
		} else if downVelocity > upVelocity {
			winner = domain.TokenTypeDown
			winnerVelocity = downVelocity
		} else {
			if de.config.GetPreferHigherPrice() {
				upPriceCents := yesPrice.ToCents()
				downPriceCents := noPrice.ToCents()
				if upPriceCents >= de.config.GetMinPreferredPriceCents() &&
					upPriceCents > downPriceCents {
					winner = domain.TokenTypeUp
					winnerVelocity = upVelocity
				} else if downPriceCents >= de.config.GetMinPreferredPriceCents() {
					winner = domain.TokenTypeDown
					winnerVelocity = downVelocity
				} else {
					if upPriceCents > downPriceCents {
						winner = domain.TokenTypeUp
						winnerVelocity = upVelocity
					} else {
						winner = domain.TokenTypeDown
						winnerVelocity = downVelocity
					}
				}
			} else {
				winner = domain.TokenTypeUp
				winnerVelocity = upVelocity
			}
		}
	} else if upSatisfied {
		winner = domain.TokenTypeUp
		winnerVelocity = upVelocity
	} else {
		winner = domain.TokenTypeDown
		winnerVelocity = downVelocity
	}

	return winner, winnerVelocity, nil
}

func (de *DecisionEngine) UpdateSamplesFromPriceEvent(ctx context.Context, e *events.PriceChangedEvent) {
	if e == nil || e.Market == nil || de.tradingService == nil {
		return
	}
	de.mu.Lock()
	defer de.mu.Unlock()
	now := time.Now().UnixMilli()

	if e.TokenType == domain.TokenTypeUp && e.Market.YesAssetID != "" {
		de.updateSamples(domain.TokenTypeUp, e.Market.YesAssetID, e.NewPrice, now)
	} else if e.TokenType == domain.TokenTypeDown && e.Market.NoAssetID != "" {
		de.updateSamples(domain.TokenTypeDown, e.Market.NoAssetID, e.NewPrice, now)
	} else {
		_, yesAsk, err := de.tradingService.GetBestPrice(ctx, e.Market.YesAssetID)
		if err == nil {
			yesPrice := domain.PriceFromDecimal(yesAsk)
			de.updateSamples(domain.TokenTypeUp, e.Market.YesAssetID, yesPrice, now)
		}
		_, noAsk, err := de.tradingService.GetBestPrice(ctx, e.Market.NoAssetID)
		if err == nil {
			noPrice := domain.PriceFromDecimal(noAsk)
			de.updateSamples(domain.TokenTypeDown, e.Market.NoAssetID, noPrice, now)
		}
	}
}

func (de *DecisionEngine) GetCurrentVelocity(ctx context.Context, market *domain.Market) (upVelocity, downVelocity float64, upMove, downMove int, direction string, err error) {
	_ = ctx
	if market == nil {
		return 0, 0, 0, 0, "", nil
	}
	de.mu.RLock()
	defer de.mu.RUnlock()
	now := time.Now().UnixMilli()
	upVelocity, upMove = de.calculateVelocity(domain.TokenTypeUp, now)
	downVelocity, downMove = de.calculateVelocity(domain.TokenTypeDown, now)

	upSatisfied := upMove >= de.config.GetMinMoveCents() &&
		upVelocity >= de.config.GetMinVelocityCentsPerSec()
	downSatisfied := downMove >= de.config.GetMinMoveCents() &&
		downVelocity >= de.config.GetMinVelocityCentsPerSec()

	if upSatisfied && downSatisfied {
		if upVelocity > downVelocity {
			direction = string(domain.TokenTypeUp)
		} else {
			direction = string(domain.TokenTypeDown)
		}
	} else if upSatisfied {
		direction = string(domain.TokenTypeUp)
	} else if downSatisfied {
		direction = string(domain.TokenTypeDown)
	}
	return upVelocity, downVelocity, upMove, downMove, direction, nil
}

func (de *DecisionEngine) updateSamples(tokenType domain.TokenType, assetID string, price domain.Price, timestamp int64) {
	_ = assetID
	if price.Pips <= 0 {
		return
	}
	priceCents := price.ToCents()
	samples := de.samples[tokenType]
	samples = append(samples, PriceSample{Timestamp: timestamp, PriceCents: priceCents})

	windowMs := int64(de.config.GetWindowSeconds() * 1000)
	cutoff := timestamp - windowMs
	validSamples := make([]PriceSample, 0, len(samples))
	for _, s := range samples {
		if s.Timestamp >= cutoff {
			validSamples = append(validSamples, s)
		}
	}
	de.samples[tokenType] = validSamples
}

func (de *DecisionEngine) calculateVelocity(tokenType domain.TokenType, now int64) (float64, int) {
	samples := de.samples[tokenType]
	if len(samples) < 2 {
		return 0, 0
	}

	windowMs := int64(de.config.GetWindowSeconds() * 1000)
	cutoff := now - windowMs
	oldestSample := samples[0]
	newestSample := samples[len(samples)-1]
	if oldestSample.Timestamp < cutoff {
		for _, s := range samples {
			if s.Timestamp >= cutoff {
				oldestSample = s
				break
			}
		}
	}

	move := newestSample.PriceCents - oldestSample.PriceCents
	timeWindow := float64(newestSample.Timestamp-oldestSample.Timestamp) / 1000.0
	if timeWindow <= 0 {
		return 0, move
	}
	velocity := float64(move) / timeWindow
	return velocity, move
}

func (de *DecisionEngine) updateVelocityHistory(tokenType domain.TokenType, velocity float64, timestamp int64) {
	if velocity <= 0 {
		return
	}
	history := de.velocityHistory[tokenType]
	history = append(history, VelocitySample{Timestamp: timestamp, Velocity: velocity})
	historyWindowMs := int64(de.config.GetVelocityHistoryWindowSeconds() * 1000)
	cutoff := timestamp - historyWindowMs
	validHistory := make([]VelocitySample, 0, len(history))
	for _, s := range history {
		if s.Timestamp >= cutoff {
			validHistory = append(validHistory, s)
		}
	}
	de.velocityHistory[tokenType] = validHistory
}

func (de *DecisionEngine) classifyVelocityState(tokenType domain.TokenType, currentVelocity float64, now int64) VelocityState {
	_ = tokenType
	if currentVelocity <= 0 {
		return VelocityStateSlow
	}
	threshold := de.config.GetFastVelocityThresholdCentsPerSec()
	if currentVelocity >= threshold {
		avgVelocity := de.calculateAverageVelocity(tokenType, now)
		multiplier := de.config.GetVelocityComparisonMultiplier()
		if avgVelocity > 0 && currentVelocity >= avgVelocity*multiplier {
			return VelocityStateFast
		}
		if currentVelocity >= threshold*1.2 {
			return VelocityStateFast
		}
	}
	return VelocityStateSlow
}

func (de *DecisionEngine) calculateAverageVelocity(tokenType domain.TokenType, now int64) float64 {
	history := de.velocityHistory[tokenType]
	if len(history) == 0 {
		return 0
	}
	historyWindowMs := int64(de.config.GetVelocityHistoryWindowSeconds() * 1000)
	cutoff := now - historyWindowMs
	var sum float64
	var count int
	for _, s := range history {
		if s.Timestamp >= cutoff && s.Velocity > 0 {
			sum += s.Velocity
			count++
		}
	}
	if count == 0 {
		return 0
	}
	return sum / float64(count)
}

func (de *DecisionEngine) Evaluate(
	ctx context.Context,
	e *events.PriceChangedEvent,
	direction domain.TokenType,
	velocity float64,
	positionState *PositionState,
) (bool, string, domain.Price, domain.Price, float64, float64) {
	_ = positionState
	if e == nil || e.Market == nil {
		return false, "市场信息为空", domain.Price{}, domain.Price{}, 0, 0
	}
	if de.tradingService == nil {
		return false, "TradingService 未初始化", domain.Price{}, domain.Price{}, 0, 0
	}

	now := time.Now().UnixMilli()
	velocityState := de.classifyVelocityState(direction, velocity, now)
	deLog.Debugf("📊 [DecisionEngine] 速度状态: direction=%s velocity=%.3f state=%s", direction, velocity, velocityState)

	if velocityState == VelocityStateFast {
		return de.evaluateFastStrategy(ctx, e, direction, positionState)
	}
	return de.evaluateSlowStrategy(ctx, e, direction, positionState)
}

func (de *DecisionEngine) evaluateFastStrategy(
	ctx context.Context,
	e *events.PriceChangedEvent,
	direction domain.TokenType,
	positionState *PositionState,
) (bool, string, domain.Price, domain.Price, float64, float64) {
	_ = positionState
	_, yesAsk, _, noAsk, _, err := de.tradingService.GetTopOfBook(ctx, e.Market)
	if err != nil {
		return false, "获取订单簿价格失败: " + err.Error(), domain.Price{}, domain.Price{}, 0, 0
	}

	var entryPrice domain.Price
	var hedgePrice domain.Price
	if direction == domain.TokenTypeUp {
		entryPrice = yesAsk
	} else {
		entryPrice = noAsk
	}
	entryPriceCents := entryPrice.ToCents()
	if de.config.GetMinEntryPriceCents() > 0 && entryPriceCents < de.config.GetMinEntryPriceCents() {
		return false, "Entry 价格过低", domain.Price{}, domain.Price{}, 0, 0
	}
	if de.config.GetMaxEntryPriceCents() > 0 && entryPriceCents > de.config.GetMaxEntryPriceCents() {
		return false, "Entry 价格过高", domain.Price{}, domain.Price{}, 0, 0
	}

	hedgePriceCents := 100 - entryPriceCents - de.config.GetHedgeOffsetCents()
	if hedgePriceCents < 0 {
		hedgePriceCents = 0
	}
	hedgePrice = domain.PriceFromDecimal(float64(hedgePriceCents) / 100.0)
	totalCostCents := entryPriceCents + hedgePriceCents
	if totalCostCents > 100 {
		return false, "总成本超过 100c", domain.Price{}, domain.Price{}, 0, 0
	}

	entrySize := de.config.GetOrderSize()
	hedgeSize := de.config.GetHedgeOrderSize()
	if hedgeSize <= 0 {
		hedgeSize = entrySize
	}
	minSize := math.Min(entrySize, hedgeSize)
	entrySize = minSize
	hedgeSize = minSize
	return true, "快速策略：主leg优先买高价上涨侧", entryPrice, hedgePrice, entrySize, hedgeSize
}

func (de *DecisionEngine) evaluateSlowStrategy(
	ctx context.Context,
	e *events.PriceChangedEvent,
	direction domain.TokenType,
	positionState *PositionState,
) (bool, string, domain.Price, domain.Price, float64, float64) {
	_ = positionState
	yesBid, yesAsk, noBid, noAsk, _, err := de.tradingService.GetTopOfBook(ctx, e.Market)
	if err != nil {
		return false, "获取订单簿价格失败: " + err.Error(), domain.Price{}, domain.Price{}, 0, 0
	}

	yesAskCents := yesAsk.ToCents()
	noAskCents := noAsk.ToCents()
	spreadCents := math.Abs(float64(yesAskCents + noAskCents - 100))
	if spreadCents > float64(de.config.GetSlowStrategyMaxSpreadCents()) {
		return false, "价差过大", domain.Price{}, domain.Price{}, 0, 0
	}

	aggressiveness := de.config.GetSlowStrategyPriceAggressiveness()
	if aggressiveness <= 0 {
		aggressiveness = 0.8
	}
	if aggressiveness > 1.0 {
		aggressiveness = 1.0
	}

	targetTotalCents := 100 - de.config.GetHedgeOffsetCents()
	if targetTotalCents < 95 {
		targetTotalCents = 95
	}

	var entryPrice domain.Price
	var hedgePrice domain.Price

	if direction == domain.TokenTypeUp {
		yesBidCents := yesBid.ToCents()
		entryPriceCents := int(float64(yesBidCents) + float64(yesAskCents-yesBidCents)*aggressiveness)
		if entryPriceCents > yesAskCents {
			entryPriceCents = yesAskCents
		}
		hedgePriceCents := targetTotalCents - entryPriceCents
		if hedgePriceCents < 0 {
			hedgePriceCents = 0
		}
		if hedgePriceCents > noAskCents {
			hedgePriceCents = noAskCents
			entryPriceCents = targetTotalCents - hedgePriceCents
			if entryPriceCents < yesBidCents {
				entryPriceCents = yesBidCents
			}
		}
		entryPrice = domain.PriceFromDecimal(float64(entryPriceCents) / 100.0)
		hedgePrice = domain.PriceFromDecimal(float64(hedgePriceCents) / 100.0)
	} else {
		noBidCents := noBid.ToCents()
		entryPriceCents := int(float64(noBidCents) + float64(noAskCents-noBidCents)*aggressiveness)
		if entryPriceCents > noAskCents {
			entryPriceCents = noAskCents
		}
		hedgePriceCents := targetTotalCents - entryPriceCents
		if hedgePriceCents < 0 {
			hedgePriceCents = 0
		}
		if hedgePriceCents > yesAskCents {
			hedgePriceCents = yesAskCents
			entryPriceCents = targetTotalCents - hedgePriceCents
			if entryPriceCents < noBidCents {
				entryPriceCents = noBidCents
			}
		}
		entryPrice = domain.PriceFromDecimal(float64(entryPriceCents) / 100.0)
		hedgePrice = domain.PriceFromDecimal(float64(hedgePriceCents) / 100.0)
	}

	entryPriceCents := entryPrice.ToCents()
	hedgePriceCents := hedgePrice.ToCents()
	if de.config.GetMinEntryPriceCents() > 0 && entryPriceCents < de.config.GetMinEntryPriceCents() {
		return false, "Entry 价格过低", domain.Price{}, domain.Price{}, 0, 0
	}
	if de.config.GetMaxEntryPriceCents() > 0 && entryPriceCents > de.config.GetMaxEntryPriceCents() {
		return false, "Entry 价格过高", domain.Price{}, domain.Price{}, 0, 0
	}
	totalCostCents := entryPriceCents + hedgePriceCents
	if totalCostCents > 100 {
		return false, "总成本超过 100c", domain.Price{}, domain.Price{}, 0, 0
	}

	entrySize := de.config.GetOrderSize()
	hedgeSize := de.config.GetHedgeOrderSize()
	if hedgeSize <= 0 {
		hedgeSize = entrySize
	}
	minSize := math.Min(entrySize, hedgeSize)
	entrySize = minSize
	hedgeSize = minSize
	return true, "慢速策略：两边挂限价买单", entryPrice, hedgePrice, entrySize, hedgeSize
}

