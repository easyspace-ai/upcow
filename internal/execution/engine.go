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
	TickSize  types.TickSize // 价格精度（可选）
	NegRisk   *bool          // 是否为负风险市场（可选）
	// IsEntry: 标记该腿是否为 Entry（用于风控/监控语义；hedge/平衡腿应为 false）。
	IsEntry bool
	// BypassRiskOff: 风控动作（hedge/止损）可绕过短时 risk-off。
	BypassRiskOff bool
	// DisableSizeAdjust: 禁用系统层自动 size 调整（避免被放大到 minShareSize=5 等导致过度对冲）。
	DisableSizeAdjust bool
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
			if leg.AssetID == "" || leg.Size <= 0 || leg.Price.Pips <= 0 {
				errC <- fmt.Errorf("invalid leg %d", i)
				return
			}
			// Polymarket 对 GTC 限价单常见最小 size=5 shares。
			// 对于 hedge 腿（非 entry），若 size < 5，直接用 FAK，避免“系统/交易所把它放大到 5”导致过度对冲。
			orderType := leg.OrderType
			const minGTCShareSize = 5.0
			if !leg.IsEntry && orderType == types.OrderTypeGTC && leg.Size < minGTCShareSize {
				orderType = types.OrderTypeFAK
			}
			order := &domain.Order{
				MarketSlug:   req.MarketSlug,
				AssetID:      leg.AssetID,
				Side:         leg.Side,
				Price:        leg.Price,
				Size:         leg.Size,
				TokenType:    leg.TokenType,
				IsEntryOrder: leg.IsEntry,
				BypassRiskOff: leg.BypassRiskOff,
				DisableSizeAdjust: leg.DisableSizeAdjust,
				Status:       domain.OrderStatusPending,
				CreatedAt:    time.Now(),
				OrderType:    orderType,
				TickSize:     leg.TickSize, // 使用 LegIntent 中的精度信息
				NegRisk:      leg.NegRisk,   // 使用 LegIntent 中的 neg_risk 信息
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
		Price:        domain.Price{Pips: priceCents * 100}, // 1 cent = 100 pips
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
	// 优化：基于市场、方向和主要入场订单价格计算去重 key
	// 这样可以避免相同方向、相似价格的重复下单，同时允许价格变化时的新订单
	parts := make([]string, 0, len(req.Legs))
	
	// 找到入场订单（通常是 FAK 订单）作为主要标识
	var entryLeg *LegIntent
	for i := range req.Legs {
		if req.Legs[i].IsEntry || req.Legs[i].OrderType == types.OrderTypeFAK || req.Legs[i].Name == "taker_buy_winner" {
			entryLeg = &req.Legs[i]
			break
		}
	}
	
	// 如果找到入场订单，使用更精确的去重 key（包含价格范围，允许小幅价格变化）
	if entryLeg != nil {
		// 价格取整到 1 cent，允许小幅价格波动
		priceCents := int(entryLeg.Price.ToDecimal() * 100)
		parts = append(parts, fmt.Sprintf("%s|%s|%s|%dc",
			entryLeg.AssetID, entryLeg.TokenType, entryLeg.Side, priceCents))
	} else {
		// 回退到原来的逻辑
		for _, l := range req.Legs {
			parts = append(parts, fmt.Sprintf("%s|%s|%s|%.4f|%.4f|%s",
				l.AssetID, l.TokenType, l.Side, l.Price.ToDecimal(), l.Size, l.OrderType))
		}
		sort.Strings(parts)
	}
	
	return strings.Join(append([]string{req.MarketSlug}, parts...), "||")
}

