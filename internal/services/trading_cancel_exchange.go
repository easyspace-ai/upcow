package services

import (
	"context"
	"fmt"
	"time"

	"github.com/betbot/gobet/clob/types"
)

// CancelOpenOrdersOptions 撤单选项（用于撤消“交易所侧 open orders”，包括他部挂单）。
type CancelOpenOrdersOptions struct {
	// MarketSlug 为空表示不按市场过滤；不为空则只撤指定 market 的挂单
	MarketSlug string
	// AssetIDs 为空表示不按资产过滤；不为空则只撤这些资产的挂单
	AssetIDs []string
	// OnlyUntracked=true 表示只撤“本系统未追踪”的挂单（他部挂单/历史残留）
	OnlyUntracked bool
	// MaxToCancel>0 时限制最多撤多少单（防止误操作）
	MaxToCancel int
}

// CancelOpenOrdersReport 撤单报告（用于策略做可观测性/风控）。
type CancelOpenOrdersReport struct {
	TotalFromExchange int
	Eligible          int
	Cancelled         int
	SkippedByFilter   int
	SkippedTracked    int
	Errors            int
}

// CancelExchangeOpenOrders 撤消交易所侧的 open orders（可选仅撤“他部挂单/未追踪挂单”）。
//
// 注意：
// - 该方法绕过 OrderEngine 的“订单必须已知”约束，专门用于处理外部/历史残留挂单。
// - 建议在策略启动/周期切换时由策略显式调用（你要求策略负责撤单）。
func (s *TradingService) CancelExchangeOpenOrders(ctx context.Context, opt CancelOpenOrdersOptions) (*CancelOpenOrdersReport, error) {
	if s == nil || s.clobClient == nil {
		return nil, fmt.Errorf("trading service not initialized")
	}

	report := &CancelOpenOrdersReport{}

	// 交易所 open orders
	openOrdersResp, err := s.clobClient.GetOpenOrders(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("get open orders failed: %w", err)
	}
	report.TotalFromExchange = len(openOrdersResp)

	// tracked set：只用“当前活跃订单”作为追踪依据
	tracked := make(map[string]struct{}, 256)
	if opt.OnlyUntracked {
		for _, o := range s.GetActiveOrders() {
			if o == nil || o.OrderID == "" {
				continue
			}
			tracked[o.OrderID] = struct{}{}
		}
	}

	assetSet := make(map[string]struct{}, len(opt.AssetIDs))
	for _, a := range opt.AssetIDs {
		if a == "" {
			continue
		}
		assetSet[a] = struct{}{}
	}

	// 顺序撤单：简单、可控，避免瞬时打满限流
	cancelled := 0
	for _, oo := range openOrdersResp {
		select {
		case <-ctx.Done():
			return report, ctx.Err()
		default:
		}

		d := openOrderToDomain(types.OpenOrder(oo))
		if d == nil || d.OrderID == "" {
			continue
		}

		// filter: market
		if opt.MarketSlug != "" && d.MarketSlug != opt.MarketSlug {
			report.SkippedByFilter++
			continue
		}
		// filter: assetIDs
		if len(assetSet) > 0 {
			if _, ok := assetSet[d.AssetID]; !ok {
				report.SkippedByFilter++
				continue
			}
		}
		// filter: only untracked
		if opt.OnlyUntracked {
			if _, ok := tracked[d.OrderID]; ok {
				report.SkippedTracked++
				continue
			}
		}

		report.Eligible++
		if opt.MaxToCancel > 0 && cancelled >= opt.MaxToCancel {
			break
		}

		if s.dryRun {
			cancelled++
			report.Cancelled++
			continue
		}

		_, cancelErr := s.clobClient.CancelOrder(ctx, d.OrderID)
		if cancelErr != nil {
			report.Errors++
			log.Warnf("⚠️ [撤消他部挂单] CancelOrder 失败: orderID=%s market=%s assetID=%s err=%v",
				d.OrderID, d.MarketSlug, d.AssetID, cancelErr)
			continue
		}

		cancelled++
		report.Cancelled++

		// 小延迟：避免瞬时触发限流（保守但稳）
		time.Sleep(10 * time.Millisecond)
	}

	return report, nil
}

