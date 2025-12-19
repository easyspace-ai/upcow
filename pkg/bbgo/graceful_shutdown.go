package bbgo

import (
	"context"
	"sync"

	"github.com/betbot/gobet/pkg/shutdown"
	"github.com/sirupsen/logrus"
)

var shutdownLog = logrus.WithField("component", "graceful_shutdown")

// ShutdownHandler 关闭处理函数（BBGO 风格）
type ShutdownHandler func(ctx context.Context, wg *sync.WaitGroup)

// OnShutdown 注册关闭回调（BBGO 风格）
// 这个函数可以从策略的 Run 方法中调用，注册关闭时的清理逻辑
// 注意：在我们的实现中，策略的 Shutdown 方法已经通过 Trader 自动注册
// 这个函数主要用于保持 BBGO 风格的 API 兼容性
// 如果策略需要在 Run 方法中注册额外的关闭回调，可以通过其他方式实现
func OnShutdown(ctx context.Context, handler ShutdownHandler) {
	// 注意：BBGO 原项目使用 isolation context 来存储 shutdown manager
	// 我们的简化实现中，策略的 Shutdown 方法已经通过 Trader 自动注册
	// 如果策略需要在 Run 方法中注册额外的关闭回调，可以通过其他方式实现
	shutdownLog.Debugf("OnShutdown 被调用，策略的 Shutdown 方法已通过 Trader 自动注册")
}

// Shutdown 执行关闭（BBGO 风格）
// 这个函数应该从 main 中调用，传入一个带超时的 context
func Shutdown(ctx context.Context, shutdownManager *shutdown.Manager) {
	shutdownLog.Infof("shutting down...")
	if shutdownManager != nil {
		shutdownManager.Shutdown(ctx)
	} else {
		shutdownLog.Warnf("shutdown manager 为空，跳过关闭回调")
	}
}

