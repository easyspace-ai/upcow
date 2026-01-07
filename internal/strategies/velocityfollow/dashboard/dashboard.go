package dashboard

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/services"
	"github.com/betbot/gobet/pkg/logger"
	"github.com/betbot/gobet/pkg/marketspec"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/sirupsen/logrus"
	"golang.org/x/term"
)

var log = logrus.WithField("module", "dashboard")

// absFloat 返回浮点数的绝对值
func absFloat(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// getCycleDurationFromMarket 从 market slug 解析周期时长
// 支持两种 slug 格式：
// 1. timestamp 格式: {symbol}-{kind}-{timeframe}-{timestamp}
//    例如: eth-updown-1h-1767717000
// 2. hourly ET 格式: {coinName}-up-or-down-{month}-{day}-{hour}{am|pm}-et
//    例如: ethereum-up-or-down-january-6-11am-et
func getCycleDurationFromMarket(market *domain.Market) time.Duration {
	if market == nil || market.Slug == "" {
		// 默认返回 15 分钟（向后兼容）
		return 15 * time.Minute
	}

	slug := market.Slug
	
	// 方法1: 尝试从 timestamp 格式解析（第三个部分是 timeframe）
	parts := strings.Split(slug, "-")
	if len(parts) >= 3 {
		timeframeStr := parts[2] // 例如 "1h", "15m", "4h"
		tf, err := marketspec.ParseTimeframe(timeframeStr)
		if err == nil {
			// 成功解析，返回对应的周期时长
			return tf.Duration()
		}
		// 如果解析失败，继续尝试其他方法
	}

	// 方法2: 检查是否为 hourly ET 格式（包含 "am" 或 "pm"）
	// hourly ET 格式通常是 1 小时市场
	slugLower := strings.ToLower(slug)
	if strings.Contains(slugLower, "am") || strings.Contains(slugLower, "pm") {
		// 检查是否包含 "-et" 后缀（hourly ET 格式的特征）
		if strings.HasSuffix(slugLower, "-et") || strings.Contains(slugLower, "-et-") {
			log.Debugf("✅ [Dashboard] 检测到 hourly ET 格式 slug，使用 1 小时周期: slug=%s", slug)
			return 1 * time.Hour
		}
	}

	// 方法3: 检查是否包含月份名称（hourly ET 格式的另一个特征）
	months := []string{"january", "february", "march", "april", "may", "june",
		"july", "august", "september", "october", "november", "december"}
	for _, month := range months {
		if strings.Contains(slugLower, month) {
			log.Debugf("✅ [Dashboard] 检测到包含月份名称的 slug，推断为 1 小时周期: slug=%s", slug)
			return 1 * time.Hour
		}
	}

	// 无法解析，返回默认 15 分钟
	log.Warnf("⚠️ [Dashboard] 无法从 slug 解析周期时长: slug=%s，使用默认 15 分钟", slug)
	return 15 * time.Minute
}

// Snapshot 仪表板快照数据
type Snapshot struct {
	// 市场信息
	MarketSlug string
	YesPrice   float64
	NoPrice    float64
	YesBid     float64
	YesAsk     float64
	NoBid      float64
	NoAsk      float64

	// 速度信息
	UpVelocity   float64
	DownVelocity float64
	UpMove       int
	DownMove     int
	Direction    string // "UP" | "DOWN" | ""

	// 持仓信息
	UpSize      float64
	DownSize    float64
	UpCost      float64
	DownCost    float64
	UpAvgPrice  float64
	DownAvgPrice float64
	IsHedged    bool

	// 盈利信息
	ProfitIfUpWin   float64
	ProfitIfDownWin  float64
	TotalCost        float64
	IsProfitLocked   bool

	// 交易统计
	TradesThisCycle int
	LastTriggerTime time.Time

	// 合并状态
	MergeStatus      string // "idle" | "merging" | "completed" | "failed"
	MergeAmount      float64
	MergeTxHash      string
	LastMergeTime    time.Time
	MergeCount       int // 本周期 merge 次数

	// 赎回状态
	RedeemStatus     string // "idle" | "redeeming" | "completed" | "failed"
	RedeemCount      int
	LastRedeemTime   time.Time

	// 订单状态
	PendingHedges    int
	OpenOrders       int

	// 风控状态
	RiskManagement *RiskManagementStatus

	// 决策条件
	DecisionConditions *DecisionConditions

	// 周期信息
	CycleEndTime      time.Time // 周期结束时间
	CycleRemainingSec float64   // 周期剩余时间（秒）
}

// RiskManagementStatus 风控状态信息
type RiskManagementStatus struct {
	// 风险敞口
	RiskExposuresCount int
	RiskExposures      []RiskExposureInfo

	// 当前操作状态
	CurrentAction      string // "idle" | "canceling" | "reordering" | "aggressive_hedging" | "fak_eating"
	CurrentActionEntry string // 当前操作的 Entry 订单 ID
	CurrentActionHedge string // 当前操作的 Hedge 订单 ID
	CurrentActionTime  time.Time // 当前操作开始时间
	CurrentActionDesc  string // 当前操作描述

	// 统计信息
	TotalReorders      int // 总重下次数
	TotalAggressiveHedges int // 总激进对冲次数
	TotalFakEats       int // 总 FAK 吃单次数
	
	// 调价详情（用于 UI 显示）
	RepriceOldPriceCents    int    // 原价格（分）
	RepriceNewPriceCents    int    // 新价格（分）
	RepricePriceChangeCents int    // 价格变化（分）
	RepriceStrategy         string // 调价策略描述
	RepriceEntryCostCents   int    // Entry成本（分）
	RepriceMarketAskCents   int    // 市场ask价格（分）
	RepriceIdealPriceCents  int    // 理想价格（分）
	RepriceTotalCostCents   int    // 总成本（分）
	RepriceProfitCents      int    // 利润（分）
}

// RiskExposureInfo 风险敞口信息（用于 UI 显示）
type RiskExposureInfo struct {
	EntryOrderID    string
	EntryTokenType  string
	EntrySize       float64
	EntryPriceCents int
	HedgeOrderID    string
	HedgeStatus     string
	ExposureSeconds float64
	MaxLossCents    int
	// 调价信息（如果重新下单了）
	OriginalHedgePriceCents int // 原对冲单价格（分）
	NewHedgePriceCents      int // 新对冲单价格（分），如果为0表示未重新下单
	CountdownSeconds        float64 // 倒计时（秒），到激进对冲超时的时间
}

// DecisionConditions 决策条件状态（从 brain 模块复制，避免循环导入）
type DecisionConditions struct {
	// 速度条件
	UpVelocityOK       bool
	UpVelocityValue    float64
	UpMoveOK           bool
	UpMoveValue        int
	DownVelocityOK     bool
	DownVelocityValue  float64
	DownMoveOK         bool
	DownMoveValue      int
	Direction          string

	// 价格条件
	EntryPriceOK       bool
	EntryPriceValue    float64
	EntryPriceMin      float64
	EntryPriceMax      float64
	TotalCostOK        bool
	TotalCostValue     float64
	HedgePriceOK       bool
	HedgePriceValue    float64

	// 持仓条件
	HasUnhedgedRisk    bool
	IsProfitLocked     bool
	ProfitIfUpWin      float64
	ProfitIfDownWin    float64

	// 周期条件
	CooldownOK         bool
	CooldownRemaining  float64
	WarmupOK           bool
	WarmupRemaining    float64
	TradesLimitOK      bool
	TradesThisCycle    int
	MaxTradesPerCycle  int

	// 市场条件
	MarketValid        bool
	HasPendingHedge    bool

	// 总体状态
	CanTrade           bool
	BlockReason        string
}

// Dashboard 仪表板
type Dashboard struct {
	tradingService *services.TradingService
	mu             sync.RWMutex
	snapshot       *Snapshot
	enabled        bool
	useNativeTUI   bool // 是否使用原生TUI（默认 false，使用 Bubble Tea）
	program        *tea.Program
	nativeTUI      *NativeTUI // 原生TUI实例
	updateCh       chan *Snapshot
	logFile        *os.File
	logFilepath    string // 保存日志文件路径，用于周期切换后重新应用
	stopLogGuard   chan struct{} // 用于停止日志守护 goroutine
	programDone    chan struct{} // 用于等待 program goroutine 退出
	exitCh         chan struct{} // 用于接收退出信号（原生TUI）
	exitCallback   func()       // 退出回调函数（当原生TUI退出时调用）
}

// New 创建新的仪表板
// useNativeTUI: 是否使用原生TUI
//   - 如果提供了参数，使用参数值（从配置文件读取）
//   - 如果未提供参数，检查环境变量 DASHBOARD_USE_NATIVE_TUI
//   - 如果环境变量也未设置，默认使用原生TUI（true）
// 注意：由于bool零值是false，无法区分"未设置"和"明确设置为false"
// 所以如果从配置文件传入false，我们无法知道是用户设置的还是默认值
// 因此，我们采用策略：如果参数为false，仍然使用false（尊重用户设置）
// 如果未提供参数（len(useNativeTUI) == 0），则默认使用原生TUI
func New(ts *services.TradingService, useNativeTUI ...bool) *Dashboard {
	var useNative bool
	if len(useNativeTUI) > 0 {
		// 从参数获取（从配置文件中读取）
		// 注意：如果yaml中未设置dashboardUseNativeTUI，bool零值是false
		// 但用户希望默认使用原生TUI，所以我们需要在config.go的Defaults()中处理
		useNative = useNativeTUI[0]
	} else {
		// 检查环境变量（向后兼容）
		envValue := os.Getenv("DASHBOARD_USE_NATIVE_TUI")
		if envValue == "true" {
			useNative = true
		} else if envValue == "false" {
			useNative = false
		} else {
			// 环境变量未设置，默认使用原生TUI
			useNative = true
		}
	}
	
	d := &Dashboard{
		tradingService: ts,
		snapshot:       &Snapshot{},
		enabled:        true,
		useNativeTUI:   useNative,
		updateCh:       make(chan *Snapshot, 10),
		stopLogGuard:   make(chan struct{}),
		programDone:    make(chan struct{}),
		exitCh:         make(chan struct{}, 1), // 缓冲通道，避免阻塞
		exitCallback:   nil,                    // 将在 Start 时设置
	}
	
	if useNative {
		log.Infof("✅ [Dashboard] 使用原生TUI实现（tcell）")
	} else {
		log.Infof("✅ [Dashboard] 使用Bubble Tea实现")
	}
	
	// 立即初始化日志文件，确保日志重定向在启动前就生效
	logDir := "logs"
	if err := os.MkdirAll(logDir, 0755); err != nil {
		logDir = os.TempDir()
	}
	logFile := filepath.Join(logDir, "velocityfollow-dashboard.log")
	d.logFilepath = logFile
	if file, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666); err == nil {
		d.logFile = file
		// 立即应用日志重定向
		d.applyLogRedirect()
	}
	
	return d
}

