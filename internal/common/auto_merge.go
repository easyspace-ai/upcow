package common

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/services"
)

// AutoMergeConfig is a per-strategy config for automatically merging complete sets (YES+NO -> USDC).
// It is disabled by default; strategies opt-in via config.
type AutoMergeConfig struct {
	Enabled bool `yaml:"enabled" json:"enabled"`

	// MinCompleteSets: minimum complete sets (shares) required to trigger merge.
	MinCompleteSets float64 `yaml:"minCompleteSets" json:"minCompleteSets"`
	// MaxCompleteSetsPerRun: cap merge amount per run (0 means no cap).
	MaxCompleteSetsPerRun float64 `yaml:"maxCompleteSetsPerRun" json:"maxCompleteSetsPerRun"`
	// MergeRatio: merge amount = min(YES,NO) * MergeRatio. (0..1], default 1.
	MergeRatio float64 `yaml:"mergeRatio" json:"mergeRatio"`

	// IntervalSeconds: minimum time between auto-merge attempts.
	IntervalSeconds int `yaml:"intervalSeconds" json:"intervalSeconds"`

	// OnlyIfNoOpenOrders: require zero active open orders before merging (safer).
	OnlyIfNoOpenOrders bool `yaml:"onlyIfNoOpenOrders" json:"onlyIfNoOpenOrders"`

	// ReconcileAfterMerge: best-effort reconcile positions via Data API after submitting merge.
	ReconcileAfterMerge bool `yaml:"reconcileAfterMerge" json:"reconcileAfterMerge"`
	// ReconcileMaxWaitSeconds: how long to poll Data API to see inventory update (0 disables polling).
	ReconcileMaxWaitSeconds int `yaml:"reconcileMaxWaitSeconds" json:"reconcileMaxWaitSeconds"`

	// MergeTriggerDelaySeconds: delay before triggering merge and syncing positions (seconds).
	// This ensures exchange and Data API data are fully synchronized before merge.
	// Default: 15 seconds.
	MergeTriggerDelaySeconds int `yaml:"mergeTriggerDelaySeconds" json:"mergeTriggerDelaySeconds"`

	// Metadata: optional relayer metadata (<=500 chars). If empty, a default is used.
	Metadata string `yaml:"metadata" json:"metadata"`
}

func (c *AutoMergeConfig) Normalize() {
	if c == nil {
		return
	}
	if c.MinCompleteSets < 0 {
		c.MinCompleteSets = 0
	}
	if c.MergeRatio <= 0 || c.MergeRatio > 1.0 {
		c.MergeRatio = 1.0
	}
	if c.IntervalSeconds <= 0 {
		c.IntervalSeconds = 60
	}
	if c.ReconcileMaxWaitSeconds < 0 {
		c.ReconcileMaxWaitSeconds = 0
	}
	if c.MergeTriggerDelaySeconds <= 0 {
		c.MergeTriggerDelaySeconds = 15 // 默认 15 秒
	}
	// Safe default: require no open orders if user enables auto merge, unless explicitly set false.
	if c.OnlyIfNoOpenOrders == false {
		// keep user's value; no default override
	}
}

// AutoMergeController keeps runtime state (throttle/in-flight) per strategy instance.
type AutoMergeController struct {
	mu       sync.Mutex
	lastAt   time.Time
	inFlight bool
}

// AutoMergeCallback 合并完成后的回调函数
type AutoMergeCallback func(status string, amount float64, txHash string, err error)

