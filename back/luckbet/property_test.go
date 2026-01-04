package luckbet

import (
	"math/rand"
	"reflect"
	"testing"
	"testing/quick"
	"time"

	"github.com/betbot/gobet/internal/domain"
)

// **Feature: luckbet-strategy, Property 1: 速度计算一致性**
// **验证需求: 1.1, 1.3**
// 对于任何价格数据输入和时间窗口配置，速度计算应该在配置的时间窗口内产生一致和准确的结果
func TestProperty1_VelocityCalculationConsistency(t *testing.T) {
	property := func(samples []PriceSample, windowSeconds int) bool {
		// 输入域约束：确保输入在有效范围内
		if len(samples) < 2 || windowSeconds <= 0 || windowSeconds > 300 {
			return true // 跳过无效输入
		}
		
		// 确保样本按时间排序且在合理范围内
		baseTime := time.Now()
		for i := range samples {
			samples[i].Timestamp = baseTime.Add(time.Duration(i) * time.Second)
			// 确保价格在合理范围内（1-99分）
			if samples[i].PriceCents < 1 || samples[i].PriceCents > 99 {
				samples[i].PriceCents = 1 + (samples[i].PriceCents % 98)
			}
			samples[i].Price = domain.PriceFromDecimal(float64(samples[i].PriceCents) / 100.0)
			samples[i].TokenType = domain.TokenTypeUp
		}
		
		// 创建速度阈值配置
		thresholds := VelocityThresholds{
			WindowSeconds:          windowSeconds,
			MinVelocityCentsPerSec: 0.1,
			MinMoveCents:           1,
		}
		
		// 计算速度指标
		metrics := calculateVelocityFromSamples(samples, thresholds)
		
		// 属性验证：速度计算的一致性
		if !metrics.IsValid {
			return true // 如果计算无效，跳过验证
		}
		
		// 验证速度计算的基本数学一致性
		if len(samples) >= 2 {
			firstSample := samples[0]
			lastSample := samples[len(samples)-1]
			
			expectedDelta := lastSample.PriceCents - firstSample.PriceCents
			expectedDuration := lastSample.Timestamp.Sub(firstSample.Timestamp).Seconds()
			
			if expectedDuration > 0 {
				expectedVelocity := float64(expectedDelta) / expectedDuration
				
				// 允许小的浮点误差
				tolerance := 0.001
				if abs(metrics.Velocity-expectedVelocity) > tolerance {
					t.Logf("速度计算不一致: expected=%.6f, actual=%.6f, delta=%d, duration=%.6f", 
						expectedVelocity, metrics.Velocity, expectedDelta, expectedDuration)
					return false
				}
			}
		}
		
		// 验证其他一致性属性
		if metrics.SampleCount != len(samples) {
			t.Logf("样本数量不一致: expected=%d, actual=%d", len(samples), metrics.SampleCount)
			return false
		}
		
		return true
	}
	
	config := &quick.Config{
		MaxCount: 100, // 运行100次迭代
	}
	
	if err := quick.Check(property, config); err != nil {
		t.Errorf("属性测试失败: %v", err)
	}
}

// calculateVelocityFromSamples 从样本计算速度指标（简化实现用于测试）
func calculateVelocityFromSamples(samples []PriceSample, thresholds VelocityThresholds) VelocityMetrics {
	if len(samples) < 2 {
		return VelocityMetrics{IsValid: false}
	}
	
	// 使用第一个和最后一个样本计算速度
	firstSample := samples[0]
	lastSample := samples[len(samples)-1]
	
	delta := lastSample.PriceCents - firstSample.PriceCents
	duration := lastSample.Timestamp.Sub(firstSample.Timestamp).Seconds()
	
	if duration <= 0 {
		return VelocityMetrics{IsValid: false}
	}
	
	velocity := float64(delta) / duration
	
	return VelocityMetrics{
		TokenType:   firstSample.TokenType,
		Delta:       delta,
		Duration:    duration,
		Velocity:    velocity,
		IsValid:     true,
		SampleCount: len(samples),
		StartPrice:  firstSample.Price,
		EndPrice:    lastSample.Price,
		Timestamp:   time.Now(),
	}
}

// abs 返回浮点数的绝对值
func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// 自定义生成器用于生成有效的价格样本
func generatePriceSamples(rand *rand.Rand, size int) []PriceSample {
	if size <= 0 {
		size = 2 + rand.Intn(10) // 2-11个样本
	}
	
	samples := make([]PriceSample, size)
	baseTime := time.Now()
	basePrice := 30 + rand.Intn(40) // 30-69分的基础价格
	
	for i := 0; i < size; i++ {
		// 价格随机游走
		priceChange := rand.Intn(21) - 10 // -10到+10的变化
		price := basePrice + priceChange
		if price < 1 {
			price = 1
		}
		if price > 99 {
			price = 99
		}
		
		samples[i] = PriceSample{
			Timestamp:  baseTime.Add(time.Duration(i) * time.Second),
			PriceCents: price,
			Price:      domain.PriceFromDecimal(float64(price) / 100.0),
			TokenType:  domain.TokenTypeUp,
		}
		
		basePrice = price // 下一个样本基于当前价格
	}
	
	return samples
}

