package execution

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/ports"
)

// ExecutionEngine: 统一多腿执行引擎（队列 + in-flight 去重 + 并发下单 + 自动对冲）。
//
// 设计目标（对齐 poly-kalshi-arb 的核心思路）：
// - 热路径：策略只提交请求，不直接耦合到“多腿并发/去重/兜底”
// - 高并发：多腿并发 PlaceOrder，减少跨腿时差
// - 安全：同一市场同类请求做 in-flight gate；成交不匹配时自动对冲

type TradingOps interface {
	ports.OrderPlacer
	ports.OrderCanceler
	ports.BestPriceGetter
}

type LegIntent struct {
	Name      string
	AssetID   string
	TokenType domain.TokenType
	Side      types.Side
	Price     domain.Price
	Size      float64
	OrderType types.OrderType
}

type AutoHedgeConfig struct {
	Enabled bool
	// Delay: 等待一小段时间让 WS/结算回流（避免过早对冲）
	Delay time.Duration
	// SellPriceOffsetCents: 对冲卖单价格在 bestBid 基础上向下偏移的 cents（更容易成交）
	SellPriceOffsetCents int
	// MinExposureToHedge: 超过该暴露（shares）才触发对冲
	MinExposureToHedge float64
}

type MultiLegRequest struct {
	Name      string
	MarketSlug string
	Legs      []LegIntent
	Hedge     AutoHedgeConfig
	// InFlightKey: 若为空，会自动根据 marketSlug+legs 计算
	InFlightKey string
}

type MultiLegResult struct {
	Created []*domain.Order
	Err     error
}

type MultiLegTicket struct {
	ID      string
	ResultC <-chan MultiLegResult
}

type ExecutionEngine struct {
	ops TradingOps

	inFlight *InFlightDeduper

	reqC chan queuedReq

	mu sync.RWMutex
	// orderID -> execID
	orderToExec map[string]string
	// execID -> state
	execs map[string]*execState
}

type queuedReq struct {
	id     string
	req    MultiLegRequest
	ctx    context.Context
	result chan MultiLegResult
}

type execState struct {
	id        string
	req       MultiLegRequest
	created   []*domain.Order
	createdMu sync.Mutex

	// filled snapshot
	filled map[string]float64 // orderID -> filledSize

	// hedge control
	hedgeOnce sync.Once
}

func NewExecutionEngine(ops TradingOps) *ExecutionEngine {
	return &ExecutionEngine{
		ops:        ops,
		inFlight:   NewInFlightDeduper(10*time.Second, 64),
		reqC:       make(chan queuedReq, 512),
		orderToExec: make(map[string]string),
		execs:      make(map[string]*execState),
	}
}

// Start 启动执行循环（建议在 TradingService.Start 中启动）。
func (e *ExecutionEngine) Start(ctx context.Context) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case q := <-e.reqC:
				// 每个请求独立 goroutine，避免阻塞队列
				go e.process(q)
			}
		}
	}()
}

// Submit 提交一个多腿执行请求，返回 ticket（可等待结果）。
func (e *ExecutionEngine) Submit(ctx context.Context, req MultiLegRequest) (*MultiLegTicket, error) {
	if e == nil || e.ops == nil {
		return nil, fmt.Errorf("execution engine not initialized")
	}
	if req.MarketSlug == "" {
		return nil, fmt.Errorf("MarketSlug 不能为空")
	}
	if len(req.Legs) < 1 {
		return nil, fmt.Errorf("需要至少 1 条腿")
	}

	id := fmt.Sprintf("exec_%d", time.Now().UnixNano())
	if req.InFlightKey == "" {
		req.InFlightKey = computeInFlightKey(req)
	}

	// in-flight gate：同类请求短窗口去重
	if e.inFlight != nil {
		if err := e.inFlight.TryAcquire(req.InFlightKey); err != nil {
			return nil, err
		}
	}

	resultC := make(chan MultiLegResult, 1)
	q := queuedReq{
		id:     id,
		req:    req,
		ctx:    ctx,
		result: resultC,
	}

	select {
	case e.reqC <- q:
		return &MultiLegTicket{ID: id, ResultC: resultC}, nil
	case <-ctx.Done():
		if e.inFlight != nil {
			e.inFlight.Release(req.InFlightKey)
		}
		return nil, ctx.Err()
	}
}

func (e *ExecutionEngine) process(q queuedReq) {
	defer func() {
		// 释放 in-flight（允许很快重试；成功/失败都释放，由 TTL 再兜底）
		if e.inFlight != nil && q.req.InFlightKey != "" {
			e.inFlight.Release(q.req.InFlightKey)
		}
	}()

	st := &execState{
		id:     q.id,
		req:    q.req,
		filled: make(map[string]float64),
	}
	e.mu.Lock()
	e.execs[q.id] = st
	e.mu.Unlock()

	created, err := e.placeAllLegs(q.ctx, q.req)
	st.createdMu.Lock()
	st.created = created
	st.createdMu.Unlock()

	// 将订单映射到 execID（用于后续订单更新驱动的自动对冲）
	e.mu.Lock()
	for _, o := range created {
		if o == nil || o.OrderID == "" {
			continue
		}
		e.orderToExec[o.OrderID] = q.id
	}
	e.mu.Unlock()

	select {
	case q.result <- MultiLegResult{Created: created, Err: err}:
	default:
	}
}