// SetEnabled 设置是否启用
func (d *Dashboard) SetEnabled(enabled bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.enabled = enabled
}

// SetExitCallback 设置退出回调函数（当原生TUI退出时调用）
func (d *Dashboard) SetExitCallback(callback func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.exitCallback = callback
	log.Infof("✅ [Dashboard] 已设置退出回调函数")
}

// CheckAndResetOnMarketChange 检查市场是否切换，如果切换则重置快照
// 返回 true 表示发生了市场切换
func (d *Dashboard) CheckAndResetOnMarketChange(market *domain.Market) bool {
	if !d.enabled || market == nil {
		return false
	}

	d.mu.Lock()

	// 如果市场 slug 发生变化，说明周期已切换，需要重置快照
	if d.snapshot != nil && d.snapshot.MarketSlug != "" && d.snapshot.MarketSlug != market.Slug {
		log.Infof("🔄 [Dashboard] 检测到市场切换: %s -> %s，重置快照", d.snapshot.MarketSlug, market.Slug)
		// 完全重置快照，清空所有旧数据
		d.snapshot = &Snapshot{
			MarketSlug: market.Slug,
		}
		// 计算周期结束时间和剩余时间
		if market.Timestamp > 0 {
			// 从市场信息动态获取周期时长（支持 15m/1h/4h）
			cycleDuration := getCycleDurationFromMarket(market)
			cycleStartTime := time.Unix(market.Timestamp, 0)
			d.snapshot.CycleEndTime = cycleStartTime.Add(cycleDuration)
			now := time.Now()
			if now.Before(d.snapshot.CycleEndTime) {
				d.snapshot.CycleRemainingSec = d.snapshot.CycleEndTime.Sub(now).Seconds()
				log.Infof("✅ [Dashboard] 周期结束时间已设置: start=%s end=%s remaining=%.1fs", 
					cycleStartTime.Format("15:04:05"), d.snapshot.CycleEndTime.Format("15:04:05"), d.snapshot.CycleRemainingSec)
			} else {
				d.snapshot.CycleRemainingSec = 0
				log.Warnf("⚠️ [Dashboard] 周期已结束: start=%s end=%s now=%s", 
					cycleStartTime.Format("15:04:05"), d.snapshot.CycleEndTime.Format("15:04:05"), now.Format("15:04:05"))
			}
		} else {
			log.Warnf("⚠️ [Dashboard] 市场时间戳无效: timestamp=%d", market.Timestamp)
		}
		// 清空 channel 中的旧数据
		drained := false
		for !drained {
			select {
			case <-d.updateCh:
			default:
				drained = true
			}
		}

		// 原生 TUI：必须直接推送，否则 updateCh 不会被消费，导致切周期后 UI 不及时刷新
		if d.useNativeTUI && d.nativeTUI != nil {
			snapshot := d.snapshot
			native := d.nativeTUI
			d.mu.Unlock()
			native.UpdateSnapshot(snapshot)
			log.Debugf("✅ [Dashboard] 已发送重置后的快照到原生TUI")
			return true
		}

		// Bubble Tea：发送重置后的快照到 UI
		select {
		case d.updateCh <- d.snapshot:
			log.Debugf("✅ [Dashboard] 已发送重置后的快照到 UI")
		default:
			log.Warnf("⚠️ [Dashboard] 发送重置后的快照失败（channel 已满）")
		}
		d.mu.Unlock()
		return true // 发生了市场切换
	}
	d.mu.Unlock()
	return false // 没有发生市场切换
}

