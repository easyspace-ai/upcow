package services

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/infrastructure/websocket"
	"github.com/betbot/gobet/pkg/logger"
)

// Scheduler 市场调度服务
type Scheduler struct {
	marketDataService *MarketDataService
	tradingService    *TradingService
	userCreds        *websocket.UserCredentials
	proxyURL          string
	currentMarket     *domain.Market
	currentBot        *TradingBot
	nextBot           *TradingBot
	mu                sync.RWMutex
}

// TradingBot 交易机器人
type TradingBot struct {
	market           *domain.Market
	marketWS         *websocket.MarketWebSocket
	userWS           *websocket.UserWebSocket
	tradingEnabled   bool
	mu               sync.RWMutex
}

// NewScheduler 创建新的调度服务（BBGO风格：不需要 Publisher）
func NewScheduler(
	marketDataService *MarketDataService,
	tradingService *TradingService,
	userCreds *websocket.UserCredentials,
	proxyURL string,
) *Scheduler {
	return &Scheduler{
		marketDataService: marketDataService,
		tradingService:    tradingService,
		userCreds:         userCreds,
		proxyURL:          proxyURL,
	}
}

// Start 启动调度服务
func (s *Scheduler) Start(ctx context.Context) error {
	logger.Info("启动市场调度服务...")

	// 获取当前周期的市场
	currentTs := GetCurrent15MinTimestamp()
	slug := Generate15MinSlug(currentTs)

	market, err := s.marketDataService.FetchMarketInfo(ctx, slug)
	if err != nil {
		return fmt.Errorf("获取当前市场失败: %w", err)
	}

	s.mu.Lock()
	s.currentMarket = market
	s.mu.Unlock()

	// 更新日志系统的市场周期时间戳，以便日志文件按市场周期命名
	logger.SetMarketTimestamp(market.Timestamp)
	// 强制切换日志文件（使用市场周期时间戳命名）
	if err := logger.CheckAndRotateLogWithForce(logger.Config{
		LogByCycle:    true,
		CycleDuration: 15 * time.Minute,
		OutputFile:    "", // 空字符串表示使用保存的基础路径
	}, true); err != nil {
		logger.Errorf("切换日志文件失败: %v", err)
	}

	// 启动当前周期的 bot
	bot, err := s.createBot(ctx, market)
	if err != nil {
		return fmt.Errorf("创建交易机器人失败: %w", err)
	}

	s.mu.Lock()
	s.currentBot = bot
	s.mu.Unlock()

	// 启动调度循环
	go s.scheduleLoop(ctx)

	logger.Info("市场调度服务已启动")
	return nil
}

// createBot 创建交易机器人
func (s *Scheduler) createBot(ctx context.Context, market *domain.Market) (*TradingBot, error) {
	// 创建市场价格 WebSocket（BBGO风格：不需要 Publisher）
	marketWS := websocket.NewMarketWebSocket()

	// 连接到市场价格 WebSocket（使用代理）
	if err := marketWS.Connect(ctx, market, s.proxyURL); err != nil {
		return nil, fmt.Errorf("连接市场价格 WebSocket 失败: %w", err)
	}

	// 创建用户订单 WebSocket（如果凭证存在）
	var userWS *websocket.UserWebSocket
	if s.userCreds != nil {
		userWS = websocket.NewUserWebSocket()
		if err := userWS.Connect(ctx, s.userCreds, s.proxyURL); err != nil {
			logger.Errorf("连接用户订单 WebSocket 失败: %v", err)
			// 不阻止 bot 创建，只记录错误
		} else {
			logger.Infof("用户订单 WebSocket 已连接")
		}
	}

	bot := &TradingBot{
		market:         market,
		marketWS:       marketWS,
		userWS:         userWS,
		tradingEnabled: true,
	}

	logger.Infof("创建交易机器人: market=%s", market.Slug)
	return bot, nil
}

// scheduleLoop 调度循环
func (s *Scheduler) scheduleLoop(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.checkAndSwitchMarket(ctx)
		}
	}
}

// checkAndSwitchMarket 检查并切换市场
func (s *Scheduler) checkAndSwitchMarket(ctx context.Context) {
	s.mu.RLock()
	currentMarket := s.currentMarket
	currentBot := s.currentBot
	s.mu.RUnlock()

	if currentMarket == nil {
		return
	}

	now := time.Now().Unix()
	currentEndTs := currentMarket.Timestamp + 900 // 15 分钟 = 900 秒

	// 检查是否需要切换到下一个市场
	if now >= currentEndTs {
		logger.Infof("当前市场周期结束: %s", currentMarket.Slug)

		// 停止当前 bot
		if currentBot != nil {
			if currentBot.marketWS != nil {
				if err := currentBot.marketWS.Close(); err != nil {
					logger.Errorf("关闭市场价格 WebSocket 失败: %v", err)
				}
			}
			if currentBot.userWS != nil {
				if err := currentBot.userWS.Close(); err != nil {
					logger.Errorf("关闭用户订单 WebSocket 失败: %v", err)
				}
			}
		}

		// 切换到下一个市场（此时数据应该已经在缓存中，零延迟）
		nextTs := currentEndTs
		nextSlug := Generate15MinSlug(nextTs)

		// 从缓存获取（应该已经在缓存中，< 1 微秒）
		nextMarket, err := s.marketDataService.FetchMarketInfo(ctx, nextSlug)
		if err != nil {
			logger.Errorf("获取下一个市场失败: %v", err)
			return
		}

		nextBot, err := s.createBot(ctx, nextMarket)
		if err != nil {
			logger.Errorf("创建下一个机器人失败: %v", err)
			return
		}

		s.mu.Lock()
		s.currentMarket = nextMarket
		s.currentBot = nextBot
		s.mu.Unlock()

		// 更新日志系统的市场周期时间戳，以便日志文件按市场周期命名
		logger.SetMarketTimestamp(nextMarket.Timestamp)
		// 强制切换日志文件（使用市场周期时间戳命名）
		if err := logger.CheckAndRotateLogWithForce(logger.Config{
			LogByCycle:    true,
			CycleDuration: 15 * time.Minute,
			OutputFile:    "", // 空字符串表示使用保存的基础路径
		}, true); err != nil {
			logger.Errorf("切换日志文件失败: %v", err)
		}

		logger.Infof("已切换到下一个市场: %s", nextMarket.Slug)
	}
}

// Stop 停止调度服务
func (s *Scheduler) Stop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.currentBot != nil {
		if s.currentBot.marketWS != nil {
			if err := s.currentBot.marketWS.Close(); err != nil {
				logger.Errorf("关闭当前 bot 市场价格 WebSocket 失败: %v", err)
			}
		}
		if s.currentBot.userWS != nil {
			if err := s.currentBot.userWS.Close(); err != nil {
				logger.Errorf("关闭当前 bot 用户订单 WebSocket 失败: %v", err)
			}
		}
	}

	if s.nextBot != nil {
		if s.nextBot.marketWS != nil {
			if err := s.nextBot.marketWS.Close(); err != nil {
				logger.Errorf("关闭下一个 bot 市场价格 WebSocket 失败: %v", err)
			}
		}
		if s.nextBot.userWS != nil {
			if err := s.nextBot.userWS.Close(); err != nil {
				logger.Errorf("关闭下一个 bot 用户订单 WebSocket 失败: %v", err)
			}
		}
	}

	logger.Info("市场调度服务已停止")
	return nil
}

