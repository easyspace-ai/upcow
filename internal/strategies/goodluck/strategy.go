package goodluck

import (
	"context"

	"github.com/betbot/gobet/pkg/bbgo"
)

// 注意：
// 该仓库当前仅保留了 GoodLuck 的策略配置样例（yml/goodluck.yaml），
// 但策略实现代码在历史演进中被移除/拆分。
//
// 为了保证 `go test ./...` 与策略注册表可正常编译，这里提供一个“空实现”占位策略：
// - 能被配置加载与注册
// - 不做任何交易行为
//
// 后续会以模块化方式补齐 GoodLuck 的完整实现。

func init() {
	bbgo.RegisterStrategy("goodluck", &Strategy{})
}

type Strategy struct{}

func (s *Strategy) ID() string { return "goodluck" }

// Run implements bbgo.SingleExchangeStrategy. It blocks until ctx is done.
func (s *Strategy) Run(ctx context.Context, _ bbgo.OrderExecutor, _ *bbgo.ExchangeSession) error {
	<-ctx.Done()
	return ctx.Err()
}

