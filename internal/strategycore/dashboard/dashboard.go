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
func getCycleDurationFromMarket(market *domain.Market) time.Duration {
	if market == nil || market.Slug == "" {
		return 15 * time.Minute
	}

	slug := market.Slug
	parts := strings.Split(slug, "-")
	if len(parts) >= 3 {
		timeframeStr := parts[2]
		tf, err := marketspec.ParseTimeframe(timeframeStr)
		if err == nil {
			return tf.Duration()
		}
	}

	slugLower := strings.ToLower(slug)
	if strings.Contains(slugLower, "am") || strings.Contains(slugLower, "pm") {
		if strings.HasSuffix(slugLower, "-et") || strings.Contains(slugLower, "-et-") {
			return 1 * time.Hour
		}
	}
	months := []string{"january", "february", "march", "april", "may", "june",
		"july", "august", "september", "october", "november", "december"}
	for _, month := range months {
		if strings.Contains(slugLower, month) {
			return 1 * time.Hour
		}
	}
	return 15 * time.Minute
}

// Snapshot 仪表板快照数据
type Snapshot struct {
	// UI 标题（策略名/看板名）
	Title string

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
	UpSize       float64
	DownSize     float64
	UpCost       float64
	DownCost     float64
	UpAvgPrice   float64
	DownAvgPrice float64
	IsHedged     bool

	// 盈利信息
	ProfitIfUpWin    float64
	ProfitIfDownWin  float64
	TotalCost        float64
	IsProfitLocked   bool

	// 交易统计
	TradesThisCycle int
	LastTriggerTime time.Time

	// 合并状态
	MergeStatus   string
	MergeAmount   float64
	MergeTxHash   string
	LastMergeTime time.Time
	MergeCount    int

	// 赎回状态
	RedeemStatus   string
	RedeemCount    int
	LastRedeemTime time.Time

	// 订单状态
	PendingHedges int
	OpenOrders    int

	// OMS 运行指标（职业交易系统可观测性）
	OMSQueueLen        int
	HedgeEWMASec       float64
	ReorderBudgetSkips int64
	FAKBudgetWarnings  int64

	MarketCooldownRemainingSec float64
	MarketCooldownReason       string

	// 风控状态
	RiskManagement *RiskManagementStatus

	// 决策条件
	DecisionConditions *DecisionConditions

	// Gate 状态（如盘口质量/价格稳定性），由具体策略写入
	GateAllowed bool
	GateReason  string

	// 价格盯盘状态（实时盯盘协程信息）
	PriceStopWatches *PriceStopWatchesStatus

	// 周期信息
	CycleEndTime      time.Time
	CycleRemainingSec float64
}

// RiskManagementStatus 风控状态信息
type RiskManagementStatus struct {
	RiskExposuresCount int
	RiskExposures      []RiskExposureInfo

	CurrentAction      string
	CurrentActionEntry string
	CurrentActionHedge string
	CurrentActionTime  time.Time
	CurrentActionDesc  string

	TotalReorders         int
	TotalAggressiveHedges int
	TotalFakEats          int

	RepriceOldPriceCents     int
	RepriceNewPriceCents     int
	RepricePriceChangeCents  int
	RepriceStrategy          string
	RepriceEntryCostCents    int
	RepriceMarketAskCents    int
	RepriceIdealPriceCents   int
	RepriceTotalCostCents    int
	RepriceProfitCents       int
}

type RiskExposureInfo struct {
	EntryOrderID    string
	EntryTokenType  string
	EntrySize       float64
	EntryPriceCents int
	HedgeOrderID    string
	HedgeStatus     string
	ExposureSeconds float64
	MaxLossCents    int

	OriginalHedgePriceCents int
	NewHedgePriceCents      int
	CountdownSeconds        float64
}

// PriceStopWatchesStatus 价格盯盘状态信息
type PriceStopWatchesStatus struct {
	Enabled          bool
	ActiveWatches    int
	WatchDetails     []PriceStopWatchInfo
	SoftLossCents    int
	HardLossCents    int
	TakeProfitCents  int
	ConfirmTicks     int
	LastEvalTime     time.Time
}