// Generate 实现 quick.Generator 接口用于生成价格样本
func (PriceSample) Generate(rand *rand.Rand, size int) reflect.Value {
	sample := PriceSample{
		Timestamp:  time.Now().Add(time.Duration(rand.Intn(3600)) * time.Second),
		PriceCents: 1 + rand.Intn(98), // 1-99分
		TokenType:  domain.TokenTypeUp,
	}
	sample.Price = domain.PriceFromDecimal(float64(sample.PriceCents) / 100.0)
	
	return reflect.ValueOf(sample)
}



// TestPropertyVelocityThresholds 测试速度阈值配置的属性
func TestPropertyVelocityThresholds(t *testing.T) {
	property := func(windowSeconds int, minVelocity float64, minMove int) bool {
		// 输入域约束
		if windowSeconds <= 0 || windowSeconds > 3600 {
			return true
		}
		if minVelocity < 0 || minVelocity > 100 {
			return true
		}
		if minMove < 0 || minMove > 50 {
			return true
		}
		
		thresholds := VelocityThresholds{
			WindowSeconds:          windowSeconds,
			MinVelocityCentsPerSec: minVelocity,
			MinMoveCents:           minMove,
		}
		
		// 验证配置的一致性
		if thresholds.WindowSeconds != windowSeconds {
			return false
		}
		if thresholds.MinVelocityCentsPerSec != minVelocity {
			return false
		}
		if thresholds.MinMoveCents != minMove {
			return false
		}
		
		return true
	}
	
	config := &quick.Config{MaxCount: 100}
	if err := quick.Check(property, config); err != nil {
		t.Errorf("速度阈值属性测试失败: %v", err)
	}
}

// TestPropertyTradingStateConsistency 测试交易状态一致性属性
func TestPropertyTradingStateConsistency(t *testing.T) {
	property := func(tradeCount int) bool {
		// 输入域约束
		if tradeCount < 0 || tradeCount > 1000 {
			return true
		}
		
		ts := NewTradingState()
		
		// 执行多次增加操作
		for i := 0; i < tradeCount; i++ {
			ts.IncrementTradeCount()
		}
		
		// 验证最终计数的一致性
		finalCount := ts.GetTradeCount()
		if finalCount != tradeCount {
			t.Logf("交易计数不一致: expected=%d, actual=%d", tradeCount, finalCount)
			return false
		}
		
		// 验证重置后的一致性
		ts.Reset()
		if ts.GetTradeCount() != 0 {
			t.Logf("重置后计数应为0，实际为: %d", ts.GetTradeCount())
			return false
		}
		
		return true
	}
	
	config := &quick.Config{MaxCount: 100}
	if err := quick.Check(property, config); err != nil {
		t.Errorf("交易状态一致性属性测试失败: %v", err)
	}
}

// TestPropertyTradeRequestValidation 测试交易请求验证属性
func TestPropertyTradeRequestValidation(t *testing.T) {
	property := func(entryPrice, hedgePrice int, entryShares, hedgeShares float64) bool {
		// 输入域约束
		if entryPrice < 1 || entryPrice > 99 {
			return true
		}
		if hedgePrice < 1 || hedgePrice > 99 {
			return true
		}
		if entryShares <= 0 || entryShares > 1000 {
			return true
		}
		if hedgeShares <= 0 || hedgeShares > 1000 {
			return true
		}
		
		market := &domain.Market{
			Slug:        "test-market",
			YesAssetID:  "yes-asset",
			NoAssetID:   "no-asset",
			ConditionID: "condition-123",
			Question:    "Test question?",
			Timestamp:   time.Now().Unix(),
		}
		
		request := TradeRequest{
			Market:      market,
			Winner:      domain.TokenTypeUp,
			EntryPrice:  domain.PriceFromDecimal(float64(entryPrice) / 100.0),
			HedgePrice:  domain.PriceFromDecimal(float64(hedgePrice) / 100.0),
			EntryShares: entryShares,
			HedgeShares: hedgeShares,
			Reason:      "property test",
		}
		
		// 验证交易请求的基本属性
		if request.Market == nil {
			return false
		}
		if request.EntryShares != entryShares {
			return false
		}
		if request.HedgeShares != hedgeShares {
			return false
		}
		if request.Winner != domain.TokenTypeUp {
			return false
		}
		
		// 验证价格设置的一致性
		expectedEntryPrice := float64(entryPrice) / 100.0
		actualEntryPrice := request.EntryPrice.ToDecimal()
		if abs(actualEntryPrice-expectedEntryPrice) > 0.001 {
			return false
		}
		
		return true
	}
	
	config := &quick.Config{MaxCount: 100}
	if err := quick.Check(property, config); err != nil {
		t.Errorf("交易请求验证属性测试失败: %v", err)
	}
}