func (ctl *AutoMergeController) MaybeAutoMerge(
	ctx context.Context,
	ts *services.TradingService,
	market *domain.Market,
	cfg AutoMergeConfig,
	logf func(format string, args ...any),
	onComplete AutoMergeCallback, // 可选回调函数，可以为 nil
) {
	cfg.Normalize()
	if !cfg.Enabled {
		return
	}
	if ts == nil || market == nil || !market.IsValid() || market.ConditionID == "" {
		if logf != nil {
			conditionID := ""
			if market != nil {
				conditionID = market.ConditionID
			}
			logf("⏸️ autoMerge 跳过：参数无效 (ts=%v market=%v conditionID=%s)", ts != nil, market != nil && market.IsValid(), conditionID)
		}
		return
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}

	// throttle + single-flight
	ctl.mu.Lock()
	if ctl.inFlight {
		ctl.mu.Unlock()
		logf("⏸️ autoMerge 跳过：合并操作正在进行中")
		return
	}
	if !ctl.lastAt.IsZero() && time.Since(ctl.lastAt) < time.Duration(cfg.IntervalSeconds)*time.Second {
		ctl.mu.Unlock()
		elapsed := time.Since(ctl.lastAt)
		logf("⏸️ autoMerge 跳过：距离上次合并仅 %v，需要等待 %d 秒", elapsed, cfg.IntervalSeconds)
		return
	}
	ctl.inFlight = true
	ctl.lastAt = time.Now()
	ctl.mu.Unlock()
	defer func() {
		ctl.mu.Lock()
		ctl.inFlight = false
		ctl.mu.Unlock()
	}()

	// safety: require no open orders (optional)
	if cfg.OnlyIfNoOpenOrders {
		activeOrders := ts.GetActiveOrders()
		// 过滤：只检查当前市场的活跃订单
		currentMarketOrders := 0
		var orderDetails []string
		for _, o := range activeOrders {
			if o != nil && o.MarketSlug == market.Slug {
				currentMarketOrders++
				orderDetails = append(orderDetails, fmt.Sprintf("%s:%s", o.OrderID, o.Status))
			}
		}
		if currentMarketOrders > 0 {
			logf("⏸️ autoMerge 跳过：当前市场有 %d 个活跃订单（onlyIfNoOpenOrders=true）: %v", currentMarketOrders, orderDetails)
			return
		}
		logf("✅ autoMerge 检查：当前市场无活跃订单，可以合并")
	}

	// compute complete sets using local positions (fast path)
	var up, down float64
	positions := ts.GetOpenPositionsForMarket(market.Slug)
	
	// 如果持仓为空，尝试从订单重建持仓数据（持仓可能还没有同步）
	if len(positions) == 0 {
		logf("⚠️ autoMerge 持仓为空，尝试从订单重建持仓数据: market=%s", market.Slug)
		up, down = computeCompleteSetsFromOrders(ts, market.Slug, logf)
	} else {
		for _, p := range positions {
			if p == nil || !p.IsOpen() || p.Size <= 0 {
				continue
			}
			if p.TokenType == domain.TokenTypeUp {
				up += p.Size
			} else if p.TokenType == domain.TokenTypeDown {
				down += p.Size
			}
		}
	}
	
	complete := math.Min(up, down)
	
	// 添加调试日志
	logf("🔍 autoMerge 检查: market=%s UP=%.6f DOWN=%.6f complete=%.6f minCompleteSets=%.6f (持仓数量=%d)",
		market.Slug, up, down, complete, cfg.MinCompleteSets, len(positions))
	
	if cfg.MinCompleteSets > 0 && complete < cfg.MinCompleteSets {
		logf("⏸️ autoMerge 跳过：complete sets (%.6f) < minCompleteSets (%.6f)", complete, cfg.MinCompleteSets)
		return
	}
	if complete <= 0 {
		logf("⏸️ autoMerge 跳过：complete sets (%.6f) <= 0", complete)
		return
	}

	amount := complete * cfg.MergeRatio
	if amount > complete {
		amount = complete
	}
	if cfg.MaxCompleteSetsPerRun > 0 && amount > cfg.MaxCompleteSetsPerRun {
		amount = cfg.MaxCompleteSetsPerRun
	}
	if amount <= 0 {
		logf("⏸️ autoMerge 跳过：计算后的合并数量 (%.6f) <= 0", amount)
		return
	}
	
	logf("✅ autoMerge 准备合并: market=%s amount=%.6f complete=%.6f mergeRatio=%.2f maxPerRun=%.6f",
		market.Slug, amount, complete, cfg.MergeRatio, cfg.MaxCompleteSetsPerRun)

	// 触发回调：开始合并
	if onComplete != nil {
		onComplete("triggered", amount, "", nil)
	}

	// 异步执行合并操作，避免阻塞价格事件处理
	go func() {
		// 使用独立的 context，避免使用已取消的 ctx
		mergeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		// 触发回调：正在合并
		if onComplete != nil {
			onComplete("merging", amount, "", nil)
		}

		txHash, err := ts.MergeCompleteSetsViaRelayer(mergeCtx, market.ConditionID, amount, cfg.Metadata)
		if err != nil {
			logf("⚠️ autoMerge failed: market=%s amount=%.6f err=%v", market.Slug, amount, err)
			// 触发回调：合并失败
			if onComplete != nil {
				onComplete("failed", amount, "", err)
			}
			return
		}
		logf("✅ autoMerge submitted: market=%s amount=%.6f complete=%.6f tx=%s", market.Slug, amount, complete, txHash)
		
		// 触发回调：合并已提交
		if onComplete != nil {
			onComplete("submitted", amount, txHash, nil)
		}

		// 等待一小段时间，让 merge 交易有时间提交到链上
		time.Sleep(2 * time.Second)

		// 触发回调：合并完成，开始刷新余额
		if onComplete != nil {
			onComplete("refreshing_balance", amount, txHash, nil)
		}

		// 刷新余额：合并后会释放 USDC，需要刷新余额以提高资金利用率
		refreshCtx, refreshCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer refreshCancel()
		if err := ts.RefreshBalance(refreshCtx); err != nil {
			logf("⚠️ autoMerge后刷新余额失败: market=%s err=%v (不影响合并结果)", market.Slug, err)
			// 触发回调：刷新余额失败
			if onComplete != nil {
				onComplete("balance_refresh_failed", amount, txHash, err)
			}
		} else {
			logf("✅ autoMerge后余额已刷新: market=%s amount=%.6f (提高资金利用率)", market.Slug, amount)
			// 触发回调：刷新余额完成
			if onComplete != nil {
				onComplete("balance_refreshed", amount, txHash, nil)
			}
		}

		// best-effort reconcile (Data API lags; optional polling)
		// 重要：合并后会减少持仓，必须同步持仓数据以确保 Dashboard 显示正确
		if cfg.ReconcileAfterMerge {
			// 第一次同步（立即）
			_ = ts.ReconcileMarketPositionsFromDataAPI(mergeCtx, market)
			
			maxWait := time.Duration(cfg.ReconcileMaxWaitSeconds) * time.Second
			if maxWait <= 0 {
				// 即使没有配置轮询，也等待一段时间后再次同步（Data API 可能有延迟）
				// 等待 5 秒后再次同步，确保持仓数据更新
				time.Sleep(5 * time.Second)
				reconcileCtx, reconcileCancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer reconcileCancel()
				if err := ts.ReconcileMarketPositionsFromDataAPI(reconcileCtx, market); err != nil {
					logf("⚠️ autoMerge后二次同步持仓失败: market=%s err=%v", market.Slug, err)
				} else {
					logf("✅ autoMerge后持仓已同步: market=%s amount=%.6f (确保Dashboard显示正确)", market.Slug, amount)
					// 触发回调：合并完全完成（包括同步持仓）
					if onComplete != nil {
						onComplete("completed", amount, txHash, nil)
					}
				}
				return
			}
			
			// 配置了轮询，按配置执行
			deadline := time.Now().Add(maxWait)
			for time.Now().Before(deadline) {
				time.Sleep(3 * time.Second)
				_ = ts.ReconcileMarketPositionsFromDataAPI(mergeCtx, market)
			}
		} else {
			// 即使未启用 reconcileAfterMerge，也尝试同步一次（确保持仓正确）
			// 因为合并会改变持仓，必须同步才能正确显示
			time.Sleep(5 * time.Second)
			reconcileCtx, reconcileCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer reconcileCancel()
			if err := ts.ReconcileMarketPositionsFromDataAPI(reconcileCtx, market); err != nil {
				logf("⚠️ autoMerge后同步持仓失败（未启用reconcileAfterMerge）: market=%s err=%v", market.Slug, err)
			} else {
				logf("✅ autoMerge后持仓已同步: market=%s amount=%.6f (确保Dashboard显示正确)", market.Slug, amount)
				// 触发回调：合并完全完成（包括同步持仓）
				if onComplete != nil {
					onComplete("completed", amount, txHash, nil)
				}
			}
		}
	}()
}

