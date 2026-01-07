package oms

import (
	"context"
	"errors"
	"time"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/execution"
	"github.com/betbot/gobet/internal/services"
)

var errTradingQueueClosed = errors.New("trading queue closed")

// queuedTrading 将“下单/撤单/多腿执行”串行化，避免并发打架。
// 说明：
// - 只串行化“写操作”（PlaceOrder/CancelOrder/ExecuteMultiLeg）
// - 读操作（GetOrder/GetTopOfBook 等）维持原样，避免队列阻塞扩大影响
type queuedTrading struct {
	ts *services.TradingService

	ch   chan tradeReq
	done chan struct{}

	// 节流：连续写操作之间最小间隔（避免极端行情 API spam）
	minInterval time.Duration
}

type tradeReq struct {
	ctx  context.Context
	fn   func(context.Context) (any, error)
	resp chan tradeResp
}

type tradeResp struct {
	v   any
	err error
}

func newQueuedTrading(ts *services.TradingService, buffer int, minInterval time.Duration) *queuedTrading {
	if buffer <= 0 {
		buffer = 128
	}
	if minInterval < 0 {
		minInterval = 0
	}
	qt := &queuedTrading{
		ts:          ts,
		ch:          make(chan tradeReq, buffer),
		done:        make(chan struct{}),
		minInterval: minInterval,
	}
	go qt.loop()
	return qt
}

func (qt *queuedTrading) Close() {
	if qt == nil {
		return
	}
	select {
	case <-qt.done:
		return
	default:
		close(qt.done)
	}
}

func (qt *queuedTrading) loop() {
	var lastWrite time.Time
	for {
		select {
		case <-qt.done:
			return
		case req := <-qt.ch:
			if req.resp == nil {
				continue
			}
			// 简单节流：保证连续写操作之间最小间隔
			if qt.minInterval > 0 && !lastWrite.IsZero() {
				sleep := qt.minInterval - time.Since(lastWrite)
				if sleep > 0 {
					timer := time.NewTimer(sleep)
					select {
					case <-qt.done:
						timer.Stop()
						return
					case <-timer.C:
					}
				}
			}

			// 即使 ctx 已取消，也执行 fn（因为已经进入队列），由下层 ctx 决定是否快速失败
			v, err := req.fn(req.ctx)
			lastWrite = time.Now()
			req.resp <- tradeResp{v: v, err: err}
		}
	}
}

func (qt *queuedTrading) do(ctx context.Context, fn func(context.Context) (any, error)) (any, error) {
	if qt == nil || qt.ts == nil {
		return nil, errors.New("trading service nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	respCh := make(chan tradeResp, 1)
	req := tradeReq{ctx: ctx, fn: fn, resp: respCh}

	select {
	case <-qt.done:
		return nil, errTradingQueueClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	case qt.ch <- req:
	}

	select {
	case <-qt.done:
		return nil, errTradingQueueClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-respCh:
		return r.v, r.err
	}
}

func (qt *queuedTrading) PlaceOrder(ctx context.Context, order *domain.Order) (*domain.Order, error) {
	v, err := qt.do(ctx, func(c context.Context) (any, error) {
		return qt.ts.PlaceOrder(c, order)
	})
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, nil
	}
	return v.(*domain.Order), nil
}

func (qt *queuedTrading) CancelOrder(ctx context.Context, orderID string) error {
	_, err := qt.do(ctx, func(c context.Context) (any, error) {
		return nil, qt.ts.CancelOrder(c, orderID)
	})
	return err
}

func (qt *queuedTrading) ExecuteMultiLeg(ctx context.Context, req execution.MultiLegRequest) ([]*domain.Order, error) {
	v, err := qt.do(ctx, func(c context.Context) (any, error) {
		return qt.ts.ExecuteMultiLeg(c, req)
	})
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, nil
	}
	return v.([]*domain.Order), nil
}

