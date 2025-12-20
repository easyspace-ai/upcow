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
	"github.com/sirupsen/logrus"
)

var schedulerLog = logrus.WithField("component", "market_scheduler")

// SessionSwitchCallback 会话切换回调函数类型
type SessionSwitchCallback func(oldSession *ExchangeSession, newSession *ExchangeSession, newMarket *domain.Market)

// MarketScheduler 市场调度器（BBGO风格）
// 负责每15分钟自动切换到下一个市场周期
type MarketScheduler struct {
	environment      *Environment
	marketDataService *services.MarketDataService
	proxyURL          string
	userCreds        *websocket.UserCredentials
	
	// 当前会话
	currentSession   *ExchangeSession
	currentMarket    *domain.Market
	sessionName      string
	
	// 会话切换回调
	sessionSwitchCallback SessionSwitchCallback
	
	// 控制
	ctx              context.Context
	cancel           context.CancelFunc
	wg               sync.WaitGroup
	mu               sync.RWMutex
}

// NewMarketScheduler 创建新的市场调度器
func NewMarketScheduler(
	environ *Environment,
	marketDataService *services.MarketDataService,
	sessionName string,
	proxyURL string,
	userCreds *websocket.UserCredentials,
) *MarketScheduler {
	ctx, cancel := context.WithCancel(context.Background())
	return &MarketScheduler{
		environment:      environ,
		marketDataService: marketDataService,
		sessionName:      sessionName,
		proxyURL:         proxyURL,
		userCreds:        userCreds,
		ctx:              ctx,
		cancel:           cancel,
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
	currentTs := services.GetCurrent15MinTimestamp()
	slug := services.Generate15MinSlug(currentTs)

	market, err := s.marketDataService.FetchMarketInfo(ctx, slug)
	if err != nil {
		return fmt.Errorf("获取当前市场失败: %w", err)
	}

	// 更新日志系统的市场周期时间戳
	logger.SetMarketTimestamp(market.Timestamp)
	// 强制切换日志文件（使用市场周期时间戳命名）
	if err := logger.CheckAndRotateLogWithForce(logger.Config{
		LogByCycle:    true,
		CycleDuration: 15 * time.Minute,
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
	session.SetMarket(market)

	// 创建 MarketStream
	marketStream := websocket.NewMarketStream()
	marketStream.SetProxyURL(s.proxyURL)
	session.SetMarketDataStream(marketStream)

	// 创建 UserWebSocket（如果凭证存在）
	if s.userCreds != nil {
		userWebSocket := websocket.NewUserWebSocket()
		session.SetUserDataStream(userWebSocket)
		
		// 异步连接 UserWebSocket
		go func() {
			if err := userWebSocket.Connect(ctx, s.userCreds, s.proxyURL); err != nil {
				schedulerLog.Errorf("连接用户订单 WebSocket 失败: %v", err)
			} else {
				schedulerLog.Infof("用户订单 WebSocket 已连接")
			}
		}()
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
	
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.checkAndSwitchMarket()
		}
	}
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
	// 正常周期结束时间（15分钟后）
	normalEndTs := currentMarket.Timestamp + 900
	
	// 检查是否需要切换到下一个市场
	// 条件：正常周期结束（15分钟后）
	if now >= normalEndTs {
		schedulerLog.Infof("当前市场周期结束: %s", currentMarket.Slug)

		// 关闭当前会话
		if currentSession != nil {
			if err := currentSession.Close(); err != nil {
				schedulerLog.Errorf("关闭当前会话失败: %v", err)
			}
		}

		// 切换到下一个市场
		// 计算下一个15分钟周期的时间戳
		nextPeriodTs := services.GetCurrent15MinTimestamp()
		// 如果当前周期还没结束，切换到下一个15分钟周期
		if nextPeriodTs <= currentMarket.Timestamp {
			nextPeriodTs = currentMarket.Timestamp + 900 // 下一个15分钟周期
		}
		nextSlug := services.Generate15MinSlug(nextPeriodTs)

		// 从缓存获取下一个市场
		schedulerLog.Infof("准备切换到下一个市场: %s (当前周期=%d, 下一个周期=%d)",
			nextSlug, currentMarket.Timestamp, nextPeriodTs)
		nextMarket, err := s.marketDataService.FetchMarketInfo(s.ctx, nextSlug)
		if err != nil {
			schedulerLog.Errorf("获取下一个市场失败: %v", err)
			return
		}

		// 更新日志系统的市场周期时间戳（在创建新会话之前，确保新会话的连接日志写入新周期的日志文件）
		logger.SetMarketTimestamp(nextMarket.Timestamp)
		// 强制切换日志文件（在创建新会话之前）
		if err := logger.CheckAndRotateLogWithForce(logger.Config{
			LogByCycle:    true,
			CycleDuration: 15 * time.Minute,
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

