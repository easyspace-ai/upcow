package domain

import (
	"time"
)

// Position 仓位领域模型
type Position struct {
	ID              string     // 仓位 ID
	MarketSlug      string     // 所属市场周期（便于只管理本周期）
	Market          *Market    // 市场
	EntryOrder      *Order     // 入场订单
	HedgeOrder      *Order     // 对冲订单（可选）
	EntryPrice      Price      // 入场价格（首次成交价格，兼容旧代码）
	EntryTime       time.Time  // 入场时间
	ExitOrder       *Order     // 出场订单（可选）
	ExitPrice       *Price     // 出场价格（可选）
	ExitTime        *time.Time // 出场时间（可选）
	Size            float64    // 仓位大小（当前持仓数量）
	TokenType       TokenType  // Token 类型
	Status          PositionStatus // 仓位状态
	Unhedged        bool       // 是否未对冲
	
	// 成本基础跟踪（支持多次成交累加）
	CostBasis       float64    // 总成本（USDC），累计所有成交的成本
	AvgPrice        float64    // 平均价格，自动计算 = CostBasis / TotalFilledSize
	TotalFilledSize float64    // 累计成交数量（用于计算平均价格）
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

// AddFill 添加成交记录，累加成本基础（支持多次成交）
// size: 成交数量
// price: 成交价格
func (p *Position) AddFill(size float64, price Price) {
	if size <= 0 {
		return
	}
	
	cost := price.ToDecimal() * size
	p.CostBasis += cost
	p.TotalFilledSize += size
	
	// 计算平均价格
	if p.TotalFilledSize > 0 {
		p.AvgPrice = p.CostBasis / p.TotalFilledSize
	}
	
	// 更新 EntryPrice（如果这是首次成交，保持向后兼容）
	if p.EntryPrice.Pips == 0 {
		p.EntryPrice = price
	}
}

// UnrealizedPnL 计算未实现盈亏（USDC）
// currentPrice: 当前市场价格
// 返回：未实现盈亏（USDC），正数表示盈利，负数表示亏损
func (p *Position) UnrealizedPnL(currentPrice Price) float64 {
	if p.TotalFilledSize <= 0 {
		return 0
	}
	currentValue := currentPrice.ToDecimal() * p.TotalFilledSize
	return currentValue - p.CostBasis
}

// RealizedPnL 计算已实现盈亏（USDC）
// 返回：已实现盈亏（USDC），只有在平仓时才有值
func (p *Position) RealizedPnL() float64 {
	if p.ExitPrice == nil || p.TotalFilledSize <= 0 {
		return 0
	}
	exitValue := p.ExitPrice.ToDecimal() * p.TotalFilledSize
	return exitValue - p.CostBasis
}

// CalculateProfit 计算利润（分）
// 优先使用成本基础计算，如果没有成本基础则使用 EntryPrice（向后兼容）
func (p *Position) CalculateProfit(currentPrice Price) int {
	if p.ExitPrice != nil {
		// 已平仓，使用出场价格
		if p.TotalFilledSize > 0 && p.CostBasis > 0 {
			// 使用成本基础计算已实现盈亏
			realizedPnL := p.RealizedPnL()
			return int(realizedPnL * 100) // 转换为分
		}
		// 向后兼容：使用 EntryPrice
		if p.TokenType == TokenTypeUp {
			return p.ExitPrice.ToCents() - p.EntryPrice.ToCents()
		}
		return p.EntryPrice.ToCents() - p.ExitPrice.ToCents()
	}
	// 未平仓，使用当前价格
	if p.TotalFilledSize > 0 && p.CostBasis > 0 {
		// 使用成本基础计算未实现盈亏
		unrealizedPnL := p.UnrealizedPnL(currentPrice)
		return int(unrealizedPnL * 100) // 转换为分
	}
	// 向后兼容：使用 EntryPrice
	if p.TokenType == TokenTypeUp {
		return currentPrice.ToCents() - p.EntryPrice.ToCents()
	}
	return p.EntryPrice.ToCents() - currentPrice.ToCents()
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

// GetElapsedTimeAt 获取距离周期开始的已过时间（秒），以传入的 nowUnix 为准。
// 说明：套利策略的阶段判断应尽量使用事件时间（PriceChangedEvent.Timestamp），
// 避免由于消息延迟/回放导致的阶段漂移。
func (s *ArbitragePositionState) GetElapsedTimeAt(nowUnix int64) int64 {
	elapsed := nowUnix - s.CycleStartTime
	if elapsed < 0 {
		return 0
	}
	return elapsed
}

// GetElapsedTime 获取距离周期开始的已过时间（秒）（兼容旧用法，基于本机时间）。
func (s *ArbitragePositionState) GetElapsedTime() int64 {
	return s.GetElapsedTimeAt(time.Now().Unix())
}

// DetectPhaseAt 判断当前处于哪个阶段，以传入的 nowUnix 为准。
func (s *ArbitragePositionState) DetectPhaseAt(nowUnix int64, cycleDuration, lockStart int64) Phase {
	elapsed := s.GetElapsedTimeAt(nowUnix)
	ratio := float64(elapsed) / float64(cycleDuration)
	
	if ratio < 1.0/3.0 {
		return PhaseBuild
	}
	if elapsed < lockStart {
		return PhaseAdjust
	}
	return PhaseLock
}

// DetectPhase 判断当前处于哪个阶段（兼容旧用法，基于本机时间）。
func (s *ArbitragePositionState) DetectPhase(cycleDuration, lockStart int64) Phase {
	return s.DetectPhaseAt(time.Now().Unix(), cycleDuration, lockStart)
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

