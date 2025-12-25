package services

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
	"strconv"
	"strings"

	"github.com/sirupsen/logrus"

	"github.com/betbot/gobet/clob/client"
	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/execution"
	"github.com/betbot/gobet/internal/marketstate"
	"github.com/betbot/gobet/internal/ports"
	"github.com/betbot/gobet/internal/risk"
	"github.com/betbot/gobet/pkg/cache"
	"github.com/betbot/gobet/pkg/persistence"
)

var log = logrus.WithField("component", "trading_service")

// OrderResult 订单处理结果
type OrderResult struct {
	Order   *domain.Order
	Success bool
	Error   error
}

// TradingService 交易服务（重构后，无锁，使用 OrderEngine）
type TradingService struct {
	orderEngine *OrderEngine
	ioExecutor  *IOExecutor
	clobClient  *client.Client

	// 组件化子服务（对外 API 仍由 TradingService 承载）
	orders       *OrdersService
	positions    *PositionsService
	ordersManage *OrdersManageService
	balances     *BalanceService
	snapshots    *SnapshotService
	syncer       *OrderSyncService

	// 配置
	funderAddress string
	signatureType types.SignatureType
	dryRun        bool
	minOrderSize  float64
	minShareSize  float64 // 限价单最小 share 数量（仅限价单 GTC 时应用）

	// 上下文
	ctx    context.Context
	cancel context.CancelFunc

	// 订单状态缓存（只读，可以保留）
	orderStatusCache *cache.OrderStatusCache

	// 订单状态同步配置
	orderStatusSyncIntervalWithOrders    int
	orderStatusSyncIntervalWithoutOrders int

	// 执行层保护（防重复/风控）
	inFlightDeduper *execution.InFlightDeduper
	circuitBreaker  *risk.CircuitBreaker

	// 多腿执行引擎（队列+并发+自动对冲）
	execEngine *execution.ExecutionEngine

	// 周期代号（generation）：每次 SetCurrentMarket（market 变化）递增；
	// 所有写入 OrderEngine 的命令必须携带当前 generation，旧 generation 一律丢弃。
	engineGeneration atomic.Int64

	// 重启恢复/快照
	persistence   persistence.Service
	persistenceID string

	// 当前市场（用于过滤订单状态同步）
	currentMarketSlug string
	currentMarketMu   sync.RWMutex

	// 当前市场完整信息（YES/NO assetID 等，供 BestBook 映射与执行层使用）
	currentMarket *domain.Market

	// 原子行情快照（来自 WS MarketStream）
	bestBook   *marketstate.AtomicBestBook
	bestBookMu sync.RWMutex
}

// NewTradingService 创建新的交易服务（使用 OrderEngine）
func NewTradingService(clobClient *client.Client, dryRun bool) *TradingService {
	ctx, cancel := context.WithCancel(context.Background())

	minOrderSize := 1.1 // 默认最小订单金额

	// 创建 IO 执行器
	ioExecutor := NewIOExecutor(clobClient, dryRun)

	// 创建 OrderEngine
	orderEngine := NewOrderEngine(ioExecutor, minOrderSize, dryRun)

	service := &TradingService{
		orderEngine:                          orderEngine,
		ioExecutor:                           ioExecutor,
		clobClient:                           clobClient,
		funderAddress:                        "",
		signatureType:                        types.SignatureTypeBrowser,
		dryRun:                               dryRun,
		minOrderSize:                         minOrderSize,
		minShareSize:                         5.0, // 默认 5.0 shares（Polymarket 限价单要求）
		ctx:                                  ctx,
		cancel:                               cancel,
		orderStatusCache:                     cache.NewOrderStatusCache(),
		orderStatusSyncIntervalWithOrders:    3,  // 默认3秒
		orderStatusSyncIntervalWithoutOrders: 30, // 默认30秒
		inFlightDeduper:                      execution.NewInFlightDeduper(2*time.Second, 64),
		circuitBreaker: risk.NewCircuitBreaker(risk.CircuitBreakerConfig{
			// 默认只启用“连续错误熔断”，避免误伤；当日亏损上限可后续接入完整 PnL 统计后再启用。
			MaxConsecutiveErrors: 10,
			DailyLossLimitCents:  0,
		}),
	}
	// 统一执行引擎（依赖 TradingService 自身的 PlaceOrder/GetBestPrice 等）
	service.execEngine = execution.NewExecutionEngine(service)
	// generation 从 1 开始，避免默认 0 被误用
	service.engineGeneration.Store(1)

	// 初始化组件（按职责拆分，但保持 TradingService 对外方法不变）
	service.orders = &OrdersService{s: service}
	service.positions = &PositionsService{s: service}
	service.ordersManage = &OrdersManageService{s: service}
	service.balances = &BalanceService{s: service}
	service.snapshots = &SnapshotService{s: service}
	service.syncer = &OrderSyncService{s: service}

	if dryRun {
		log.Warnf("📝 纸交易模式已启用：不会进行真实交易，订单信息仅记录在日志中")
	}

	return service
}