// PriceStopWatchInfo 单个价格盯盘协程的详细信息
type PriceStopWatchInfo struct {
	EntryOrderID     string
	EntryTokenType   string
	EntryPriceCents  int
	EntrySize        float64
	HedgeOrderID     string
	CurrentProfitCents int
	SoftHits         int
	TakeProfitHits   int
	LastEvalTime     time.Time
	Status           string // "monitoring" | "triggered" | "completed"
}

// DecisionConditions 决策条件状态（复制结构，避免循环导入）
type DecisionConditions struct {
	UpVelocityOK       bool
	UpVelocityValue    float64
	UpMoveOK           bool
	UpMoveValue        int
	DownVelocityOK     bool
	DownVelocityValue  float64
	DownMoveOK         bool
	DownMoveValue      int
	Direction          string

	EntryPriceOK       bool
	EntryPriceValue    float64
	EntryPriceMin      float64
	EntryPriceMax      float64
	TotalCostOK        bool
	TotalCostValue     float64
	HedgePriceOK       bool
	HedgePriceValue    float64

	HasUnhedgedRisk    bool
	IsProfitLocked     bool
	ProfitIfUpWin      float64
	ProfitIfDownWin    float64

	CooldownOK         bool
	CooldownRemaining  float64
	WarmupOK           bool
	WarmupRemaining    float64
	TradesLimitOK      bool
	TradesThisCycle    int
	MaxTradesPerCycle  int

	MarketValid        bool
	HasPendingHedge    bool

	CanTrade           bool
	BlockReason        string
}

// Dashboard 仪表板
type Dashboard struct {
	tradingService *services.TradingService
	mu             sync.RWMutex
	snapshot       *Snapshot
	enabled        bool
	useNativeTUI   bool

	// strategy-level options
	opts Options
	title string

	program   *tea.Program
	nativeTUI *NativeTUI
	updateCh  chan *Snapshot

	logFile      *os.File
	logFilepath  string
	stopLogGuard chan struct{}
	programDone  chan struct{}

	exitCh        chan struct{}
	exitCallback  func()
	stopRequested bool
}

// New 创建 Dashboard（核心实现）
// 注意：useNativeTUI 默认值由调用方（策略配置层）控制。
func New(ts *services.TradingService, opts Options, useNativeTUI bool) *Dashboard {
	if strings.TrimSpace(opts.StrategyID) == "" {
		opts.StrategyID = "strategy"
	}
	if strings.TrimSpace(opts.Title) == "" {
		opts.Title = "Strategy Dashboard"
	}

	d := &Dashboard{
		tradingService: ts,
		opts:           opts,
		title:          opts.Title,
		snapshot:       &Snapshot{Title: opts.Title},
		enabled:        true,
		useNativeTUI:   useNativeTUI,
		updateCh:       make(chan *Snapshot, 10),
		stopLogGuard:   make(chan struct{}),
		programDone:    make(chan struct{}),
		exitCh:         make(chan struct{}, 1),
	}

	// 初始化日志文件（避免 TUI 被 stdout 日志打乱）
	logDir := "logs"
	if err := os.MkdirAll(logDir, 0755); err != nil {
		logDir = os.TempDir()
	}
	logFile := filepath.Join(logDir, fmt.Sprintf("%s-dashboard.log", opts.StrategyID))
	d.logFilepath = logFile
	if file, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666); err == nil {
		d.logFile = file
		d.applyLogRedirect()
	}

	return d
}

func (d *Dashboard) SetTitle(title string) {
	if d == nil || strings.TrimSpace(title) == "" {
		return
	}
	d.mu.Lock()
	d.title = title
	if d.snapshot != nil {
		d.snapshot.Title = title
	}
	d.mu.Unlock()
}

func (d *Dashboard) SetEnabled(enabled bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.enabled = enabled
}

func (d *Dashboard) SetExitCallback(callback func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.exitCallback = callback
}

