package luckbet

import (
	"context"
	"sync"
	"time"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/services"
	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/sirupsen/logrus"
)

var log = logrus.WithField("strategy", ID)

func init() { 
	bbgo.RegisterStrategy(ID, &Strategy{}) 
}

// Strategy LuckBet策略主结构体
// 实现基于价格速度的高频交易策略，通过监控UP/DOWN代币的价格变化速度执行配对交易
type Strategy struct {
	TradingService *services.TradingService
	Config         `yaml:",inline" json:",inline"`

	mu sync.RWMutex
	// 避免在周期切换/重复 Subscribe 时重复注册 handler
	orderUpdateOnce sync.Once

	// 核心组件（将在后续任务中实现）
	velocityEngine   *VelocityEngine
	orderExecutor    *OrderExecutor
	riskController   *RiskController
	positionManager  *PositionManager
	terminalUI       *TerminalUI
	configManager    *ConfigManager

	// 交易状态
	tradingState *TradingState
	
	// 性能指标
	performanceMetrics *PerformanceMetrics
}

func (s *Strategy) ID() string      { return ID }
func (s *Strategy) Name() string    { return ID }
func (s *Strategy) Defaults() error { return nil }
func (s *Strategy) Validate() error { return s.Config.Validate() }

// Initialize 初始化策略
func (s *Strategy) Initialize() error {
	log.Infof("🚀 [%s] 初始化LuckBet策略", ID)
	
	// 应用默认配置
	s.Config.ApplyDefaults()
	
	// 初始化交易状态
	if s.tradingState == nil {
		s.tradingState = NewTradingState()
	}
	
	// 初始化性能指标
	if s.performanceMetrics == nil {
		s.performanceMetrics = &PerformanceMetrics{}
	}
	
	// 注册订单更新回调
	if s.TradingService != nil {
		s.orderUpdateOnce.Do(func() {
			handler := services.OrderUpdateHandlerFunc(s.OnOrderUpdate)
			s.TradingService.OnOrderUpdate(handler)
			log.Infof("✅ [%s] 已注册订单更新回调", ID)
		})
	}
	
	log.Infof("✅ [%s] 策略初始化完成", ID)
	return nil
}

// Subscribe 订阅市场事件
func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
	session.OnPriceChanged(s)
	log.Infof("✅ [%s] 策略已订阅价格变化事件 (session=%s)", ID, session.Name)

	// 兜底：确保订单更新回调已注册
	if s.TradingService != nil {
		s.orderUpdateOnce.Do(func() {
			handler := services.OrderUpdateHandlerFunc(s.OnOrderUpdate)
			s.TradingService.OnOrderUpdate(handler)
			log.Infof("✅ [%s] 已注册订单更新回调（Subscribe兜底）", ID)
		})
	}
}

// Run 运行策略
func (s *Strategy) Run(ctx context.Context, _ bbgo.OrderExecutor, _ *bbgo.ExchangeSession) error {
	log.Infof("🏃 [%s] 策略开始运行", ID)
	
	// 启动终端UI（如果启用）
	if s.EnableTerminalUI && s.terminalUI != nil {
		go func() {
			if err := s.terminalUI.Start(ctx); err != nil {
				log.Warnf("⚠️ [%s] 终端UI启动失败: %v", ID, err)
			}
		}()
	}
	
	// 等待上下文取消
	<-ctx.Done()
	
	// 清理资源
	if s.terminalUI != nil {
		s.terminalUI.Stop()
	}
	
	log.Infof("🛑 [%s] 策略已停止", ID)
	return ctx.Err()
}

// OnCycle 周期切换回调
func (s *Strategy) OnCycle(_ context.Context, _ *domain.Market, newMarket *domain.Market) {
	s.mu.Lock()
	defer s.mu.Unlock()
	
	log.Infof("🔄 [%s] 周期切换: %s", ID, newMarket.Slug)
	
	// 重置交易状态
	s.tradingState.Reset()
	s.tradingState.CurrentCycle = newMarket.Slug
	s.tradingState.CycleStartTime = time.Unix(newMarket.Timestamp, 0)
	
	// 重置组件状态（将在后续任务中实现）
	// if s.velocityEngine != nil {
	//     s.velocityEngine.Reset()
	// }
	// if s.riskController != nil {
	//     s.riskController.ResetCycle()
	// }
	
	log.Infof("✅ [%s] 周期切换完成: %s", ID, newMarket.Slug)
}

