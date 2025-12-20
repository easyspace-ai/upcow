package grid

import (
	"context"
	"fmt"
	"time"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/pkg/bbgo"
)

type gridCmdKind string

const (
	gridCmdPlaceEntry gridCmdKind = "place_entry"
	gridCmdPlaceHedge gridCmdKind = "place_hedge"
	gridCmdCancel     gridCmdKind = "cancel"
	gridCmdSync       gridCmdKind = "sync"
	gridCmdSupplement gridCmdKind = "supplement"
)

type gridCmdResult struct {
	planID string
	kind   gridCmdKind
	order  *domain.Order
	created *domain.Order
	err    error
}

func (s *GridStrategy) submitPlaceOrderCmd(ctx context.Context, planID string, kind gridCmdKind, order *domain.Order) error {
	if s.Executor == nil {
		return fmt.Errorf("Executor 未设置")
	}
	if s.tradingService == nil {
		return fmt.Errorf("交易服务未设置")
	}
	if order == nil {
		return fmt.Errorf("order=nil")
	}

	ok := s.Executor.Submit(bbgo.Command{
		Name:    fmt.Sprintf("grid_%s_%s_%s_%dc", kind, planID, order.TokenType, order.Price.Cents),
		Timeout: 30 * time.Second,
		Do: func(runCtx context.Context) {
			created, err := s.tradingService.PlaceOrder(runCtx, order)
			select {
			case s.cmdResultC <- gridCmdResult{planID: planID, kind: kind, order: order, created: created, err: err}:
			default:
			}
		},
	})
	if !ok {
		return fmt.Errorf("执行器队列已满，无法提交命令")
	}
	return nil
}

func (s *GridStrategy) submitCancelOrderCmd(planID string, orderID string) error {
	if s.Executor == nil {
		return fmt.Errorf("Executor 未设置")
	}
	if s.tradingService == nil {
		return fmt.Errorf("交易服务未设置")
	}
	if orderID == "" {
		return fmt.Errorf("orderID 为空")
	}

	ok := s.Executor.Submit(bbgo.Command{
		Name:    fmt.Sprintf("grid_cancel_%s_%s", planID, orderID),
		Timeout: 20 * time.Second,
		Do: func(runCtx context.Context) {
			err := s.tradingService.CancelOrder(runCtx, orderID)
			select {
			case s.cmdResultC <- gridCmdResult{planID: planID, kind: gridCmdCancel, err: err}:
			default:
			}
		},
	})
	if !ok {
		return fmt.Errorf("执行器队列已满，无法提交取消命令")
	}
	return nil
}

func (s *GridStrategy) submitSyncOrderCmd(planID string, orderID string) error {
	if s.Executor == nil {
		return fmt.Errorf("Executor 未设置")
	}
	if s.tradingService == nil {
		return fmt.Errorf("交易服务未设置")
	}
	if orderID == "" {
		return fmt.Errorf("orderID 为空")
	}

	ok := s.Executor.Submit(bbgo.Command{
		Name:    fmt.Sprintf("grid_sync_%s_%s", planID, orderID),
		Timeout: 20 * time.Second,
		Do: func(runCtx context.Context) {
			err := s.tradingService.SyncOrderStatus(runCtx, orderID)
			select {
			case s.cmdResultC <- gridCmdResult{planID: planID, kind: gridCmdSync, err: err}:
			default:
			}
		},
	})
	if !ok {
		return fmt.Errorf("执行器队列已满，无法提交同步命令")
	}
	return nil
}

func (s *GridStrategy) handleCmdResultInternal(_ context.Context, res gridCmdResult) error {
	// 无论是否匹配当前 plan，只要拿到了服务器回包，就优先把本地 order 指针更新成权威信息
	// 这样即便是“非 plan”路径（例如补充对冲/补仓），也能正确拿到 OrderID，后续订单更新才能对上。
	if res.order != nil && res.created != nil {
		res.order.OrderID = res.created.OrderID
		res.order.Status = res.created.Status
		if res.created.FilledAt != nil {
			res.order.FilledAt = res.created.FilledAt
		}
		if res.created.Size > 0 {
			res.order.Size = res.created.Size
		}
	}

	// plan 可能在周期切换时被重置，需容错
	if s.plan == nil || res.planID == "" || s.plan.ID != res.planID {
		return nil
	}

	switch res.kind {
	case gridCmdPlaceEntry:
		if res.err != nil {
			s.plan.State = PlanFailed
			s.plan.LastError = res.err.Error()

			// 失败：允许重试该层级
			if s.plan.LevelKey != "" && s.processedGridLevels != nil {
				delete(s.processedGridLevels, s.plan.LevelKey)
			}

			s.placeOrderMu.Lock()
			s.isPlacingOrder = false
			s.isPlacingOrderSetTime = time.Time{}
			s.placeOrderMu.Unlock()

			// 失败后清理 plan（避免卡死）
			s.plan = nil
			return nil
		}

		// 更新本地 entry 订单指针为服务器返回的权威信息（尤其是 OrderID）
		if res.order != nil && res.created != nil {
			res.order.OrderID = res.created.OrderID
			res.order.Status = res.created.Status
			if res.created.FilledAt != nil {
				res.order.FilledAt = res.created.FilledAt
			}
			if res.created.Size > 0 {
				res.order.Size = res.created.Size
			}
		}

		s.plan.EntryCreated = res.created
		s.plan.State = PlanEntryOpen

		if res.created != nil {
			s.plan.EntryOrderID = res.created.OrderID
		} else if res.order != nil {
			s.plan.EntryOrderID = res.order.OrderID
		}
		s.plan.StateAt = time.Now()

		s.placeOrderMu.Lock()
		s.isPlacingOrder = false
		s.isPlacingOrderSetTime = time.Time{}
		s.placeOrderMu.Unlock()
		return nil

	case gridCmdPlaceHedge:
		if res.err != nil {
			// 对冲失败：进入退避重试
			s.plan.LastError = res.err.Error()
			s.plan.State = PlanRetryWait
			s.plan.StateAt = time.Now()
			delay := time.Duration(1<<minInt(s.plan.HedgeAttempts, 3)) * time.Second
			s.plan.NextRetryAt = time.Now().Add(delay)
			return nil
		}
		// 更新本地 hedge 订单指针为服务器返回的权威信息（尤其是 OrderID）
		if res.order != nil && res.created != nil {
			res.order.OrderID = res.created.OrderID
			res.order.Status = res.created.Status
			if res.created.FilledAt != nil {
				res.order.FilledAt = res.created.FilledAt
			}
			if res.created.Size > 0 {
				res.order.Size = res.created.Size
			}
		}
		s.plan.HedgeCreated = res.created
		if res.created != nil {
			s.plan.HedgeOrderID = res.created.OrderID
		} else if res.order != nil {
			s.plan.HedgeOrderID = res.order.OrderID
		}
		s.plan.State = PlanHedgeOpen
		s.plan.StateAt = time.Now()
		return nil

	case gridCmdCancel:
		s.plan.LastCancelAt = time.Now()
		s.plan.StateAt = time.Now()
		return nil

	case gridCmdSync:
		s.plan.LastSyncAt = time.Now()
		s.plan.StateAt = time.Now()
		return nil

	case gridCmdSupplement:
		// 补仓命令返回：允许后续补仓
		s.plan.SupplementInFlight = false
		s.plan.LastSupplementAt = time.Now()
		return nil

	default:
		return nil
	}
}

