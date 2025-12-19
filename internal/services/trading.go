package services

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/betbot/gobet/clob/client"
	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/pkg/cache"
)

var log = logrus.WithField("component", "trading_service")

// OrderResult 订单处理结果
type OrderResult struct {
	Order   *domain.Order
	Success bool
	Error   error
}

// TradingService 交易服务
// OrderUpdateHandler 订单更新处理器接口（BBGO风格）
type OrderUpdateHandler interface {
	OnOrderUpdate(ctx context.Context, order *domain.Order) error
}

// TradingService 交易服务（重构后，无锁，使用 OrderEngine）
type TradingService struct {
	orderEngine *OrderEngine
	ioExecutor  *IOExecutor
	clobClient  *client.Client

	// 配置
	funderAddress string
	signatureType types.SignatureType
	dryRun        bool
	minOrderSize  float64

	// 上下文
	ctx    context.Context
	cancel context.CancelFunc

	// 订单状态缓存（只读，可以保留）
	orderStatusCache *cache.OrderStatusCache

	// 订单状态同步配置
	orderStatusSyncIntervalWithOrders    int
	orderStatusSyncIntervalWithoutOrders int
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
		ctx:                                  ctx,
		cancel:                               cancel,
		orderStatusCache:                     cache.NewOrderStatusCache(),
		orderStatusSyncIntervalWithOrders:    3,  // 默认3秒
		orderStatusSyncIntervalWithoutOrders: 30, // 默认30秒
	}

	if dryRun {
		log.Warnf("📝 纸交易模式已启用：不会进行真实交易，订单信息仅记录在日志中")
	}

	return service
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
func (s *TradingService) OnOrderUpdate(handler OrderUpdateHandler) {
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

	// 初始化余额（从 API 获取）
	if !s.dryRun {
		go s.initializeBalance(ctx)
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

// initializeBalance 初始化余额（优先从链上查询，然后从 API 获取授权）
func (s *TradingService) initializeBalance(ctx context.Context) {
	// 等待一小段时间，确保 OrderEngine 已启动
	time.Sleep(100 * time.Millisecond)

	// 获取账号地址（优先使用 funderAddress，如果没有则从私钥计算）
	accountAddress := s.funderAddress
	if accountAddress == "" {
		// 尝试从 Client 获取地址
		if addr, err := s.clobClient.GetAddress(); err == nil {
			accountAddress = addr.Hex()
		} else {
			accountAddress = "未设置（无法获取地址）"
		}
	}

	// 优先从链上查询余额（直接查询代理钱包地址的余额）
	var balance float64
	var balanceStr string
	var balanceRaw int64
	var balanceInfo *types.BalanceAllowanceResponse // 用于存储 API 响应，避免重复调用

	if accountAddress != "" && accountAddress != "未设置（无法获取地址）" {
		onChainBalance, err := s.getOnChainUSDCBalance(ctx, accountAddress)
		if err != nil {
			log.Warnf("⚠️ [余额初始化] 链上余额查询失败: %v，将尝试从 API 获取", err)
		} else {
			balance = onChainBalance
			balanceRaw = int64(balance * 1e6)
			balanceStr = fmt.Sprintf("%d", balanceRaw) // 转换为6位小数字符串
			log.Infof("✅ [余额初始化] 从链上查询到余额: %.6f USDC (地址: %s)", balance, accountAddress)
		}
	}

	// 如果链上查询失败，尝试从 API 获取余额
	// 同时，无论链上查询是否成功，都需要从 API 获取授权额度，所以统一调用一次 API
	// 传递 signatureType 参数（参考 test/clob.go 的实现）
	sigType := s.signatureType
	params := &types.BalanceAllowanceParams{
		AssetType:     types.AssetTypeCollateral,
		SignatureType: &sigType, // 传递签名类型
	}
	balanceInfo, err := s.clobClient.GetBalanceAllowance(ctx, params)
	if err != nil {
		log.Errorf("❌ [余额初始化] 获取余额和授权失败: %v", err)
		// 即使获取失败，也继续运行（可能网络问题）
		return
	}

	// 调试：显示完整的 API 响应
	log.Debugf("📊 [余额API响应] Balance=%q, Allowance=%q, CollateralBalance=%q, CollateralAllowance=%q",
		balanceInfo.Balance, balanceInfo.Allowance, balanceInfo.CollateralBalance, balanceInfo.CollateralAllowance)

	// 如果链上查询失败（balance == 0），使用 API 返回的余额
	if balance == 0 {
		// 解析余额（字符串格式，6位小数，需要除以 1e6 转换为 USDC 单位）
		// 优先使用 CollateralBalance（代理钱包余额），如果没有则使用 Balance
		balanceStr = balanceInfo.CollateralBalance
		if balanceStr == "" {
			balanceStr = balanceInfo.Balance
		}
		if balanceStr == "" {
			balanceStr = "0"
			log.Debugf("余额字段为空，使用默认值 0")
		}

		// 使用更大的整数类型，避免溢出（USDC 可能有很大的值）
		var parseErr error
		balanceRaw, parseErr = strconv.ParseInt(balanceStr, 10, 64)
		if parseErr != nil {
			log.Errorf("❌ [余额初始化] 解析余额失败 (值: %q): %v", balanceStr, parseErr)
			return
		}

		// 转换为 USDC 单位（除以 1e6）
		balance = float64(balanceRaw) / 1e6

		// 调试：显示原始值和计算过程
		log.Debugf("📊 [余额解析] 原始字符串: %q, 解析为整数: %d, 除以 1e6: %.6f USDC",
			balanceStr, balanceRaw, balance)
	}

	// 获取授权额度（复用上面的 API 响应，避免重复调用）
	var allowance float64
	var allowanceStr string
	if balanceInfo != nil {
		// 解析授权额度（字符串格式，6位小数，需要除以 1e6 转换为 USDC 单位）
		// 优先使用 CollateralAllowance（代理钱包授权），如果没有则使用 Allowance
		allowanceStr = balanceInfo.CollateralAllowance
		if allowanceStr == "" {
			allowanceStr = balanceInfo.Allowance
		}

		// 如果 Allowances map 存在，查找最大授权额度（而不是最小值）
		// 因为如果所有合约都有足够授权，最小值可能是 0，但最大值可能是有意义的
		// 同时，如果所有值都是 "0"，可能表示授权足够大（unlimited）或查询方式不对
		if allowanceStr == "" && balanceInfo.Allowances != nil && len(balanceInfo.Allowances) > 0 {
			log.Debugf("📊 [授权额度] Allowances map 包含 %d 个条目", len(balanceInfo.Allowances))
			maxAllowance := ""
			allZero := true
			for spenderAddr, v := range balanceInfo.Allowances {
				log.Debugf("📊 [授权额度] Spender=%s, Allowance=%s", spenderAddr, v)
				if v != "" && v != "0" {
					allZero = false
					if maxAllowance == "" || v > maxAllowance {
						maxAllowance = v
					}
				}
			}

			if !allZero && maxAllowance != "" {
				// 如果存在非零授权，使用最大值
				allowanceStr = maxAllowance
				log.Debugf("📊 [授权额度] 使用 Allowances map 中的最大值: %s", allowanceStr)
			} else if allZero {
				// 如果所有值都是 "0"，可能表示授权足够大（unlimited）
				// 或者需要检查代理钱包地址的授权
				log.Warnf("⚠️ [授权额度] Allowances map 中所有值都是 0，可能表示授权足够大（unlimited）或查询方式不对")
				// 如果用户可以在其他平台下单，说明授权是够的，我们假设授权足够大
				// 设置一个很大的值，避免误判为授权不足
				allowanceStr = "999999999999" // 999,999,999.999 USDC，足够大
				log.Infof("💡 [授权额度] 由于可以在其他平台下单，假设授权足够大，使用默认值: %s", allowanceStr)
			}
		}

		if allowanceStr == "" {
			allowanceStr = "0"
			log.Debugf("授权字段为空，使用默认值 0")
		}

		// 使用 big.Int 解析授权额度，因为可能是 uint256 最大值（无限授权）
		// uint256 最大值 = 2^256 - 1 = 115792089237316195423570985008687907853269984665640564039457584007913129639935
		allowanceBig := new(big.Int)
		allowanceBig, ok := allowanceBig.SetString(allowanceStr, 10)
		if !ok {
			log.Warnf("⚠️ [余额初始化] 解析授权失败 (值: %q): 无法转换为 big.Int", allowanceStr)
			allowance = 0
		} else {
			// 检查是否是 uint256 最大值（无限授权）
			// uint256 最大值 = 2^256 - 1
			maxUint256 := new(big.Int)
			maxUint256.SetString("115792089237316195423570985008687907853269984665640564039457584007913129639935", 10)
			
			// 如果授权值 >= maxUint256 - 1000（允许一些误差），认为是无限授权
			threshold := new(big.Int).Sub(maxUint256, big.NewInt(1000))
			if allowanceBig.Cmp(threshold) >= 0 {
				log.Infof("✅ [授权额度] 检测到无限授权（uint256 最大值），设置为足够大的值")
				allowance = 999999999.999 // 999,999,999.999 USDC，足够大
			} else {
				// 转换为 float64（除以 1e6 转换为 USDC 单位）
				allowanceFloat := new(big.Float).SetInt(allowanceBig)
				divisor := new(big.Float).SetFloat64(1e6)
				allowanceFloat.Quo(allowanceFloat, divisor)
				allowance, _ = allowanceFloat.Float64()
			}
		}
	} else {
		log.Warnf("⚠️ [余额初始化] balanceInfo 为 nil，无法获取授权")
		allowance = 0
		allowanceStr = "0"
	}

	// 更新 OrderEngine 余额
	updateCmd := &UpdateBalanceCommand{
		id:       fmt.Sprintf("init_balance_%d", time.Now().UnixNano()),
		Balance:  balance,
		Currency: "USDC",
	}
	s.orderEngine.SubmitCommand(updateCmd)

	// 格式化显示账号信息、余额和授权额度
	log.Infof("═══════════════════════════════════════════════════════════")
	log.Infof("📋 [账号信息]")
	log.Infof("   账号地址: %s", accountAddress)
	log.Infof("   余额:     %.6f USDC (原始值: %s, 整数: %d)", balance, balanceStr, balanceRaw)
	log.Infof("   授权额度: %.6f USDC (原始值: %s)", allowance, allowanceStr)
	if allowance < balance {
		log.Warnf("   ⚠️  授权额度小于余额，可能需要增加授权才能下单")
	}
	if balance < 0.01 {
		log.Warnf("   ⚠️  余额非常低 (%.6f USDC)，可能无法下单", balance)
	}
	log.Infof("═══════════════════════════════════════════════════════════")
}

// getOnChainUSDCBalance 从 Polygon 链上查询 USDC 余额（参考 test/clob.go）
// 直接查询指定地址的链上余额，不需要认证
func (s *TradingService) getOnChainUSDCBalance(ctx context.Context, walletAddress string) (float64, error) {
	// USDC 合约地址（Polygon）
	const USDCContractPolygon = "0x2791Bca1f2de4661ED88A30C99A7a9449Aa84174"

	// 标准化地址
	walletAddress = strings.ToLower(strings.TrimSpace(walletAddress))
	if !strings.HasPrefix(walletAddress, "0x") {
		walletAddress = "0x" + walletAddress
	}

	// 将地址填充到 32 字节（64 个十六进制字符）
	paddedAddr := strings.TrimPrefix(walletAddress, "0x")
	paddedAddr = fmt.Sprintf("%064s", paddedAddr)

	// balanceOf(address) 函数选择器: 0x70a08231
	data := "0x70a08231" + paddedAddr

	// JSON-RPC 请求
	reqBody := fmt.Sprintf(`{
		"jsonrpc": "2.0",
		"method": "eth_call",
		"params": [{
			"to": "%s",
			"data": "%s"
		}, "latest"],
		"id": 1
	}`, USDCContractPolygon, data)

	req, err := http.NewRequestWithContext(ctx, "POST", "https://polygon-rpc.com", strings.NewReader(reqBody))
	if err != nil {
		return 0, fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("RPC 请求失败: %w", err)
	}
	defer resp.Body.Close()

	var rpcResp struct {
		Result string `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return 0, fmt.Errorf("解析响应失败: %w", err)
	}

	if rpcResp.Error != nil {
		return 0, fmt.Errorf("RPC 错误: %s", rpcResp.Error.Message)
	}

	// 解析十六进制结果为 big.Int
	result := strings.TrimPrefix(rpcResp.Result, "0x")
	if result == "" || result == "0" {
		return 0, nil
	}

	balance := new(big.Int)
	balance.SetString(result, 16)

	// USDC 有 6 位小数
	balanceFloat := new(big.Float).SetInt(balance)
	divisor := new(big.Float).SetFloat64(1e6)
	balanceFloat.Quo(balanceFloat, divisor)

	result64, _ := balanceFloat.Float64()
	return result64, nil
}

// isLocalGeneratedOrderID 检查是否是本地生成的订单ID
// 本地生成的订单ID通常以 "entry-", "hedge-", "smart-" 开头
func isLocalGeneratedOrderID(orderID string) bool {
	if orderID == "" {
		return false
	}
	// 检查是否是本地生成的ID格式
	if len(orderID) > 10 && orderID[:10] == "entry-up-" {
		return true
	}
	if len(orderID) > 12 && orderID[:12] == "hedge-down-" {
		return true
	}
	if len(orderID) > 5 && orderID[:5] == "smart" {
		return true
	}
	if len(orderID) > 6 && orderID[:6] == "entry-" {
		return true
	}
	if len(orderID) > 6 && orderID[:6] == "hedge-" {
		return true
	}
	return false
}

// startOrderStatusSync 定期同步订单状态（通过 API 查询）
// 如果 WebSocket 失败，会自动缩短同步间隔
func (s *TradingService) startOrderStatusSync(ctx context.Context) {
	// 获取配置的同步间隔（用于日志）
	withOrdersSeconds := s.orderStatusSyncIntervalWithOrders
	withoutOrdersSeconds := s.orderStatusSyncIntervalWithoutOrders

	log.Infof("🔄 [订单状态同步] 启动定期订单状态同步（有活跃订单时每%d秒，无活跃订单时每%d秒）",
		withOrdersSeconds, withoutOrdersSeconds)

	// 立即执行一次（不等待）
	s.syncAllOrderStatus(ctx)

	// 使用 ticker 来定期同步，但需要动态调整间隔
	// 使用较短的 ticker 间隔（1秒），然后根据条件决定是否执行同步
	// 这样可以更灵活地响应配置变化
	ticker := time.NewTicker(1 * time.Second) // 每1秒检查一次
	defer ticker.Stop()

	lastSyncTime := time.Now()

	for {
		select {
		case <-ctx.Done():
			log.Info("🔄 [订单状态同步] 订单状态同步已停止")
			return
		case <-ticker.C:
			// 检查是否有活跃订单（通过 OrderEngine 查询）
			openOrders := s.GetActiveOrders()
			hasActiveOrders := len(openOrders) > 0

			// 重新读取配置（支持运行时修改）
			currentSyncIntervalWithOrders := time.Duration(s.orderStatusSyncIntervalWithOrders) * time.Second
			currentSyncIntervalWithoutOrders := time.Duration(s.orderStatusSyncIntervalWithoutOrders) * time.Second

			// 根据是否有活跃订单选择同步间隔
			var syncInterval time.Duration
			if hasActiveOrders {
				syncInterval = currentSyncIntervalWithOrders
			} else {
				syncInterval = currentSyncIntervalWithoutOrders
			}

			// 检查是否到了同步时间
			if time.Since(lastSyncTime) >= syncInterval {
				s.syncAllOrderStatus(ctx)
				lastSyncTime = time.Now()
			}
		}
	}
}

// syncAllOrderStatus 同步所有活跃订单的状态
func (s *TradingService) syncAllOrderStatus(ctx context.Context) {
	// 通过 OrderEngine 获取活跃订单
	openOrders := s.GetActiveOrders()
	orderIDs := make([]string, 0, len(openOrders))
	for _, order := range openOrders {
		orderIDs = append(orderIDs, order.OrderID)
	}

	if len(orderIDs) == 0 {
		log.Debugf("🔄 [订单状态同步] 没有活跃订单需要同步")
		return
	}

	log.Debugf("🔄 [订单状态同步] 开始同步 %d 个活跃订单的状态", len(orderIDs))

	// 获取所有开放订单
	openOrdersResp, err := s.clobClient.GetOpenOrders(ctx, nil)
	if err != nil {
		log.Warnf("🔄 [订单状态同步] 获取开放订单失败: %v", err)
		return
	}

	log.Debugf("🔄 [订单状态同步] API 返回 %d 个开放订单", len(openOrdersResp))

	// 构建开放订单 ID 集合（用于快速查找）
	openOrderIDs := make(map[string]bool)
	// 构建开放订单属性映射（用于通过属性匹配，处理订单 ID 不匹配的情况）
	openOrdersByAttrs := make(map[string]string) // key: "assetID:side:price", value: orderID
	for _, order := range openOrdersResp {
		openOrderIDs[order.ID] = true
		// 构建属性键（用于匹配）
		// order.Price 是 string 类型（来自 API），需要标准化格式
		// 解析价格并格式化为统一格式（保留4位小数）
		apiPrice, err := strconv.ParseFloat(order.Price, 64)
		if err != nil {
			log.Debugf("🔄 [订单状态同步] 解析API订单价格失败: orderID=%s, price=%s, error=%v", order.ID, order.Price, err)
			// 如果解析失败，使用原始字符串（可能格式不一致）
			attrsKey := fmt.Sprintf("%s:%s:%s", order.AssetID, order.Side, order.Price)
			openOrdersByAttrs[attrsKey] = order.ID
		} else {
			// 标准化价格格式（保留4位小数）
			normalizedPrice := fmt.Sprintf("%.4f", apiPrice)
			attrsKey := fmt.Sprintf("%s:%s:%s", order.AssetID, order.Side, normalizedPrice)
			openOrdersByAttrs[attrsKey] = order.ID
		}
	}

	// 检查本地订单是否还在开放订单列表中
	// 通过 OrderEngine 获取当前活跃订单
	localOrders := s.GetActiveOrders()
	localOrdersMap := make(map[string]*domain.Order)
	for _, order := range localOrders {
		localOrdersMap[order.OrderID] = order
	}

	filledCount := 0
	updatedOrderIDs := make(map[string]string) // oldID -> newID

	for _, orderID := range orderIDs {
		order, exists := localOrdersMap[orderID]
		if !exists {
			continue
		}

		// 风险4修复：WebSocket和API状态一致性检查
		// 如果订单已经通过 WebSocket 更新为已成交或已取消，优先使用WebSocket状态
		if order.Status == domain.OrderStatusFilled || order.Status == domain.OrderStatusCanceled {
			// 检查API返回的开放订单列表中是否还有这个订单（状态不一致）
			if openOrderIDs[orderID] {
				// WebSocket显示已成交/已取消，但API显示仍在开放列表中，记录警告
				log.Warnf("⚠️ [状态一致性] WebSocket和API状态不一致: orderID=%s, WebSocket状态=%s, API状态=open",
					orderID, order.Status)
				// 优先使用WebSocket状态（更及时），但记录不一致情况
			}
			log.Debugf("🔄 [订单状态同步] 订单已通过WebSocket更新为 %s，跳过同步: orderID=%s", order.Status, orderID)
			// 更新缓存（标记为已关闭）
			s.orderStatusCache.Set(orderID, false)
			// 发送 UpdateOrderCommand 更新 OrderEngine 状态
			updateCmd := &UpdateOrderCommand{
				id:    fmt.Sprintf("sync_update_%s", orderID),
				Order: order,
			}
			s.orderEngine.SubmitCommand(updateCmd)
			continue
		}

		// 检查缓存（如果缓存显示订单已关闭，直接处理）
		if cachedIsOpen, exists := s.orderStatusCache.Get(orderID); exists && !cachedIsOpen {
			// 缓存显示订单已关闭，但本地还在 activeOrders 中，需要处理
			log.Debugf("🔄 [订单状态同步] 缓存显示订单已关闭: orderID=%s", orderID)
		}

		// 首先通过订单 ID 匹配
		if openOrderIDs[orderID] {
			// 订单仍在开放订单列表中，更新缓存
			s.orderStatusCache.Set(orderID, true)

			// 风险4修复：检查WebSocket状态和API状态是否一致
			// 如果WebSocket状态是pending，但API显示open，这是正常的（订单已提交但未成交）
			// 如果WebSocket状态是open，但API也显示open，状态一致
			if order.Status == domain.OrderStatusPending {
				// WebSocket显示pending，API显示open，这是正常的过渡状态
				log.Debugf("🔄 [订单状态同步] 订单状态一致: orderID=%s, WebSocket=pending, API=open (正常过渡状态)", orderID)
			} else if order.Status == domain.OrderStatusOpen {
				// WebSocket和API都显示open，状态一致
				log.Debugf("🔄 [订单状态同步] 订单状态一致: orderID=%s, WebSocket=open, API=open", orderID)
			} else {
				// 其他状态不一致的情况，记录警告
				log.Warnf("⚠️ [状态一致性] 订单状态可能不一致: orderID=%s, WebSocket状态=%s, API状态=open",
					orderID, order.Status)
			}
			continue
		}

		// 如果订单 ID 不匹配，尝试通过属性匹配（assetID + side + price）
		// 利用业务规则优化匹配：
		// - 入场订单价格范围：60-90（网格层级）
		// - 对冲订单价格范围：1-40（因为总成本 <= 100，且要保证利润目标）
		priceStr := fmt.Sprintf("%.4f", order.Price.ToDecimal())
		attrsKey := fmt.Sprintf("%s:%s:%s", order.AssetID, string(order.Side), priceStr)

		// 首先尝试精确匹配（assetID + side + price）
		if matchedOrderID, exists := openOrdersByAttrs[attrsKey]; exists {
			// 找到匹配的订单（通过属性），说明订单 ID 不匹配，需要更新
			log.Infof("🔄 [订单状态同步] 通过属性匹配找到订单: 本地ID=%s, 服务器ID=%s, assetID=%s, side=%s, price=%.4f",
				orderID, matchedOrderID, order.AssetID, order.Side, order.Price.ToDecimal())

			// 更新订单 ID
			order.OrderID = matchedOrderID
			updatedOrderIDs[orderID] = matchedOrderID

			// 发送 UpdateOrderCommand 更新 OrderEngine 状态
			updateCmd := &UpdateOrderCommand{
				id:    fmt.Sprintf("sync_update_%s", orderID),
				Order: order,
			}
			s.orderEngine.SubmitCommand(updateCmd)

			// 更新缓存
			s.orderStatusCache.Delete(orderID)
			s.orderStatusCache.Set(matchedOrderID, true)

			log.Debugf("🔄 [订单状态同步] 订单 ID 已更新: %s -> %s", orderID, matchedOrderID)
			continue
		}

		// 风险5修复：改进订单ID匹配算法
		// 如果精确匹配失败，尝试通过业务规则匹配（仅用于网格策略）
		// 入场订单：价格 60-90，对冲订单：价格 1-40
		// 通过 assetID + side 匹配，然后验证价格范围（允许 ±2 分误差）
		matched := false
		var bestMatch *struct {
			orderID string
			price   int
			score   float64 // 匹配分数：价格差异越小，分数越高
		}

		if order.IsEntryOrder {
			// 入场订单：价格应该在 60-90 之间
			if order.Price.Cents >= 60 && order.Price.Cents <= 90 {
				// 尝试通过 assetID + side 匹配（允许价格略有差异）
				for _, apiOrder := range openOrdersResp {
					// 解析 API 返回的价格字符串
					apiPrice, err := strconv.ParseFloat(apiOrder.Price, 64)
					if err != nil {
						continue
					}
					apiPriceCents := int(apiPrice * 100)

					if apiOrder.AssetID == order.AssetID &&
						apiOrder.Side == string(order.Side) &&
						// 价格允许一定误差（±2分），且价格在合理范围内（60-90）
						apiPriceCents >= 60 && apiPriceCents <= 90 {
						priceDiff := math.Abs(float64(apiPriceCents - order.Price.Cents))
						if priceDiff <= 2 {
							// 计算匹配分数（价格差异越小，分数越高）
							score := 1.0 / (1.0 + priceDiff)
							if bestMatch == nil || score > bestMatch.score {
								bestMatch = &struct {
									orderID string
									price   int
									score   float64
								}{
									orderID: apiOrder.ID,
									price:   apiPriceCents,
									score:   score,
								}
							}
						}
					}
				}
			}
		} else {
			// 对冲订单：价格应该在 1-40 之间
			if order.Price.Cents >= 1 && order.Price.Cents <= 40 {
				// 尝试通过 assetID + side 匹配（允许价格略有差异）
				for _, apiOrder := range openOrdersResp {
					// 解析 API 返回的价格字符串
					apiPrice, err := strconv.ParseFloat(apiOrder.Price, 64)
					if err != nil {
						continue
					}
					apiPriceCents := int(apiPrice * 100)

					if apiOrder.AssetID == order.AssetID &&
						apiOrder.Side == string(order.Side) &&
						// 价格允许一定误差（±2分），且价格在合理范围内（1-40）
						apiPriceCents >= 1 && apiPriceCents <= 40 {
						priceDiff := math.Abs(float64(apiPriceCents - order.Price.Cents))
						if priceDiff <= 2 {
							// 计算匹配分数（价格差异越小，分数越高）
							score := 1.0 / (1.0 + priceDiff)
							if bestMatch == nil || score > bestMatch.score {
								bestMatch = &struct {
									orderID string
									price   int
									score   float64
								}{
									orderID: apiOrder.ID,
									price:   apiPriceCents,
									score:   score,
								}
							}
						}
					}
				}
			}
		}

		// 如果找到最佳匹配，使用它
		if bestMatch != nil {
			matchedOrderID := bestMatch.orderID
			matchedPriceCents := bestMatch.price
			orderType := "入场订单"
			if !order.IsEntryOrder {
				orderType = "对冲订单"
			}
			log.Infof("🔄 [订单状态同步] 通过业务规则匹配找到%s: 本地ID=%s, 服务器ID=%s, assetID=%s, side=%s, 本地价格=%dc, 服务器价格=%dc, 匹配分数=%.2f",
				orderType, orderID, matchedOrderID, order.AssetID, order.Side, order.Price.Cents, matchedPriceCents, bestMatch.score)

			// 更新订单 ID 和价格
			order.OrderID = matchedOrderID
			order.Price = domain.Price{Cents: matchedPriceCents}
			updatedOrderIDs[orderID] = matchedOrderID

			// 发送 UpdateOrderCommand 更新 OrderEngine 状态
			updateCmd := &UpdateOrderCommand{
				id:    fmt.Sprintf("sync_update_%s", orderID),
				Order: order,
			}
			s.orderEngine.SubmitCommand(updateCmd)

			// 更新缓存
			s.orderStatusCache.Delete(orderID)
			s.orderStatusCache.Set(matchedOrderID, true)

			log.Debugf("🔄 [订单状态同步] %s ID 已更新: %s -> %s", orderType, orderID, matchedOrderID)
			matched = true
		} else if order.IsEntryOrder || (!order.IsEntryOrder && order.Price.Cents >= 1 && order.Price.Cents <= 40) {
			// 风险5修复：如果应该能找到匹配但没找到，记录警告
			orderType := "入场订单"
			if !order.IsEntryOrder {
				orderType = "对冲订单"
			}
			log.Warnf("⚠️ [订单匹配失败] 无法通过业务规则匹配%s: orderID=%s, assetID=%s, side=%s, price=%dc, 可能订单已成交或取消",
				orderType, orderID, order.AssetID, order.Side, order.Price.Cents)
		}

		// 如果通过业务规则匹配成功，跳过后续处理
		if matched {
			continue
		}

		// 如果订单不在开放订单列表中（既没有通过 ID 匹配，也没有通过属性匹配），说明已成交、取消或失败
		// 风险4修复：检查WebSocket状态和API状态的一致性

		// 首先检查订单是否已经标记为失败（提交失败）
		if order.Status == domain.OrderStatusFailed {
			// 订单已标记为失败，不需要再处理
			log.Debugf("🔄 [订单状态同步] 订单已标记为失败，跳过同步: orderID=%s", orderID)
			continue
		}

		// 检查订单是否真的提交成功
		// 如果订单状态是 pending，且不在开放列表中，可能是提交失败
		// 检查订单是否有真实的服务器 OrderID（服务器返回的 OrderID 通常格式不同）
		hasServerOrderID := order.OrderID != "" &&
			order.OrderID != orderID && // 订单ID已更新（不再是本地ID）
			!isLocalGeneratedOrderID(order.OrderID) // 不是本地生成的ID

		// 如果订单状态是 pending，且没有服务器 OrderID，且不在开放列表中，很可能是提交失败
		if order.Status == domain.OrderStatusPending && !hasServerOrderID {
			// 订单只有本地ID，且状态是pending，但不在开放列表中
			// 这很可能是订单提交失败（API返回错误），而不是已成交
			log.Warnf("⚠️ [订单状态同步] 订单可能提交失败: orderID=%s, 本地ID=%s, WebSocket状态=%s, API状态=不在开放列表中（可能是提交失败，而非已成交）",
				orderID, order.OrderID, order.Status)

			// 标记为失败，而不是已成交
			order.Status = domain.OrderStatusFailed

			// 发送 UpdateOrderCommand 更新 OrderEngine 状态
			updateCmd := &UpdateOrderCommand{
				id:    fmt.Sprintf("sync_failed_%s", orderID),
				Order: order,
			}
			s.orderEngine.SubmitCommand(updateCmd)

			// 更新缓存（标记为已关闭）
			s.orderStatusCache.Set(orderID, false)
			continue
		}

		if order.Status == domain.OrderStatusFilled {
			// WebSocket已经标记为已成交，API也显示不在开放列表中，状态一致
			log.Debugf("🔄 [订单状态同步] 订单已通过WebSocket更新为已成交，API确认不在开放列表中，状态一致: orderID=%s", orderID)
			continue
		} else if order.Status == domain.OrderStatusOpen || order.Status == domain.OrderStatusPending {
			// WebSocket显示订单仍在开放/等待中，但API显示不在开放列表中，状态不一致
			// 这可能是因为：
			// 1. 订单刚刚成交，WebSocket消息还未到达
			// 2. 订单被取消，但WebSocket消息还未到达
			// 3. API轮询延迟，订单实际上已经成交
			// 优先使用API状态（因为API查询的是当前实际状态），但记录警告
			log.Warnf("⚠️ [状态一致性] WebSocket和API状态不一致: orderID=%s, WebSocket状态=%s, API状态=已成交/已取消",
				orderID, order.Status)
		}

		log.Infof("🔄 [订单状态同步] 订单已成交: orderID=%s, side=%s, price=%.4f, size=%.2f",
			orderID, order.Side, order.Price.ToDecimal(), order.Size)

		// 更新订单状态为已成交
		order.Status = domain.OrderStatusFilled
		now := time.Now()
		order.FilledAt = &now

		// 发送 UpdateOrderCommand 更新 OrderEngine 状态
		updateCmd := &UpdateOrderCommand{
			id:    fmt.Sprintf("sync_filled_%s", orderID),
			Order: order,
		}
		s.orderEngine.SubmitCommand(updateCmd)

		filledCount++

		// 更新缓存（标记为已关闭）
		s.orderStatusCache.Set(orderID, false)
	}

	if filledCount > 0 {
		log.Debugf("🔄 [订单状态同步] 完成：发现 %d 个订单已成交", filledCount)
	} else {
		log.Debugf("🔄 [订单状态同步] 完成：所有 %d 个订单仍在开放订单列表中", len(orderIDs))
	}
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

// convertOrderResponseToDomain 将 OrderResponse 转换为 domain.Order
func (s *TradingService) convertOrderResponseToDomain(orderResp *types.OrderResponse, originalOrder *domain.Order) *domain.Order {
	// 根据订单响应状态确定订单状态
	var status domain.OrderStatus
	var filledAt *time.Time
	var actualSize float64 = originalOrder.Size // 默认使用原始订单大小

	if orderResp.Status == "matched" {
		// 订单立即成交
		status = domain.OrderStatusFilled
		now := time.Now()
		filledAt = &now

		// 解析实际成交数量
		// 注意：takingAmount 和 makingAmount 的单位可能不是 token 数量
		// 根据订单 payload 分析：
		// - makerAmount/takerAmount 是 wei 单位（6位小数）
		// - takingAmount/makingAmount 可能是其他单位
		// 为了安全，我们使用原始订单数量，而不是响应中的值
		// 实际成交数量应该从 WebSocket 的 trade 消息中获取
		actualSize = originalOrder.Size
		log.Debugf("📊 [订单响应] 订单立即成交: takingAmount=%s, makingAmount=%s, 使用原始订单数量=%.4f",
			orderResp.TakingAmount, orderResp.MakingAmount, actualSize)
	} else {
		// 订单已提交但未成交
		status = domain.OrderStatusOpen
	}

	createdOrder := &domain.Order{
		OrderID:      orderResp.OrderID,
		AssetID:      originalOrder.AssetID,
		Side:         originalOrder.Side,
		Price:        originalOrder.Price,
		Size:         actualSize, // 使用实际成交数量（如果是 matched）或原始数量
		Status:       status,
		FilledAt:     filledAt,
		CreatedAt:    time.Now(),
		TokenType:    originalOrder.TokenType,
		GridLevel:    originalOrder.GridLevel,
		IsEntryOrder: originalOrder.IsEntryOrder,
		HedgeOrderID: originalOrder.HedgeOrderID,
		PairOrderID:  originalOrder.PairOrderID,
	}
	return createdOrder
}

// PlaceOrder 下单（通过 OrderEngine 发送命令）
func (s *TradingService) PlaceOrder(ctx context.Context, order *domain.Order) (*domain.Order, error) {
	// 调整订单大小（在发送命令前）
	order = s.adjustOrderSize(order)

	// 发送下单命令到 OrderEngine
	reply := make(chan *PlaceOrderResult, 1)
	cmd := &PlaceOrderCommand{
		id:      fmt.Sprintf("place_%d", time.Now().UnixNano()),
		Order:   order,
		Reply:   reply,
		Context: ctx,
	}

	s.orderEngine.SubmitCommand(cmd)

	// 等待结果
	select {
	case result := <-reply:
		return result.Order, result.Error
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// adjustOrderSize 调整订单大小（确保满足最小要求）
func (s *TradingService) adjustOrderSize(order *domain.Order) *domain.Order {
	// 创建订单副本
	adjustedOrder := *order

	// 计算订单所需金额（USDC）
	requiredAmount := order.Price.ToDecimal() * order.Size

	// 检查并调整最小订单金额和最小 share 数量
	minOrderSize := s.minOrderSize
	if minOrderSize <= 0 {
		minOrderSize = 1.1 // 默认值
	}

	// Polymarket 要求最小 share 数量为 5
	const minShareSize = 5.0

	// 检查并调整订单大小
	originalSize := adjustedOrder.Size
	originalAmount := requiredAmount
	adjusted := false

	// 1. 首先检查 share 数量是否满足最小值
	if adjustedOrder.Size < minShareSize {
		adjustedOrder.Size = minShareSize
		adjusted = true
		log.Infof("⚠️ 订单 share 数量 %.4f 小于最小值 %.0f，自动调整: %.4f → %.4f shares",
			originalSize, minShareSize, originalSize, adjustedOrder.Size)
	}

	// 2. 重新计算金额（如果调整了 share 数量）
	requiredAmount = adjustedOrder.Price.ToDecimal() * adjustedOrder.Size

	// 3. 检查金额是否满足最小值
	if requiredAmount < minOrderSize {
		// 订单金额小于最小要求，自动调整 order.Size
		adjustedOrder.Size = minOrderSize / adjustedOrder.Price.ToDecimal()
		// 确保调整后的数量不小于最小 share 数量
		if adjustedOrder.Size < minShareSize {
			adjustedOrder.Size = minShareSize
		}
		adjusted = true
		// 重新计算所需金额
		requiredAmount = adjustedOrder.Price.ToDecimal() * adjustedOrder.Size
		log.Infof("⚠️ 订单金额 %.2f USDC 小于最小要求 %.2f USDC，自动调整数量: %.4f → %.4f shares (金额: %.2f → %.2f USDC)",
			originalAmount, minOrderSize, originalSize, adjustedOrder.Size, originalAmount, requiredAmount)
	}

	if adjusted {
		log.Infof("✅ 订单大小已调整: 原始=%.4f shares (%.2f USDC), 调整后=%.4f shares (%.2f USDC)",
			originalSize, originalAmount, adjustedOrder.Size, requiredAmount)
	}

	return &adjustedOrder
}

// 注意：旧的 PlaceOrder 实现已移除，现在通过 OrderEngine 处理
// 以下代码保留用于参考，但不再使用
/*
	// 检查余额和授权（暂时禁用，直接尝试下单）
	// TODO: 修复余额检测逻辑后重新启用
	if order.Side == types.SideBuy {
		// 获取 USDC 余额和授权
		// 传递 signatureType 参数（参考 test/clob.go 的实现）
		sigType := s.signatureType
		params := &types.BalanceAllowanceParams{
			AssetType:     types.AssetTypeCollateral,
			SignatureType: &sigType, // 传递签名类型
		}
		balanceInfo, err := s.clobClient.GetBalanceAllowance(ctx, params)
		if err != nil {
			log.Warnf("⚠️ 获取余额和授权失败，继续尝试下单: %v", err)
		} else {
			// 解析余额和授权（字符串格式，6位小数，需要除以 1e6 转换为 USDC 单位）
			// 处理空字符串情况，使用默认值 "0"
			balanceStr := balanceInfo.Balance
			if balanceStr == "" {
				balanceStr = "0"
				log.Debugf("余额字段为空，使用默认值 0")
			}

			allowanceStr := balanceInfo.Allowance
			if allowanceStr == "" {
				allowanceStr = "0"
				log.Debugf("授权字段为空，使用默认值 0")
			}

			// 记录原始值，用于调试
			log.Debugf("📊 [余额检查] API 返回原始值: balance=%q, allowance=%q", balanceStr, allowanceStr)

			balanceRaw, err := strconv.ParseInt(balanceStr, 10, 64)
			if err != nil {
				log.Warnf("⚠️ 解析余额失败 (值: %q)，继续尝试下单: %v", balanceStr, err)
			} else {
				allowanceRaw, err := strconv.ParseInt(allowanceStr, 10, 64)
				if err != nil {
					log.Warnf("⚠️ 解析授权失败 (值: %q)，继续尝试下单: %v", allowanceStr, err)
				} else {
					// 转换为 USDC 单位（除以 1e6）
					balance := float64(balanceRaw) / 1e6
					allowance := float64(allowanceRaw) / 1e6

					// 记录解析后的值
					log.Debugf("📊 [余额检查] 解析后: balanceRaw=%d, allowanceRaw=%d, balance=%.2f USDC, allowance=%.2f USDC, 需要=%.2f USDC",
						balanceRaw, allowanceRaw, balance, allowance, requiredAmount)

					// 检查余额
					if balance < requiredAmount {
						return nil, fmt.Errorf("余额不足: 需要 %.2f USDC，当前余额 %.2f USDC (原始值: %s)", requiredAmount, balance, balanceStr)
					}

					// 检查授权
					if allowance < requiredAmount {
						return nil, fmt.Errorf("授权不足: 需要 %.2f USDC，当前授权 %.2f USDC (原始值: %s)。请先授权USDC给CLOB合约", requiredAmount, allowance, allowanceStr)
					}

					log.Debugf("✅ 余额检查通过: 余额=%.2f USDC, 授权=%.2f USDC, 需要=%.2f USDC", balance, allowance, requiredAmount)
				}
			}
		}
	}
*/

// 注意：startOrderStatusChecker、startOrderStatusPolling 和 checkAndUpdateOrderStatus 方法已移除
// 订单状态现在通过 WebSocket 实时更新，不再需要轮询检查

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

// handleOrderPlaced 处理订单下单事件（通过 OrderEngine）
func (s *TradingService) handleOrderPlaced(order *domain.Order, market *domain.Market) error {
	log.Debugf("📥 [WebSocket] 订单已下单: orderID=%s, status=%s", order.OrderID, order.Status)

	// 发送 UpdateOrderCommand 到 OrderEngine
	updateCmd := &UpdateOrderCommand{
		id:    fmt.Sprintf("websocket_placed_%s", order.OrderID),
		Order: order,
	}
	s.orderEngine.SubmitCommand(updateCmd)

	// 更新缓存
	if order.Status == domain.OrderStatusOpen {
		s.orderStatusCache.Set(order.OrderID, true)
	}

	// 如果订单状态是 open，检查价格偏差
	if order.Status == domain.OrderStatusOpen && market != nil {
		// 在 goroutine 中异步检查价格偏差，避免阻塞
		go s.checkAndCorrectOrderPrice(context.Background(), order, market)
	}

	return nil
}

// checkAndCorrectOrderPrice 检查订单价格偏差并自动修正
func (s *TradingService) checkAndCorrectOrderPrice(ctx context.Context, order *domain.Order, market *domain.Market) {
	// 获取当前订单簿最佳价格
	var currentBestPrice float64
	var err error

	if order.Side == types.SideBuy {
		// 买入订单：使用最佳卖价（best ask）
		_, currentBestPrice, err = s.GetBestPrice(ctx, order.AssetID)
	} else {
		// 卖出订单：使用最佳买价（best bid）
		currentBestPrice, _, err = s.GetBestPrice(ctx, order.AssetID)
	}

	if err != nil {
		log.Warnf("⚠️ 无法获取订单簿价格，跳过价格偏差检查: orderID=%s, error=%v", order.OrderID, err)
		return
	}

	if currentBestPrice <= 0 {
		log.Warnf("⚠️ 订单簿价格无效，跳过价格偏差检查: orderID=%s", order.OrderID)
		return
	}

	// 计算价格偏差（分）
	expectedPrice := order.Price.ToDecimal()
	priceDeviationCents := int((currentBestPrice - expectedPrice) * 100)
	if priceDeviationCents < 0 {
		priceDeviationCents = -priceDeviationCents
	}

	// 价格偏差阈值：默认 2 cents
	// 注意：对于网格策略，如果订单价格与订单簿价格偏差超过 2 cents，说明价格已经变化，需要重新下单
	deviationThreshold := 2

	// 如果价格偏差超过阈值，撤单并重新下单
	if priceDeviationCents > deviationThreshold {
		log.Warnf("⚠️ 订单价格偏差过大: orderID=%s, 预期价格=%.4f, 当前最佳价格=%.4f, 偏差=%dc (阈值=%dc)",
			order.OrderID, expectedPrice, currentBestPrice, priceDeviationCents, deviationThreshold)

		// 检查订单是否仍然存在且状态为 open（通过 OrderEngine 查询）
		openOrders := s.GetActiveOrders()
		var existingOrder *domain.Order
		for _, o := range openOrders {
			if o.OrderID == order.OrderID {
				existingOrder = o
				break
			}
		}

		if existingOrder == nil || existingOrder.Status != domain.OrderStatusOpen {
			log.Debugf("订单状态已变化，跳过价格修正: orderID=%s", order.OrderID)
			return
		}

		// 撤单
		if err := s.CancelOrder(ctx, order.OrderID); err != nil {
			log.Errorf("❌ 撤单失败: orderID=%s, error=%v", order.OrderID, err)
			return
		}

		log.Infof("✅ 已撤单: orderID=%s (价格偏差过大: %dc)", order.OrderID, priceDeviationCents)

		// 等待一小段时间，确保撤单完成
		time.Sleep(500 * time.Millisecond)

		// 使用最新价格重新下单
		newPrice := domain.PriceFromDecimal(currentBestPrice)

		// 创建新的订单（使用新的订单 ID）
		newOrder := &domain.Order{
			OrderID:      fmt.Sprintf("%s-corrected-%d", order.OrderID, time.Now().UnixNano()),
			AssetID:      order.AssetID,
			Side:         order.Side,
			Price:        newPrice,
			Size:         order.Size,
			GridLevel:    order.GridLevel,
			TokenType:    order.TokenType,
			HedgeOrderID: order.HedgeOrderID,
			IsEntryOrder: order.IsEntryOrder,
			PairOrderID:  order.PairOrderID,
			Status:       domain.OrderStatusPending,
			CreatedAt:    time.Now(),
		}

		// 如果是配对订单（entry/hedge），需要同时处理对冲订单
		if order.PairOrderID != nil {
			// 通过 OrderEngine 查询配对订单
			openOrders := s.GetActiveOrders()
			var pairOrder *domain.Order
			for _, o := range openOrders {
				if o.OrderID == *order.PairOrderID {
					pairOrder = o
					break
				}
			}

			if pairOrder != nil && pairOrder.Status == domain.OrderStatusOpen {
				// 获取对冲订单的最佳价格
				var hedgeBestPrice float64
				if pairOrder.Side == types.SideBuy {
					_, hedgeBestPrice, err = s.GetBestPrice(ctx, pairOrder.AssetID)
				} else {
					hedgeBestPrice, _, err = s.GetBestPrice(ctx, pairOrder.AssetID)
				}

				if err == nil && hedgeBestPrice > 0 {
					// 计算对冲订单的价格偏差
					hedgeExpectedPrice := pairOrder.Price.ToDecimal()
					hedgeDeviationCents := int((hedgeBestPrice - hedgeExpectedPrice) * 100)
					if hedgeDeviationCents < 0 {
						hedgeDeviationCents = -hedgeDeviationCents
					}

					// 如果对冲订单价格偏差也超过阈值，同时撤单并重新下单
					if hedgeDeviationCents > deviationThreshold {
						log.Warnf("⚠️ 对冲订单价格偏差过大: orderID=%s, 预期价格=%.4f, 当前最佳价格=%.4f, 偏差=%dc (阈值=%dc)",
							pairOrder.OrderID, hedgeExpectedPrice, hedgeBestPrice, hedgeDeviationCents, deviationThreshold)

						// 撤单对冲订单
						if err := s.CancelOrder(ctx, pairOrder.OrderID); err != nil {
							log.Errorf("❌ 撤单对冲订单失败: orderID=%s, error=%v", pairOrder.OrderID, err)
						} else {
							log.Infof("✅ 已撤单对冲订单: orderID=%s (价格偏差过大: %dc)", pairOrder.OrderID, hedgeDeviationCents)

							// 等待撤单完成
							time.Sleep(500 * time.Millisecond)

							// 创建新的对冲订单（使用最新价格）
							hedgeNewPrice := domain.PriceFromDecimal(hedgeBestPrice)
							newHedgeOrder := &domain.Order{
								OrderID:      fmt.Sprintf("%s-corrected-%d", pairOrder.OrderID, time.Now().UnixNano()),
								AssetID:      pairOrder.AssetID,
								Side:         pairOrder.Side,
								Price:        hedgeNewPrice,
								Size:         pairOrder.Size,
								GridLevel:    pairOrder.GridLevel,
								TokenType:    pairOrder.TokenType,
								HedgeOrderID: pairOrder.HedgeOrderID,
								IsEntryOrder: pairOrder.IsEntryOrder,
								PairOrderID:  &newOrder.OrderID, // 更新配对订单 ID
								Status:       domain.OrderStatusPending,
								CreatedAt:    time.Now(),
							}

							// 更新配对关系
							newOrder.PairOrderID = &newHedgeOrder.OrderID
							newOrder.HedgeOrderID = &newHedgeOrder.OrderID
							newHedgeOrder.HedgeOrderID = &newOrder.OrderID

							// 先重新下单对冲订单
							_, err := s.PlaceOrder(ctx, newHedgeOrder)
							if err != nil {
								log.Errorf("❌ 重新下单对冲订单失败: error=%v", err)
							} else {
								log.Infof("✅ 已重新下单对冲订单: orderID=%s, 新价格=%.4f (原价格=%.4f, 偏差=%dc)",
									newHedgeOrder.OrderID, hedgeBestPrice, hedgeExpectedPrice, hedgeDeviationCents)
							}
						}
					} else {
						// 对冲订单价格正常，但需要更新配对关系
						newOrder.PairOrderID = &pairOrder.OrderID
						newOrder.HedgeOrderID = &pairOrder.OrderID
						log.Debugf("对冲订单价格正常，保持配对关系: pairOrderID=%s, 偏差=%dc (阈值=%dc)",
							pairOrder.OrderID, hedgeDeviationCents, deviationThreshold)
					}
				}
			}
		}

		// 重新下单
		_, err := s.PlaceOrder(ctx, newOrder)
		if err != nil {
			log.Errorf("❌ 重新下单失败: error=%v", err)
		} else {
			log.Infof("✅ 已重新下单: orderID=%s, 新价格=%.4f (原价格=%.4f, 偏差=%dc)",
				newOrder.OrderID, currentBestPrice, expectedPrice, priceDeviationCents)
		}
	} else {
		log.Debugf("✅ 订单价格正常: orderID=%s, 预期价格=%.4f, 当前最佳价格=%.4f, 偏差=%dc (阈值=%dc)",
			order.OrderID, expectedPrice, currentBestPrice, priceDeviationCents, deviationThreshold)
	}
}

// handleOrderFilled 处理订单成交事件（通过 OrderEngine）
func (s *TradingService) handleOrderFilled(order *domain.Order, market *domain.Market) error {
	// 确保 FilledAt 已设置
	if order.FilledAt == nil {
		now := time.Now()
		order.FilledAt = &now
	}

	// 更新订单状态
	order.Status = domain.OrderStatusFilled

	// 发送 UpdateOrderCommand 到 OrderEngine
	updateCmd := &UpdateOrderCommand{
		id:    fmt.Sprintf("websocket_filled_%s", order.OrderID),
		Order: order,
	}
	s.orderEngine.SubmitCommand(updateCmd)

	// 更新缓存（标记为已关闭）
	s.orderStatusCache.Set(order.OrderID, false)

	log.Infof("✅ [WebSocket] 订单已成交: orderID=%s, size=%.2f", order.OrderID, order.Size)

	return nil
}

// HandleTrade 处理交易事件（通过 OrderEngine）
func (s *TradingService) HandleTrade(ctx context.Context, trade *domain.Trade) {
	log.Debugf("📥 [WebSocket] 收到交易事件: tradeID=%s, orderID=%s, size=%.2f", trade.ID, trade.OrderID, trade.Size)

	// 发送 ProcessTradeCommand 到 OrderEngine
	cmd := &ProcessTradeCommand{
		id:    fmt.Sprintf("process_trade_%d", time.Now().UnixNano()),
		Trade: trade,
	}
	s.orderEngine.SubmitCommand(cmd)
}

// handleOrderCanceled 处理订单取消事件（通过 OrderEngine）
func (s *TradingService) handleOrderCanceled(order *domain.Order) error {
	// 更新订单状态
	order.Status = domain.OrderStatusCanceled

	// 发送 UpdateOrderCommand 到 OrderEngine
	updateCmd := &UpdateOrderCommand{
		id:    fmt.Sprintf("websocket_canceled_%s", order.OrderID),
		Order: order,
	}
	s.orderEngine.SubmitCommand(updateCmd)

	log.Infof("❌ [WebSocket] 订单已取消: orderID=%s", order.OrderID)

	return nil
}

// 注意：updatePositionFromOrder 方法已移除
// 仓位更新现在通过 TradeCollector 处理交易事件，而不是直接根据订单更新

// boolPtr 返回 bool 指针
func boolPtr(b bool) *bool {
	return &b
}

// CancelOrder 取消订单（通过 OrderEngine）
func (s *TradingService) CancelOrder(ctx context.Context, orderID string) error {
	reply := make(chan error, 1)
	cmd := &CancelOrderCommand{
		id:      fmt.Sprintf("cancel_%d", time.Now().UnixNano()),
		OrderID: orderID,
		Reply:   reply,
		Context: ctx,
	}

	s.orderEngine.SubmitCommand(cmd)

	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// GetActiveOrders 获取活跃订单（通过 OrderEngine 查询）
func (s *TradingService) GetActiveOrders() []*domain.Order {
	reply := make(chan *StateSnapshot, 1)
	cmd := &QueryStateCommand{
		id:    fmt.Sprintf("query_open_orders_%d", time.Now().UnixNano()),
		Query: QueryOpenOrders,
		Reply: reply,
	}

	s.orderEngine.SubmitCommand(cmd)

	select {
	case snapshot := <-reply:
		return snapshot.OpenOrders
	case <-time.After(5 * time.Second):
		return []*domain.Order{} // 超时返回空列表
	}
}

// GetPosition 获取仓位（通过 OrderEngine 查询）
func (s *TradingService) GetPosition(positionID string) (*domain.Position, error) {
	reply := make(chan *StateSnapshot, 1)
	cmd := &QueryStateCommand{
		id:    fmt.Sprintf("query_position_%d", time.Now().UnixNano()),
		Query: QueryPosition,
		Reply: reply,
	}

	s.orderEngine.SubmitCommand(cmd)

	select {
	case snapshot := <-reply:
		if snapshot.Position != nil && snapshot.Position.ID == positionID {
			return snapshot.Position, nil
		}
		return nil, fmt.Errorf("仓位不存在: %s", positionID)
	case <-time.After(5 * time.Second):
		return nil, fmt.Errorf("查询仓位超时: %s", positionID)
	}
}

// CreatePosition 创建仓位（通过 OrderEngine）
func (s *TradingService) CreatePosition(ctx context.Context, position *domain.Position) error {
	reply := make(chan error, 1)
	cmd := &CreatePositionCommand{
		id:       fmt.Sprintf("create_position_%d", time.Now().UnixNano()),
		Position: position,
		Reply:    reply,
	}

	s.orderEngine.SubmitCommand(cmd)

	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// UpdatePosition 更新仓位（通过 OrderEngine）
func (s *TradingService) UpdatePosition(ctx context.Context, positionID string, updater func(*domain.Position)) error {
	reply := make(chan error, 1)
	cmd := &UpdatePositionCommand{
		id:         fmt.Sprintf("update_position_%d", time.Now().UnixNano()),
		PositionID: positionID,
		Updater:    updater,
		Reply:      reply,
	}

	s.orderEngine.SubmitCommand(cmd)

	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ClosePosition 关闭仓位（通过 OrderEngine）
func (s *TradingService) ClosePosition(ctx context.Context, positionID string, exitPrice domain.Price, exitOrder *domain.Order) error {
	reply := make(chan error, 1)
	cmd := &ClosePositionCommand{
		id:         fmt.Sprintf("close_position_%d", time.Now().UnixNano()),
		PositionID: positionID,
		ExitPrice:  exitPrice,
		ExitOrder:  exitOrder,
		Reply:      reply,
	}

	s.orderEngine.SubmitCommand(cmd)

	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// GetAllPositions 获取所有仓位（通过 OrderEngine 查询）
func (s *TradingService) GetAllPositions() []*domain.Position {
	reply := make(chan *StateSnapshot, 1)
	cmd := &QueryStateCommand{
		id:    fmt.Sprintf("query_all_positions_%d", time.Now().UnixNano()),
		Query: QueryAllPositions,
		Reply: reply,
	}

	s.orderEngine.SubmitCommand(cmd)

	select {
	case snapshot := <-reply:
		return snapshot.Positions
	case <-time.After(5 * time.Second):
		return []*domain.Position{} // 超时返回空列表
	}
}

// GetOpenPositions 获取开放仓位（通过 OrderEngine 查询）
func (s *TradingService) GetOpenPositions() []*domain.Position {
	reply := make(chan *StateSnapshot, 1)
	cmd := &QueryStateCommand{
		id:    fmt.Sprintf("query_open_positions_%d", time.Now().UnixNano()),
		Query: QueryOpenPositions,
		Reply: reply,
	}

	s.orderEngine.SubmitCommand(cmd)

	select {
	case snapshot := <-reply:
		return snapshot.Positions
	case <-time.After(5 * time.Second):
		return []*domain.Position{} // 超时返回空列表
	}
}

// GetBestPrice 获取订单簿的最佳买卖价格（买一价和卖一价）
// SyncOrderStatus 同步订单状态（通过 API 查询，然后通过 OrderEngine 更新）
func (s *TradingService) SyncOrderStatus(ctx context.Context, orderID string) error {
	// 获取订单详情
	order, err := s.clobClient.GetOrder(ctx, orderID)
	if err != nil {
		return fmt.Errorf("获取订单详情失败: %w", err)
	}

	// 通过 OrderEngine 查询本地订单
	openOrders := s.GetActiveOrders()
	var localOrder *domain.Order
	for _, o := range openOrders {
		if o.OrderID == orderID {
			localOrder = o
			break
		}
	}

	if localOrder == nil {
		return nil // 订单不在本地，无需同步
	}

	// 解析订单状态
	originalSize, _ := strconv.ParseFloat(order.OriginalSize, 64)
	sizeMatched, _ := strconv.ParseFloat(order.SizeMatched, 64)

	// 如果订单已完全成交（sizeMatched >= originalSize），更新状态
	if sizeMatched >= originalSize && localOrder.Status != domain.OrderStatusFilled {
		log.Infof("🔄 [订单状态同步] 订单已完全成交: orderID=%s, sizeMatched=%.2f, originalSize=%.2f",
			orderID, sizeMatched, originalSize)

		localOrder.Status = domain.OrderStatusFilled
		now := time.Now()
		localOrder.FilledAt = &now
		localOrder.Size = sizeMatched

		// 发送 UpdateOrderCommand 到 OrderEngine
		updateCmd := &UpdateOrderCommand{
			id:    fmt.Sprintf("sync_status_%s", orderID),
			Order: localOrder,
		}
		s.orderEngine.SubmitCommand(updateCmd)
	} else if order.Status == "CANCELLED" && localOrder.Status != domain.OrderStatusCanceled {
		// 订单已取消
		log.Infof("🔄 [订单状态同步] 订单已取消: orderID=%s", orderID)

		localOrder.Status = domain.OrderStatusCanceled

		// 发送 UpdateOrderCommand 到 OrderEngine
		updateCmd := &UpdateOrderCommand{
			id:    fmt.Sprintf("sync_status_%s", orderID),
			Order: localOrder,
		}
		s.orderEngine.SubmitCommand(updateCmd)
	}

	return nil
}

func (s *TradingService) GetBestPrice(ctx context.Context, assetID string) (bestBid float64, bestAsk float64, err error) {
	// 获取订单簿
	book, err := s.clobClient.GetOrderBook(ctx, assetID, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("获取订单簿失败: %w", err)
	}

	// 获取最佳买一价（bids 中价格最高的）
	if len(book.Bids) > 0 {
		bestBid, err = strconv.ParseFloat(book.Bids[0].Price, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("解析买一价失败: %w", err)
		}
	}

	// 获取最佳卖一价（asks 中价格最低的）
	if len(book.Asks) > 0 {
		bestAsk, err = strconv.ParseFloat(book.Asks[0].Price, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("解析卖一价失败: %w", err)
		}
	}

	return bestBid, bestAsk, nil
}

// checkOrderBookLiquidity 检查订单簿是否有足够的流动性来匹配订单
// 返回: (是否有流动性, 实际可用价格)
func (s *TradingService) checkOrderBookLiquidity(ctx context.Context, assetID string, side types.Side, price float64, size float64) (bool, float64) {
	// 获取订单簿
	book, err := s.clobClient.GetOrderBook(ctx, assetID, nil)
	if err != nil {
		log.Debugf("⚠️ [订单簿检查] 获取订单簿失败，假设有流动性: %v", err)
		return true, price // 假设有流动性，使用原价格
	}

	// 根据订单方向检查对应的订单簿
	var levels []types.OrderSummary
	if side == types.SideBuy {
		// 买入订单：检查卖单（asks）
		levels = book.Asks
	} else {
		// 卖出订单：检查买单（bids）
		levels = book.Bids
	}

	if len(levels) == 0 {
		log.Debugf("⚠️ [订单簿检查] 订单簿为空，无流动性")
		return false, 0
	}

	// 检查是否有价格匹配的订单
	// 对于买入订单：asks 中的价格应该 <= 我们的价格
	// 对于卖出订单：bids 中的价格应该 >= 我们的价格
	matchedLevels := make([]types.OrderSummary, 0)
	totalSize := 0.0

	for _, level := range levels {
		levelPrice, err := strconv.ParseFloat(level.Price, 64)
		if err != nil {
			continue
		}

		levelSize, err := strconv.ParseFloat(level.Size, 64)
		if err != nil {
			continue
		}

		// 检查价格是否匹配
		if side == types.SideBuy {
			// 买入：asks 价格应该 <= 我们的价格
			if levelPrice <= price {
				matchedLevels = append(matchedLevels, level)
				totalSize += levelSize
			}
		} else {
			// 卖出：bids 价格应该 >= 我们的价格
			if levelPrice >= price {
				matchedLevels = append(matchedLevels, level)
				totalSize += levelSize
			}
		}

		// 如果已经累积足够的数量，停止检查
		if totalSize >= size {
			break
		}
	}

	// 检查是否有足够的流动性
	if len(matchedLevels) == 0 {
		log.Debugf("⚠️ [订单簿检查] 无价格匹配的订单: 订单价格=%.4f, 订单簿价格范围=[%.4f, %.4f]",
			price, getFirstPrice(levels), getLastPrice(levels))
		return false, 0
	}

	if totalSize < size {
		log.Debugf("⚠️ [订单簿检查] 流动性不足: 需要=%.4f, 可用=%.4f", size, totalSize)
		// 即使流动性不足，也返回 true，让 FAK 订单尝试部分成交
		// 但返回实际可用价格
		if len(matchedLevels) > 0 {
			actualPrice, _ := strconv.ParseFloat(matchedLevels[0].Price, 64)
			return true, actualPrice
		}
		return false, 0
	}

	// 有足够的流动性，返回最佳价格
	if len(matchedLevels) > 0 {
		actualPrice, _ := strconv.ParseFloat(matchedLevels[0].Price, 64)
		return true, actualPrice
	}

	return true, price
}

// getFirstPrice 获取订单簿第一个价格
func getFirstPrice(levels []types.OrderSummary) float64 {
	if len(levels) == 0 {
		return 0
	}
	price, _ := strconv.ParseFloat(levels[0].Price, 64)
	return price
}

// getLastPrice 获取订单簿最后一个价格
func getLastPrice(levels []types.OrderSummary) float64 {
	if len(levels) == 0 {
		return 0
	}
	price, _ := strconv.ParseFloat(levels[len(levels)-1].Price, 64)
	return price
}

// startOrderConfirmationTimeoutCheck 启动订单确认超时检测
// 如果订单提交后30秒内未收到WebSocket确认，则通过API拉取持仓来校正
func (s *TradingService) startOrderConfirmationTimeoutCheck(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.checkOrderConfirmationTimeout(ctx)
		}
	}
}

// checkOrderConfirmationTimeout 检查订单确认超时（已简化，不再使用锁）
func (s *TradingService) checkOrderConfirmationTimeout(ctx context.Context) {
	// 此功能已简化，现在通过 OrderEngine 管理订单状态
	// 如果需要超时检测，可以通过 OrderEngine 查询订单状态
	log.Debugf("订单确认超时检测已简化，现在通过 OrderEngine 管理")
}

// FetchUserPositionsFromAPI 从Polymarket Data API拉取用户持仓并校正本地状态
func (s *TradingService) FetchUserPositionsFromAPI(ctx context.Context) error {
	if s.funderAddress == "" {
		return fmt.Errorf("funder地址未设置，无法拉取持仓")
	}

	// 构建API请求URL
	apiURL := fmt.Sprintf("https://data-api.polymarket.com/positions?user=%s&sizeThreshold=0.01&limit=500", s.funderAddress)

	// 创建HTTP请求
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return fmt.Errorf("创建HTTP请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	// 发送请求
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("API返回错误状态码: %d", resp.StatusCode)
	}

	// 解析响应
	var positions []map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&positions); err != nil {
		return fmt.Errorf("解析响应失败: %w", err)
	}

	log.Infof("📊 [仓位同步] 从API拉取到 %d 个持仓", len(positions))

	// 更新本地仓位状态（这里可以根据实际需求实现更复杂的逻辑）
	// 注意：由于API返回的持仓格式可能与本地不同，这里只记录日志
	// 实际校正逻辑需要根据API返回的数据结构来实现
	for _, pos := range positions {
		if asset, ok := pos["asset"].(string); ok {
			if size, ok := pos["size"].(string); ok {
				sizeFloat, _ := strconv.ParseFloat(size, 64)
				log.Debugf("📊 [仓位同步] 持仓: asset=%s, size=%.4f", asset, sizeFloat)
			}
		}
	}

	return nil
}
