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

type snapshotMeta struct {
	UpdatedAt  time.Time `json:"updated_at"`
	MarketSlug string    `json:"market_slug"`
	Gen        int64     `json:"gen"`
	Tag        string    `json:"tag"`
}

func snapshotTag(marketSlug string, gen int64) string {
	if marketSlug == "" || gen <= 0 {
		return "snapshot"
	}
	return fmt.Sprintf("snapshot:%s:g%d", marketSlug, gen)
}

func (s *TradingService) SetPersistence(ps persistence.Service, id string) {
	s.persistence = ps
	s.persistenceID = id
	if s.persistenceID == "" {
		s.persistenceID = "default"
	}
}

func (ss *SnapshotService) loadSnapshot() {
	// 快照恢复功能已禁用：不再从快照文件恢复持仓和订单
	// 系统将只使用实时 API 数据，不依赖本地缓存
	log.Infof("ℹ️ [快照恢复] 快照恢复功能已禁用，将使用实时 API 数据")
	return
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

	currentMarketSlug := s.GetCurrentMarket()
	currentGen := s.currentEngineGeneration()
	tag := snapshotTag(currentMarketSlug, currentGen)
	store := s.persistence.NewStore("trading", s.persistenceID, tag)
	_ = store.Save(&tradingSnapshot{
		UpdatedAt:  time.Now(),
		Balance:    balance,
		OpenOrders: openOrders,
		Positions:  positions,
	})
	// 写入 latest 指针（用于审计/定位、以及跨版本兼容加载）
	_ = s.persistence.NewStore("trading", s.persistenceID, "snapshot_latest").Save(&snapshotMeta{
		UpdatedAt:  time.Now(),
		MarketSlug: currentMarketSlug,
		Gen:        currentGen,
		Tag:        tag,
	})
	metrics.SnapshotSaves.Add(1)
}

func (ss *SnapshotService) bootstrapOpenOrdersFromExchange(ctx context.Context) {
	s := ss.s
	if s.dryRun {
		return
	}

	// 获取当前市场（只恢复当前周期的订单）
	currentMarketSlug := s.GetCurrentMarket()

	openOrdersResp, err := s.clobClient.GetOpenOrders(ctx, nil)
	if err != nil {
		log.Warnf("🔄 [重启恢复] 获取 open orders 失败: %v", err)
		return
	}
	if len(openOrdersResp) == 0 {
		return
	}

	// 只恢复当前周期的订单
	restoredCount := 0
	skippedCount := 0
	log.Infof("🔄 [重启恢复] 交易所 open orders=%d，开始注入 OrderEngine（当前周期=%s）", len(openOrdersResp), currentMarketSlug)
	for _, oo := range openOrdersResp {
		o := openOrderToDomain(oo)
		if o == nil || o.OrderID == "" {
			continue
		}
		// 如果设置了当前市场，只恢复当前周期的订单
		if currentMarketSlug != "" {
			if o.MarketSlug == "" || o.MarketSlug != currentMarketSlug {
				skippedCount++
				log.Debugf("🔄 [重启恢复] 跳过非当前周期的订单: orderID=%s, marketSlug=%s, 当前周期=%s",
					o.OrderID, o.MarketSlug, currentMarketSlug)
				continue
			}
		}
		s.orderEngine.SubmitCommand(&UpdateOrderCommand{
			id:    fmt.Sprintf("bootstrap_open_%s", o.OrderID),
			Gen:   s.currentEngineGeneration(),
			Order: o,
		})
		restoredCount++
	}
	if restoredCount > 0 || skippedCount > 0 {
		log.Infof("🔄 [重启恢复] 恢复订单: 当前周期=%d, 跳过旧周期=%d", restoredCount, skippedCount)
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
