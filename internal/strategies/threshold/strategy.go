package threshold

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/strategies"
	"github.com/betbot/gobet/internal/strategies/orderutil"
	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/betbot/gobet/pkg/logger"
	"github.com/sirupsen/logrus"
)

const ID = "threshold"

var log = logrus.WithField("strategy", ID)

func init() {
	// BBGO风格：在init函数中注册策略及其配置适配器
	bbgo.RegisterStrategyWithAdapter(ID, &ThresholdStrategy{}, &ThresholdConfigAdapter{})
}

// ThresholdStrategy 价格阈值策略实现
// 当价格超过阈值时买入，当价格低于阈值时卖出（如果配置了卖出阈值）
// 支持止盈止损：止盈 +N cents，止损 -N cents
type ThresholdStrategy struct {
	Executor       bbgo.CommandExecutor
	config         *ThresholdStrategyConfig
	tradingService TradingServiceInterface
	hasPosition    bool          // 是否已有仓位
	entryPrice     *domain.Price // 买入价格（用于计算止盈止损）
	entryTokenType domain.TokenType // 买入的 Token 类型

	// 统一：单线程 loop（价格合并 + 订单更新 + 命令结果）
	loopOnce     sync.Once
	loopCancel   context.CancelFunc
	priceSignalC chan struct{}
	priceMu      sync.Mutex
	latestPrice  *events.PriceChangedEvent
	orderC       chan *domain.Order
	cmdResultC   chan thresholdCmdResult

	currentMarket *domain.Market
	pendingEntry  bool
	pendingExit   bool

	mu             sync.RWMutex
	isPlacingOrder bool
	placeOrderMu   sync.Mutex
}

// TradingServiceInterface 交易服务接口（避免循环依赖）
type TradingServiceInterface interface {
	PlaceOrder(ctx context.Context, order *domain.Order) (*domain.Order, error)
	CancelOrder(ctx context.Context, orderID string) error
	GetOpenPositions() []*domain.Position
	GetBestPrice(ctx context.Context, assetID string) (bestBid float64, bestAsk float64, err error)
}

// NewThresholdStrategy 创建新的价格阈值策略
func NewThresholdStrategy() *ThresholdStrategy {
	return &ThresholdStrategy{}
}

// SetTradingService 设置交易服务（在初始化后调用）
func (s *ThresholdStrategy) SetTradingService(ts TradingServiceInterface) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tradingService = ts
}

// ID 返回策略ID（BBGO风格）
func (s *ThresholdStrategy) ID() string {
	return ID
}

// Name 返回策略名称（兼容旧接口）
func (s *ThresholdStrategy) Name() string {
	return ID
}

// Defaults 设置默认值（BBGO风格）
func (s *ThresholdStrategy) Defaults() error {
	return nil
}

// Validate 验证配置（BBGO风格）
func (s *ThresholdStrategy) Validate() error {
	if s.config == nil {
		return fmt.Errorf("策略配置未设置")
	}
	return s.config.Validate()
}

// Initialize 初始化策略（BBGO风格）
func (s *ThresholdStrategy) Initialize() error {
	// BBGO风格的Initialize方法，使用已设置的config
	if s.config == nil {
		return fmt.Errorf("策略配置未设置")
	}
	return nil
}

// InitializeWithConfig 初始化策略（兼容旧接口）
func (s *ThresholdStrategy) InitializeWithConfig(ctx context.Context, config strategies.StrategyConfig) error {
	thresholdConfig, ok := config.(*ThresholdStrategyConfig)
	if !ok {
		return fmt.Errorf("无效的配置类型")
	}

	if err := thresholdConfig.Validate(); err != nil {
		return fmt.Errorf("配置验证失败: %w", err)
	}

	s.config = thresholdConfig

	log.Infof("价格阈值策略已初始化: 买入阈值=%.4f, 卖出阈值=%.4f, 订单大小=%.2f, Token类型=%s, 止盈=%dc, 止损=%dc",
		thresholdConfig.BuyThreshold,
		thresholdConfig.SellThreshold,
		thresholdConfig.OrderSize,
		thresholdConfig.TokenType,
		thresholdConfig.ProfitTargetCents,
		thresholdConfig.StopLossCents)

	return nil
}

