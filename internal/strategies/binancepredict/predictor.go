package binancepredict

import (
	"math"
	"time"

	"github.com/betbot/gobet/internal/services"
)

// PredictionDirection 预测方向
type PredictionDirection string

const (
	DirectionUp      PredictionDirection = "UP"
	DirectionDown    PredictionDirection = "DOWN"
	DirectionNeutral PredictionDirection = "NEUTRAL"
)

// Predictor Binance 预测模块
type Predictor struct {
	binanceKlines *services.BinanceFuturesKlines
	config        Config

	lastPredictionAt time.Time
	lastDirection    PredictionDirection
}

// NewPredictor 创建新的预测器
func NewPredictor(binanceKlines *services.BinanceFuturesKlines, config Config) *Predictor {
	return &Predictor{
		binanceKlines: binanceKlines,
		config:        config,
	}
}

// Predict 预测 BTC 涨跌方向
// 返回：UP（预测上涨，买入 UP）、DOWN（预测下跌，买入 DOWN）、NEUTRAL（无明确方向）
func (p *Predictor) Predict(now time.Time) (PredictionDirection, string) {
	// 检查冷却时间
	if !p.lastPredictionAt.IsZero() {
		cooldown := time.Duration(p.config.PredictionCooldownMs) * time.Millisecond
		if now.Sub(p.lastPredictionAt) < cooldown {
			return p.lastDirection, "cooldown"
		}
	}

	// 获取当前和过去的 K 线
	currentKline, okCurrent := p.binanceKlines.Latest("1s")
	if !okCurrent || currentKline.Close <= 0 {
		return DirectionNeutral, "no_current_kline"
	}

	windowMs := int64(p.config.PredictionWindowSeconds) * 1000
	pastKline, okPast := p.binanceKlines.NearestAtOrBefore("1s", now.UnixMilli()-windowMs)
	if !okPast || pastKline.Close <= 0 {
		return DirectionNeutral, "no_past_kline"
	}

	// 计算价格变化率
	priceChange := (currentKline.Close - pastKline.Close) / pastKline.Close
	priceChangeBps := int(math.Abs(priceChange) * 10000)

	// 检查是否达到最小变化阈值
	if priceChangeBps < p.config.MinPriceChangeBps {
		p.lastPredictionAt = now
		p.lastDirection = DirectionNeutral
		return DirectionNeutral, "change_too_small"
	}

	// 确定方向
	var direction PredictionDirection
	var reason string
	if priceChange > 0 {
		direction = DirectionUp
		reason = "price_up"
	} else {
		direction = DirectionDown
		reason = "price_down"
	}

	p.lastPredictionAt = now
	p.lastDirection = direction
	return direction, reason
}

// GetPriceChangeBps 获取当前价格变化（bps），用于日志记录
func (p *Predictor) GetPriceChangeBps(now time.Time) (int, bool) {
	currentKline, okCurrent := p.binanceKlines.Latest("1s")
	if !okCurrent || currentKline.Close <= 0 {
		return 0, false
	}

	windowMs := int64(p.config.PredictionWindowSeconds) * 1000
	pastKline, okPast := p.binanceKlines.NearestAtOrBefore("1s", now.UnixMilli()-windowMs)
	if !okPast || pastKline.Close <= 0 {
		return 0, false
	}

	priceChange := (currentKline.Close - pastKline.Close) / pastKline.Close
	priceChangeBps := int(math.Abs(priceChange) * 10000)
	return priceChangeBps, true
}