// IsDryRun 返回是否为纸交易模式
func (s *TradingService) IsDryRun() bool {
	return s.dryRun
}

func (s *TradingService) currentEngineGeneration() int64 {
	if s == nil {
		return 0
	}
	return s.engineGeneration.Load()
}

// deriveCycleTokenFromMarketSlug 尝试从 marketSlug 提取周期 token（通常为末尾时间戳）。
// 例如：btc-updown-15m-1766322000 -> 1766322000
func deriveCycleTokenFromMarketSlug(marketSlug string) (int64, bool) {
	if marketSlug == "" {
		return 0, false
	}
	i := strings.LastIndex(marketSlug, "-")
	if i < 0 || i+1 >= len(marketSlug) {
		return 0, false
	}
	ts, err := strconv.ParseInt(marketSlug[i+1:], 10, 64)
	if err != nil || ts <= 0 {
		return 0, false
	}
	return ts, true
}

func (s *TradingService) computeNextGeneration(prevGen int64, marketSlug string) int64 {
	// 默认：单调递增（进程内 token）
	desired := prevGen + 1
	// 若能从 slug 提取周期时间戳，则用它做“跨重启稳定 token”（更适合审计与严格隔离）
	if ts, ok := deriveCycleTokenFromMarketSlug(marketSlug); ok {
		desired = ts
	}
	// 永不回退：即使传入了旧的 slug，也强制单调递增
	if desired <= prevGen {
		desired = prevGen + 1
	}
	return desired
}

// SetCurrentMarket 设置当前市场（用于过滤订单状态同步）
func (s *TradingService) SetCurrentMarket(marketSlug string) {
	s.currentMarketMu.Lock()
	prev := s.currentMarketSlug
	s.currentMarketSlug = marketSlug
	s.currentMarketMu.Unlock()

	log.Infof("✅ [周期切换] 已设置当前市场: %s", marketSlug)

	// 架构层约束：新周期必须是“全新世界”
	// - 清空 OrderEngine 的周期相关状态（openOrders/orderStore/positions/pendingTrades）
	// - 清空订单状态缓存/去重器，避免跨周期串单或误去重
	if prev != marketSlug && marketSlug != "" {
		prevGen := s.engineGeneration.Load()
		newGen := s.computeNextGeneration(prevGen, marketSlug)
		s.engineGeneration.Store(newGen)
		if s.orderEngine != nil {
			s.orderEngine.ResetForNewCycle(marketSlug, "TradingService.SetCurrentMarket", newGen)
		}
		if s.orderStatusCache != nil {
			s.orderStatusCache.Clear()
		}
		if s.inFlightDeduper != nil {
			s.inFlightDeduper.Clear()
		}
		log.Warnf("🔄 [周期切换] 已重置本地状态：orders/positions/cache/inflight（prev=%s -> new=%s gen=%d）", prev, marketSlug, newGen)
	}
}

// SetCurrentMarketInfo 设置当前市场完整信息（推荐：替代只传 slug 的 SetCurrentMarket）。
// - 会同步调用 SetCurrentMarket(market.Slug) 做周期隔离
// - 并存储 YES/NO assetId 等信息，供 BestBook/执行层使用
func (s *TradingService) SetCurrentMarketInfo(market *domain.Market) {
	if s == nil {
		return
	}
	if market == nil {
		return
	}
	s.SetCurrentMarket(market.Slug)
	s.currentMarketMu.Lock()
	// 复制一份，避免外部复用指针导致竞态
	cp := *market
	s.currentMarket = &cp
	s.currentMarketMu.Unlock()
}

