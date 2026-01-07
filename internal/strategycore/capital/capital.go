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

	merger   *Merger
	redeemer *Redeemer

	mu             sync.Mutex
	cycleStartTime time.Time
	redeemTimer    *time.Timer
	mergeCount     int

	mergeStatus   string
	mergeAmount   float64
	mergeTxHash   string
	lastMergeTime time.Time
}

func New(ts *services.TradingService, cfg ConfigInterface) (*Capital, error) {
	if ts == nil {
		return nil, nil
	}

	merger := NewMerger(ts, cfg)
	redeemer := NewRedeemer(ts, cfg)
	capital := &Capital{
		tradingService: ts,
		config:         cfg,
		merger:         merger,
		redeemer:       redeemer,
	}
	merger.SetCapital(capital)
	return capital, nil
}

func (c *Capital) OnCycle(ctx context.Context, oldMarket *domain.Market, newMarket *domain.Market) {
	c.OnCycleWithPositions(ctx, oldMarket, newMarket, nil)
}

// OnCycleWithPositions 周期切换回调（带旧周期持仓）
func (c *Capital) OnCycleWithPositions(ctx context.Context, oldMarket *domain.Market, newMarket *domain.Market, oldPositions []*domain.Position) {
	_ = ctx
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cycleStartTime = time.Now()
	c.mergeCount = 0
	c.mergeStatus = "idle"
	c.mergeAmount = 0
	c.mergeTxHash = ""

	if oldMarket != nil && c.merger != nil {
		autoMerge := c.config.GetAutoMerge()
		if !autoMerge.Enabled {
			log.Debugf("⏸️ [Capital] 自动合并未启用，跳过合并: oldMarket=%s", getMarketSlug(oldMarket))
			c.mergeStatus = "idle"
		} else {
			c.mergeStatus = "merging"
			log.Infof("🔄 [Capital] 开始合并上一周期持仓: oldMarket=%s positions=%d", getMarketSlug(oldMarket), len(oldPositions))

			go func() {
				mergeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()

				var amount float64
				var txHash string
				var err error
				if len(oldPositions) > 0 {
					amount, txHash, err = c.merger.MergePreviousCycleWithPositions(mergeCtx, oldMarket, oldPositions)
				} else {
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
					c.mergeStatus = "idle"
					log.Debugf("⏸️ [Capital] 上一周期无 complete sets 或条件不满足，无需合并")
				}
			}()
		}
	}

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

func (c *Capital) GetMergeCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mergeCount
}

func (c *Capital) IncrementMergeCount() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mergeCount++
	log.Infof("📊 [Capital] Merge 次数增加: %d", c.mergeCount)
}

func (c *Capital) GetMergeStatus() (status string, amount float64, txHash string, lastTime time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mergeStatus, c.mergeAmount, c.mergeTxHash, c.lastMergeTime
}

func (c *Capital) TryMergeCurrentCycle(ctx context.Context, market *domain.Market) {
	if market == nil || c.merger == nil {
		return
	}
	autoMerge := c.config.GetAutoMerge()
	if !autoMerge.Enabled {
		return
	}
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
			log.Debugf("⏸️ [Capital] 当前周期无 complete sets 或条件不满足，无需合并")
		}
	}()
}

func getMarketSlug(market *domain.Market) string {
	if market == nil {
		return "<nil>"
	}
	return market.Slug
}

