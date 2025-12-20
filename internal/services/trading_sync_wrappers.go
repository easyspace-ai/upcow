package services

import "context"

// 这些 wrapper 保留 TradingService 既有对外/对内方法名，
// 但实现已迁移到更小的文件中，避免 trading.go 继续膨胀。

func (s *TradingService) startOrderStatusSync(ctx context.Context) {
	if s.syncer == nil {
		return
	}
	s.syncer.startOrderStatusSyncImpl(ctx)
}

func (s *TradingService) syncAllOrderStatus(ctx context.Context) {
	if s.syncer == nil {
		return
	}
	s.syncer.syncAllOrderStatusImpl(ctx)
}

func (s *TradingService) SyncOrderStatus(ctx context.Context, orderID string) error {
	if s.syncer == nil {
		return nil
	}
	return s.syncer.syncOrderStatusImpl(ctx, orderID)
}

func (s *TradingService) startOrderConfirmationTimeoutCheck(ctx context.Context) {
	if s.syncer == nil {
		return
	}
	s.syncer.startOrderConfirmationTimeoutCheckImpl(ctx)
}

func (s *TradingService) checkOrderConfirmationTimeout(ctx context.Context) {
	if s.syncer == nil {
		return
	}
	s.syncer.checkOrderConfirmationTimeoutImpl(ctx)
}

func (s *TradingService) FetchUserPositionsFromAPI(ctx context.Context) error {
	if s.syncer == nil {
		return nil
	}
	return s.syncer.fetchUserPositionsFromAPIImpl(ctx)
}
