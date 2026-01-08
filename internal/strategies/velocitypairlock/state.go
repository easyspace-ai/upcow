package velocitypairlock

import (
	"context"
	"sync"
	"time"

	"github.com/betbot/gobet/internal/common"
	"github.com/betbot/gobet/internal/domain"
)

type pairPhase string

const (
	phaseIdle     pairPhase = "idle"
	phasePlacing  pairPhase = "placing"
	phaseOpen     pairPhase = "open"     // 并发模式：两边订单都已成功创建（可能尚未成交）
	phasePrimaryOpen pairPhase = "primary_open" // 顺序模式：主 leg 已下单（等待成交）
	phaseHedgeOpen   pairPhase = "hedge_open"   // 顺序模式：对冲 leg 已下单（等待成交）
	phaseFilled   pairPhase = "filled"   // 两边都成交
	phaseMerging  pairPhase = "merging"  // 正在 merge complete sets
	phaseCooldown pairPhase = "cooldown" // 冷却/等待下一次
)

type stopLevel string

const (
	stopNone stopLevel = "none"
	stopSoft stopLevel = "soft"
	stopHard stopLevel = "hard"
)

type pairRuntime struct {
	phase pairPhase

	// 当前 market（用于 autoMerge）
	market *domain.Market

	// 并发模式：两边订单
	upOrderID   string
	downOrderID string
	upFilled    bool
	downFilled  bool

	// 顺序模式：主/对冲 legs
	primaryToken      domain.TokenType
	primaryOrderID    string
	primaryFilled     bool
	primaryFillCents  int
	primaryFillSize   float64
	hedgeToken        domain.TokenType
	hedgeOrderID      string
	hedgeFilled       bool
	hedgeTargetCents  int // 初始锁利目标价（profit lock）

	// 止损盯盘协程
	stopLevel       stopLevel
	monitorCancel   context.CancelFunc
	monitorRunning  bool

	// 开对次数（每周期）
	tradesThisCycle int

	// 时间控制
	cooldownUntil time.Time

	// auto merge runtime controller（每个策略实例一个）
	autoMergeCtl common.AutoMergeController
}

type state struct {
	mu sync.Mutex

	cfg Config

	// 速度追踪
	upVel   *VelocityTracker
	downVel *VelocityTracker

	rt pairRuntime
}