// OnPriceChanged 处理价格变化事件（快路径：只合并信号，实际逻辑在 loop 内串行执行）
func (s *ThresholdStrategy) OnPriceChanged(ctx context.Context, event *events.PriceChangedEvent) error {
	if event == nil {
		return nil
	}
	s.startLoop(ctx)
	s.priceMu.Lock()
	s.latestPrice = event
	s.priceMu.Unlock()
	select {
	case s.priceSignalC <- struct{}{}:
	default:
	}
	return nil
}

func (s *ThresholdStrategy) onPriceChangedInternal(ctx context.Context, event *events.PriceChangedEvent) error {
	s.mu.RLock()
	tradingService := s.tradingService
	config := s.config
	s.mu.RUnlock()

	if tradingService == nil {
		return fmt.Errorf("交易服务未设置")
	}
	if event == nil || event.Market == nil || config == nil {
		return nil
	}

	// 只管理本周期：market slug 变化即清理本地状态
	if s.currentMarket != nil && s.currentMarket.Slug != "" && s.currentMarket.Slug != event.Market.Slug {
		_ = s.Cleanup(ctx)
	}
	s.currentMarket = event.Market

	// 检查 Token 类型过滤
	if config.TokenType != "" {
		// 将 event.TokenType (up/down) 转换为 YES/NO 进行比较
		var eventTokenTypeStr string
		if event.TokenType == domain.TokenTypeUp {
			eventTokenTypeStr = "YES"
		} else if event.TokenType == domain.TokenTypeDown {
			eventTokenTypeStr = "NO"
		}
		if eventTokenTypeStr != config.TokenType {
			return nil // 不是我们要监控的 Token 类型
		}
	}

	// 统一：通过 pending 标记避免重复投递命令
	if s.pendingEntry || s.pendingExit {
		return nil
	}

	// 检查价格是否达到买入阈值
	buyThresholdPrice := domain.PriceFromDecimal(config.BuyThreshold)
	if event.NewPrice.GreaterThan(buyThresholdPrice) || event.NewPrice.Cents == buyThresholdPrice.Cents {
		// 检查是否已有仓位
		s.mu.RLock()
		hasPosition := s.hasPosition
		s.mu.RUnlock()

		if !hasPosition {
			// 检查是否有开放仓位
			positions := tradingService.GetOpenPositions()
			if len(positions) == 0 {
				logger.Infof("价格阈值策略: 价格 %.4f 达到买入阈值 %.4f，准备买入",
					event.NewPrice.ToDecimal(), config.BuyThreshold)

				// 确定买入的 Token 类型
				tokenType := event.TokenType
				if config.TokenType != "" {
					// 将 YES/NO 转换为 up/down
					if config.TokenType == "YES" {
						tokenType = domain.TokenTypeUp
					} else if config.TokenType == "NO" {
						tokenType = domain.TokenTypeDown
					}
				}

				assetID := event.Market.GetAssetID(tokenType)

				// 统一下单模板：买入默认取 bestAsk（可选滑点保护，threshold 这里默认不设上限）
				maxBuy := 0
				if config.MaxBuySlippageCents > 0 {
					maxBuy = buyThresholdPrice.Cents + config.MaxBuySlippageCents
				}
				askPrice, err := orderutil.QuoteBuyPrice(ctx, tradingService, assetID, maxBuy)
				if err != nil {
					logger.Errorf("价格阈值策略: 获取订单簿失败: %v", err)
					return err
				}

				order := orderutil.NewOrder(event.Market.Slug, assetID, types.SideBuy, askPrice, config.OrderSize, tokenType, true, types.OrderTypeFAK)

				// 下单
				if err := s.placeOrder(ctx, tradingService, order); err != nil {
					logger.Errorf("价格阈值策略: 买入订单失败: %v", err)
					return err
				}
				// 命令已投递：等待订单更新/成交事件来确认仓位
				s.pendingEntry = true

				logger.Infof("价格阈值策略: 买入订单已提交: Token=%s, Price=%.4f, Size=%.2f",
					tokenType, event.NewPrice.ToDecimal(), config.OrderSize)
			}
		}
	}

	// 如果有仓位，检查止盈止损
	s.mu.RLock()
	hasPosition := s.hasPosition
	entryPrice := s.entryPrice
	entryTokenType := s.entryTokenType
	s.mu.RUnlock()

	if hasPosition && entryPrice != nil {
		// 只检查相同 Token 类型的价格
		if event.TokenType == entryTokenType {
			// 计算价格差（分）
			priceDiff := event.NewPrice.Cents - entryPrice.Cents

			// 检查止盈（+N cents）
			if config.ProfitTargetCents > 0 && priceDiff >= config.ProfitTargetCents {
				logger.Infof("价格阈值策略: 触发止盈！当前价格=%.4f, 买入价格=%.4f, 盈利=%dc (目标=%dc)",
					event.NewPrice.ToDecimal(), entryPrice.ToDecimal(), priceDiff, config.ProfitTargetCents)

				assetID := event.Market.GetAssetID(entryTokenType)

				// 止盈卖出：默认取 bestBid（更容易成交），可选滑点保护（不低于触发价-滑点）
				minSell := 0
				if config.MaxSellSlippageCents > 0 {
					trigger := entryPrice.Cents + config.ProfitTargetCents
					minSell = trigger - config.MaxSellSlippageCents
					if minSell < 0 {
						minSell = 0
					}
				}
				bidPrice, err := orderutil.QuoteSellPrice(ctx, tradingService, assetID, minSell)
				if err != nil {
					logger.Errorf("价格阈值策略: 获取订单簿失败: %v", err)
					return err
				}

				// 创建卖出订单
				if err := s.createSellOrder(ctx, tradingService, event.Market, entryTokenType, bidPrice, config.OrderSize); err != nil {
					logger.Errorf("价格阈值策略: 止盈卖出订单失败: %v", err)
					return err
				}
				// 命令已投递：等待订单成交/取消更新来确认出场
				s.pendingExit = true

				logger.Infof("价格阈值策略: 止盈卖出订单已提交")
				return nil
			}

			// 检查止损（-N cents）
			if config.StopLossCents > 0 && priceDiff <= -config.StopLossCents {
				logger.Infof("价格阈值策略: 触发止损！当前价格=%.4f, 买入价格=%.4f, 亏损=%dc (止损=%dc)",
					event.NewPrice.ToDecimal(), entryPrice.ToDecimal(), priceDiff, config.StopLossCents)

				assetID := event.Market.GetAssetID(entryTokenType)

				// 止损卖出：bestBid（成交优先），可选滑点保护
				minSell := 0
				if config.MaxSellSlippageCents > 0 {
					trigger := entryPrice.Cents - config.StopLossCents
					minSell = trigger - config.MaxSellSlippageCents
					if minSell < 0 {
						minSell = 0
					}
				}
				bidPrice, err := orderutil.QuoteSellPrice(ctx, tradingService, assetID, minSell)
				if err != nil {
					logger.Errorf("价格阈值策略: 获取订单簿失败: %v", err)
					return err
				}

				// 创建卖出订单
				if err := s.createSellOrder(ctx, tradingService, event.Market, entryTokenType, bidPrice, config.OrderSize); err != nil {
					logger.Errorf("价格阈值策略: 止损卖出订单失败: %v", err)
					return err
				}
				// 命令已投递：等待订单成交/取消更新来确认出场
				s.pendingExit = true

				logger.Infof("价格阈值策略: 止损卖出订单已提交")
				return nil
			}
		}
	}

	// 检查价格是否达到卖出阈值（如果配置了）
	if config.SellThreshold > 0 {
		sellThresholdPrice := domain.PriceFromDecimal(config.SellThreshold)
		if event.NewPrice.LessThan(sellThresholdPrice) {
			s.mu.RLock()
			hasPosition := s.hasPosition
			entryTokenType := s.entryTokenType
			s.mu.RUnlock()

			if hasPosition {
				logger.Infof("价格阈值策略: 价格 %.4f 达到卖出阈值 %.4f，准备卖出",
					event.NewPrice.ToDecimal(), config.SellThreshold)

				assetID := event.Market.GetAssetID(entryTokenType)
				// 卖出使用买一价（更容易成交）
				minSell := 0
				if config.MaxSellSlippageCents > 0 {
					minSell = sellThresholdPrice.Cents - config.MaxSellSlippageCents
					if minSell < 0 {
						minSell = 0
					}
				}
				bidPrice, err := orderutil.QuoteSellPrice(ctx, tradingService, assetID, minSell)
				if err != nil {
					logger.Errorf("价格阈值策略: 获取订单簿失败: %v", err)
					return err
				}
				if err := s.createSellOrder(ctx, tradingService, event.Market, entryTokenType, bidPrice, config.OrderSize); err != nil {
					logger.Errorf("价格阈值策略: 阈值卖出订单失败: %v", err)
					return err
				}
				s.pendingExit = true

				logger.Infof("价格阈值策略: 卖出信号已触发")
			}
		}
	}

	return nil
}