// UpdateSnapshot 更新快照数据
func (d *Dashboard) UpdateSnapshot(ctx context.Context, market *domain.Market, data *UpdateData) {
	if !d.enabled {
		return
	}

	// 如果使用原生TUI，直接更新
	if d.useNativeTUI {
		d.mu.Lock()
		// 更新快照数据（与Bubble Tea版本相同的逻辑）
		if d.snapshot == nil {
			d.snapshot = &Snapshot{}
		}

		// 更新市场信息
		if market != nil {
			if d.snapshot.MarketSlug != market.Slug {
				log.Debugf("🔄 [Dashboard] UpdateSnapshot 检测到市场切换: %s -> %s", d.snapshot.MarketSlug, market.Slug)
				if d.snapshot.MarketSlug == "" || d.snapshot.CycleEndTime.IsZero() {
					if market.Timestamp > 0 {
						cycleDuration := getCycleDurationFromMarket(market)
						cycleStartTime := time.Unix(market.Timestamp, 0)
						d.snapshot.CycleEndTime = cycleStartTime.Add(cycleDuration)
						now := time.Now()
						if now.Before(d.snapshot.CycleEndTime) {
							d.snapshot.CycleRemainingSec = d.snapshot.CycleEndTime.Sub(now).Seconds()
						} else {
							d.snapshot.CycleRemainingSec = 0
						}
					}
				}
			}
			d.snapshot.MarketSlug = market.Slug
		}

		// 更新数据（与Bubble Tea版本相同的逻辑）
		// 关键修复：强制更新所有字段，即使为0也要更新，确保周期切换时旧数据被清零
		if data != nil {
			// 价格信息（强制更新，包括0值）
			d.snapshot.YesPrice = data.YesPrice
			d.snapshot.NoPrice = data.NoPrice
			d.snapshot.YesBid = data.YesBid
			d.snapshot.YesAsk = data.YesAsk
			d.snapshot.NoBid = data.NoBid
			d.snapshot.NoAsk = data.NoAsk
			
			// 速度信息（强制更新，包括0值）
			d.snapshot.UpVelocity = data.UpVelocity
			d.snapshot.DownVelocity = data.DownVelocity
			d.snapshot.UpMove = data.UpMove
			d.snapshot.DownMove = data.DownMove
			// Direction 需要特殊处理：如果为空字符串，也要更新（清空旧值）
			d.snapshot.Direction = data.Direction
			
			// 持仓信息（强制更新，包括0值）
			if data.PositionState != nil {
				d.snapshot.UpSize = data.PositionState.UpSize
				d.snapshot.DownSize = data.PositionState.DownSize
				d.snapshot.UpCost = data.PositionState.UpCost
				d.snapshot.DownCost = data.PositionState.DownCost
				d.snapshot.UpAvgPrice = data.PositionState.UpAvgPrice
				d.snapshot.DownAvgPrice = data.PositionState.DownAvgPrice
				d.snapshot.IsHedged = data.PositionState.IsHedged
			} else {
				// 如果 PositionState 为 nil，清零所有持仓字段
				d.snapshot.UpSize = 0
				d.snapshot.DownSize = 0
				d.snapshot.UpCost = 0
				d.snapshot.DownCost = 0
				d.snapshot.UpAvgPrice = 0
				d.snapshot.DownAvgPrice = 0
				d.snapshot.IsHedged = false
			}
			
			// 盈利信息（强制更新，包括0值）
			d.snapshot.ProfitIfUpWin = data.ProfitIfUpWin
			d.snapshot.ProfitIfDownWin = data.ProfitIfDownWin
			d.snapshot.TotalCost = data.TotalCost
			d.snapshot.IsProfitLocked = data.IsProfitLocked
			
			// 交易统计（强制更新，包括0值）
			d.snapshot.TradesThisCycle = data.TradesThisCycle
			d.snapshot.LastTriggerTime = data.LastTriggerTime
			
			// 合并状态（强制更新）
			d.snapshot.MergeStatus = data.MergeStatus
			d.snapshot.MergeAmount = data.MergeAmount
			d.snapshot.MergeTxHash = data.MergeTxHash
			d.snapshot.LastMergeTime = data.LastMergeTime
			d.snapshot.MergeCount = data.MergeCount
			
			// 赎回状态（强制更新）
			d.snapshot.RedeemStatus = data.RedeemStatus
			d.snapshot.RedeemCount = data.RedeemCount
			d.snapshot.LastRedeemTime = data.LastRedeemTime
			
			// 订单状态（强制更新，包括0值）
			d.snapshot.PendingHedges = data.PendingHedges
			d.snapshot.OpenOrders = data.OpenOrders
			
			// 风控状态和决策条件（如果为 nil，也要设置为 nil，清空旧数据）
			d.snapshot.RiskManagement = data.RiskManagement
			d.snapshot.DecisionConditions = data.DecisionConditions
			
			// 周期信息（强制更新）
			d.snapshot.CycleEndTime = data.CycleEndTime
			d.snapshot.CycleRemainingSec = data.CycleRemainingSec
		}

		snapshot := d.snapshot
		d.mu.Unlock()

		// 直接更新原生TUI（立即渲染）
		if d.nativeTUI != nil {
			d.nativeTUI.UpdateSnapshot(snapshot)
		}

		return
	}

	// Bubble Tea实现
	d.mu.Lock()

	if d.snapshot == nil {
		d.snapshot = &Snapshot{}
	}

	// 更新市场信息（如果提供了新的市场，强制更新）
	// 注意：如果市场切换，应该在 CheckAndResetOnMarketChange 中处理，这里只更新市场 slug
	if market != nil {
		// 如果市场 slug 发生变化，说明周期已切换
		// 但这里不应该重置快照，因为 CheckAndResetOnMarketChange 或 ResetSnapshot 已经处理了
		// 这里只更新市场 slug，确保快照中的市场信息是最新的
		if d.snapshot.MarketSlug != market.Slug {
			log.Debugf("🔄 [Dashboard] UpdateSnapshot 检测到市场切换: %s -> %s", d.snapshot.MarketSlug, market.Slug)
			// 如果快照已经重置（MarketSlug 为空或已更新），只更新 slug
			// 否则，说明这是第一次更新，需要设置周期结束时间
			if d.snapshot.MarketSlug == "" || d.snapshot.CycleEndTime.IsZero() {
				// 计算周期结束时间和剩余时间
				if market.Timestamp > 0 {
					cycleDuration := getCycleDurationFromMarket(market)
					cycleStartTime := time.Unix(market.Timestamp, 0)
					d.snapshot.CycleEndTime = cycleStartTime.Add(cycleDuration)
					now := time.Now()
					if now.Before(d.snapshot.CycleEndTime) {
						d.snapshot.CycleRemainingSec = d.snapshot.CycleEndTime.Sub(now).Seconds()
					} else {
						d.snapshot.CycleRemainingSec = 0
					}
				}
			}
		}
		// 更新市场 slug（无论是否切换）
		d.snapshot.MarketSlug = market.Slug
	}

	// 更新价格信息（即使为 0 也更新，避免显示旧数据）
	if data != nil {
		d.snapshot.YesPrice = data.YesPrice
		d.snapshot.NoPrice = data.NoPrice
		d.snapshot.YesBid = data.YesBid
		d.snapshot.YesAsk = data.YesAsk
		d.snapshot.NoBid = data.NoBid
		d.snapshot.NoAsk = data.NoAsk

		// 更新速度信息
		d.snapshot.UpVelocity = data.UpVelocity
		d.snapshot.DownVelocity = data.DownVelocity
		d.snapshot.UpMove = data.UpMove
		d.snapshot.DownMove = data.DownMove
		if data.Direction != "" {
			d.snapshot.Direction = data.Direction
		}

		// 更新持仓信息
		if data.PositionState != nil {
			d.snapshot.UpSize = data.PositionState.UpSize
			d.snapshot.DownSize = data.PositionState.DownSize
			d.snapshot.UpCost = data.PositionState.UpCost
			d.snapshot.DownCost = data.PositionState.DownCost
			d.snapshot.UpAvgPrice = data.PositionState.UpAvgPrice
			d.snapshot.DownAvgPrice = data.PositionState.DownAvgPrice
			d.snapshot.IsHedged = data.PositionState.IsHedged
		}

		// 更新盈利信息
		d.snapshot.ProfitIfUpWin = data.ProfitIfUpWin
		d.snapshot.ProfitIfDownWin = data.ProfitIfDownWin
		d.snapshot.TotalCost = data.TotalCost
		d.snapshot.IsProfitLocked = data.IsProfitLocked

		// 更新交易统计
		d.snapshot.TradesThisCycle = data.TradesThisCycle
		d.snapshot.LastTriggerTime = data.LastTriggerTime

		// 更新合并状态
		if data.MergeStatus != "" {
			d.snapshot.MergeStatus = data.MergeStatus
		}
		if data.MergeAmount > 0 {
			d.snapshot.MergeAmount = data.MergeAmount
		}
		if data.MergeTxHash != "" {
			d.snapshot.MergeTxHash = data.MergeTxHash
		}
		if !data.LastMergeTime.IsZero() {
			d.snapshot.LastMergeTime = data.LastMergeTime
		}
		d.snapshot.MergeCount = data.MergeCount

		// 更新赎回状态
		if data.RedeemStatus != "" {
			d.snapshot.RedeemStatus = data.RedeemStatus
		}
		if data.RedeemCount > 0 {
			d.snapshot.RedeemCount = data.RedeemCount
		}
		if !data.LastRedeemTime.IsZero() {
			d.snapshot.LastRedeemTime = data.LastRedeemTime
		}

		// 更新订单状态
		d.snapshot.PendingHedges = data.PendingHedges
		d.snapshot.OpenOrders = data.OpenOrders

		// 更新风控状态
		if data.RiskManagement != nil {
			d.snapshot.RiskManagement = data.RiskManagement
		}

		// 更新决策条件
		// 关键修复：只有当 DecisionConditions 真正变化时才更新，避免因为 CooldownRemaining/WarmupRemaining 的微小变化导致频繁渲染
		if data.DecisionConditions != nil {
			// 比较关键字段，只有当真正变化时才更新
			shouldUpdate := false
			if d.snapshot.DecisionConditions == nil {
				shouldUpdate = true
			} else {
				old := d.snapshot.DecisionConditions
				new := data.DecisionConditions
				// 比较关键字段（不包括实时变化的 CooldownRemaining 和 WarmupRemaining）
				// 关键修复：对浮点数值使用阈值比较，避免微小变化触发频繁渲染
				const floatEpsilon = 0.001 // 浮点数比较阈值
				if old.CanTrade != new.CanTrade ||
					old.BlockReason != new.BlockReason ||
					old.UpVelocityOK != new.UpVelocityOK ||
					absFloat(old.UpVelocityValue-new.UpVelocityValue) > floatEpsilon ||
					old.UpMoveOK != new.UpMoveOK ||
					old.UpMoveValue != new.UpMoveValue ||
					old.DownVelocityOK != new.DownVelocityOK ||
					absFloat(old.DownVelocityValue-new.DownVelocityValue) > floatEpsilon ||
					old.DownMoveOK != new.DownMoveOK ||
					old.DownMoveValue != new.DownMoveValue ||
					old.Direction != new.Direction ||
					old.EntryPriceOK != new.EntryPriceOK ||
					absFloat(old.EntryPriceValue-new.EntryPriceValue) > floatEpsilon ||
					old.HedgePriceOK != new.HedgePriceOK ||
					absFloat(old.HedgePriceValue-new.HedgePriceValue) > floatEpsilon ||
					old.TotalCostOK != new.TotalCostOK ||
					absFloat(old.TotalCostValue-new.TotalCostValue) > floatEpsilon ||
					old.IsProfitLocked != new.IsProfitLocked ||
					absFloat(old.ProfitIfUpWin-new.ProfitIfUpWin) > floatEpsilon ||
					absFloat(old.ProfitIfDownWin-new.ProfitIfDownWin) > floatEpsilon ||
					old.CooldownOK != new.CooldownOK ||
					old.WarmupOK != new.WarmupOK ||
					old.TradesLimitOK != new.TradesLimitOK ||
					old.TradesThisCycle != new.TradesThisCycle ||
					old.HasPendingHedge != new.HasPendingHedge {
					shouldUpdate = true
				} else {
					// 即使关键字段相同，也定期更新 CooldownRemaining 和 WarmupRemaining（但降低频率）
					// 使用取整后的值比较，避免微小变化触发更新
					// 关键修复：只有当倒计时的整数部分真正变化时才更新，减少更新频率
					oldCooldown := int(old.CooldownRemaining)
					newCooldown := int(new.CooldownRemaining)
					oldWarmup := int(old.WarmupRemaining)
					newWarmup := int(new.WarmupRemaining)
					
					// 只有当整数部分变化时才更新（减少更新频率）
					cooldownChanged := oldCooldown != newCooldown
					warmupChanged := oldWarmup != newWarmup
					
					if cooldownChanged || warmupChanged {
						shouldUpdate = true
					} else {
						// 即使整数部分没变化，也更新浮点数值（用于精确显示），但不触发整个对象更新
						// 直接更新字段，不替换整个对象
						d.snapshot.DecisionConditions.CooldownRemaining = new.CooldownRemaining
						d.snapshot.DecisionConditions.WarmupRemaining = new.WarmupRemaining
					}
				}
			}
			
			if shouldUpdate {
				d.snapshot.DecisionConditions = data.DecisionConditions
			} else {
				// 关键字段没变化，但需要更新 CooldownRemaining 和 WarmupRemaining（用于倒计时显示）
				// 直接更新这两个字段，不替换整个对象
				if d.snapshot.DecisionConditions != nil {
					d.snapshot.DecisionConditions.CooldownRemaining = data.DecisionConditions.CooldownRemaining
					d.snapshot.DecisionConditions.WarmupRemaining = data.DecisionConditions.WarmupRemaining
				}
			}
		}

		// 更新周期信息
		if !data.CycleEndTime.IsZero() {
			d.snapshot.CycleEndTime = data.CycleEndTime
		}
		d.snapshot.CycleRemainingSec = data.CycleRemainingSec
	}

	// 发送更新到 UI
	// 关键修复：在发送前，先清空 channel 中的旧数据，确保只保留最新的快照
	// 这样可以避免 UI 显示多个快照导致重复显示
	drained := false
	for !drained {
		select {
		case <-d.updateCh:
			// 清空旧数据
		default:
			drained = true
		}
	}
	
	// 保存快照和 program 的引用（在锁内）
	snapshot := d.snapshot
	program := d.program
	d.mu.Unlock() // 释放锁，避免在发送消息时持有锁

	// 发送最新的快照到 channel
	select {
	case d.updateCh <- snapshot:
		// 成功发送到 channel
		log.Debugf("✅ [Dashboard.UpdateSnapshot] 已发送快照到 channel: market=%s", snapshot.MarketSlug)
	default:
		// channel 仍然满（不应该发生），记录警告
		log.Warnf("⚠️ [Dashboard] 发送快照失败（channel 仍然满）")
	}

	// 同时使用 program.Send() 强制发送更新消息（如果 program 可用）
	// 这样可以确保即使 channel 满了，UI 也能收到更新
	if program != nil {
		updateMsg := UpdateMsg{Snapshot: snapshot}
		program.Send(updateMsg)
		log.Debugf("✅ [Dashboard.UpdateSnapshot] 已通过 program.Send() 发送更新: market=%s", snapshot.MarketSlug)
	}
}

