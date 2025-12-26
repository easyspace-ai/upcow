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

	// fail-safe：当无法获取/校验下一周期市场时，进入暂停模式，确保“不交易”
	paused       bool
	pendingSlug  string
	pendingSince time.Time

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

// pauseTradingAndCloseSession 进入“暂停交易”模式（fail-safe），确保不会继续交易旧周期。
// - 尽最大努力撤单（CancelOrdersNotInMarket("") => cancel all）
// - TradingService 进入 PauseTrading（PlaceOrder 直接拒绝）
// - 关闭当前 session（断开 WS，停止行情输入）
// - 记录 pendingSlug，后续周期调度会持续重试直到恢复
func (s *MarketScheduler) pauseTradingAndCloseSession(pendingSlug string, reason string, err error) {
	if s == nil {
		return
	}
	if reason == "" {
		reason = "unknown"
	}
	// 1) 先撤单 + 暂停 TradingService（保证“不交易”）
	if s.environment != nil && s.environment.TradingService != nil {
		cancelCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		s.environment.TradingService.CancelOrdersNotInMarket(cancelCtx, "")
		cancel()
		s.environment.TradingService.PauseTrading(reason)
	}

	// 2) 关闭当前 session（停止 WS 输入）
	s.mu.Lock()
	oldSession := s.currentSession
	s.currentSession = nil
	s.currentMarket = nil
	s.paused = true
	s.pendingSlug = pendingSlug
	s.pendingSince = time.Now()
	s.mu.Unlock()

	if oldSession != nil {
		_ = oldSession.Close()
	}

	schedulerLog.Errorf("🛑 [暂停交易] 已进入 fail-safe：pendingSlug=%s reason=%s err=%v", pendingSlug, reason, err)
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
	paused := s.paused
	pendingSlug := s.pendingSlug
	s.mu.RUnlock()

	// 0) 暂停模式：持续重试获取 pendingSlug，成功后恢复交易
	if paused && pendingSlug != "" && (currentMarket == nil || currentSession == nil) {
		nextMarket, err := s.marketDataService.FetchMarketInfo(s.ctx, pendingSlug)
		if err != nil {
			schedulerLog.Errorf("⏳ [暂停交易] 仍无法获取下一周期市场，继续暂停：slug=%s err=%v", pendingSlug, err)
			return
		}
		nextSession, err := s.createSession(s.ctx, nextMarket)
		if err != nil {
			schedulerLog.Errorf("⏳ [暂停交易] 创建恢复会话失败，继续暂停：slug=%s err=%v", pendingSlug, err)
			return
		}

		s.mu.Lock()
		s.environment.AddSession(s.sessionName, nextSession)
		callback := s.sessionSwitchCallback
		s.currentSession = nextSession
		s.currentMarket = nextMarket
		s.paused = false
		s.pendingSlug = ""
		s.pendingSince = time.Time{}
		s.mu.Unlock()

		schedulerLog.Warnf("✅ [恢复交易] 已恢复到新周期：market=%s", nextMarket.Slug)
		if callback != nil {
			callback(nil, nextSession, nextMarket)
		}
		return
	}

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
			// fail-safe：拿不到下一周期 market，必须立刻停止交易（避免继续交易旧周期）
			s.pauseTradingAndCloseSession(nextSlug, "fetch_next_market_failed", err)
			return
		}

		// 更新日志系统的市场周期时间戳（在切换市场之前，确保连接日志写入新周期的日志文件）
		logger.SetMarketInfo(nextMarket.Slug, nextMarket.Timestamp)
		// 强制切换日志文件（在切换市场之前）
		if err := logger.CheckAndRotateLogWithForce(logger.Config{
			LogByCycle:    true,
			CycleDuration: s.spec.Duration(),
			OutputFile:    "",
		}, true); err != nil {
			schedulerLog.Errorf("切换日志文件失败: %v", err)
		}

		// 使用动态订阅切换市场（不关闭连接）
		if currentSession != nil && currentSession.MarketDataStream != nil {
			if ms, ok := currentSession.MarketDataStream.(*websocket.MarketStream); ok {
				schedulerLog.Infof("🔄 [切换市场] 使用动态订阅切换: %s -> %s", currentMarket.Slug, nextMarket.Slug)

				// 【修复】先更新会话的市场信息，确保策略能获取到正确的市场信息
				currentSession.SetMarket(nextMarket)

				// 【关键修复】在“更新当前市场信息并触发回调”之前，先原地清空 WS bestBook。
				// 否则会出现一个严重窗口：
				// - 回调里 TradingService.SetCurrentMarketInfo 已更新为新周期
				// - 策略立刻调用 GetTopOfBook/ GetBestPrice（source=ws.bestbook）
				// - 但 bestBook 仍然是旧周期的“新鲜数据”，会被当作新周期使用（你日志里的 0.99/1.0）
				if bb := ms.BestBook(); bb != nil {
					bb.Reset()
				}

				// 【修复】先触发回调注册价格处理器，然后再订阅市场（避免价格数据丢失）
				// 注意：这里先更新状态，让回调中的策略能获取到正确的市场信息
				s.mu.Lock()
				oldSession := s.currentSession
				s.currentMarket = nextMarket
				callback := s.sessionSwitchCallback
				s.mu.Unlock()

				// 先触发回调，让策略注册价格处理器
				if callback != nil {
					schedulerLog.Infof("🔄 [切换市场] 先注册价格处理器，然后再订阅市场")
					callback(oldSession, currentSession, nextMarket)
					// 等待一小段时间，确保价格处理器已注册
					time.Sleep(100 * time.Millisecond)
				}

				// 现在订阅新市场（价格处理器已注册）
				if err := ms.SwitchMarket(s.ctx, currentMarket, nextMarket); err != nil {
					schedulerLog.Errorf("动态切换市场失败: %v，回退到创建新会话", err)
					// 回退：如果动态切换失败，创建新会话
					nextSession, err := s.createSession(s.ctx, nextMarket)
					if err != nil {
						s.pauseTradingAndCloseSession(nextMarket.Slug, "create_session_failed_after_switch_fail", err)
						return
					}

					s.mu.Lock()
					// 动态切换失败时：必须关闭旧 session，避免旧 WS/旧 user stream 继续推送导致重复事件与资源泄漏。
					if currentSession != nil {
						_ = currentSession.Close()
					}
					s.environment.AddSession(s.sessionName, nextSession)
					oldSession := s.currentSession
					s.currentSession = nextSession
					s.currentMarket = nextMarket
					callback := s.sessionSwitchCallback
					s.mu.Unlock()

					schedulerLog.Infof("已切换到下一个市场（回退模式）: %s", nextMarket.Slug)

					if callback != nil {
						schedulerLog.Infof("触发会话切换回调，重新注册策略到新会话")
						callback(oldSession, nextSession, nextMarket)
					}
					return
				}
				// 动态切换成功，市场信息已在上面更新
				schedulerLog.Infof("✅ 已切换到下一个市场（动态订阅）: %s", nextMarket.Slug)
				return
			} else {
				schedulerLog.Warnf("⚠️ MarketDataStream 不是 MarketStream 类型，无法使用动态订阅，回退到创建新会话")
				// 回退：创建新会话
				nextSession, err := s.createSession(s.ctx, nextMarket)
				if err != nil {
					s.pauseTradingAndCloseSession(nextMarket.Slug, "create_session_failed_fallback_not_marketstream", err)
					return
				}

				s.mu.Lock()
				// 关闭旧会话
				if currentSession != nil {
					_ = currentSession.Close()
				}
				s.environment.AddSession(s.sessionName, nextSession)
				oldSession := s.currentSession
				s.currentSession = nextSession
				s.currentMarket = nextMarket
				callback := s.sessionSwitchCallback
				s.mu.Unlock()

				schedulerLog.Infof("已切换到下一个市场（回退模式）: %s", nextMarket.Slug)

				if callback != nil {
					schedulerLog.Infof("触发会话切换回调，重新注册策略到新会话")
					callback(oldSession, nextSession, nextMarket)
				}
				return
			}
		} else {
			// 会话或 MarketDataStream 不存在，创建新会话
			schedulerLog.Infof("会话或 MarketDataStream 不存在，创建新会话")
			nextSession, err := s.createSession(s.ctx, nextMarket)
			if err != nil {
				s.pauseTradingAndCloseSession(nextMarket.Slug, "create_session_failed_no_session_or_stream", err)
				return
			}

			s.mu.Lock()
			// 关闭旧会话（如果存在）
			if currentSession != nil {
				_ = currentSession.Close()
			}
			s.environment.AddSession(s.sessionName, nextSession)
			oldSession := s.currentSession
			s.currentSession = nextSession
			s.currentMarket = nextMarket
			callback := s.sessionSwitchCallback
			s.mu.Unlock()

			schedulerLog.Infof("已切换到下一个市场（新建会话）: %s", nextMarket.Slug)

			if callback != nil {
				schedulerLog.Infof("触发会话切换回调，重新注册策略到新会话")
				callback(oldSession, nextSession, nextMarket)
			}
			return
		}

		// 这段代码不应该执行到（上面已经 return），但保留作为安全网
		s.mu.Lock()
		oldSession := s.currentSession
		s.currentMarket = nextMarket
		callback := s.sessionSwitchCallback
		s.mu.Unlock()

		schedulerLog.Warnf("⚠️ [切换市场] 执行到不应该到达的代码路径，市场=%s", nextMarket.Slug)

		// 触发会话切换回调（会话对象不变，只更新市场订阅）
		if callback != nil {
			schedulerLog.Infof("触发会话切换回调，更新策略市场信息")
			callback(oldSession, currentSession, nextMarket)
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