func (e *ExecutionEngine) placeAllLegs(ctx context.Context, req MultiLegRequest) ([]*domain.Order, error) {
	created := make([]*domain.Order, len(req.Legs))
	errC := make(chan error, len(req.Legs))

	var wg sync.WaitGroup
	wg.Add(len(req.Legs))

	for i := range req.Legs {
		i := i
		leg := req.Legs[i]
		go func() {
			defer wg.Done()
			if leg.AssetID == "" || leg.Size <= 0 || leg.Price.Cents <= 0 {
				errC <- fmt.Errorf("invalid leg %d", i)
				return
			}
			order := &domain.Order{
				MarketSlug:   req.MarketSlug,
				AssetID:      leg.AssetID,
				Side:         leg.Side,
				Price:        leg.Price,
				Size:         leg.Size,
				TokenType:    leg.TokenType,
				IsEntryOrder: true,
				Status:       domain.OrderStatusPending,
				CreatedAt:    time.Now(),
				OrderType:    leg.OrderType,
			}
			o, err := e.ops.PlaceOrder(ctx, order)
			if err != nil {
				errC <- err
			}
			created[i] = o
		}()
	}

	wg.Wait()
	close(errC)
	for err := range errC {
		if err != nil {
			return created, err
		}
	}
	return created, nil
}

// OnOrderUpdate 用订单更新驱动“成交不匹配自动对冲”。
func (e *ExecutionEngine) OnOrderUpdate(ctx context.Context, order *domain.Order) error {
	if e == nil || order == nil || order.OrderID == "" {
		return nil
	}

	e.mu.RLock()
	execID := e.orderToExec[order.OrderID]
	st := e.execs[execID]
	e.mu.RUnlock()
	if execID == "" || st == nil {
		return nil
	}

	// 更新 filled 快照
	st.filled[order.OrderID] = order.FilledSize

	// 自动对冲：只要任意腿出现部分成交不一致，就尝试对冲
	if !st.req.Hedge.Enabled {
		return nil
	}

	// 只触发一次对冲调度（内部会根据最新 filled 计算）
	st.hedgeOnce.Do(func() {
		go func() {
			delay := st.req.Hedge.Delay
			if delay <= 0 {
				delay = 2 * time.Second
			}
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				return
			}
			e.tryAutoHedge(ctx, st)
		}()
	})

	return nil
}

func (e *ExecutionEngine) tryAutoHedge(ctx context.Context, st *execState) {
	st.createdMu.Lock()
	created := append([]*domain.Order(nil), st.created...)
	st.createdMu.Unlock()
	if len(created) < 2 {
		return
	}

	// 仅对“买入-买入”的常见双腿场景做兜底（YES/NO 同时买入）
	// 计算两腿的成交量差，差额部分卖出回补（对冲）
	var buys []*domain.Order
	for _, o := range created {
		if o == nil {
			continue
		}
		if o.Side == types.SideBuy {
			buys = append(buys, o)
		}
	}
	if len(buys) < 2 {
		return
	}
	// 固定取前两腿（需要更通用的多腿匹配时再扩展）
	a, b := buys[0], buys[1]
	af := st.filled[a.OrderID]
	bf := st.filled[b.OrderID]
	excess := af - bf
	excessOrder := a
	if excess < 0 {
		excess = -excess
		excessOrder = b
	}

	min := st.req.Hedge.MinExposureToHedge
	if min <= 0 {
		min = 0.0001
	}
	if excess < min {
		return
	}

	// 使用 bestBid - offset 作为卖出价格（更容易成交）
	bestBid, _, err := e.ops.GetBestPrice(ctx, excessOrder.AssetID)
	if err != nil || bestBid <= 0 {
		return
	}
	priceCents := int(bestBid*100 + 0.5)
	off := st.req.Hedge.SellPriceOffsetCents
	if off <= 0 {
		off = 2
	}
	priceCents -= off
	if priceCents < 1 {
		priceCents = 1
	}

	closeOrder := &domain.Order{
		MarketSlug:   st.req.MarketSlug,
		AssetID:      excessOrder.AssetID,
		Side:         types.SideSell,
		Price:        domain.Price{Cents: priceCents},
		Size:         excess,
		TokenType:    excessOrder.TokenType,
		IsEntryOrder: false,
		Status:       domain.OrderStatusPending,
		CreatedAt:    time.Now(),
		OrderType:    types.OrderTypeFAK,
	}
	_, _ = e.ops.PlaceOrder(ctx, closeOrder)
}

func computeInFlightKey(req MultiLegRequest) string {
	parts := make([]string, 0, len(req.Legs))
	for _, l := range req.Legs {
		parts = append(parts, fmt.Sprintf("%s|%s|%s|%dc|%.4f|%s",
			l.AssetID, l.TokenType, l.Side, l.Price.Cents, l.Size, l.OrderType))
	}
	sort.Strings(parts)
	return strings.Join(append([]string{req.MarketSlug}, parts...), "||")
}