// UpdateData 更新数据
type UpdateData struct {
	YesPrice   float64
	NoPrice    float64
	YesBid     float64
	YesAsk     float64
	NoBid      float64
	NoAsk      float64

	UpVelocity   float64
	DownVelocity float64
	UpMove       int
	DownMove     int
	Direction    string

	PositionState *PositionState

	ProfitIfUpWin   float64
	ProfitIfDownWin float64
	TotalCost        float64
	IsProfitLocked   bool

	TradesThisCycle int
	LastTriggerTime time.Time

	MergeStatus   string
	MergeAmount   float64
	MergeTxHash   string
	LastMergeTime time.Time
	MergeCount    int

	RedeemStatus   string
	RedeemCount    int
	LastRedeemTime time.Time

	PendingHedges int
	OpenOrders    int

	RiskManagement *RiskManagementStatus

	DecisionConditions *DecisionConditions

	CycleEndTime      time.Time
	CycleRemainingSec float64
}

// PositionState 持仓状态（从 brain 模块复制，避免循环导入）
type PositionState struct {
	UpSize      float64
	DownSize    float64
	UpCost      float64
	DownCost    float64
	UpAvgPrice  float64
	DownAvgPrice float64
	IsHedged    bool
}

// Render 渲染仪表板（兼容旧接口，现在由 bubbletea 处理）
func (d *Dashboard) Render() {
	// 这个方法现在由 bubbletea 自动调用，保留是为了兼容性
}

