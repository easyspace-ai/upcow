package luckbet

import (
	"sync"
	"time"

	"github.com/betbot/gobet/internal/domain"
)

// PriceSample 价格样本数据结构
// 用于存储特定时间点的价格信息，支持速度计算
type PriceSample struct {
	Timestamp  time.Time    // 价格采样时间戳
	PriceCents int          // 价格（分），兼容旧系统
	Price      domain.Price // 价格（新精度系统）
	TokenType  domain.TokenType // 代币类型（UP/DOWN）
}

// VelocityMetrics 速度计算指标
// 包含价格变化速度的详细信息和验证状态
type VelocityMetrics struct {
	TokenType    domain.TokenType // 代币类型
	Delta        int              // 价格变化（分）
	Duration     float64          // 时间窗口（秒）
	Velocity     float64          // 速度（分/秒）
	IsValid      bool             // 计算结果是否有效
	SampleCount  int              // 参与计算的样本数量
	StartPrice   domain.Price     // 起始价格
	EndPrice     domain.Price     // 结束价格
	Timestamp    time.Time        // 计算时间戳
}

// VelocityThresholds 速度阈值配置
// 定义触发交易的速度条件
type VelocityThresholds struct {
	MinVelocityCentsPerSec float64 // 最小速度阈值（分/秒）
	MinMoveCents           int     // 最小价格变化（分）
	WindowSeconds          int     // 计算窗口大小（秒）
}

// TradingState 交易状态管理
// 跟踪策略的运行状态和统计信息
type TradingState struct {
	mu sync.RWMutex // 读写锁保护并发访问

	// 周期状态
	CurrentCycle   string    // 当前周期标识
	CycleStartTime time.Time // 周期开始时间
	FirstSeenAt    time.Time // 首次接收到价格数据的时间

	// 交易统计
	TradesThisCycle int                    // 本周期交易次数
	PendingTrades   map[string]string      // 待处理交易 entryOrderID -> hedgeOrderID
	LastTriggerAt   time.Time              // 最后触发时间
	LastTriggerSide domain.TokenType       // 最后触发的代币方向

	// 速度数据
	VelocitySamples map[domain.TokenType][]PriceSample // 速度计算样本

	// 风险状态
	InventoryBalance float64                    // 库存平衡（UP - DOWN）
	UnhedgedEntries  map[string]*domain.Order   // 未对冲的入场订单

	// 外部数据状态
	BiasReady  bool             // 外部偏向数据是否就绪
	BiasToken  domain.TokenType // 偏向的代币方向
	BiasReason string           // 偏向原因说明
}

// NewTradingState 创建新的交易状态
func NewTradingState() *TradingState {
	return &TradingState{
		PendingTrades:   make(map[string]string),
		VelocitySamples: make(map[domain.TokenType][]PriceSample),
		UnhedgedEntries: make(map[string]*domain.Order),
	}
}

// Reset 重置交易状态（周期切换时调用）
func (ts *TradingState) Reset() {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	ts.TradesThisCycle = 0
	ts.PendingTrades = make(map[string]string)
	ts.LastTriggerAt = time.Time{}
	ts.LastTriggerSide = ""
	ts.VelocitySamples = make(map[domain.TokenType][]PriceSample)
	ts.InventoryBalance = 0
	ts.UnhedgedEntries = make(map[string]*domain.Order)
	ts.BiasReady = false
	ts.BiasToken = ""
	ts.BiasReason = ""
}

// GetTradeCount 获取本周期交易次数（线程安全）
func (ts *TradingState) GetTradeCount() int {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return ts.TradesThisCycle
}

// IncrementTradeCount 增加交易次数（线程安全）
func (ts *TradingState) IncrementTradeCount() {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.TradesThisCycle++
}

// ExecutionMode 订单执行模式
type ExecutionMode string

const (
	SequentialMode ExecutionMode = "sequential" // 顺序执行：先Entry后Hedge
	ParallelMode   ExecutionMode = "parallel"   // 并行执行：同时提交Entry和Hedge
)

// TradeRequest 交易请求结构
// 包含执行配对交易所需的所有信息
type TradeRequest struct {
	Market      *domain.Market   // 目标市场
	Winner      domain.TokenType // 获胜方向（Entry方向）
	EntryPrice  domain.Price     // Entry订单价格
	HedgePrice  domain.Price     // Hedge订单价格
	EntryShares float64          // Entry订单数量
	HedgeShares float64          // Hedge订单数量
	Reason      string           // 交易原因说明
}

// TradeResult 交易执行结果
// 包含订单执行的状态和错误信息
type TradeResult struct {
	EntryOrderID string // Entry订单ID
	HedgeOrderID string // Hedge订单ID
	Success      bool   // 执行是否成功
	Error        error  // 错误信息
	ExecutedAt   time.Time // 执行时间
}

// RiskCheckResult 风险检查结果
// 用于风险控制模块的决策输出
type RiskCheckResult struct {
	Allowed bool   // 是否允许交易
	Reason  string // 拒绝原因或通过说明
	Score   int    // 风险评分（可选）
}

// ExitAction 退出动作定义
// 描述头寸退出的具体操作
type ExitAction struct {
	PositionID string           // 头寸ID
	TokenType  domain.TokenType // 代币类型
	Size       float64          // 退出数量
	Reason     string           // 退出原因
	Urgent     bool             // 是否紧急退出（使用市价单）
}

// PartialTakeProfit 分批止盈配置
// 支持多层次的止盈策略
type PartialTakeProfit struct {
	ProfitCents int     `yaml:"profitCents" json:"profitCents"` // 止盈价格（分）
	Percentage  float64 `yaml:"percentage" json:"percentage"`   // 止盈比例（0-1）
}