func (d *Dashboard) applyLogRedirect() {
	if d.logFile == nil && d.logFilepath != "" {
		file, err := os.OpenFile(d.logFilepath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err == nil {
			d.logFile = file
		} else {
			return
		}
	}
	if d.logFile == nil {
		return
	}

	// 关键修复：只使用 dashboard 日志文件，不包含 stdout，避免日志输出到终端打乱 UI
	// 原有的日志系统（pkg/logger）已经配置了文件输出，会继续独立写入其配置的文件
	// Dashboard 只负责写入自己的日志文件
	// 注意：不修改 logger.Logger 的输出，让它继续使用原有的配置（包含文件输出）
	
	// 只使用 dashboard 日志文件（不包含 stdout）
	logrus.SetOutput(d.logFile)
	logrus.SetFormatter(&logrus.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: "2006-01-02 15:04:05",
		DisableColors:   true,
	})
	// 注意：不修改 logger.Logger 的输出，让它继续使用原有的配置
	// 这样原有的日志文件（bot_*.log, market logs）会继续由 logger 系统写入
	if logger.Logger != nil {
		// logger.Logger 保持原有输出配置，不修改
		// 这样原有日志文件会继续写入，且不会输出到终端（因为 logger 的配置可能已经排除了 stdout）
	}
}