// ForceRender 强制触发UI重绘（通过发送当前快照）
func (d *Dashboard) ForceRender() {
	if !d.enabled {
		return
	}

	d.mu.RLock()
	snapshot := d.snapshot
	nativeTUI := d.nativeTUI
	program := d.program
	d.mu.RUnlock()

	if snapshot == nil {
		snapshot = &Snapshot{}
	}

	// 如果使用原生TUI，直接更新
	if d.useNativeTUI && nativeTUI != nil {
		nativeTUI.UpdateSnapshot(snapshot)
		return
	}

	// Bubble Tea实现
	// 清空 channel 中的旧数据，确保新数据能立即显示
	drained := false
	for !drained {
		select {
		case <-d.updateCh:
			// 清空旧数据
		default:
			drained = true
		}
	}

	// 发送更新到 UI（非阻塞）
	select {
	case d.updateCh <- snapshot:
		// 成功发送到 channel
		log.Debugf("✅ [Dashboard.ForceRender] 已发送快照到 channel: market=%s", snapshot.MarketSlug)
	default:
		// 如果 channel 满了，再次尝试发送（已经清空过了）
		select {
		case d.updateCh <- snapshot:
			log.Debugf("✅ [Dashboard.ForceRender] 已发送快照到 channel（重试）: market=%s", snapshot.MarketSlug)
		default:
			log.Warnf("⚠️ [Dashboard.ForceRender] 发送快照到 channel 失败")
		}
	}

	// 同时使用 program.Send() 强制发送更新消息（如果 program 可用）
	if program != nil {
		// 使用 tea.Send 直接发送消息，不依赖 channel
		// 注意：使用 UpdateMsg 类型（导出的类型）
		updateMsg := UpdateMsg{Snapshot: snapshot}
		program.Send(updateMsg)
		log.Debugf("✅ [Dashboard.ForceRender] 已通过 program.Send() 发送更新: market=%s", snapshot.MarketSlug)
	}
}