// placeOrder 下单（带锁保护，避免并发下单）
func (s *ThresholdStrategy) placeOrder(ctx context.Context, tradingService TradingServiceInterface, order *domain.Order) error {
	// 统一工程化：策略 loop 不直接做网络 IO；优先把下单投递到全局 Executor。
	if s.Executor == nil {
		_, err := tradingService.PlaceOrder(ctx, order)
		return err
	}

	s.initLoopIfNeeded()
	ok := s.Executor.Submit(bbgo.Command{
		Name:    fmt.Sprintf("threshold_place_%s_%s", order.Side, order.TokenType),
		Timeout: 25 * time.Second,
		Do: func(runCtx context.Context) {
			created, err := tradingService.PlaceOrder(runCtx, order)
			select {
			case s.cmdResultC <- thresholdCmdResult{order: order, created: created, err: err}:
			default:
			}
		},
	})
	if !ok {
		return fmt.Errorf("执行器队列已满，无法提交订单")
	}
	return nil
}

// createSellOrder 创建卖出订单
func (s *ThresholdStrategy) createSellOrder(ctx context.Context, tradingService TradingServiceInterface, market *domain.Market, tokenType domain.TokenType, price domain.Price, size float64) error {
	order := orderutil.NewOrder(market.Slug, market.GetAssetID(tokenType), types.SideSell, price, size, tokenType, false, types.OrderTypeFAK)

	return s.placeOrder(ctx, tradingService, order)
}

