package capital

import (
	"context"
	"sync"
	"time"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/services"
	"github.com/sirupsen/logrus"
)

var log = logrus.WithField("module", "capital")

// Capital 资金模块
type Capital struct {
	tradingService *services.TradingService
	config         ConfigInterface

	// 子模块
	merger   *Merger
	redeemer *Redeemer

	// 周期状态
	mu              sync.Mutex
	cycleStartTime  time.Time
	redeemTimer     *time.Timer
	mergeCount      int // 本周期 merge 次数
	
	// Merge 状态跟踪
	mergeStatus      string    // "idle" | "merging" | "completed" | "failed"
	mergeAmount      float64   // 最后一次 merge 的数量
	mergeTxHash      string    // 最后一次 merge 的 txHash
	lastMergeTime    time.Time // 最后一次 merge 的时间
}

// New 创建新的 Capital 实例
func New(ts *services.TradingService, cfg ConfigInterface) (*Capital, error) {
	if ts == nil {
		return nil, nil // 允许延迟初始化
	}

	merger := NewMerger(ts, cfg)
	redeemer := NewRedeemer(ts, cfg)

	capital := &Capital{
		tradingService: ts,
		config:         cfg,
		merger:         merger,
		redeemer:       redeemer,
	}

	// 设置反向引用
	merger.SetCapital(capital)

	return capital, nil
}

// OnCycle 周期切换回调
func (c *Capital) OnCycle(ctx context.Context, oldMarket *domain.Market, newMarket *domain.Market) {
	c.OnCycleWithPositions(ctx, oldMarket, newMarket, nil)
}

// OnCycleWithPositions 周期切换回调（带旧周期持仓）
// 关键修复：在 ResetForNewCycle 清空持仓之前，先保存旧周期持仓，然后传递给此方法
func (c *Capital) OnCycleWithPositions(ctx context.Context, oldMarket *domain.Market, newMarket *domain.Market, oldPositions []*domain.Position) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cycleStartTime = time.Now()
	c.mergeCount = 0 // 重置 merge 次数
	c.mergeStatus = "idle" // 重置 merge 状态
	c.mergeAmount = 0
	c.mergeTxHash = ""

	// 1. 合并上一周期的 up/down
	if oldMarket != nil && c.merger != nil {
		// 检查是否启用自动合并
		autoMerge := c.config.GetAutoMerge()
		if !autoMerge.Enabled {
			log.Debugf("⏸️ [Capital] 自动合并未启用，跳过合并: oldMarket=%s", getMarketSlug(oldMarket))
			c.mergeStatus = "idle"
		} else {
			// 设置状态为 merging
			c.mu.Lock()
			c.mergeStatus = "merging"
			c.mu.Unlock()
			
			log.Infof("🔄 [Capital] 开始合并上一周期持仓: oldMarket=%s positions=%d", getMarketSlug(oldMarket), len(oldPositions))
			
			go func() {
				mergeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()

				// 如果提供了旧周期持仓，使用它们；否则从 TradingService 获取（可能已经被清空）
				var amount float64
				var txHash string
				var err error
				if len(oldPositions) > 0 {
					// 使用提供的持仓进行合并
					amount, txHash, err = c.merger.MergePreviousCycleWithPositions(mergeCtx, oldMarket, oldPositions)
				} else {
					// 回退到原来的方法（从 TradingService 获取，可能已经被清空）
					amount, txHash, err = c.merger.MergePreviousCycle(mergeCtx, oldMarket)
				}
				
				c.mu.Lock()
				defer c.mu.Unlock()
				
				if err != nil {
					c.mergeStatus = "failed"
					log.Warnf("⚠️ [Capital] 合并上一周期持仓失败: %v", err)
				} else if txHash != "" {
					c.mergeStatus = "completed"
					c.mergeAmount = amount
					c.mergeTxHash = txHash
					c.lastMergeTime = time.Now()
					log.Infof("✅ [Capital] 合并上一周期持仓成功: amount=%.4f txHash=%s", amount, txHash)
				} else {
					// 没有 complete sets 或条件不满足，不需要合并
					c.mergeStatus = "idle"
					log.Debugf("⏸️ [Capital] 上一周期无 complete sets 或条件不满足，无需合并")
				}
			}()
		}
	}

	// 2. 启动 2 分钟定时器，触发赎回
	if c.redeemTimer != nil {
		c.redeemTimer.Stop()
	}

	c.redeemTimer = time.AfterFunc(2*time.Minute, func() {
		if c.redeemer != nil {
			redeemCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			if err := c.redeemer.RedeemSettledPositions(redeemCtx); err != nil {
				log.Warnf("⚠️ [Capital] 赎回失败: %v", err)
			}
		}
	})

	log.Infof("✅ [Capital] 周期切换处理完成: oldMarket=%s newMarket=%s",
		getMarketSlug(oldMarket), getMarketSlug(newMarket))
}

// GetMergeCount 获取本周期 merge 次数（线程安全）
func (c *Capital) GetMergeCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mergeCount
}

// IncrementMergeCount 增加 merge 次数（线程安全）
func (c *Capital) IncrementMergeCount() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mergeCount++
	log.Infof("📊 [Capital] Merge 次数增加: %d", c.mergeCount)
}

// GetMergeStatus 获取 merge 状态（线程安全）
func (c *Capital) GetMergeStatus() (status string, amount float64, txHash string, lastTime time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mergeStatus, c.mergeAmount, c.mergeTxHash, c.lastMergeTime
}

// TryMergeCurrentCycle 尝试合并当前周期的 complete sets（在对冲单完成时调用）
func (c *Capital) TryMergeCurrentCycle(ctx context.Context, market *domain.Market) {
	if market == nil || c.merger == nil {
		return
	}

	// 检查是否启用自动合并
	autoMerge := c.config.GetAutoMerge()
	if !autoMerge.Enabled {
		return
	}

	// 异步执行合并，避免阻塞订单更新流程
	go func() {
		mergeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		amount, txHash, err := c.merger.MergeCurrentCycle(mergeCtx, market)
		
		c.mu.Lock()
		defer c.mu.Unlock()
		
		if err != nil {
			log.Warnf("⚠️ [Capital] 合并当前周期持仓失败: %v", err)
		} else if txHash != "" {
			c.mergeStatus = "completed"
			c.mergeAmount = amount
			c.mergeTxHash = txHash
			c.lastMergeTime = time.Now()
			log.Infof("✅ [Capital] 合并当前周期持仓成功: amount=%.4f txHash=%s", amount, txHash)
		} else {
			// 没有 complete sets 或条件不满足，不需要合并
			log.Debugf("⏸️ [Capital] 当前周期无 complete sets 或条件不满足，无需合并")
		}
	}()
}

// getMarketSlug 获取市场 slug（安全处理 nil）
func getMarketSlug(market *domain.Market) string {
	if market == nil {
		return "<nil>"
	}
	return market.Slug
}