// SendUpdate 直接通过 program.Send() 发送更新消息（不依赖 channel）
// 这个方法用于周期切换时强制更新 UI
func (d *Dashboard) SendUpdate() {
	if !d.enabled {
		return
	}

	d.mu.RLock()
	snapshot := d.snapshot
	nativeTUI := d.nativeTUI
	program := d.program
	d.mu.RUnlock()

	if snapshot == nil {
		snapshot = &Snapshot{}
	}

	// 如果使用原生TUI，直接更新
	if d.useNativeTUI && nativeTUI != nil {
		nativeTUI.UpdateSnapshot(snapshot)
		log.Debugf("✅ [Dashboard.SendUpdate] 已通过原生TUI更新: market=%s", snapshot.MarketSlug)
		return
	}

	// Bubble Tea实现
	if program == nil {
		log.Debugf("⚠️ [Dashboard.SendUpdate] program 未初始化，无法发送更新")
		return
	}

	// 直接通过 program.Send() 发送更新消息
	// 使用 UpdateMsg 类型（导出的类型）
	updateMsg := UpdateMsg{Snapshot: snapshot}
	program.Send(updateMsg)
	log.Debugf("✅ [Dashboard.SendUpdate] 已通过 program.Send() 发送更新: market=%s", snapshot.MarketSlug)
}

// Start 启动 Dashboard UI（在独立的 goroutine 中运行）
func (d *Dashboard) Start(ctx context.Context) error {
	if !d.enabled {
		return nil
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	// 如果使用原生TUI
	if d.useNativeTUI {
		if d.nativeTUI != nil {
			// 已经启动，不需要重复启动
			return nil
		}

		// 创建原生TUI
		nativeTUI, err := NewNativeTUI()
		if err != nil {
			return fmt.Errorf("创建原生TUI失败: %w", err)
		}

		d.nativeTUI = nativeTUI

		// 设置退出回调函数，当原生TUI退出时通知Dashboard
		exitCallback := func() {
			select {
			case d.exitCh <- struct{}{}:
				log.Infof("🛑 [Dashboard] 收到原生TUI退出信号")
			default:
				// 通道已满，忽略（不应该发生）
			}
		}
		
		// 启动原生TUI（传入退出回调）
		if err := nativeTUI.Start(ctx, exitCallback); err != nil {
			return fmt.Errorf("启动原生TUI失败: %w", err)
		}

		// 启动系统信号监听（作为备用方案，确保 Ctrl+C 能够退出）
		// 即使 tcell 没有捕获到 Ctrl+C，系统信号也能触发退出
		// 关键修复：必须在设置退出回调之后启动信号监听，确保回调已设置
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)
		log.Infof("✅ [Dashboard] 已启动系统信号监听（SIGINT, SIGTERM, SIGQUIT）")
		
		// 启动退出信号监听 goroutine
		// 关键修复：确保系统信号能够正确触发退出
		go func() {
			defer signal.Stop(sigChan)
			log.Infof("✅ [Dashboard] 退出信号监听 goroutine 已启动")
			select {
			case <-ctx.Done():
				// context 已取消，正常退出
				log.Infof("🛑 [Dashboard] context 已取消，退出信号监听")
				return
			case sig := <-sigChan:
				// 收到系统信号（Ctrl+C 等）
				log.Infof("🛑 [Dashboard] 收到系统信号: %v，通知主程序退出", sig)
				// 调用退出回调（必须在锁外调用，避免死锁）
				d.mu.RLock()
				callback := d.exitCallback
				d.mu.RUnlock()
				if callback != nil {
					log.Infof("🛑 [Dashboard] 调用退出回调（来自系统信号）")
					callback()
				} else {
					log.Errorf("❌ [Dashboard] 退出回调为 nil，无法退出！请检查 SetExitCallback 是否已调用")
					// 即使回调为 nil，也尝试发送到 exitCh
					select {
					case d.exitCh <- struct{}{}:
						log.Infof("🛑 [Dashboard] 已发送退出信号到 exitCh（回调为 nil 的备用方案）")
					default:
						log.Errorf("❌ [Dashboard] exitCh 已满且回调为 nil，无法退出")
					}
				}
				// 同时发送到 exitCh，确保退出
				select {
				case d.exitCh <- struct{}{}:
					log.Infof("🛑 [Dashboard] 已发送退出信号到 exitCh")
				default:
					log.Warnf("⚠️ [Dashboard] exitCh 已满，无法发送退出信号")
				}
				// 强制退出，不等待其他信号
				return
			case <-d.exitCh:
				// 收到原生TUI的退出信号，通知主程序
				log.Infof("🛑 [Dashboard] 原生TUI已退出，通知主程序")
				// 如果设置了退出回调，调用它
				d.mu.RLock()
				callback := d.exitCallback
				d.mu.RUnlock()
				if callback != nil {
					log.Infof("🛑 [Dashboard] 调用退出回调（来自原生TUI）")
					callback()
				} else {
					log.Errorf("❌ [Dashboard] 退出回调为 nil，无法退出！请检查 SetExitCallback 是否已调用")
				}
				// 停止信号监听
				return
			}
		}()

		log.Infof("✅ [Dashboard] 原生TUI已启动")
		return nil
	}

	// 使用Bubble Tea实现
	// 如果已经启动，不要重复启动
	if d.program != nil {
		// 检查 program 是否还在运行
		select {
		case <-d.programDone:
			// program 已退出，需要重新启动
			d.program = nil
		default:
			// program 还在运行，不需要重启
			return nil
		}
	}

	// 重新初始化 programDone channel（如果已关闭）
	select {
	case <-d.programDone:
		// channel 已关闭，重新创建
		d.programDone = make(chan struct{})
	default:
		// channel 未关闭，创建新的
		if d.programDone == nil {
			d.programDone = make(chan struct{})
		}
	}

	// 重定向日志到文件，避免干扰 TUI（仅 logrus，不改动 stdout/stderr）
	// 创建日志目录
	logDir := "logs"
	if err := os.MkdirAll(logDir, 0755); err != nil {
		logDir = os.TempDir()
	}

	// 日志文件路径
	logFile := filepath.Join(logDir, "velocityfollow-dashboard.log")
	d.logFilepath = logFile
	
	// 如果日志文件还没有打开，打开它
	if d.logFile == nil {
		file, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err == nil {
			d.logFile = file
		}
	}
	
	// 立即应用日志重定向（必须在创建 program 之前）
	if d.logFile != nil {
		d.applyLogRedirect()
		log.Infof("日志已重定向到文件: %s", logFile)
	}

	// 启用 bubbletea 调试日志（如果设置了 DEBUG 环境变量）
	if len(os.Getenv("DEBUG")) > 0 {
		debugLogFile := filepath.Join(logDir, "velocityfollow-debug.log")
		if _, err := tea.LogToFile(debugLogFile, "debug"); err != nil {
			log.Warnf("无法创建 bubbletea 调试日志文件: %v", err)
		}
	}

	// 检查是否在交互式终端中运行
	// 如果不是交互式终端（比如CI/CD环境），跳过Dashboard启动
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		log.Warnf("⚠️ 非交互式终端，跳过 Dashboard UI 启动")
		return nil
	}

	// 创建 model
	m := newModel(d.updateCh)

	// 创建 tea program
	d.program = tea.NewProgram(m, tea.WithAltScreen())

	// 启动日志守护 goroutine，定期检查并重新应用日志重定向
	// 防止周期切换时日志系统覆盖重定向设置
	go d.logRedirectGuard(ctx)

	// 在 goroutine 中运行 Bubble Tea program
	// 注意：必须在日志重定向之后启动，确保日志不会干扰 UI
	go func() {
		defer func() {
			// 恢复 panic，避免导致整个程序退出
			if r := recover(); r != nil {
				log.Errorf("Dashboard UI panic: %v", r)
			}
			close(d.programDone)
		}()
		// 稍微延迟一下，确保日志重定向已生效
		time.Sleep(100 * time.Millisecond)
		if _, err := d.program.Run(); err != nil {
			log.Errorf("Dashboard UI 运行错误: %v", err)
		}
	}()

	return nil
}

