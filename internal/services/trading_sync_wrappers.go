package services

import "context"

// 这些 wrapper 保留 TradingService 既有对外/对内方法名，
// 但实现已迁移到更小的文件中，避免 trading.go 继续膨胀。

func (s *TradingService) startOrderStatusSync(ctx context.Context) {
	s.startOrderStatusSyncImpl(ctx)
}

func (s *TradingService) syncAllOrderStatus(ctx context.Context) {
	s.syncAllOrderStatusImpl(ctx)
}

func (s *TradingService) SyncOrderStatus(ctx context.Context, orderID string) error {
	return s.syncOrderStatusImpl(ctx, orderID)
}

func (s *TradingService) startOrderConfirmationTimeoutCheck(ctx context.Context) {
	s.startOrderConfirmationTimeoutCheckImpl(ctx)
}

func (s *TradingService) checkOrderConfirmationTimeout(ctx context.Context) {
	s.checkOrderConfirmationTimeoutImpl(ctx)
}

func (s *TradingService) FetchUserPositionsFromAPI(ctx context.Context) error {
	return s.fetchUserPositionsFromAPIImpl(ctx)
}
