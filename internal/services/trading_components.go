package services

// Component wrappers for TradingService.
// These are internal helpers that make responsibilities explicit,
// while TradingService keeps the public surface for compatibility.

type OrdersService struct{ s *TradingService }

type PositionsService struct{ s *TradingService }

type OrdersManageService struct{ s *TradingService }

type BalanceService struct{ s *TradingService }

type SnapshotService struct{ s *TradingService }

type OrderSyncService struct{ s *TradingService }