// computeCompleteSetsFromOrders 从已成交订单计算 complete sets
// 当持仓数据还没有同步时，使用此方法从订单重建持仓数据
func computeCompleteSetsFromOrders(ts *services.TradingService, marketSlug string, logf func(format string, args ...any)) (up float64, down float64) {
	if ts == nil || marketSlug == "" {
		return 0, 0
	}

	// 获取所有订单
	allOrders := ts.GetAllOrders()
	entryOrdersSeen := make(map[string]bool) // 用于去重

	// 筛选已成交的Entry订单
	for _, order := range allOrders {
		if order == nil {
			continue
		}
		// 只统计当前市场的订单
		if order.MarketSlug != marketSlug {
			continue
		}
		// 只统计已成交的Entry订单，且必须是买单
		if !order.IsEntryOrder {
			continue
		}
		// 只统计买单，不统计卖单（卖单是用户手动操作的）
		if order.Side != types.SideBuy {
			continue
		}
		// 检查订单是否已成交
		if order.Status != domain.OrderStatusFilled {
			continue
		}
		if order.FilledSize <= 0 {
			continue
		}
		// 去重
		if entryOrdersSeen[order.OrderID] {
			continue
		}
		entryOrdersSeen[order.OrderID] = true

		// 验证FilledSize合理性
		filledSize := order.FilledSize
		if order.Size > 0 && filledSize > order.Size {
			filledSize = order.Size
		}
		if filledSize <= 0 {
			continue
		}

		// 按TokenType分组累加
		if order.TokenType == domain.TokenTypeUp {
			up += filledSize
		} else if order.TokenType == domain.TokenTypeDown {
			down += filledSize
		}
	}

	if up > 0 || down > 0 {
		logf("✅ autoMerge 从订单重建持仓: market=%s UP=%.6f DOWN=%.6f", marketSlug, up, down)
	}

	return up, down
}
