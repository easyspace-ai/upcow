package domain

import (
	"time"
)

// Position 仓位领域模型
type Position struct {
	ID              string     // 仓位 ID
	Market          *Market    // 市场
	EntryOrder      *Order     // 入场订单
	HedgeOrder      *Order     // 对冲订单（可选）
	EntryPrice      Price      // 入场价格
	EntryTime       time.Time  // 入场时间
	ExitOrder       *Order     // 出场订单（可选）
	ExitPrice       *Price     // 出场价格（可选）
	ExitTime        *time.Time // 出场时间（可选）
	Size            float64    // 仓位大小
	TokenType       TokenType  // Token 类型
	Status          PositionStatus // 仓位状态
	Unhedged        bool       // 是否未对冲
}

// PositionStatus 仓位状态
type PositionStatus string

const (
	PositionStatusOpen   PositionStatus = "open"   // 开放中
	PositionStatusClosed PositionStatus = "closed" // 已关闭
)

// IsOpen 检查仓位是否开放
func (p *Position) IsOpen() bool {
	return p.Status == PositionStatusOpen
}

// IsHedged 检查仓位是否已对冲
// 仓位已对冲的条件：入场订单和对冲订单都已成交
func (p *Position) IsHedged() bool {
	return p.EntryOrder != nil && p.EntryOrder.IsFilled() &&
		p.HedgeOrder != nil && p.HedgeOrder.IsFilled()
}

// CalculateProfit 计算利润（分）
func (p *Position) CalculateProfit(currentPrice Price) int {
	if p.ExitPrice != nil {
		// 已平仓，使用出场价格
		if p.TokenType == TokenTypeUp {
			return p.ExitPrice.Cents - p.EntryPrice.Cents
		}
		return p.EntryPrice.Cents - p.ExitPrice.Cents
	}
	// 未平仓，使用当前价格
	if p.TokenType == TokenTypeUp {
		return currentPrice.Cents - p.EntryPrice.Cents
	}
	return p.EntryPrice.Cents - currentPrice.Cents
}

// CalculateLoss 计算亏损（分）
func (p *Position) CalculateLoss(currentPrice Price) int {
	profit := p.CalculateProfit(currentPrice)
	if profit < 0 {
		return -profit
	}
	return 0
}

// Phase 套利策略阶段
type Phase int

const (
	PhaseBuild Phase = iota // 0-5 分钟：基础建仓
	PhaseAdjust             // 5-10 分钟：中段调整
	PhaseLock               // 10-15 分钟：锁盈阶段
)

// ArbitragePositionState 套利策略双向持仓状态
type ArbitragePositionState struct {
	QUp          float64   // UP 持仓数量
	QDown        float64   // DOWN 持仓数量
	CUp          float64   // UP 总成本（USDC）
	CDown        float64   // DOWN 总成本（USDC）
	Market       *Market   // 市场信息
	CycleStartTime int64   // 周期开始时间（Unix时间戳）
}

// ProfitIfUpWin 计算若UP获胜的即时利润（USDC）
func (s *ArbitragePositionState) ProfitIfUpWin() float64 {
	return s.QUp*1.0 - s.CUp - s.CDown
}

// ProfitIfDownWin 计算若DOWN获胜的即时利润（USDC）
func (s *ArbitragePositionState) ProfitIfDownWin() float64 {
	return s.QDown*1.0 - s.CUp - s.CDown
}

// GetElapsedTime 获取距离周期开始的已过时间（秒）
func (s *ArbitragePositionState) GetElapsedTime() int64 {
	now := time.Now().Unix()
	elapsed := now - s.CycleStartTime
	if elapsed < 0 {
		return 0
	}
	return elapsed
}

// DetectPhase 判断当前处于哪个阶段
func (s *ArbitragePositionState) DetectPhase(cycleDuration, lockStart int64) Phase {
	elapsed := s.GetElapsedTime()
	ratio := float64(elapsed) / float64(cycleDuration)
	
	if ratio < 1.0/3.0 {
		return PhaseBuild
	}
	if elapsed < lockStart {
		return PhaseAdjust
	}
	return PhaseLock
}

// NewArbitragePositionState 创建新的套利持仓状态
func NewArbitragePositionState(market *Market) *ArbitragePositionState {
	return &ArbitragePositionState{
		QUp:           0,
		QDown:         0,
		CUp:           0,
		CDown:         0,
		Market:        market,
		CycleStartTime: market.Timestamp,
	}
}

