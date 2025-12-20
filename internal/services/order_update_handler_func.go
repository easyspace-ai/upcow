package services

import (
	"context"

	"github.com/betbot/gobet/internal/domain"
)

type OrderUpdateHandlerFunc func(ctx context.Context, order *domain.Order) error

func (f OrderUpdateHandlerFunc) OnOrderUpdate(ctx context.Context, order *domain.Order) error {
	return f(ctx, order)
}

