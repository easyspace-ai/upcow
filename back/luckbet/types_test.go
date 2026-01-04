package luckbet

import (
	"testing"
	"time"

	"github.com/betbot/gobet/internal/domain"
)

// TestTradingStateCreation 测试交易状态创建
func TestTradingStateCreation(t *testing.T) {
	ts := NewTradingState()
	
	if ts == nil {
		t.Fatal("NewTradingState() 返回了 nil")
	}
	
	if ts.PendingTrades == nil {
		t.Error("PendingTrades 应该被初始化")
	}
	
	if ts.VelocitySamples == nil {
		t.Error("VelocitySamples 应该被初始化")
	}
	
	if ts.UnhedgedEntries == nil {
		t.Error("UnhedgedEntries 应该被初始化")
	}
	
	if ts.TradesThisCycle != 0 {
		t.Errorf("TradesThisCycle 应该为 0，实际为 %d", ts.TradesThisCycle)
	}
}

// TestTradingStateReset 测试交易状态重置
func TestTradingStateReset(t *testing.T) {
	ts := NewTradingState()
	
	// 设置一些状态
	ts.TradesThisCycle = 5
	ts.PendingTrades["order1"] = "hedge1"
	ts.LastTriggerAt = time.Now()
	ts.LastTriggerSide = domain.TokenTypeUp
	ts.VelocitySamples[domain.TokenTypeUp] = []PriceSample{{Timestamp: time.Now()}}
	ts.InventoryBalance = 10.5
	ts.UnhedgedEntries["order2"] = &domain.Order{OrderID: "order2"}
	ts.BiasReady = true
	ts.BiasToken = domain.TokenTypeDown
	ts.BiasReason = "test"
	
	// 重置状态
	ts.Reset()
	
	// 验证重置结果
	if ts.TradesThisCycle != 0 {
		t.Errorf("重置后 TradesThisCycle 应该为 0，实际为 %d", ts.TradesThisCycle)
	}
	
	if len(ts.PendingTrades) != 0 {
		t.Errorf("重置后 PendingTrades 应该为空，实际长度为 %d", len(ts.PendingTrades))
	}
	
	if !ts.LastTriggerAt.IsZero() {
		t.Error("重置后 LastTriggerAt 应该为零值")
	}
	
	if ts.LastTriggerSide != "" {
		t.Errorf("重置后 LastTriggerSide 应该为空，实际为 %s", ts.LastTriggerSide)
	}
	
	if len(ts.VelocitySamples) != 0 {
		t.Errorf("重置后 VelocitySamples 应该为空，实际长度为 %d", len(ts.VelocitySamples))
	}
	
	if ts.InventoryBalance != 0 {
		t.Errorf("重置后 InventoryBalance 应该为 0，实际为 %.2f", ts.InventoryBalance)
	}
	
	if len(ts.UnhedgedEntries) != 0 {
		t.Errorf("重置后 UnhedgedEntries 应该为空，实际长度为 %d", len(ts.UnhedgedEntries))
	}
	
	if ts.BiasReady {
		t.Error("重置后 BiasReady 应该为 false")
	}
	
	if ts.BiasToken != "" {
		t.Errorf("重置后 BiasToken 应该为空，实际为 %s", ts.BiasToken)
	}
	
	if ts.BiasReason != "" {
		t.Errorf("重置后 BiasReason 应该为空，实际为 %s", ts.BiasReason)
	}
}

// TestTradingStateThreadSafety 测试交易状态线程安全性
func TestTradingStateThreadSafety(t *testing.T) {
	ts := NewTradingState()
	
	// 并发读写测试
	done := make(chan bool, 2)
	
	// 写入协程
	go func() {
		for i := 0; i < 100; i++ {
			ts.IncrementTradeCount()
		}
		done <- true
	}()
	
	// 读取协程
	go func() {
		for i := 0; i < 100; i++ {
			_ = ts.GetTradeCount()
		}
		done <- true
	}()
	
	// 等待两个协程完成
	<-done
	<-done
	
	// 验证最终结果
	if ts.GetTradeCount() != 100 {
		t.Errorf("并发操作后交易次数应该为 100，实际为 %d", ts.GetTradeCount())
	}
}

