package grid

import (
	"context"
	"fmt"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/strategies/orderutil"
)

func (s *GridStrategy) submitStrongHedgeSupplement(
	ctx context.Context,
	p *HedgePlan,
	tokenType domain.TokenType,
	assetID string,
	price domain.Price,
	dQ float64,
) {
	if s == nil || p == nil || s.currentMarket == nil {
		return
	}

	order := orderutil.NewOrder(s.currentMarket.Slug, assetID, types.SideBuy, price, dQ, tokenType, false, types.OrderTypeFAK)
	order.OrderID = fmt.Sprintf("plan-supp-%s-%d-%d", tokenType, price.Cents, time.Now().UnixNano())

	p.SupplementInFlight = true
	if p.SupplementDebouncer != nil {
		p.SupplementDebouncer.MarkNow()
	}
	p.StateAt = time.Now()
	_ = s.submitPlaceOrderCmd(ctx, p.ID, gridCmdSupplement, order)
}