// SetBestBook 注入 MarketStream 的 AtomicBestBook（可在周期切换时更新指向）。
func (s *TradingService) SetBestBook(book *marketstate.AtomicBestBook) {
	if s == nil {
		return
	}
	s.bestBookMu.Lock()
	s.bestBook = book
	s.bestBookMu.Unlock()
}

func (s *TradingService) getBestBook() *marketstate.AtomicBestBook {
	if s == nil {
		return nil
	}
	s.bestBookMu.RLock()
	defer s.bestBookMu.RUnlock()
	return s.bestBook
}

func (s *TradingService) getCurrentMarketInfo() *domain.Market {
	if s == nil {
		return nil
	}
	s.currentMarketMu.RLock()
	defer s.currentMarketMu.RUnlock()
	if s.currentMarket == nil {
		return nil
	}
	cp := *s.currentMarket
	return &cp
}

// GetCurrentMarket 获取当前市场
func (s *TradingService) GetCurrentMarket() string {
	s.currentMarketMu.RLock()
	defer s.currentMarketMu.RUnlock()
	return s.currentMarketSlug
}

// SetOrderStatusSyncConfig 设置订单状态同步配置（无锁版本）
func (s *TradingService) SetOrderStatusSyncConfig(withOrdersSeconds, withoutOrdersSeconds int) {
	if withOrdersSeconds > 0 {
		s.orderStatusSyncIntervalWithOrders = withOrdersSeconds
	}
	if withoutOrdersSeconds > 0 {
		s.orderStatusSyncIntervalWithoutOrders = withoutOrdersSeconds
	}
	log.Infof("订单状态同步配置已更新: 有活跃订单时=%d秒, 无活跃订单时=%d秒", s.orderStatusSyncIntervalWithOrders, s.orderStatusSyncIntervalWithoutOrders)
}

// OnOrderUpdate 注册订单更新回调（通过 OrderEngine）
func (s *TradingService) OnOrderUpdate(handler ports.OrderUpdateHandler) {
	s.orderEngine.OnOrderUpdate(handler)
}

// emitOrderUpdate 触发订单更新回调（已移至 OrderEngine，保留此方法用于向后兼容）
func (s *TradingService) emitOrderUpdate(ctx context.Context, order *domain.Order) {
	// 此方法已废弃，回调现在由 OrderEngine 处理
	log.Debugf("emitOrderUpdate 已废弃，请使用 OrderEngine 的回调机制")
}

// Start 启动交易服务（使用 OrderEngine）
func (s *TradingService) Start(ctx context.Context) error {
	// 取消旧的 context（如果存在）
	if s.cancel != nil {
		s.cancel()
	}
	// 创建新的 context 和 cancel 函数
	s.ctx, s.cancel = context.WithCancel(ctx)

	log.Info("✅ 交易服务已启动（使用 OrderEngine）")

	// 启动 OrderEngine 主循环
	go s.orderEngine.Run(s.ctx)

	// 启动多腿执行引擎 + 注册订单更新回调（用于自动对冲）
	if s.execEngine != nil {
		s.execEngine.Start(s.ctx)
		s.OnOrderUpdate(s.execEngine)
	}

	// 重启恢复：先加载快照（热启动），后续再用交易所 open orders 对账纠偏
	// 注意：需要在设置当前市场之后才能恢复订单（否则会恢复所有旧周期的订单）
	// 因此快照恢复会在周期切换回调中或启动后延迟执行
	if s.snapshots != nil {
		// 延迟执行，等待当前市场设置完成
		go func() {
			time.Sleep(500 * time.Millisecond)
			s.snapshots.loadSnapshot()
		}()
	}
	go func() {
		// 等待 OrderEngine 就绪和当前市场设置完成
		time.Sleep(500 * time.Millisecond)
		if s.snapshots != nil {
			s.snapshots.bootstrapOpenOrdersFromExchange(s.ctx)
		}
	}()

	// 快照持久化：订单/仓位有变化时做一次 debounce 保存
	if s.persistence != nil {
		if s.snapshots != nil {
			s.snapshots.startSnapshotLoop(s.ctx)
		}
	}

	// 初始化余额（从 API 获取）
	if !s.dryRun {
		if s.balances != nil {
			go s.balances.initializeBalance(ctx)
		}
	} else {
		// 纸交易模式：设置一个很大的初始余额
		updateCmd := &UpdateBalanceCommand{
			id:       fmt.Sprintf("init_balance_%d", time.Now().UnixNano()),
			Balance:  1000000.0, // 纸交易模式使用很大的余额
			Currency: "USDC",
		}
		s.orderEngine.SubmitCommand(updateCmd)
		log.Infof("📊 [余额初始化] 纸交易模式：设置初始余额为 %.2f USDC", 1000000.0)
	}

	// 启动定期订单状态同步（如果需要）
	go s.startOrderStatusSync(s.ctx)

	return nil
}

