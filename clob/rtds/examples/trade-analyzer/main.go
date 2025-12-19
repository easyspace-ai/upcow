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

// TradeSummary 交易摘要
type TradeSummary struct {
	MarketID      string
	AssetID       string
	TotalTrades   int64
	BuyTrades     int64
	SellTrades    int64
	TotalVolume   float64
	BuyVolume     float64
	SellVolume    float64
	LastTradeTime time.Time
	LastPrice     string
}

func main() {
	fmt.Println("=== Polymarket 交易活动分析器 ===\n")

	// 存储交易统计
	tradeStats := make(map[string]*TradeSummary)

	// 创建客户端
	client := polymarketrtds.NewClient()

	// 交易处理器
	tradeHandler := polymarketrtds.CreateTradeHandler(func(trade *polymarketrtds.Trade) error {
		marketKey := fmt.Sprintf("%s-%s", trade.Market, trade.AssetID)

		stats, exists := tradeStats[marketKey]
		if !exists {
			stats = &TradeSummary{
				MarketID:      trade.Market,
				AssetID:       trade.AssetID,
				LastTradeTime: time.Now(),
			}
			tradeStats[marketKey] = stats
		}

		// 更新统计
		stats.TotalTrades++
		stats.LastTradeTime = time.Now()
		stats.LastPrice = trade.Price.String()

		// 解析交易量（简化处理，实际应该正确解析字符串）
		if trade.Side == "BUY" {
			stats.BuyTrades++
			// stats.BuyVolume += parseFloat(trade.Size)
		} else if trade.Side == "SELL" {
			stats.SellTrades++
			// stats.SellVolume += parseFloat(trade.Size)
		}

		// 实时显示大额交易
		// if parseFloat(trade.Size) > 100 {
		// 	fmt.Printf("\n💰 大额交易: %s %s @ %s (Size: %s)\n",
		// 		trade.Side, trade.Outcome, trade.Price, trade.Size)
		// }

		return nil
	})

	// 注册处理器
	client.RegisterHandler("activity", func(msg *polymarketrtds.Message) error {
		if msg.Type == "trades" || msg.Type == "orders_matched" {
			return tradeHandler(msg)
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

	// 订阅所有交易活动
	fmt.Println("订阅交易活动...")
	if err := client.SubscribeToActivity("", "", "trades", "orders_matched"); err != nil {
		log.Fatalf("订阅失败: %v", err)
	}
	fmt.Println("✅ 订阅成功！\n")

	// 定期显示统计
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// 等待中断信号
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	fmt.Println("分析中... (每5秒更新一次统计，按 Ctrl+C 退出)\n")

	for {
		select {
		case <-sigChan:
			displayFinalStats(tradeStats)
			return
		case <-ticker.C:
			displayStats(tradeStats)
		}
	}
}

func displayStats(stats map[string]*TradeSummary) {
	fmt.Print("\033[2J\033[H") // 清屏
	fmt.Println("=== Polymarket 交易活动分析 ===")
	fmt.Printf("更新时间: %s\n\n", time.Now().Format("2006-01-02 15:04:05"))

	if len(stats) == 0 {
		fmt.Println("暂无交易数据...")
		return
	}

	// 按交易总数排序
	type statEntry struct {
		key   string
		stats *TradeSummary
	}
	entries := make([]statEntry, 0, len(stats))
	for k, s := range stats {
		entries = append(entries, statEntry{k, s})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].stats.TotalTrades > entries[j].stats.TotalTrades
	})

	// 显示表格
	fmt.Printf("%-20s %-15s %-10s %-10s %-10s %-12s %-15s\n",
		"市场", "资产", "总交易", "买入", "卖出", "最后价格", "最后交易时间")
	fmt.Println("-------------------------------------------------------------------------------------------")

	for _, entry := range entries {
		s := entry.stats
		timeStr := s.LastTradeTime.Format("15:04:05")
		if time.Since(s.LastTradeTime) > 1*time.Minute {
			timeStr = "无活动"
		}

		fmt.Printf("%-20s %-15s %-10d %-10d %-10d %-12s %-15s\n",
			truncate(s.MarketID, 18),
			truncate(s.AssetID, 13),
			s.TotalTrades,
			s.BuyTrades,
			s.SellTrades,
			s.LastPrice,
			timeStr)
	}

	fmt.Println()
}

func displayFinalStats(stats map[string]*TradeSummary) {
	fmt.Println("\n\n=== 最终统计 ===")
	fmt.Printf("监控市场数: %d\n", len(stats))

	totalTrades := int64(0)
	totalBuy := int64(0)
	totalSell := int64(0)

	for _, s := range stats {
		totalTrades += s.TotalTrades
		totalBuy += s.BuyTrades
		totalSell += s.SellTrades
	}

	fmt.Printf("总交易数: %d\n", totalTrades)
	fmt.Printf("买入交易: %d (%.1f%%)\n", totalBuy, float64(totalBuy)/float64(totalTrades)*100)
	fmt.Printf("卖出交易: %d (%.1f%%)\n", totalSell, float64(totalSell)/float64(totalTrades)*100)
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
