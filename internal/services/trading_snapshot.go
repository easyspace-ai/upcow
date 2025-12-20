package services

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/metrics"
	"github.com/betbot/gobet/pkg/persistence"
)

type tradingSnapshot struct {
	UpdatedAt  time.Time          `json:"updated_at"`
	Balance    float64            `json:"balance"`
	OpenOrders []*domain.Order    `json:"open_orders"`
	Positions  []*domain.Position `json:"positions"`
}

func (s *TradingService) SetPersistence(ps persistence.Service, id string) {
	s.persistence = ps
	s.persistenceID = id
	if s.persistenceID == "" {
		s.persistenceID = "default"
	}
}

func (ss *SnapshotService) loadSnapshot() {
	s := ss.s
	if s.persistence == nil {
		return
	}
	store := s.persistence.NewStore("trading", s.persistenceID, "snapshot")
	var snap tradingSnapshot
	if err := store.Load(&snap); err != nil {
		return
	}
	metrics.SnapshotLoads.Add(1)

	// 恢复余额/订单/仓位（快速热启动），后续会由对账循环纠偏
	if snap.Balance > 0 {
		s.orderEngine.SubmitCommand(&UpdateBalanceCommand{
			id:       fmt.Sprintf("restore_balance_%d", time.Now().UnixNano()),
			Balance:  snap.Balance,
			Currency: "USDC",
		})
	}

	for _, o := range snap.OpenOrders {
		if o == nil || o.OrderID == "" {
			continue
		}
		s.orderEngine.SubmitCommand(&UpdateOrderCommand{
			id:    fmt.Sprintf("restore_order_%s", o.OrderID),
			Order: o,
		})
	}

	for _, p := range snap.Positions {
		if p == nil || p.ID == "" {
			continue
		}
		// 兼容旧快照：若 MarketSlug 为空，尝试从 Market/EntryOrder 补齐
		if p.MarketSlug == "" {
			if p.Market != nil && p.Market.Slug != "" {
				p.MarketSlug = p.Market.Slug
			} else if p.EntryOrder != nil && p.EntryOrder.MarketSlug != "" {
				p.MarketSlug = p.EntryOrder.MarketSlug
			}
		}
		_ = s.CreatePosition(context.Background(), p)
	}
}

func (ss *SnapshotService) saveSnapshot() {
	s := ss.s
	if s.persistence == nil {
		return
	}

	reply := make(chan *StateSnapshot, 1)
	s.orderEngine.SubmitCommand(&QueryStateCommand{
		id:    fmt.Sprintf("snapshot_%d", time.Now().UnixNano()),
		Query: QueryAllPositions,
		Reply: reply,
	})
	var positions []*domain.Position
	select {
	case snap := <-reply:
		positions = snap.Positions
	case <-time.After(3 * time.Second):
		return
	}

	openOrders := s.GetActiveOrders()

	// balance
	balanceReply := make(chan *StateSnapshot, 1)
	s.orderEngine.SubmitCommand(&QueryStateCommand{
		id:    fmt.Sprintf("snapshot_balance_%d", time.Now().UnixNano()),
		Query: QueryBalance,
		Reply: balanceReply,
	})
	balance := 0.0
	select {
	case snap := <-balanceReply:
		balance = snap.Balance
	case <-time.After(3 * time.Second):
	}

	store := s.persistence.NewStore("trading", s.persistenceID, "snapshot")
	_ = store.Save(&tradingSnapshot{
		UpdatedAt:  time.Now(),
		Balance:    balance,
		OpenOrders: openOrders,
		Positions:  positions,
	})
	metrics.SnapshotSaves.Add(1)
}

func (ss *SnapshotService) bootstrapOpenOrdersFromExchange(ctx context.Context) {
	s := ss.s
	if s.dryRun {
		return
	}
	openOrdersResp, err := s.clobClient.GetOpenOrders(ctx, nil)
	if err != nil {
		log.Warnf("🔄 [重启恢复] 获取 open orders 失败: %v", err)
		return
	}
	if len(openOrdersResp) == 0 {
		return
	}
	log.Infof("🔄 [重启恢复] 交易所 open orders=%d，开始注入 OrderEngine", len(openOrdersResp))
	for _, oo := range openOrdersResp {
		o := openOrderToDomain(oo)
		if o == nil || o.OrderID == "" {
			continue
		}
		s.orderEngine.SubmitCommand(&UpdateOrderCommand{
			id:    fmt.Sprintf("bootstrap_open_%s", o.OrderID),
			Order: o,
		})
	}
}

func (ss *SnapshotService) startSnapshotLoop(ctx context.Context) {
	s := ss.s
	// 每次订单更新触发一次保存（2s debounce）
	trigger := make(chan struct{}, 1)
	s.OnOrderUpdate(OrderUpdateHandlerFunc(func(_ context.Context, _ *domain.Order) error {
		select {
		case trigger <- struct{}{}:
		default:
		}
		return nil
	}))

	go func() {
		var pending bool
		var timer *time.Timer
		for {
			select {
			case <-ctx.Done():
				return
			case <-trigger:
				if !pending {
					pending = true
					timer = time.NewTimer(2 * time.Second)
				} else if timer != nil {
					timer.Reset(2 * time.Second)
				}
			case <-func() <-chan time.Time {
				if timer == nil {
					return make(chan time.Time)
				}
				return timer.C
			}():
				pending = false
				ss.saveSnapshot()
			}
		}
	}()
}

func openOrderToDomain(o types.OpenOrder) *domain.Order {
	price, _ := strconv.ParseFloat(o.Price, 64)
	orig, _ := strconv.ParseFloat(o.OriginalSize, 64)
	matched, _ := strconv.ParseFloat(o.SizeMatched, 64)

	side := types.Side(o.Side)
	if side != types.SideBuy && side != types.SideSell {
		// fallback：保持原值
		side = types.Side(o.Side)
	}

	d := &domain.Order{
		OrderID:    o.ID,
		MarketSlug: o.Market,
		AssetID:    o.AssetID,
		Side:       side,
		Price:      domain.PriceFromDecimal(price),
		Size:       orig,
		FilledSize: matched,
		CreatedAt:  time.Unix(o.CreatedAt, 0),
		Status:     domain.OrderStatusOpen,
	}

	// 状态映射
	if matched > 0 && orig > 0 && matched < orig {
		d.Status = domain.OrderStatusPartial
	} else if orig > 0 && matched >= orig {
		d.Status = domain.OrderStatusFilled
		now := time.Now()
		d.FilledAt = &now
		d.FilledSize = orig
	} else {
		switch o.Status {
		case "OPEN", "PENDING":
			d.Status = domain.OrderStatusOpen
		case "CANCELLED":
			d.Status = domain.OrderStatusCanceled
		case "FILLED":
			d.Status = domain.OrderStatusFilled
			now := time.Now()
			d.FilledAt = &now
			d.FilledSize = d.Size
		case "PARTIALLY_FILLED":
			d.Status = domain.OrderStatusPartial
		default:
			d.Status = domain.OrderStatusOpen
		}
	}

	return d
}