// ExecutionEngine 暴露统一多腿执行引擎（可供策略直接使用）。
func (s *TradingService) ExecutionEngine() *execution.ExecutionEngine {
	if s == nil {
		return nil
	}
	return s.execEngine
}

// Stop 停止交易服务
func (s *TradingService) Stop() {
	log.Info("正在停止交易服务...")

	// 先取消context，通知所有goroutine停止
	if s.cancel != nil {
		s.cancel()
	}

	log.Info("交易服务已停止")
}

// SetFunderAddress 设置 funder 地址和签名类型（无锁版本）
func (s *TradingService) SetFunderAddress(funderAddress string, signatureType types.SignatureType) {
	s.funderAddress = funderAddress
	s.signatureType = signatureType
	// 关键：IOExecutor 下单签名必须同步使用 funderAddress，否则 maker 仍会是 EOA
	if s.ioExecutor != nil {
		s.ioExecutor.SetFunderAddress(funderAddress, signatureType)
	}
}

// SetMinOrderSize 设置最小订单金额（USDC）（无锁版本）
func (s *TradingService) SetMinOrderSize(minOrderSize float64) {
	if minOrderSize < 1.0 {
		minOrderSize = 1.0 // 交易所要求不能小于 1.0
	}
	s.minOrderSize = minOrderSize
	// 更新 OrderEngine 的最小订单金额
	s.orderEngine.MinOrderSize = minOrderSize
	log.Infof("✅ 已设置最小订单金额: %.2f USDC", minOrderSize)
}

// SetMinShareSize 设置限价单最小 share 数量（无锁版本）
func (s *TradingService) SetMinShareSize(minShareSize float64) {
	if minShareSize < 0 {
		minShareSize = 5.0 // 默认值
	}
	s.minShareSize = minShareSize
	log.Infof("✅ 已设置限价单最小 share 数量: %.2f（仅限价单 GTC 时应用）", minShareSize)
}

// WaitOrderResult 等待订单处理结果（已废弃，现在通过 OrderEngine 处理）
// 保留此方法用于向后兼容，但不再使用
func (s *TradingService) WaitOrderResult(ctx context.Context, orderID string, timeout time.Duration) (*OrderResult, error) {
	// 通过 OrderEngine 查询订单状态
	reply := make(chan *StateSnapshot, 1)
	cmd := &QueryStateCommand{
		id:    fmt.Sprintf("query_order_%s", orderID),
		Query: QueryOrder,
		Reply: reply,
	}

	s.orderEngine.SubmitCommand(cmd)

	select {
	case snapshot := <-reply:
		if snapshot.Order != nil && snapshot.Order.OrderID == orderID {
			return &OrderResult{
				Order:   snapshot.Order,
				Success: snapshot.Order.Status != domain.OrderStatusFailed,
				Error:   snapshot.Error,
			}, nil
		}
		return nil, fmt.Errorf("订单不存在: %s", orderID)
	case <-time.After(timeout):
		return nil, fmt.Errorf("等待订单结果超时: %s", orderID)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// 注意：processOrderQueue 和 processOrderAsync 方法已移除
// 订单提交现在改为同步提交，不再使用异步队列

// 注意：processOrderAsync 方法已完全移除
// 订单提交现在在 PlaceOrder 中同步完成，不再需要异步处理
