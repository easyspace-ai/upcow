package main

import (
	"context"
	"log"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/betbot/gobet/clob/client"
	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/services"
)

func main() {
	// 设置日志级别
	logrus.SetLevel(logrus.DebugLevel)

	log.Println("🚀 开始测试 OrderEngine（纸模式）...")

	// 创建 CLOB 客户端（纸模式下不会真正调用）
	clobClient := client.NewClient("https://clob.polymarket.com", types.ChainAmoy, nil, nil)

	// 创建交易服务（纸模式）
	tradingService := services.NewTradingService(clobClient, true) // dryRun = true

	// 启动交易服务
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := tradingService.Start(ctx); err != nil {
		log.Fatalf("❌ 启动交易服务失败: %v", err)
	}

	log.Println("✅ 交易服务已启动")

	// 等待 OrderEngine 启动
	time.Sleep(100 * time.Millisecond)

	// 测试1: 创建订单
	log.Println("\n📝 测试1: 创建订单")
	order := &domain.Order{
		AssetID:   "test_asset_123",
		Side:      types.SideBuy,
		Price:     domain.Price{Cents: 60}, // 0.60 USDC
		Size:      10.0,                    // 10 shares
		TokenType: domain.TokenTypeUp,
		GridLevel: 60,
	}

	createdOrder, err := tradingService.PlaceOrder(ctx, order)
	if err != nil {
		log.Fatalf("❌ 下单失败: %v", err)
	}

	log.Printf("✅ 订单创建成功: OrderID=%s, Status=%s", createdOrder.OrderID, createdOrder.Status)

	// 测试2: 查询活跃订单
	log.Println("\n📝 测试2: 查询活跃订单")
	time.Sleep(50 * time.Millisecond) // 等待订单处理完成
	activeOrders := tradingService.GetActiveOrders()
	log.Printf("✅ 活跃订单数量: %d", len(activeOrders))
	for _, o := range activeOrders {
		log.Printf("  - OrderID: %s, Status: %s, Price: %.2f, Size: %.2f",
			o.OrderID, o.Status, o.Price.ToDecimal(), o.Size)
	}

	// 测试3: 创建仓位
	log.Println("\n📝 测试3: 创建仓位")
	position := &domain.Position{
		ID:        "test_position_1",
		TokenType: domain.TokenTypeUp,
		Size:      10.0,
		Status:    domain.PositionStatusOpen,
	}

	if err := tradingService.CreatePosition(ctx, position); err != nil {
		log.Fatalf("❌ 创建仓位失败: %v", err)
	}
	log.Println("✅ 仓位创建成功")

	// 测试4: 查询仓位
	log.Println("\n📝 测试4: 查询仓位")
	time.Sleep(50 * time.Millisecond)
	positions := tradingService.GetAllPositions()
	log.Printf("✅ 仓位数量: %d", len(positions))
	for _, p := range positions {
		log.Printf("  - PositionID: %s, Size: %.2f, Status: %s",
			p.ID, p.Size, p.Status)
	}

	// 测试5: 处理交易事件
	log.Println("\n📝 测试5: 处理交易事件")
	trade := &domain.Trade{
		ID:        "test_trade_1",
		OrderID:   createdOrder.OrderID,
		AssetID:   "test_asset_123",
		Side:      types.SideBuy,
		Price:     domain.Price{Cents: 60},
		Size:      10.0,
		TokenType: domain.TokenTypeUp,
		Time:      time.Now(),
	}

	tradingService.HandleTrade(ctx, trade)
	time.Sleep(100 * time.Millisecond) // 等待交易处理完成

	// 再次查询订单状态
	activeOrders = tradingService.GetActiveOrders()
	log.Printf("✅ 交易处理后，活跃订单数量: %d", len(activeOrders))

	// 测试6: 取消订单
	log.Println("\n📝 测试6: 取消订单")
	if len(activeOrders) > 0 {
		orderToCancel := activeOrders[0]
		if err := tradingService.CancelOrder(ctx, orderToCancel.OrderID); err != nil {
			log.Printf("⚠️ 取消订单失败: %v", err)
		} else {
			log.Printf("✅ 订单已取消: %s", orderToCancel.OrderID)
		}
		time.Sleep(50 * time.Millisecond)
		activeOrders = tradingService.GetActiveOrders()
		log.Printf("✅ 取消后，活跃订单数量: %d", len(activeOrders))
	}

	// 测试7: 获取统计信息
	log.Println("\n📝 测试7: 获取 OrderEngine 统计信息")
	// 注意：这里需要通过反射或其他方式访问 orderEngine，或者添加一个公开方法
	// 暂时跳过，因为 orderEngine 是私有字段

	log.Println("\n✅ 所有测试完成！")
}