// applyLogRedirect 应用日志重定向到文件（不输出到终端）
func (d *Dashboard) applyLogRedirect() {
	if d.logFile == nil && d.logFilepath != "" {
		// 如果文件已关闭，重新打开
		file, err := os.OpenFile(d.logFilepath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err == nil {
			d.logFile = file
		} else {
			return
		}
	}
	
	if d.logFile != nil {
		// 设置 logrus 输出到文件（不输出到终端）
		logrus.SetOutput(d.logFile)
		logrus.SetFormatter(&logrus.TextFormatter{
			FullTimestamp:   true,
			TimestampFormat: "2006-01-02 15:04:05",
			DisableColors:   true, // 禁用颜色，因为写入文件
		})

		// 同时更新 pkg/logger 的全局 Logger，避免 INFO[...] 输出到终端
		// 注意：pkg/logger 可能使用 MultiWriter，需要完全重定向到文件
		if logger.Logger != nil {
			// 直接设置输出到文件，不输出到 stdout
			logger.Logger.SetOutput(d.logFile)
			logger.Logger.SetFormatter(&logrus.TextFormatter{
				FullTimestamp:   true,
				TimestampFormat: "2006-01-02 15:04:05",
				DisableColors:   true,
			})
		}
		
		// 确保全局 logrus 也重定向到文件
		logrus.SetOutput(d.logFile)
		logrus.SetFormatter(&logrus.TextFormatter{
			FullTimestamp:   true,
			TimestampFormat: "2006-01-02 15:04:05",
			DisableColors:   true,
		})
	}
}

// ReapplyLogRedirect 重新应用日志重定向（用于周期切换后）
func (d *Dashboard) ReapplyLogRedirect() {
	if !d.enabled {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.applyLogRedirect()
}

// ResetSnapshot 重置快照数据（用于周期切换时重建UI状态）
func (d *Dashboard) ResetSnapshot(market *domain.Market) {
	if !d.enabled {
		return
	}

	d.mu.Lock()
	// 注意：不能使用 defer，因为原生TUI分支需要提前解锁
	// defer d.mu.Unlock()

	// 创建新的快照，完全清空所有旧数据
	d.snapshot = &Snapshot{
		// 重置所有字段为零值
		YesPrice:           0,
		NoPrice:            0,
		YesBid:             0,
		YesAsk:             0,
		NoBid:              0,
		NoAsk:              0,
		UpVelocity:         0,
		DownVelocity:       0,
		UpMove:             0,
		DownMove:           0,
		Direction:           "",
		UpSize:             0,
		DownSize:           0,
		UpCost:             0,
		DownCost:           0,
		UpAvgPrice:         0,
		DownAvgPrice:       0,
		IsHedged:           false,
		ProfitIfUpWin:      0,
		ProfitIfDownWin:    0,
		TotalCost:          0,
		IsProfitLocked:     false,
		TradesThisCycle:    0,
		LastTriggerTime:    time.Time{},
		PendingHedges:      0,
		OpenOrders:         0,
		MergeCount:         0,
		MergeStatus:        "",
		MergeAmount:        0,
		MergeTxHash:        "",
		LastMergeTime:      time.Time{},
		RedeemCount:        0,
		RedeemStatus:        "",
		LastRedeemTime:     time.Time{},
		RiskManagement:      nil,
		DecisionConditions: nil,
		CycleEndTime:       time.Time{},
		CycleRemainingSec:  0,
	}
	
	if market != nil {
		d.snapshot.MarketSlug = market.Slug
		// 计算周期结束时间和剩余时间
		if market.Timestamp > 0 {
			// 从市场信息动态获取周期时长（支持 15m/1h/4h）
			cycleDuration := getCycleDurationFromMarket(market)
			cycleStartTime := time.Unix(market.Timestamp, 0)
			d.snapshot.CycleEndTime = cycleStartTime.Add(cycleDuration)
			now := time.Now()
			if now.Before(d.snapshot.CycleEndTime) {
				d.snapshot.CycleRemainingSec = d.snapshot.CycleEndTime.Sub(now).Seconds()
			} else {
				d.snapshot.CycleRemainingSec = 0
			}
		}
	}

	// 清空 channel 中的旧数据，确保新数据能立即显示
	drained := false
	for !drained {
		select {
		case <-d.updateCh:
			// 清空旧数据
		default:
			// channel 已空
			drained = true
		}
	}

	// 如果使用原生TUI，直接更新
	if d.useNativeTUI && d.nativeTUI != nil {
		// 保存快照引用，在解锁后使用
		snapshot := d.snapshot
		// 手动解锁（不使用 defer，因为需要在解锁后调用 UpdateSnapshot）
		d.mu.Unlock()
		d.nativeTUI.UpdateSnapshot(snapshot)
		log.Debugf("✅ [Dashboard] 已重置快照并发送更新（原生TUI）: market=%s", getMarketSlug(market))
		return
	}

	// Bubble Tea实现
	// 发送重置后的快照到 UI（确保立即更新）
	select {
	case d.updateCh <- d.snapshot:
		log.Debugf("✅ [Dashboard] 已重置快照并发送更新: market=%s", getMarketSlug(market))
	default:
		// 如果 channel 满了，强制发送（清空后重试）
		// 这种情况不应该发生，但为了安全起见
		log.Warnf("⚠️ [Dashboard] 重置快照时 channel 已满，强制清空后重试")
		// 再次清空并发送
		for {
			select {
			case <-d.updateCh:
			default:
				goto send
			}
		}
	send:
		select {
		case d.updateCh <- d.snapshot:
		default:
			log.Warnf("⚠️ [Dashboard] 重置快照发送失败")
		}
	}
	// 手动解锁（Bubble Tea分支）
	d.mu.Unlock()
}

// getMarketSlug 获取市场 slug（安全处理 nil）
func getMarketSlug(market *domain.Market) string {
	if market == nil {
		return "<nil>"
	}
	return market.Slug
}

// logRedirectGuard 日志重定向守护 goroutine
// 定期检查并重新应用日志重定向，防止日志系统覆盖设置
func (d *Dashboard) logRedirectGuard(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond) // 每500ms检查一次，更频繁
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-d.stopLogGuard:
			return
		case <-ticker.C:
			if d.enabled && d.logFile != nil {
				// 直接重新应用重定向，确保日志系统覆盖后能立即恢复
				d.mu.Lock()
				d.applyLogRedirect()
				d.mu.Unlock()
			}
		}
	}
}