// PositionSummary 头寸汇总信息
// 用于UI显示和监控
type PositionSummary struct {
	TotalPositions   int     // 总头寸数量
	OpenPositions    int     // 开放头寸数量
	HedgedPositions  int     // 已对冲头寸数量
	UnhedgedPositions int    // 未对冲头寸数量
	UpTokens         float64 // UP代币总量
	DownTokens       float64 // DOWN代币总量
	InventoryBalance float64 // 库存平衡
}

// PnLSummary 盈亏汇总信息
// 包含详细的盈亏统计
type PnLSummary struct {
	UnrealizedPnL    float64 // 未实现盈亏（USDC）
	RealizedPnL      float64 // 已实现盈亏（USDC）
	TotalPnL         float64 // 总盈亏（USDC）
	WinningTrades    int     // 盈利交易数
	LosingTrades     int     // 亏损交易数
	TotalTrades      int     // 总交易数
	WinRate          float64 // 胜率
	AverageWin       float64 // 平均盈利
	AverageLoss      float64 // 平均亏损
	ProfitFactor     float64 // 盈亏比
	MaxDrawdown      float64 // 最大回撤
}

// UIUpdate UI更新消息
// 用于向终端界面推送数据更新
type UIUpdate struct {
	Type      string      // 更新类型
	Data      interface{} // 更新数据
	Timestamp time.Time   // 更新时间戳
}

// UIUpdateType UI更新类型常量
const (
	UIUpdateTypePrice     = "price"     // 价格更新
	UIUpdateTypeVelocity  = "velocity"  // 速度更新
	UIUpdateTypePosition  = "position"  // 头寸更新
	UIUpdateTypePnL       = "pnl"       // 盈亏更新
	UIUpdateTypeTrade     = "trade"     // 交易更新
	UIUpdateTypeRisk      = "risk"      // 风险更新
	UIUpdateTypeCycle     = "cycle"     // 周期更新
)

// PerformanceMetrics 性能指标
// 用于策略性能分析和监控
type PerformanceMetrics struct {
	// 交易统计
	TotalTrades      int     // 总交易数
	SuccessfulTrades int     // 成功交易数
	FailedTrades     int     // 失败交易数
	SuccessRate      float64 // 成功率

	// 盈亏统计
	TotalPnL        float64 // 总盈亏
	WinningTrades   int     // 盈利交易数
	LosingTrades    int     // 亏损交易数
	AverageWin      float64 // 平均盈利
	AverageLoss     float64 // 平均亏损
	ProfitFactor    float64 // 盈亏比

	// 风险指标
	MaxDrawdown           float64 // 最大回撤
	SharpeRatio           float64 // 夏普比率
	MaxInventoryImbalance float64 // 最大库存不平衡

	// 执行指标
	AverageExecutionTime time.Duration // 平均执行时间
	OrderFillRate        float64       // 订单成交率
}

// SlippageMetrics 滑点统计
// 用于分析订单执行质量
type SlippageMetrics struct {
	AverageSlippage float64 // 平均滑点（分）
	MaxSlippage     float64 // 最大滑点（分）
	SlippageCount   int     // 滑点事件数量
	TotalOrders     int     // 总订单数
}

// ErrorTypes 错误类型定义
var (
	ErrInvalidConfig        = "invalid_config"        // 配置无效
	ErrInsufficientSamples  = "insufficient_samples"  // 样本不足
	ErrVelocityBelowThreshold = "velocity_below_threshold" // 速度低于阈值
	ErrMarketQualityPoor    = "market_quality_poor"   // 市场质量差
	ErrRiskLimitExceeded    = "risk_limit_exceeded"   // 风险限制超出
	ErrOrderExecutionFailed = "order_execution_failed" // 订单执行失败
	ErrPositionNotFound     = "position_not_found"    // 头寸未找到
	ErrInvalidTokenType     = "invalid_token_type"    // 无效代币类型
	ErrCycleEnded           = "cycle_ended"           // 周期已结束
	ErrSystemPaused         = "system_paused"         // 系统暂停
)

// Constants 常量定义
const (
	// 默认配置值
	DefaultWindowSeconds         = 30    // 默认速度计算窗口（秒）
	DefaultMinVelocity          = 0.5    // 默认最小速度阈值（分/秒）
	DefaultMinMoveCents         = 5      // 默认最小价格变化（分）
	DefaultMaxSamples           = 512    // 默认最大样本数量
	DefaultOrderSize            = 10.0   // 默认订单大小
	DefaultHedgeOffsetCents     = 2      // 默认对冲偏移（分）
	DefaultMaxTradesPerCycle    = 10     // 默认每周期最大交易次数
	DefaultTakeProfitCents      = 10     // 默认止盈（分）
	DefaultStopLossCents        = 20     // 默认止损（分）
	DefaultMaxHoldSeconds       = 600    // 默认最大持有时间（秒）
	DefaultUIUpdateIntervalMs   = 1000   // 默认UI更新间隔（毫秒）

	// 系统限制
	MaxVelocitySamples          = 1024   // 最大速度样本数量
	MaxPendingTrades            = 100    // 最大待处理交易数量
	MaxUnhedgedEntries          = 50     // 最大未对冲入场订单数量
	MinOrderExecutionTimeout    = 5      // 最小订单执行超时（秒）
	MaxOrderExecutionTimeout    = 60     // 最大订单执行超时（秒）

	// 精度相关
	PricePrecision              = 4      // 价格精度（小数位数）
	SizePrecision               = 6      // 数量精度（小数位数）
	CentsPerDollar              = 100    // 每美元的分数
	PipsPerCent                 = 100    // 每分的pips数
)