func (d *Dashboard) ReapplyLogRedirect() {
	if d == nil || !d.enabled {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.applyLogRedirect()
}

// CheckAndResetOnMarketChange 检测 market 切换并清空快照
func (d *Dashboard) CheckAndResetOnMarketChange(market *domain.Market) bool {
	if !d.enabled || market == nil {
		return false
	}

	d.mu.Lock()
	if d.snapshot != nil && d.snapshot.MarketSlug != "" && d.snapshot.MarketSlug != market.Slug {
		d.snapshot = &Snapshot{Title: d.title, MarketSlug: market.Slug}
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

		// drain channel
		for {
			select {
			case <-d.updateCh:
			default:
				goto doneDrain
			}
		}
	doneDrain:

		// 原生：直接推送
		if d.useNativeTUI && d.nativeTUI != nil {
			snap := d.snapshot
			native := d.nativeTUI
			d.mu.Unlock()
			native.UpdateSnapshot(snap)
			return true
		}

		select {
		case d.updateCh <- d.snapshot:
		default:
		}
		d.mu.Unlock()
		return true
	}
	d.mu.Unlock()
	return false
}

// UpdateData 更新数据（保持与原实现一致）
type UpdateData struct {
	YesPrice float64
	NoPrice  float64
	YesBid   float64
	YesAsk   float64
	NoBid    float64
	NoAsk    float64

	UpVelocity   float64
	DownVelocity float64
	UpMove       int
	DownMove     int
	Direction    string

	PositionState *PositionState

	ProfitIfUpWin    float64
	ProfitIfDownWin  float64
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

	OMSQueueLen        int
	HedgeEWMASec       float64
	ReorderBudgetSkips int64
	FAKBudgetWarnings  int64

	MarketCooldownRemainingSec float64
	MarketCooldownReason       string

	RiskManagement     *RiskManagementStatus
	DecisionConditions *DecisionConditions

	// Gate 状态（如盘口质量/价格稳定性），由具体策略写入
	GateAllowed bool
	GateReason  string

	// 价格盯盘状态（实时盯盘协程信息）
	PriceStopWatches *PriceStopWatchesStatus

	CycleEndTime      time.Time
	CycleRemainingSec float64
}

type PositionState struct {
	UpSize       float64
	DownSize     float64
	UpCost       float64
	DownCost     float64
	UpAvgPrice   float64
	DownAvgPrice float64
	IsHedged     bool
}

// UpdateSnapshot 更新快照（包含原生与 Bubble Tea 两种分支）
func (d *Dashboard) UpdateSnapshot(ctx context.Context, market *domain.Market, data *UpdateData) {
	_ = ctx
	if !d.enabled {
		return
	}

	// 原生：直接更新并推送
	if d.useNativeTUI {
		d.mu.Lock()
		if d.snapshot == nil {
			d.snapshot = &Snapshot{}
		}
		d.snapshot.Title = d.title

		if market != nil {
			d.snapshot.MarketSlug = market.Slug
		}
		if data != nil {
			d.snapshot.YesPrice = data.YesPrice
			d.snapshot.NoPrice = data.NoPrice
			d.snapshot.YesBid = data.YesBid
			d.snapshot.YesAsk = data.YesAsk
			d.snapshot.NoBid = data.NoBid
			d.snapshot.NoAsk = data.NoAsk

			d.snapshot.UpVelocity = data.UpVelocity
			d.snapshot.DownVelocity = data.DownVelocity
			d.snapshot.UpMove = data.UpMove
			d.snapshot.DownMove = data.DownMove
			d.snapshot.Direction = data.Direction

			if data.PositionState != nil {
				d.snapshot.UpSize = data.PositionState.UpSize
				d.snapshot.DownSize = data.PositionState.DownSize
				d.snapshot.UpCost = data.PositionState.UpCost
				d.snapshot.DownCost = data.PositionState.DownCost
				d.snapshot.UpAvgPrice = data.PositionState.UpAvgPrice
				d.snapshot.DownAvgPrice = data.PositionState.DownAvgPrice
				d.snapshot.IsHedged = data.PositionState.IsHedged
			} else {
				d.snapshot.UpSize = 0
				d.snapshot.DownSize = 0
				d.snapshot.UpCost = 0
				d.snapshot.DownCost = 0
				d.snapshot.UpAvgPrice = 0
				d.snapshot.DownAvgPrice = 0
				d.snapshot.IsHedged = false
			}

			d.snapshot.ProfitIfUpWin = data.ProfitIfUpWin
			d.snapshot.ProfitIfDownWin = data.ProfitIfDownWin
			d.snapshot.TotalCost = data.TotalCost
			d.snapshot.IsProfitLocked = data.IsProfitLocked

			d.snapshot.TradesThisCycle = data.TradesThisCycle
			d.snapshot.LastTriggerTime = data.LastTriggerTime

			d.snapshot.MergeStatus = data.MergeStatus
			d.snapshot.MergeAmount = data.MergeAmount
			d.snapshot.MergeTxHash = data.MergeTxHash
			d.snapshot.LastMergeTime = data.LastMergeTime
			d.snapshot.MergeCount = data.MergeCount

			d.snapshot.RedeemStatus = data.RedeemStatus
			d.snapshot.RedeemCount = data.RedeemCount
			d.snapshot.LastRedeemTime = data.LastRedeemTime

			d.snapshot.PendingHedges = data.PendingHedges
			d.snapshot.OpenOrders = data.OpenOrders
			d.snapshot.OMSQueueLen = data.OMSQueueLen
			d.snapshot.HedgeEWMASec = data.HedgeEWMASec
			d.snapshot.ReorderBudgetSkips = data.ReorderBudgetSkips
			d.snapshot.FAKBudgetWarnings = data.FAKBudgetWarnings
			d.snapshot.MarketCooldownRemainingSec = data.MarketCooldownRemainingSec
			d.snapshot.MarketCooldownReason = data.MarketCooldownReason

			d.snapshot.RiskManagement = data.RiskManagement
			if data.DecisionConditions != nil {
				d.updateDecisionConditionsLocked(data.DecisionConditions)
			} else {
				d.snapshot.DecisionConditions = nil
			}

			// Gate 状态
			if data.GateReason != "" || data.GateAllowed {
				d.snapshot.GateAllowed = data.GateAllowed
				d.snapshot.GateReason = data.GateReason
			}

			d.snapshot.PriceStopWatches = data.PriceStopWatches

			d.snapshot.CycleEndTime = data.CycleEndTime
			d.snapshot.CycleRemainingSec = data.CycleRemainingSec
		}

		snap := d.snapshot
		native := d.nativeTUI
		d.mu.Unlock()
		if native != nil {
			native.UpdateSnapshot(snap)
		}
		return
	}

	// Bubble Tea
	d.mu.Lock()
	if d.snapshot == nil {
		d.snapshot = &Snapshot{}
	}
	d.snapshot.Title = d.title
	if market != nil {
		d.snapshot.MarketSlug = market.Slug
	}
	if data != nil {
		d.snapshot.YesPrice = data.YesPrice
		d.snapshot.NoPrice = data.NoPrice
		d.snapshot.YesBid = data.YesBid
		d.snapshot.YesAsk = data.YesAsk
		d.snapshot.NoBid = data.NoBid
		d.snapshot.NoAsk = data.NoAsk

		d.snapshot.UpVelocity = data.UpVelocity
		d.snapshot.DownVelocity = data.DownVelocity
		d.snapshot.UpMove = data.UpMove
		d.snapshot.DownMove = data.DownMove
		d.snapshot.Direction = data.Direction

		if data.PositionState != nil {
			d.snapshot.UpSize = data.PositionState.UpSize
			d.snapshot.DownSize = data.PositionState.DownSize
			d.snapshot.UpCost = data.PositionState.UpCost
			d.snapshot.DownCost = data.PositionState.DownCost
			d.snapshot.UpAvgPrice = data.PositionState.UpAvgPrice
			d.snapshot.DownAvgPrice = data.PositionState.DownAvgPrice
			d.snapshot.IsHedged = data.PositionState.IsHedged
		}

		d.snapshot.ProfitIfUpWin = data.ProfitIfUpWin
		d.snapshot.ProfitIfDownWin = data.ProfitIfDownWin
		d.snapshot.TotalCost = data.TotalCost
		d.snapshot.IsProfitLocked = data.IsProfitLocked

		d.snapshot.TradesThisCycle = data.TradesThisCycle
		d.snapshot.LastTriggerTime = data.LastTriggerTime

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

		if data.RedeemStatus != "" {
			d.snapshot.RedeemStatus = data.RedeemStatus
		}
		if data.RedeemCount > 0 {
			d.snapshot.RedeemCount = data.RedeemCount
		}
		if !data.LastRedeemTime.IsZero() {
			d.snapshot.LastRedeemTime = data.LastRedeemTime
		}

		d.snapshot.PendingHedges = data.PendingHedges
		d.snapshot.OpenOrders = data.OpenOrders
		d.snapshot.OMSQueueLen = data.OMSQueueLen
		d.snapshot.HedgeEWMASec = data.HedgeEWMASec
		d.snapshot.ReorderBudgetSkips = data.ReorderBudgetSkips
		d.snapshot.FAKBudgetWarnings = data.FAKBudgetWarnings
		d.snapshot.MarketCooldownRemainingSec = data.MarketCooldownRemainingSec
		d.snapshot.MarketCooldownReason = data.MarketCooldownReason

		if data.RiskManagement != nil {
			d.snapshot.RiskManagement = data.RiskManagement
		}
		if data.DecisionConditions != nil {
			d.updateDecisionConditionsLocked(data.DecisionConditions)
		}
		// Gate 状态
		if data.GateReason != "" || data.GateAllowed {
			d.snapshot.GateAllowed = data.GateAllowed
			d.snapshot.GateReason = data.GateReason
		}
		d.snapshot.PriceStopWatches = data.PriceStopWatches
		if !data.CycleEndTime.IsZero() {
			d.snapshot.CycleEndTime = data.CycleEndTime
		}
		d.snapshot.CycleRemainingSec = data.CycleRemainingSec
	}

	// drain + send latest snapshot
	for {
		select {
		case <-d.updateCh:
		default:
			goto drained
		}
	}
drained:
	snap := d.snapshot
	prog := d.program
	d.mu.Unlock()

	select {
	case d.updateCh <- snap:
	default:
	}
	if prog != nil {
		prog.Send(UpdateMsg{Snapshot: snap})
	}
}

// updateDecisionConditionsLocked 降低刷新频率（避免狂闪）
func (d *Dashboard) updateDecisionConditionsLocked(in *DecisionConditions) {
	if d == nil {
		return
	}
	if in == nil {
		d.snapshot.DecisionConditions = nil
		return
	}
	if d.snapshot.DecisionConditions == nil {
		d.snapshot.DecisionConditions = in
		return
	}

	old := d.snapshot.DecisionConditions
	new := in

	const floatEpsilon = 0.001
	changed := old.CanTrade != new.CanTrade ||
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
		old.HasPendingHedge != new.HasPendingHedge

	if changed {
		d.snapshot.DecisionConditions = new
		return
	}
	if int(old.CooldownRemaining) != int(new.CooldownRemaining) {
		old.CooldownRemaining = new.CooldownRemaining
	}
	if int(old.WarmupRemaining) != int(new.WarmupRemaining) {
		old.WarmupRemaining = new.WarmupRemaining
	}
}

func (d *Dashboard) Render() {}

func (d *Dashboard) ForceRender() {
	if !d.enabled {
		return
	}

	d.mu.RLock()
	snap := d.snapshot
	native := d.nativeTUI
	prog := d.program
	d.mu.RUnlock()

	if snap == nil {
		snap = &Snapshot{Title: d.title}
	}
	if d.useNativeTUI && native != nil {
		native.UpdateSnapshot(snap)
		return
	}
	for {
		select {
		case <-d.updateCh:
		default:
			goto drained
		}
	}
drained:
	select {
	case d.updateCh <- snap:
	default:
	}
	if prog != nil {
		prog.Send(UpdateMsg{Snapshot: snap})
	}
}

func (d *Dashboard) SendUpdate() {
	if !d.enabled {
		return
	}
	d.mu.RLock()
	snap := d.snapshot
	native := d.nativeTUI
	prog := d.program
	d.mu.RUnlock()
	if snap == nil {
		snap = &Snapshot{Title: d.title}
	}
	if d.useNativeTUI && native != nil {
		native.UpdateSnapshot(snap)
		return
	}
	if prog == nil {
		return
	}
	prog.Send(UpdateMsg{Snapshot: snap})
}

func (d *Dashboard) Start(ctx context.Context) error {
	if !d.enabled {
		return nil
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.useNativeTUI {
		if d.nativeTUI != nil {
			return nil
		}
		nativeTUI, err := NewNativeTUI()
		if err != nil {
			return fmt.Errorf("创建原生TUI失败: %w", err)
		}
		d.nativeTUI = nativeTUI

		exitCallback := func() {
			select {
			case d.exitCh <- struct{}{}:
			default:
			}
		}
		if err := nativeTUI.Start(ctx, exitCallback); err != nil {
			return fmt.Errorf("启动原生TUI失败: %w", err)
		}

		// 备用：系统信号监听
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)
		go func() {
			defer signal.Stop(sigChan)
			select {
			case <-ctx.Done():
				return
			case <-sigChan:
				d.mu.RLock()
				cb := d.exitCallback
				d.mu.RUnlock()
				if cb != nil {
					cb()
				}
				select {
				case d.exitCh <- struct{}{}:
				default:
				}
				return
			case <-d.exitCh:
				d.mu.RLock()
				cb := d.exitCallback
				d.mu.RUnlock()
				if cb != nil {
					cb()
				}
				return
			}
		}()

		return nil
	}

	// Bubble Tea
	if d.program != nil {
		select {
		case <-d.programDone:
			d.program = nil
		default:
			return nil
		}
	}
	select {
	case <-d.programDone:
		d.programDone = make(chan struct{})
	default:
		if d.programDone == nil {
			d.programDone = make(chan struct{})
		}
	}

	if d.logFile != nil {
		d.applyLogRedirect()
	}
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return nil
	}

	m := newModel(d.updateCh)
	d.program = tea.NewProgram(m, tea.WithAltScreen())

	go d.logRedirectGuard(ctx)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Errorf("Dashboard UI panic: %v", r)
			}
			close(d.programDone)
		}()
		time.Sleep(100 * time.Millisecond)
		_, runErr := d.program.Run()
		if runErr != nil {
			log.Errorf("Dashboard UI 运行错误: %v", runErr)
		}

		d.mu.RLock()
		stopRequested := d.stopRequested
		cb := d.exitCallback
		d.mu.RUnlock()
		if !stopRequested && cb != nil {
			cb()
		}
	}()
	return nil
}