// OnOrderUpdate 订单更新回调
func (s *Strategy) OnOrderUpdate(ctx context.Context, order *domain.Order) error {
	if order == nil || order.OrderID == "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	log.Debugf("📊 [%s] 订单状态更新: orderID=%s status=%s filledSize=%.4f",
		ID, order.OrderID, order.Status, order.FilledSize)

	// 更新交易状态中的待处理订单
	if order.Status == domain.OrderStatusFilled ||
		order.Status == domain.OrderStatusCanceled ||
		order.Status == domain.OrderStatusFailed {
		
		// 从待处理交易中移除
		for entryID, hedgeID := range s.tradingState.PendingTrades {
			if entryID == order.OrderID || hedgeID == order.OrderID {
				delete(s.tradingState.PendingTrades, entryID)
				break
			}
		}
		
		// 从未对冲入场订单中移除
		delete(s.tradingState.UnhedgedEntries, order.OrderID)
	}

	// 记录订单失败
	if order.Status == domain.OrderStatusFailed {
		log.Warnf("⚠️ [%s] 订单失败: orderID=%s", ID, order.OrderID)
		s.performanceMetrics.FailedTrades++
	}

	// 记录成功交易
	if order.Status == domain.OrderStatusFilled {
		s.performanceMetrics.SuccessfulTrades++
	}

	return nil
}

// OnPriceChanged 处理价格变化事件
func (s *Strategy) OnPriceChanged(ctx context.Context, e *events.PriceChangedEvent) error {
	if e == nil || e.Market == nil || s.TradingService == nil {
		return nil
	}

	// 系统级安全兜底：仅处理当前周期market的事件
	cur := s.TradingService.GetCurrentMarket()
	if cur != "" && cur != e.Market.Slug {
		log.Debugf("🔄 [%s] 跳过非当前周期价格事件: eventMarket=%s currentMarket=%s", 
			ID, e.Market.Slug, cur)
		return nil
	}

	// 记录首次接收到价格数据的时间
	s.mu.Lock()
	if s.tradingState.FirstSeenAt.IsZero() {
		s.tradingState.FirstSeenAt = time.Now()
		log.Infof("👁️ [%s] 首次接收到价格数据: market=%s", ID, e.Market.Slug)
	}
	s.mu.Unlock()

	// 核心交易逻辑将在后续任务中实现
	// 1. 添加价格样本到速度引擎
	// 2. 计算速度指标
	// 3. 检查触发条件
	// 4. 执行风险检查
	// 5. 执行配对交易
	// 6. 更新UI显示

	log.Debugf("📈 [%s] 价格变化: market=%s tokenType=%s newPrice=%.4f", 
		ID, e.Market.Slug, e.TokenType, e.NewPrice.ToDecimal())

	return nil
}

// GetTradingState 获取交易状态（线程安全）
func (s *Strategy) GetTradingState() *TradingState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tradingState
}

// GetPerformanceMetrics 获取性能指标（线程安全）
func (s *Strategy) GetPerformanceMetrics() *PerformanceMetrics {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.performanceMetrics
}

// 占位符结构体定义（将在后续任务中实现）

// VelocityEngine 速度引擎占位符
type VelocityEngine struct {
	// 将在任务2中实现
}

// OrderExecutor 订单执行器占位符
type OrderExecutor struct {
	// 将在任务3中实现
}

// RiskController 风险控制器占位符
type RiskController struct {
	// 将在任务4中实现
}

// PositionManager 头寸管理器占位符
type PositionManager struct {
	// 将在任务5中实现
}

// TerminalUI 终端UI占位符
type TerminalUI struct {
	// 将在任务6中实现
}

// Start 启动终端UI占位符
func (ui *TerminalUI) Start(ctx context.Context) error {
	// 将在任务6中实现
	return nil
}

// Stop 停止终端UI占位符
func (ui *TerminalUI) Stop() error {
	// 将在任务6中实现
	return nil
}