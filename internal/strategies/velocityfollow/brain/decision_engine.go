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
	Timestamp int64 // Unix 时间戳（毫秒）
	PriceCents int  // 价格（分）
}

// VelocitySample 速度样本（用于历史速度计算）
type VelocitySample struct {
	Timestamp int64   // Unix 时间戳（毫秒）
	Velocity  float64 // 速度（分/秒）
}

// VelocityState 速度状态
type VelocityState string

const (
	VelocityStateFast VelocityState = "fast"
	VelocityStateSlow VelocityState = "slow"
)

// DecisionEngine 决策引擎
type DecisionEngine struct {
	config         ConfigInterface
	tradingService *services.TradingService

	mu sync.RWMutex
	// 速度计算样本队列
	samples map[domain.TokenType][]PriceSample // UP/DOWN -> samples
	// 历史速度样本（用于判断速度快慢）
	velocityHistory map[domain.TokenType][]VelocitySample // UP/DOWN -> velocity samples
}

// NewDecisionEngine 创建新的决策引擎
func NewDecisionEngine(cfg ConfigInterface) *DecisionEngine {
	return &DecisionEngine{
		config:          cfg,
		samples:         make(map[domain.TokenType][]PriceSample),
		velocityHistory: make(map[domain.TokenType][]VelocitySample),
	}
}

// SetTradingService 设置 TradingService（延迟注入）
func (de *DecisionEngine) SetTradingService(ts *services.TradingService) {
	de.tradingService = ts
}

// OnCycle 周期切换回调
func (de *DecisionEngine) OnCycle(ctx context.Context, oldMarket *domain.Market, newMarket *domain.Market) {
	de.mu.Lock()
	defer de.mu.Unlock()

	// 清空速度样本队列和历史速度
	de.samples[domain.TokenTypeUp] = nil
	de.samples[domain.TokenTypeDown] = nil
	de.velocityHistory[domain.TokenTypeUp] = nil
	de.velocityHistory[domain.TokenTypeDown] = nil
}