func (d *Dashboard) logRedirectGuard(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.stopLogGuard:
			return
		case <-ticker.C:
			if d.enabled && d.logFile != nil {
				d.mu.Lock()
				d.applyLogRedirect()
				d.mu.Unlock()
			}
		}
	}
}

func (d *Dashboard) ResetSnapshot(market *domain.Market) {
	if !d.enabled {
		return
	}
	d.mu.Lock()
	d.snapshot = &Snapshot{Title: d.title}
	if market != nil {
		d.snapshot.MarketSlug = market.Slug
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
	for {
		select {
		case <-d.updateCh:
		default:
			goto drained
		}
	}
drained:
	if d.useNativeTUI && d.nativeTUI != nil {
		snap := d.snapshot
		d.mu.Unlock()
		d.nativeTUI.UpdateSnapshot(snap)
		return
	}
	select {
	case d.updateCh <- d.snapshot:
	default:
	}
	d.mu.Unlock()
}

func (d *Dashboard) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()

	select {
	case <-d.stopLogGuard:
	default:
		close(d.stopLogGuard)
	}

	if d.useNativeTUI && d.nativeTUI != nil {
		d.nativeTUI.Stop()
		d.nativeTUI = nil
		return
	}

	if d.program != nil {
		d.stopRequested = true
		d.program.Quit()
		select {
		case <-d.programDone:
		case <-time.After(1 * time.Second):
		}
		d.program = nil
	}
}

