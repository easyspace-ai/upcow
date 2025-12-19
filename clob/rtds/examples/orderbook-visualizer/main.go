package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	polymarketrtds "github.com/betbot/gobet/clob/rtds"
)

func main() {
	fmt.Println("=== Polymarket 订单簿可视化 ===\n")

	// 市场 ID（需要替换为真实的市场 ID）
	marketID := "0x5f65177b394277fd294cd75650044e32ba009a95022d88a0c1d565897d72f8f1"

	// 存储订单簿数据
	var currentOrderbook *polymarketrtds.AggOrderbook
	var lastPrice *polymarketrtds.LastTradePrice

	// 创建客户端
	client := polymarketrtds.NewClient()

	// 订单簿处理器
	orderbookHandler := polymarketrtds.CreateAggOrderbookHandler(func(orderbook *polymarketrtds.AggOrderbook) error {
		currentOrderbook = orderbook
		return nil
	})

	// 价格处理器
	priceHandler := polymarketrtds.CreateLastTradePriceHandler(func(price *polymarketrtds.LastTradePrice) error {
		lastPrice = price
		return nil
	})

	// 注册处理器
	client.RegisterHandler("clob_market", func(msg *polymarketrtds.Message) error {
		switch msg.Type {
		case "agg_orderbook":
			return orderbookHandler(msg)
		case "last_trade_price":
			return priceHandler(msg)
		}
		return nil
	})

	// 连接
	fmt.Println("正在连接到 Polymarket RTDS...")
	if err := client.Connect(); err != nil {
		log.Fatalf("连接失败: %v", err)
	}
	defer client.Disconnect()

	fmt.Println("✅ 连接成功！\n")

	// 订阅市场数据
	fmt.Printf("订阅市场: %s\n", truncate(marketID, 50))
	marketIDs := []string{marketID}
	if err := client.SubscribeToClobMarket(marketIDs, "agg_orderbook", "last_trade_price"); err != nil {
		log.Fatalf("订阅失败: %v", err)
	}
	fmt.Println("✅ 订阅成功！\n")

	// 定期更新显示
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	// 等待中断信号
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	fmt.Println("可视化中... (每秒更新一次，按 Ctrl+C 退出)\n")

	for {
		select {
		case <-sigChan:
			fmt.Println("\n\n正在关闭...")
			return
		case <-ticker.C:
			displayOrderbook(currentOrderbook, lastPrice)
		}
	}
}

func displayOrderbook(orderbook *polymarketrtds.AggOrderbook, lastPrice *polymarketrtds.LastTradePrice) {
	fmt.Print("\033[2J\033[H") // 清屏

	if orderbook == nil {
		fmt.Println("等待订单簿数据...")
		return
	}

	fmt.Println("=== 订单簿可视化 ===")
	fmt.Printf("市场: %s\n", truncate(orderbook.Market, 60))
	fmt.Printf("资产: %s\n", truncate(orderbook.AssetID, 60))
	if lastPrice != nil {
		fmt.Printf("最后成交价: %s\n", lastPrice.Price)
	}
	fmt.Printf("更新时间: %s\n\n", time.Now().Format("2006-01-02 15:04:05"))

	// 显示卖盘（Ask）
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("卖盘 (Asks)")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Printf("%-15s %-15s %-15s\n", "价格", "数量", "累计")
	fmt.Println("───────────────────────────────────────────────")

	askDepth := 10
	if len(orderbook.Asks) < askDepth {
		askDepth = len(orderbook.Asks)
	}

	totalAskSize := 0.0
	for i := 0; i < askDepth; i++ {
		ask := orderbook.Asks[i]
		price, _ := strconv.ParseFloat(ask.Price, 64)
		size, _ := strconv.ParseFloat(ask.Size, 64)
		totalAskSize += size

		fmt.Printf("%-15.4f %-15.2f %-15.2f\n", price, size, totalAskSize)
	}

	// 显示中间价差
	if len(orderbook.Bids) > 0 && len(orderbook.Asks) > 0 {
		bestBid, _ := strconv.ParseFloat(orderbook.Bids[0].Price, 64)
		bestAsk, _ := strconv.ParseFloat(orderbook.Asks[0].Price, 64)
		spread := bestAsk - bestBid
		spreadPercent := (spread / bestBid) * 100

		fmt.Println()
		fmt.Printf("最佳买价: %.4f | 最佳卖价: %.4f | 价差: %.4f (%.2f%%)\n",
			bestBid, bestAsk, spread, spreadPercent)
		fmt.Println()
	}

	// 显示买盘（Bid）
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("买盘 (Bids)")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Printf("%-15s %-15s %-15s\n", "价格", "数量", "累计")
	fmt.Println("───────────────────────────────────────────────")

	bidDepth := 10
	if len(orderbook.Bids) < bidDepth {
		bidDepth = len(orderbook.Bids)
	}

	totalBidSize := 0.0
	for i := bidDepth - 1; i >= 0; i-- {
		bid := orderbook.Bids[i]
		price, _ := strconv.ParseFloat(bid.Price, 64)
		size, _ := strconv.ParseFloat(bid.Size, 64)
		totalBidSize += size

		fmt.Printf("%-15.4f %-15.2f %-15.2f\n", price, size, totalBidSize)
	}

	fmt.Println()
	fmt.Printf("订单簿哈希: %s\n", truncate(orderbook.Hash, 50))
	fmt.Printf("Tick Size: %s | Min Order Size: %s\n", orderbook.TickSize, orderbook.MinOrderSize)
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