// TestPriceSampleCreation 测试价格样本创建
func TestPriceSampleCreation(t *testing.T) {
	now := time.Now()
	price := domain.PriceFromDecimal(0.6500)
	
	sample := PriceSample{
		Timestamp:  now,
		PriceCents: 65,
		Price:      price,
		TokenType:  domain.TokenTypeUp,
	}
	
	if sample.Timestamp != now {
		t.Error("时间戳设置不正确")
	}
	
	if sample.PriceCents != 65 {
		t.Errorf("价格分数应该为 65，实际为 %d", sample.PriceCents)
	}
	
	if sample.Price.Pips != price.Pips {
		t.Errorf("价格 pips 应该为 %d，实际为 %d", price.Pips, sample.Price.Pips)
	}
	
	if sample.TokenType != domain.TokenTypeUp {
		t.Errorf("代币类型应该为 %s，实际为 %s", domain.TokenTypeUp, sample.TokenType)
	}
}

// TestVelocityMetricsValidation 测试速度指标验证
func TestVelocityMetricsValidation(t *testing.T) {
	metrics := VelocityMetrics{
		TokenType:   domain.TokenTypeUp,
		Delta:       10,
		Duration:    2.0,
		Velocity:    5.0,
		IsValid:     true,
		SampleCount: 5,
		StartPrice:  domain.PriceFromDecimal(0.6000),
		EndPrice:    domain.PriceFromDecimal(0.7000),
		Timestamp:   time.Now(),
	}
	
	if !metrics.IsValid {
		t.Error("有效的速度指标应该标记为有效")
	}
	
	if metrics.Velocity != 5.0 {
		t.Errorf("速度应该为 5.0，实际为 %.2f", metrics.Velocity)
	}
	
	if metrics.SampleCount != 5 {
		t.Errorf("样本数量应该为 5，实际为 %d", metrics.SampleCount)
	}
}

// TestTradeRequestValidation 测试交易请求验证
func TestTradeRequestValidation(t *testing.T) {
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
		EntryPrice:  domain.PriceFromDecimal(0.6500),
		HedgePrice:  domain.PriceFromDecimal(0.3500),
		EntryShares: 10.0,
		HedgeShares: 10.0,
		Reason:      "velocity trigger",
	}
	
	if request.Market.Slug != "test-market" {
		t.Error("市场设置不正确")
	}
	
	if request.Winner != domain.TokenTypeUp {
		t.Error("获胜方向设置不正确")
	}
	
	if request.EntryShares != 10.0 {
		t.Errorf("入场数量应该为 10.0，实际为 %.2f", request.EntryShares)
	}
	
	if request.Reason != "velocity trigger" {
		t.Errorf("交易原因应该为 'velocity trigger'，实际为 '%s'", request.Reason)
	}
}

// TestRiskCheckResult 测试风险检查结果
func TestRiskCheckResult(t *testing.T) {
	// 测试允许的情况
	allowedResult := RiskCheckResult{
		Allowed: true,
		Reason:  "all checks passed",
		Score:   85,
	}
	
	if !allowedResult.Allowed {
		t.Error("应该允许交易")
	}
	
	if allowedResult.Score != 85 {
		t.Errorf("风险评分应该为 85，实际为 %d", allowedResult.Score)
	}
	
	// 测试拒绝的情况
	rejectedResult := RiskCheckResult{
		Allowed: false,
		Reason:  "market quality too low",
		Score:   45,
	}
	
	if rejectedResult.Allowed {
		t.Error("应该拒绝交易")
	}
	
	if rejectedResult.Reason != "market quality too low" {
		t.Errorf("拒绝原因不正确: %s", rejectedResult.Reason)
	}
}

// TestPartialTakeProfitStructure 测试分批止盈配置结构
func TestPartialTakeProfitStructure(t *testing.T) {
	ptp := PartialTakeProfit{
		ProfitCents: 15,
		Percentage:  0.5,
	}
	
	if ptp.ProfitCents != 15 {
		t.Errorf("止盈价格应该为 15 分，实际为 %d", ptp.ProfitCents)
	}
	
	if ptp.Percentage != 0.5 {
		t.Errorf("止盈比例应该为 0.5，实际为 %.2f", ptp.Percentage)
	}
}

// TestUIUpdateMessage 测试UI更新消息
func TestUIUpdateMessage(t *testing.T) {
	now := time.Now()
	update := UIUpdate{
		Type:      UIUpdateTypePrice,
		Data:      map[string]float64{"up": 0.65, "down": 0.35},
		Timestamp: now,
	}
	
	if update.Type != UIUpdateTypePrice {
		t.Errorf("更新类型应该为 %s，实际为 %s", UIUpdateTypePrice, update.Type)
	}
	
	if update.Data == nil {
		t.Error("更新数据不应该为 nil")
	}
	
	if update.Timestamp != now {
		t.Error("时间戳设置不正确")
	}
}