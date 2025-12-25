package bbgo

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/infrastructure/websocket"
	"github.com/betbot/gobet/internal/services"
	"github.com/betbot/gobet/pkg/logger"
	"github.com/betbot/gobet/pkg/marketspec"
	"github.com/sirupsen/logrus"
)

var schedulerLog = logrus.WithField("component", "market_scheduler")

// SessionSwitchCallback 会话切换回调函数类型
type SessionSwitchCallback func(oldSession *ExchangeSession, newSession *ExchangeSession, newMarket *domain.Market)

// MarketScheduler 市场调度器（BBGO风格）
// 负责每15分钟自动切换到下一个市场周期
type MarketScheduler struct {
	environment       *Environment
	marketDataService *services.MarketDataService
	proxyURL          string
	userCreds         *websocket.UserCredentials
	wsManager         *WebSocketManager
	spec              marketspec.MarketSpec

	// 当前会话
	currentSession *ExchangeSession
	currentMarket  *domain.Market
	sessionName    string

	// 会话切换回调
	sessionSwitchCallback SessionSwitchCallback

	// 控制
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	mu     sync.RWMutex
}

// NewMarketScheduler 创建新的市场调度器
func NewMarketScheduler(
	environ *Environment,
	marketDataService *services.MarketDataService,
	sessionName string,
	proxyURL string,
	userCreds *websocket.UserCredentials,
	spec marketspec.MarketSpec,
) *MarketScheduler {
	ctx, cancel := context.WithCancel(context.Background())
	return &MarketScheduler{
		environment:       environ,
		marketDataService: marketDataService,
		sessionName:       sessionName,
		proxyURL:          proxyURL,
		userCreds:         userCreds,
		wsManager:         NewWebSocketManager(proxyURL, userCreds),
		spec:              spec,
		ctx:               ctx,
		cancel:            cancel,
	}
}

// OnSessionSwitch 设置会话切换回调
func (s *MarketScheduler) OnSessionSwitch(callback SessionSwitchCallback) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionSwitchCallback = callback
}

// Start 启动市场调度器
func (s *MarketScheduler) Start(ctx context.Context) error {
	schedulerLog.Info("启动市场调度器...")

	// 获取当前周期的市场
	currentTs := s.spec.CurrentPeriodStartUnix(time.Now())
	slug := s.spec.Slug(currentTs)

	market, err := s.marketDataService.FetchMarketInfo(ctx, slug)
	if err != nil {
		return fmt.Errorf("获取当前市场失败: %w", err)
	}

	// 更新日志系统的市场周期时间戳
	logger.SetMarketInfo(market.Slug, market.Timestamp)
	// 强制切换日志文件（使用市场周期时间戳命名）
	if err := logger.CheckAndRotateLogWithForce(logger.Config{
		LogByCycle:    true,
		CycleDuration: s.spec.Duration(),
		OutputFile:    "", // 空字符串表示使用保存的基础路径
	}, true); err != nil {
		schedulerLog.Errorf("切换日志文件失败: %v", err)
	}

	// 创建初始会话
	session, err := s.createSession(ctx, market)
	if err != nil {
		return fmt.Errorf("创建会话失败: %w", err)
	}

	s.mu.Lock()
	s.currentSession = session
	s.currentMarket = market
	s.environment.AddSession(s.sessionName, session)
	s.mu.Unlock()

	// 启动调度循环
	s.wg.Add(1)
	go s.scheduleLoop()

	schedulerLog.Info("市场调度器已启动")
	return nil
}

// createSession 创建新的交易所会话
func (s *MarketScheduler) createSession(ctx context.Context, market *domain.Market) (*ExchangeSession, error) {
	session := NewExchangeSession(s.sessionName)
	if s.wsManager != nil {
		_ = s.wsManager.AttachToSession(ctx, session, market)
	} else {
		session.SetMarket(market)
	}

	// 连接会话
	if err := session.Connect(ctx); err != nil {
		return nil, fmt.Errorf("连接会话失败: %w", err)
	}

	// 检查 handlers 状态（用于调试）
	if session.MarketDataStream != nil {
		if ms, ok := session.MarketDataStream.(*websocket.MarketStream); ok {
			handlerCount := ms.HandlerCount()
			schedulerLog.Infof("✅ [周期切换] 新会话 MarketStream handlers 数量=%d，市场=%s", handlerCount, market.Slug)
			if handlerCount == 0 {
				schedulerLog.Errorf("❌ [周期切换] 错误：MarketStream handlers 为空！sessionPriceHandler 未注册！市场=%s", market.Slug)
			}
		}
	}
	handlerCount := session.PriceChangeHandlerCount()
	schedulerLog.Infof("✅ [周期切换] 新会话 Session priceChangeHandlers 数量=%d，市场=%s", handlerCount, market.Slug)

	schedulerLog.Infof("创建会话: market=%s", market.Slug)
	return session, nil
}

// scheduleLoop 调度循环
func (s *MarketScheduler) scheduleLoop() {
	defer s.wg.Done()

	for {
		// 热路径优化：不再每秒轮询，改为“睡到下个周期边界再检查”。
		// 仍可被 ctx.Done() 立即打断，且在时间漂移/边界附近会自动回到短周期检查。
		sleepFor := s.nextScheduleSleep()
		timer := time.NewTimer(sleepFor)
		select {
		case <-s.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			// 到点检查/切换
			s.checkAndSwitchMarket()
		}
	}
}