// Stop 停止 Dashboard UI
// 注意：周期切换时不应该调用 Stop，Dashboard 应该持续运行
func (d *Dashboard) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()

	// 停止日志守护 goroutine
	select {
	case <-d.stopLogGuard:
		// 已经关闭了
	default:
		close(d.stopLogGuard)
	}
	
	// 如果使用原生TUI
	if d.useNativeTUI && d.nativeTUI != nil {
		d.nativeTUI.Stop()
		d.nativeTUI = nil
		return
	}
	
	// 停止 Bubble Tea program
	if d.program != nil {
		d.program.Quit()
		// 等待 program goroutine 退出（最多等待 1 秒）
		select {
		case <-d.programDone:
			// program 已退出
		case <-time.After(1 * time.Second):
			// 超时，强制退出
			log.Warnf("Dashboard program 退出超时，强制关闭")
		}
		d.program = nil
	}
	
	// 注意：不恢复日志输出到终端，因为 Dashboard 可能在周期切换后继续运行
	// 只有在完全关闭时才恢复日志输出
}

// GetSnapshot 获取快照（线程安全）
func (d *Dashboard) GetSnapshot() *Snapshot {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if d.snapshot == nil {
		return &Snapshot{}
	}

	// 返回副本
	return &Snapshot{
		MarketSlug:      d.snapshot.MarketSlug,
		YesPrice:        d.snapshot.YesPrice,
		NoPrice:         d.snapshot.NoPrice,
		YesBid:          d.snapshot.YesBid,
		YesAsk:          d.snapshot.YesAsk,
		NoBid:           d.snapshot.NoBid,
		NoAsk:           d.snapshot.NoAsk,
		UpVelocity:      d.snapshot.UpVelocity,
		DownVelocity:    d.snapshot.DownVelocity,
		UpMove:          d.snapshot.UpMove,
		DownMove:        d.snapshot.DownMove,
		Direction:       d.snapshot.Direction,
		UpSize:          d.snapshot.UpSize,
		DownSize:        d.snapshot.DownSize,
		UpCost:          d.snapshot.UpCost,
		DownCost:        d.snapshot.DownCost,
		UpAvgPrice:      d.snapshot.UpAvgPrice,
		DownAvgPrice:    d.snapshot.DownAvgPrice,
		IsHedged:        d.snapshot.IsHedged,
		ProfitIfUpWin:   d.snapshot.ProfitIfUpWin,
		ProfitIfDownWin: d.snapshot.ProfitIfDownWin,
		TotalCost:       d.snapshot.TotalCost,
		IsProfitLocked:  d.snapshot.IsProfitLocked,
		TradesThisCycle: d.snapshot.TradesThisCycle,
		LastTriggerTime: d.snapshot.LastTriggerTime,
		MergeStatus:     d.snapshot.MergeStatus,
		MergeAmount:     d.snapshot.MergeAmount,
		MergeTxHash:     d.snapshot.MergeTxHash,
		LastMergeTime:   d.snapshot.LastMergeTime,
		RedeemStatus:    d.snapshot.RedeemStatus,
		RedeemCount:     d.snapshot.RedeemCount,
		LastRedeemTime:  d.snapshot.LastRedeemTime,
		PendingHedges:   d.snapshot.PendingHedges,
		OpenOrders:      d.snapshot.OpenOrders,
		RiskManagement:  d.snapshot.RiskManagement, // 注意：这里直接引用，因为 RiskManagement 本身是只读的
		DecisionConditions: d.snapshot.DecisionConditions,
		CycleEndTime:    d.snapshot.CycleEndTime,
		CycleRemainingSec: d.snapshot.CycleRemainingSec,
	}
}

// formatDuration 格式化时长
func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	minutes := int(d.Minutes())
	seconds := int(d.Seconds()) % 60
	return fmt.Sprintf("%dm%ds", minutes, seconds)
}

// truncate 截断字符串
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
