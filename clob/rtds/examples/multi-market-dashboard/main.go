package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"sort"
	"syscall"
	"time"

	polymarketrtds "github.com/betbot/gobet/clob/rtds"
)

// MarketStats 市场统计
type MarketStats struct {
	MarketID    string
	AssetID     string
	LastPrice   string
	BestBid     string
	BestAsk     string
	Spread      string
	LastUpdate  time.Time
	UpdateCount int64
}

func main() {
	fmt.Println("=== Polymarket 多市场实时监控面板 ===\n")

	// 市场列表（示例市场 ID，实际使用时需要替换为真实的市场 ID）
	marketIDs := []string{
		"0x5f65177b394277fd294cd75650044e32ba009a95022d88a0c1d565897d72f8f1",
		// 可以添加更多市场 ID
	}

	// 存储市场统计数据
	marketStats := make(map[string]*MarketStats)

	// 创建客户端
	client := polymarketrtds.NewClient()

	// 订单簿更新处理器
	orderbookHandler := polymarketrtds.CreateAggOrderbookHandler(func(orderbook *polymarketrtds.AggOrderbook) error {
		marketKey := fmt.Sprintf("%s-%s", orderbook.Market, orderbook.AssetID)

		stats, exists := marketStats[marketKey]
		if !exists {
			stats = &MarketStats{
				MarketID:   orderbook.Market,
				AssetID:    orderbook.AssetID,
				LastUpdate: time.Now(),
			}
			marketStats[marketKey] = stats
		}

		// 更新统计数据
		if len(orderbook.Bids) > 0 {
			stats.BestBid = orderbook.Bids[0].Price
		}
		if len(orderbook.Asks) > 0 {
			stats.BestAsk = orderbook.Asks[0].Price
		}

		// 计算价差
		if stats.BestBid != "" && stats.BestAsk != "" {
			// 这里简化处理，实际应该解析字符串为数字
			stats.Spread = fmt.Sprintf("%s - %s", stats.BestBid, stats.BestAsk)
		}

		stats.LastUpdate = time.Now()
		stats.UpdateCount++

		return nil
	})

	// 最后成交价处理器
	priceHandler := polymarketrtds.CreateLastTradePriceHandler(func(price *polymarketrtds.LastTradePrice) error {
		marketKey := fmt.Sprintf("%s-%s", price.Market, price.AssetID)

		stats, exists := marketStats[marketKey]
		if !exists {
			stats = &MarketStats{
				MarketID:   price.Market,
				AssetID:    price.AssetID,
				LastUpdate: time.Now(),
			}
			marketStats[marketKey] = stats
		}

		stats.LastPrice = price.Price
		stats.LastUpdate = time.Now()
		stats.UpdateCount++

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
	if len(marketIDs) > 0 {
		fmt.Printf("订阅 %d 个市场...\n", len(marketIDs))
		if err := client.SubscribeToClobMarket(marketIDs, "agg_orderbook", "last_trade_price"); err != nil {
			log.Fatalf("订阅失败: %v", err)
		}
		fmt.Println("✅ 订阅成功！\n")
	} else {
		fmt.Println("⚠️  未配置市场 ID，请编辑代码添加市场 ID")
	}

	// 定期更新显示
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	// 等待中断信号
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	fmt.Println("监控中... (每2秒更新一次，按 Ctrl+C 退出)\n")

	for {
		select {
		case <-sigChan:
			fmt.Println("\n\n正在关闭...")
			return
		case <-ticker.C:
			displayDashboard(marketStats)
		}
	}
}

func displayDashboard(stats map[string]*MarketStats) {
	// 清屏（简化版，实际可以使用更复杂的终端控制）
	fmt.Print("\033[2J\033[H") // ANSI 清屏
	fmt.Println("=== Polymarket 多市场实时监控面板 ===")
	fmt.Printf("更新时间: %s\n\n", time.Now().Format("2006-01-02 15:04:05"))

	if len(stats) == 0 {
		fmt.Println("暂无市场数据...")
		return
	}

	// 按市场 ID 排序
	keys := make([]string, 0, len(stats))
	for k := range stats {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// 显示表格
	fmt.Printf("%-20s %-15s %-12s %-12s %-12s %-10s\n",
		"市场", "资产", "最后价格", "最佳买价", "最佳卖价", "更新次数")
	fmt.Println("--------------------------------------------------------------------------------")

	for _, key := range keys {
		s := stats[key]
		fmt.Printf("%-20s %-15s %-12s %-12s %-12s %-10d\n",
			truncate(s.MarketID, 18),
			truncate(s.AssetID, 13),
			s.LastPrice,
			s.BestBid,
			s.BestAsk,
			s.UpdateCount)
	}

	fmt.Println()
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
