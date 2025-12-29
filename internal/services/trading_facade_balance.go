package services

import (
	"context"
	"fmt"
)

// RefreshBalance 从链上/交易所 API 刷新 USDC 余额与授权，并更新 OrderEngine 的本地余额。
//
// 说明：
// - 这是“滚动放大/按余额动态定额”类策略必须的能力，因为结算/赎回带来的余额变化不一定会通过订单回调进入本地状态。
// - 该方法可能较慢（会访问 RPC + API），策略应使用短超时调用（例如 5-15 秒），失败则回退到本地余额。
func (s *TradingService) RefreshBalance(ctx context.Context) error {
	if s == nil || s.balances == nil {
		return fmt.Errorf("balance service not initialized")
	}
	// ⚠️ 纸交易模式下不刷新余额，避免覆盖纸交易模式设置的初始余额
	if s.dryRun {
		return nil
	}
	s.balances.initializeBalance(ctx)
	return nil
}