// OnOrderFilled 处理订单成交事件
func (s *ThresholdStrategy) OnOrderFilled(ctx context.Context, event *events.OrderFilledEvent) error {
	logger.Infof("价格阈值策略: 订单已成交: OrderID=%s, Side=%s, Price=%.4f",
		event.Order.OrderID, event.Order.Side, event.Order.Price.ToDecimal())

	if event.Order.Side == types.SideBuy && event.Order.IsEntryOrder {
		// 买入订单成交，记录买入价格
		entryPrice := event.Order.Price
		s.mu.Lock()
		s.hasPosition = true
		s.entryPrice = &entryPrice
		s.entryTokenType = event.Order.TokenType
		s.mu.Unlock()

		logger.Infof("价格阈值策略: 买入订单已成交，记录买入价格=%.4f", entryPrice.ToDecimal())
	} else if event.Order.Side == types.SideSell {
		// 卖出订单成交，清除仓位信息
		s.mu.Lock()
		s.hasPosition = false
		s.entryPrice = nil
		s.mu.Unlock()

		logger.Infof("价格阈值策略: 卖出订单已成交，仓位已清除")
	}

	return nil
}

// CanOpenPosition 检查是否可以开仓
func (s *ThresholdStrategy) CanOpenPosition(ctx context.Context, market *domain.Market) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return !s.hasPosition, nil
}