func (d *Dashboard) GetSnapshot() *Snapshot {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.snapshot == nil {
		return &Snapshot{Title: d.title}
	}
	return &Snapshot{
		Title:             d.snapshot.Title,
		MarketSlug:         d.snapshot.MarketSlug,
		YesPrice:           d.snapshot.YesPrice,
		NoPrice:            d.snapshot.NoPrice,
		YesBid:             d.snapshot.YesBid,
		YesAsk:             d.snapshot.YesAsk,
		NoBid:              d.snapshot.NoBid,
		NoAsk:              d.snapshot.NoAsk,
		UpVelocity:         d.snapshot.UpVelocity,
		DownVelocity:       d.snapshot.DownVelocity,
		UpMove:             d.snapshot.UpMove,
		DownMove:           d.snapshot.DownMove,
		Direction:          d.snapshot.Direction,
		UpSize:             d.snapshot.UpSize,
		DownSize:           d.snapshot.DownSize,
		UpCost:             d.snapshot.UpCost,
		DownCost:           d.snapshot.DownCost,
		UpAvgPrice:         d.snapshot.UpAvgPrice,
		DownAvgPrice:       d.snapshot.DownAvgPrice,
		IsHedged:           d.snapshot.IsHedged,
		ProfitIfUpWin:      d.snapshot.ProfitIfUpWin,
		ProfitIfDownWin:    d.snapshot.ProfitIfDownWin,
		TotalCost:          d.snapshot.TotalCost,
		IsProfitLocked:     d.snapshot.IsProfitLocked,
		TradesThisCycle:    d.snapshot.TradesThisCycle,
		LastTriggerTime:    d.snapshot.LastTriggerTime,
		MergeStatus:        d.snapshot.MergeStatus,
		MergeAmount:        d.snapshot.MergeAmount,
		MergeTxHash:        d.snapshot.MergeTxHash,
		LastMergeTime:      d.snapshot.LastMergeTime,
		MergeCount:         d.snapshot.MergeCount,
		RedeemStatus:       d.snapshot.RedeemStatus,
		RedeemCount:        d.snapshot.RedeemCount,
		LastRedeemTime:     d.snapshot.LastRedeemTime,
		PendingHedges:      d.snapshot.PendingHedges,
		OpenOrders:         d.snapshot.OpenOrders,
		OMSQueueLen:        d.snapshot.OMSQueueLen,
		HedgeEWMASec:       d.snapshot.HedgeEWMASec,
		ReorderBudgetSkips: d.snapshot.ReorderBudgetSkips,
		FAKBudgetWarnings:  d.snapshot.FAKBudgetWarnings,
		MarketCooldownRemainingSec: d.snapshot.MarketCooldownRemainingSec,
		MarketCooldownReason:       d.snapshot.MarketCooldownReason,
		RiskManagement:     d.snapshot.RiskManagement,
		DecisionConditions: d.snapshot.DecisionConditions,
		GateAllowed:        d.snapshot.GateAllowed,
		GateReason:         d.snapshot.GateReason,
		CycleEndTime:       d.snapshot.CycleEndTime,
		CycleRemainingSec:  d.snapshot.CycleRemainingSec,
	}
}

// formatDuration 格式化时长
func formatDuration(dur time.Duration) string {
	if dur < time.Second {
		return fmt.Sprintf("%dms", dur.Milliseconds())
	}
	if dur < time.Minute {
		return fmt.Sprintf("%.1fs", dur.Seconds())
	}
	minutes := int(dur.Minutes())
	seconds := int(dur.Seconds()) % 60
	return fmt.Sprintf("%dm%ds", minutes, seconds)
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