// nextScheduleSleep 计算调度循环下一次醒来的时间。
// - 正常情况下：睡到当前 market 的周期结束边界（减少无意义唤醒）
// - 异常/边界情况下：回退为短睡眠，确保能及时切换
func (s *MarketScheduler) nextScheduleSleep() time.Duration {
	s.mu.RLock()
	currentMarket := s.currentMarket
	s.mu.RUnlock()

	if currentMarket == nil {
		return 1 * time.Second
	}

	endTs := currentMarket.Timestamp + int64(s.spec.Duration().Seconds())
	deadline := time.Unix(endTs, 0)
	d := time.Until(deadline)

	// 已过期/接近边界：快速检查（避免卡在负 duration）
	if d <= 0 {
		return 50 * time.Millisecond
	}

	// 边界前做一次“提前唤醒”，给切换/日志滚动/连接时间留一点余量
	if d > 500*time.Millisecond {
		d -= 500 * time.Millisecond
	}

	// 防御：避免睡得过久导致对时间漂移完全无感；15m/1h 这种周期里每 30s 醒一次足够
	if d > 30*time.Second {
		return 30 * time.Second
	}
	return d
}

// checkAndSwitchMarket 检查并切换市场
func (s *MarketScheduler) checkAndSwitchMarket() {
	s.mu.RLock()
	currentMarket := s.currentMarket
	currentSession := s.currentSession
	s.mu.RUnlock()

	if currentMarket == nil {
		return
	}

	now := time.Now().Unix()
	// 正常周期结束时间（timeframe duration 后）
	normalEndTs := currentMarket.Timestamp + int64(s.spec.Duration().Seconds())

	// 检查是否需要切换到下一个市场
	// 条件：正常周期结束（15分钟后）
	if now >= normalEndTs {
		schedulerLog.Infof("当前市场周期结束: %s", currentMarket.Slug)

		// 关闭当前会话
		if currentSession != nil {
			schedulerLog.Infof("🔕 [unsubscribe] 准备关闭旧会话并退订：session=%s, market=%s", s.sessionName, currentMarket.Slug)
			if err := currentSession.Close(); err != nil {
				schedulerLog.Errorf("关闭当前会话失败: %v", err)
			} else {
				schedulerLog.Infof("✅ [unsubscribe] 旧会话退订完成：session=%s, market=%s", s.sessionName, currentMarket.Slug)
			}
		}

		// 切换到下一个市场
		// 计算下一个周期的时间戳
		nextPeriodTs := s.spec.CurrentPeriodStartUnix(time.Now())
		// 如果当前周期还没结束，切换到下一个周期
		if nextPeriodTs <= currentMarket.Timestamp {
			nextPeriodTs = currentMarket.Timestamp + int64(s.spec.Duration().Seconds())
		}
		nextSlug := s.spec.Slug(nextPeriodTs)

		// 从缓存获取下一个市场
		schedulerLog.Infof("准备切换到下一个市场: %s (当前周期=%d, 下一个周期=%d)",
			nextSlug, currentMarket.Timestamp, nextPeriodTs)
		nextMarket, err := s.marketDataService.FetchMarketInfo(s.ctx, nextSlug)
		if err != nil {
			schedulerLog.Errorf("获取下一个市场失败: %v", err)
			return
		}

		// 更新日志系统的市场周期时间戳（在创建新会话之前，确保新会话的连接日志写入新周期的日志文件）
		logger.SetMarketInfo(nextMarket.Slug, nextMarket.Timestamp)
		// 强制切换日志文件（在创建新会话之前）
		if err := logger.CheckAndRotateLogWithForce(logger.Config{
			LogByCycle:    true,
			CycleDuration: s.spec.Duration(),
			OutputFile:    "",
		}, true); err != nil {
			schedulerLog.Errorf("切换日志文件失败: %v", err)
		}

		// 创建新会话（在日志文件切换之后，确保连接日志写入新周期的日志文件）
		nextSession, err := s.createSession(s.ctx, nextMarket)
		if err != nil {
			schedulerLog.Errorf("创建下一个会话失败: %v", err)
			return
		}

		s.mu.Lock()
		// 更新环境中的会话
		s.environment.AddSession(s.sessionName, nextSession)
		oldSession := s.currentSession
		s.currentSession = nextSession
		s.currentMarket = nextMarket
		callback := s.sessionSwitchCallback
		s.mu.Unlock()

		schedulerLog.Infof("已切换到下一个市场: %s", nextMarket.Slug)

		// 触发会话切换回调（在锁外调用，避免死锁）
		if callback != nil {
			schedulerLog.Infof("触发会话切换回调，重新注册策略到新会话")
			callback(oldSession, nextSession, nextMarket)
		}
	}
}

// Stop 停止市场调度器
func (s *MarketScheduler) Stop(ctx context.Context) error {
	schedulerLog.Info("停止市场调度器...")

	// 取消上下文
	s.cancel()

	// 等待调度循环退出
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		schedulerLog.Info("市场调度器已停止")
	case <-ctx.Done():
		schedulerLog.Warn("停止市场调度器超时")
	}

	// 关闭当前会话
	s.mu.RLock()
	currentSession := s.currentSession
	s.mu.RUnlock()

	if currentSession != nil {
		if err := currentSession.Close(); err != nil {
			schedulerLog.Errorf("关闭当前会话失败: %v", err)
		}
	}

	return nil
}

// CurrentSession 获取当前会话
func (s *MarketScheduler) CurrentSession() *ExchangeSession {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currentSession
}

// CurrentMarket 获取当前市场
func (s *MarketScheduler) CurrentMarket() *domain.Market {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currentMarket
}