// CalculateVelocityAndDirection 计算速度并选择方向
func (de *DecisionEngine) CalculateVelocityAndDirection(ctx context.Context, e *events.PriceChangedEvent) (domain.TokenType, float64, error) {
	if e == nil || e.Market == nil || de.tradingService == nil {
		return "", 0, nil
	}

	de.mu.Lock()
	defer de.mu.Unlock()

	now := time.Now().UnixMilli()

	// 从 TradingService 获取当前价格
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

	// 更新价格样本
	de.updateSamples(domain.TokenTypeUp, e.Market.YesAssetID, yesPrice, now)
	de.updateSamples(domain.TokenTypeDown, e.Market.NoAssetID, noPrice, now)

	// 计算 UP 和 DOWN 的速度
	upVelocity, upMove := de.calculateVelocity(domain.TokenTypeUp, now)
	downVelocity, downMove := de.calculateVelocity(domain.TokenTypeDown, now)

	// 更新速度历史
	de.updateVelocityHistory(domain.TokenTypeUp, upVelocity, now)
	de.updateVelocityHistory(domain.TokenTypeDown, downVelocity, now)

	deLog.Debugf("📈 [DecisionEngine] 速度计算: UP=%.3f (move=%d) DOWN=%.3f (move=%d)",
		upVelocity, upMove, downVelocity, downMove)

	// 选择方向
	var winner domain.TokenType
	var winnerVelocity float64

	// 检查 UP 是否满足条件
	upSatisfied := upMove >= de.config.GetMinMoveCents() && 
		upVelocity >= de.config.GetMinVelocityCentsPerSec()

	// 检查 DOWN 是否满足条件
	downSatisfied := downMove >= de.config.GetMinMoveCents() && 
		downVelocity >= de.config.GetMinVelocityCentsPerSec()

	if !upSatisfied && !downSatisfied {
		return "", 0, nil
	}

	if upSatisfied && downSatisfied {
		// 两侧都满足，选择速度更快的一侧
		if upVelocity > downVelocity {
			winner = domain.TokenTypeUp
			winnerVelocity = upVelocity
		} else if downVelocity > upVelocity {
			winner = domain.TokenTypeDown
			winnerVelocity = downVelocity
		} else {
			// 速度相同，使用价格优先选择（如果启用）
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
					// 都不满足价格阈值，选择价格更高的一侧
					if upPriceCents > downPriceCents {
						winner = domain.TokenTypeUp
						winnerVelocity = upVelocity
					} else {
						winner = domain.TokenTypeDown
						winnerVelocity = downVelocity
					}
				}
			} else {
				// 不启用价格优先，选择 UP（默认）
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

// UpdateSamplesFromPriceEvent 从价格事件更新样本（不触发决策，只更新数据）
func (de *DecisionEngine) UpdateSamplesFromPriceEvent(ctx context.Context, e *events.PriceChangedEvent) {
	if e == nil || e.Market == nil || de.tradingService == nil {
		return
	}

	de.mu.Lock()
	defer de.mu.Unlock()

	now := time.Now().UnixMilli()

	// 如果事件包含当前 token 的价格，直接使用
	if e.TokenType == domain.TokenTypeUp && e.Market.YesAssetID != "" {
		de.updateSamples(domain.TokenTypeUp, e.Market.YesAssetID, e.NewPrice, now)
	} else if e.TokenType == domain.TokenTypeDown && e.Market.NoAssetID != "" {
		de.updateSamples(domain.TokenTypeDown, e.Market.NoAssetID, e.NewPrice, now)
	} else {
		// 如果事件不包含完整信息，尝试获取两个价格
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

// GetCurrentVelocity 获取当前速度信息（不触发决策，用于 Dashboard 显示）
func (de *DecisionEngine) GetCurrentVelocity(ctx context.Context, market *domain.Market) (upVelocity, downVelocity float64, upMove, downMove int, direction string, err error) {
	if market == nil {
		return 0, 0, 0, 0, "", nil
	}

	de.mu.RLock()
	defer de.mu.RUnlock()

	now := time.Now().UnixMilli()

	// 直接计算速度（不更新样本，因为样本应该已经通过 UpdateSamplesFromPriceEvent 更新）
	upVelocity, upMove = de.calculateVelocity(domain.TokenTypeUp, now)
	downVelocity, downMove = de.calculateVelocity(domain.TokenTypeDown, now)

	// 确定方向（如果满足条件）
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

// updateSamples 更新价格样本
func (de *DecisionEngine) updateSamples(tokenType domain.TokenType, assetID string, price domain.Price, timestamp int64) {
	if price.Pips <= 0 {
		return
	}

	priceCents := price.ToCents()
	samples := de.samples[tokenType]

	// 添加新样本
	samples = append(samples, PriceSample{
		Timestamp:  timestamp,
		PriceCents: priceCents,
	})

	// 清理过期样本（超过 windowSeconds）
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

// calculateVelocity 计算速度
func (de *DecisionEngine) calculateVelocity(tokenType domain.TokenType, now int64) (float64, int) {
	samples := de.samples[tokenType]
	if len(samples) < 2 {
		return 0, 0
	}

	// 获取窗口内的最早和最新样本
	windowMs := int64(de.config.GetWindowSeconds() * 1000)
	cutoff := now - windowMs

	oldestSample := samples[0]
	newestSample := samples[len(samples)-1]

	// 确保在窗口内
	if oldestSample.Timestamp < cutoff {
		// 找到窗口内的最早样本
		for _, s := range samples {
			if s.Timestamp >= cutoff {
				oldestSample = s
				break
			}
		}
	}

	// 计算位移（分）
	move := newestSample.PriceCents - oldestSample.PriceCents

	// 计算时间窗口（秒）
	timeWindow := float64(newestSample.Timestamp-oldestSample.Timestamp) / 1000.0
	if timeWindow <= 0 {
		return 0, move
	}

	// 计算速度（分/秒）
	velocity := float64(move) / timeWindow

	return velocity, move
}

// updateVelocityHistory 更新速度历史
func (de *DecisionEngine) updateVelocityHistory(tokenType domain.TokenType, velocity float64, timestamp int64) {
	if velocity <= 0 {
		return
	}

	history := de.velocityHistory[tokenType]
	history = append(history, VelocitySample{
		Timestamp: timestamp,
		Velocity:  velocity,
	})

	// 清理过期样本（超过历史窗口）
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

// classifyVelocityState 判断速度状态（快速/慢速）
func (de *DecisionEngine) classifyVelocityState(tokenType domain.TokenType, currentVelocity float64, now int64) VelocityState {
	if currentVelocity <= 0 {
		return VelocityStateSlow
	}

	// 1. 检查是否超过配置阈值
	threshold := de.config.GetFastVelocityThresholdCentsPerSec()
	if currentVelocity >= threshold {
		// 2. 检查是否超过历史平均速度的倍数
		avgVelocity := de.calculateAverageVelocity(tokenType, now)
		multiplier := de.config.GetVelocityComparisonMultiplier()
		if avgVelocity > 0 && currentVelocity >= avgVelocity*multiplier {
			return VelocityStateFast
		}
		// 即使历史平均不够，如果超过阈值也认为是快速
		if currentVelocity >= threshold*1.2 { // 额外20%缓冲
			return VelocityStateFast
		}
	}

	return VelocityStateSlow
}

// calculateAverageVelocity 计算历史平均速度
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

// Evaluate 评估是否应该交易（根据速度状态选择策略）
func (de *DecisionEngine) Evaluate(
	ctx context.Context,
	e *events.PriceChangedEvent,
	direction domain.TokenType,
	velocity float64,
	positionState *PositionState,
) (bool, string, domain.Price, domain.Price, float64, float64) {
	if e == nil || e.Market == nil {
		return false, "市场信息为空", domain.Price{}, domain.Price{}, 0, 0
	}

	if de.tradingService == nil {
		return false, "TradingService 未初始化", domain.Price{}, domain.Price{}, 0, 0
	}

	// 判断速度状态
	now := time.Now().UnixMilli()
	velocityState := de.classifyVelocityState(direction, velocity, now)

	deLog.Debugf("📊 [DecisionEngine] 速度状态: direction=%s velocity=%.3f state=%s",
		direction, velocity, velocityState)

	// 根据速度状态选择策略
	if velocityState == VelocityStateFast {
		return de.evaluateFastStrategy(ctx, e, direction, positionState)
	} else {
		return de.evaluateSlowStrategy(ctx, e, direction, positionState)
	}
}

// evaluateFastStrategy 快速变化策略：优先买价格高且上涨趋势强的一侧
func (de *DecisionEngine) evaluateFastStrategy(
	ctx context.Context,
	e *events.PriceChangedEvent,
	direction domain.TokenType,
	positionState *PositionState,
) (bool, string, domain.Price, domain.Price, float64, float64) {
	// 获取当前价格
	_, yesAsk, _, noAsk, _, err := de.tradingService.GetTopOfBook(ctx, e.Market)
	if err != nil {
		return false, "获取订单簿价格失败: " + err.Error(), domain.Price{}, domain.Price{}, 0, 0
	}

	// 识别价格高且上涨趋势强的一侧（往100方向）
	// 选择价格高且上涨的一侧作为主leg
	var entryPrice domain.Price
	var hedgePrice domain.Price

	if direction == domain.TokenTypeUp {
		// UP方向：优先买UP（价格高且上涨）
		entryPrice = yesAsk
	} else {
		// DOWN方向：优先买DOWN（价格高且上涨）
		entryPrice = noAsk
	}

	entryPriceCents := entryPrice.ToCents()

	// 检查 Entry 价格区间
	if de.config.GetMinEntryPriceCents() > 0 && entryPriceCents < de.config.GetMinEntryPriceCents() {
		return false, "Entry 价格过低", domain.Price{}, domain.Price{}, 0, 0
	}
	if de.config.GetMaxEntryPriceCents() > 0 && entryPriceCents > de.config.GetMaxEntryPriceCents() {
		return false, "Entry 价格过高", domain.Price{}, domain.Price{}, 0, 0
	}

	// 计算 Hedge 价格（反方向限价买单）
	hedgePriceCents := 100 - entryPriceCents - de.config.GetHedgeOffsetCents()
	if hedgePriceCents < 0 {
		hedgePriceCents = 0
	}
	hedgePrice = domain.PriceFromDecimal(float64(hedgePriceCents) / 100.0)

	// 检查总成本
	totalCostCents := entryPriceCents + hedgePriceCents
	if totalCostCents > 100 {
		return false, "总成本超过 100c", domain.Price{}, domain.Price{}, 0, 0
	}

	// 计算订单数量
	entrySize := de.config.GetOrderSize()
	hedgeSize := de.config.GetHedgeOrderSize()
	if hedgeSize <= 0 {
		hedgeSize = entrySize
	}

	// 确保 Entry 和 Hedge 数量相等（完全对冲）
	minSize := math.Min(entrySize, hedgeSize)
	entrySize = minSize
	hedgeSize = minSize

	return true, "快速策略：主leg优先买高价上涨侧", entryPrice, hedgePrice, entrySize, hedgeSize
}

// evaluateSlowStrategy 慢速变化策略：两边挂限价买单，动态定价
func (de *DecisionEngine) evaluateSlowStrategy(
	ctx context.Context,
	e *events.PriceChangedEvent,
	direction domain.TokenType,
	positionState *PositionState,
) (bool, string, domain.Price, domain.Price, float64, float64) {
	// 获取当前价格
	yesBid, yesAsk, noBid, noAsk, _, err := de.tradingService.GetTopOfBook(ctx, e.Market)
	if err != nil {
		return false, "获取订单簿价格失败: " + err.Error(), domain.Price{}, domain.Price{}, 0, 0
	}

	yesAskCents := yesAsk.ToCents()
	noAskCents := noAsk.ToCents()

	// 检查价差
	spreadCents := math.Abs(float64(yesAskCents + noAskCents - 100))
	if spreadCents > float64(de.config.GetSlowStrategyMaxSpreadCents()) {
		return false, "价差过大", domain.Price{}, domain.Price{}, 0, 0
	}

	// 动态计算两侧价格，确保总成本 <= 100c
	// 价格激进程度：0-1，越接近1越接近ask价
	aggressiveness := de.config.GetSlowStrategyPriceAggressiveness()
	if aggressiveness <= 0 {
		aggressiveness = 0.8 // 默认0.8
	}
	if aggressiveness > 1.0 {
		aggressiveness = 1.0
	}

	// 计算目标总成本（留一些利润空间）
	targetTotalCents := 100 - de.config.GetHedgeOffsetCents()
	if targetTotalCents < 95 {
		targetTotalCents = 95 // 至少留5分利润空间
	}

	// 根据方向选择entry和hedge
	var entryPrice domain.Price
	var hedgePrice domain.Price

	if direction == domain.TokenTypeUp {
		// UP方向：UP作为entry，DOWN作为hedge
		// 动态计算价格：更接近ask价以提高成交概率
		// UP价格：在bid和ask之间，根据激进程度调整
		yesBidCents := yesBid.ToCents()
		entryPriceCents := int(float64(yesBidCents) + float64(yesAskCents-yesBidCents)*aggressiveness)
		if entryPriceCents > yesAskCents {
			entryPriceCents = yesAskCents
		}

		// DOWN价格：确保总成本 <= targetTotalCents
		hedgePriceCents := targetTotalCents - entryPriceCents
		if hedgePriceCents < 0 {
			hedgePriceCents = 0
		}
		// 如果hedge价格高于ask，调整为ask价
		if hedgePriceCents > noAskCents {
			hedgePriceCents = noAskCents
			// 重新调整entry价格
			entryPriceCents = targetTotalCents - hedgePriceCents
			if entryPriceCents < yesBidCents {
				entryPriceCents = yesBidCents
			}
		}

		entryPrice = domain.PriceFromDecimal(float64(entryPriceCents) / 100.0)
		hedgePrice = domain.PriceFromDecimal(float64(hedgePriceCents) / 100.0)
	} else {
		// DOWN方向：DOWN作为entry，UP作为hedge
		// 动态计算价格
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

	// 检查价格区间
	if de.config.GetMinEntryPriceCents() > 0 && entryPriceCents < de.config.GetMinEntryPriceCents() {
		return false, "Entry 价格过低", domain.Price{}, domain.Price{}, 0, 0
	}
	if de.config.GetMaxEntryPriceCents() > 0 && entryPriceCents > de.config.GetMaxEntryPriceCents() {
		return false, "Entry 价格过高", domain.Price{}, domain.Price{}, 0, 0
	}

	// 检查总成本
	totalCostCents := entryPriceCents + hedgePriceCents
	if totalCostCents > 100 {
		return false, "总成本超过 100c", domain.Price{}, domain.Price{}, 0, 0
	}

	// 计算订单数量
	entrySize := de.config.GetOrderSize()
	hedgeSize := de.config.GetHedgeOrderSize()
	if hedgeSize <= 0 {
		hedgeSize = entrySize
	}

	// 确保 Entry 和 Hedge 数量相等（完全对冲）
	minSize := math.Min(entrySize, hedgeSize)
	entrySize = minSize
	hedgeSize = minSize

	return true, "慢速策略：两边挂限价买单", entryPrice, hedgePrice, entrySize, hedgeSize
}