// CalculateEntry 计算入场价格和数量
func (s *ThresholdStrategy) CalculateEntry(ctx context.Context, market *domain.Market, price domain.Price) (*domain.Order, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.hasPosition {
		return nil, fmt.Errorf("已有仓位，无法再次入场")
	}

	// 确定 Token 类型
	var tokenType domain.TokenType
	if s.config.TokenType != "" {
		// 将 YES/NO 转换为 up/down
		if s.config.TokenType == "YES" {
			tokenType = domain.TokenTypeUp
		} else if s.config.TokenType == "NO" {
			tokenType = domain.TokenTypeDown
		} else {
			tokenType = domain.TokenTypeUp // 默认
		}
	} else {
		// 默认使用 YES token (up)
		tokenType = domain.TokenTypeUp
	}

	return &domain.Order{
		AssetID:      market.GetAssetID(tokenType),
		Side:         types.SideBuy,
		Price:        price,
		Size:         s.config.OrderSize,
		TokenType:    tokenType,
		IsEntryOrder: true,
		Status:       domain.OrderStatusPending,
	}, nil
}

// CalculateHedge 计算对冲订单（此策略不需要对冲）
func (s *ThresholdStrategy) CalculateHedge(ctx context.Context, entryOrder *domain.Order) (*domain.Order, error) {
	return nil, nil
}

// CheckTakeProfitStopLoss 检查止盈止损（此策略使用阈值，不需要额外的止盈止损）
func (s *ThresholdStrategy) CheckTakeProfitStopLoss(ctx context.Context, position *domain.Position, currentPrice domain.Price) (*domain.Order, error) {
	return nil, nil
}

// Cleanup 清理资源
func (s *ThresholdStrategy) Cleanup(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hasPosition = false
	s.entryPrice = nil
	s.isPlacingOrder = false
	s.pendingEntry = false
	s.pendingExit = false
	s.currentMarket = nil
	return nil
}

// Subscribe 订阅会话事件（BBGO 风格）
func (s *ThresholdStrategy) Subscribe(session *bbgo.ExchangeSession) {
	// 注册价格变化回调
	session.OnPriceChanged(s)
	// 注册订单更新回调（用于确认成交/取消，从而驱动状态机）
	session.OnOrderUpdate(s)
	log.Infof("价格阈值策略已订阅价格变化事件")
}

// Run 运行策略（BBGO 风格）
func (s *ThresholdStrategy) Run(ctx context.Context, orderExecutor bbgo.OrderExecutor, session *bbgo.ExchangeSession) error {
	log.Infof("价格阈值策略已启动")
	s.startLoop(ctx)
	return nil
}

// Shutdown 优雅关闭（BBGO 风格）
// Shutdown 优雅关闭（BBGO 风格）
// 注意：wg 参数由 shutdown.Manager 统一管理，策略的 Shutdown 方法不应该调用 wg.Done()
func (s *ThresholdStrategy) Shutdown(ctx context.Context, wg *sync.WaitGroup) {
	log.Infof("价格阈值策略: 开始优雅关闭...")
	s.stopLoop()
	if err := s.Cleanup(ctx); err != nil {
		log.Errorf("价格阈值策略清理失败: %v", err)
	}
	log.Infof("价格阈值策略: 资源清理完成")
}

