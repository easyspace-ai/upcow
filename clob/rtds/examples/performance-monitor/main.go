package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	polymarketrtds "github.com/betbot/gobet/clob/rtds"
)

// PerformanceStats 性能统计
type PerformanceStats struct {
	StartTime       time.Time
	TotalMessages   int64
	MessagesByTopic map[string]int64
	MessagesByType  map[string]int64
	LastMessageTime time.Time
	MessageLatency  []time.Duration
	ErrorCount      int64
}

func main() {
	fmt.Println("=== Polymarket RTDS 性能监控 ===\n")

	stats := &PerformanceStats{
		StartTime:       time.Now(),
		MessagesByTopic: make(map[string]int64),
		MessagesByType:  make(map[string]int64),
		MessageLatency:  make([]time.Duration, 0),
	}

	// 创建客户端
	client := polymarketrtds.NewClient()

	// 注册全局处理器，收集性能数据
	client.RegisterHandler("*", func(msg *polymarketrtds.Message) error {
		stats.TotalMessages++
		stats.MessagesByTopic[msg.Topic]++
		stats.MessagesByType[msg.Type]++

		// 计算消息延迟（基于时间戳）
		if msg.Timestamp > 0 {
			msgTime := time.Unix(msg.Timestamp/1000, (msg.Timestamp%1000)*1000000)
			latency := time.Since(msgTime)
			stats.MessageLatency = append(stats.MessageLatency, latency)

			// 只保留最近1000条消息的延迟数据
			if len(stats.MessageLatency) > 1000 {
				stats.MessageLatency = stats.MessageLatency[len(stats.MessageLatency)-1000:]
			}
		}

		stats.LastMessageTime = time.Now()
		return nil
	})

	// 订阅多个主题以收集更多数据
	client.RegisterHandler("crypto_prices", func(msg *polymarketrtds.Message) error {
		return nil // 数据已在全局处理器中收集
	})

	client.RegisterHandler("activity", func(msg *polymarketrtds.Message) error {
		return nil
	})

	client.RegisterHandler("comments", func(msg *polymarketrtds.Message) error {
		return nil
	})

	// 连接
	fmt.Println("正在连接到 Polymarket RTDS...")
	connectStart := time.Now()
	if err := client.Connect(); err != nil {
		log.Fatalf("连接失败: %v", err)
	}
	connectDuration := time.Since(connectStart)
	defer client.Disconnect()

	fmt.Printf("✅ 连接成功！(耗时: %v)\n\n", connectDuration)

	// 订阅多个数据流
	fmt.Println("订阅多个数据流...")
	if err := client.SubscribeToCryptoPrices("binance", "btcusdt", "ethusdt"); err != nil {
		log.Printf("订阅加密货币价格失败: %v", err)
	}

	if err := client.SubscribeToActivity("", "", "trades"); err != nil {
		log.Printf("订阅交易活动失败: %v", err)
	}

	if err := client.SubscribeToComments(nil, "Event", "comment_created"); err != nil {
		log.Printf("订阅评论失败: %v", err)
	}

	fmt.Println("✅ 订阅完成！\n")

	// 定期显示性能统计
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// 等待中断信号
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	fmt.Println("监控中... (每5秒更新一次统计，按 Ctrl+C 退出)\n")

	for {
		select {
		case <-sigChan:
			displayFinalStats(stats)
			return
		case <-ticker.C:
			displayStats(stats)
		}
	}
}

func displayStats(stats *PerformanceStats) {
	fmt.Print("\033[2J\033[H") // 清屏
	fmt.Println("=== Polymarket RTDS 性能监控 ===")
	fmt.Printf("更新时间: %s\n\n", time.Now().Format("2006-01-02 15:04:05"))

	// 运行时间
	uptime := time.Since(stats.StartTime)
	fmt.Printf("运行时间: %v\n", uptime.Round(time.Second))

	// 消息统计
	fmt.Printf("\n总消息数: %d\n", stats.TotalMessages)
	if stats.TotalMessages > 0 {
		msgRate := float64(stats.TotalMessages) / uptime.Seconds()
		fmt.Printf("消息速率: %.2f 消息/秒\n", msgRate)
	}

	// 按主题统计
	if len(stats.MessagesByTopic) > 0 {
		fmt.Println("\n按主题统计:")
		for topic, count := range stats.MessagesByTopic {
			percentage := float64(count) / float64(stats.TotalMessages) * 100
			fmt.Printf("  %s: %d (%.1f%%)\n", topic, count, percentage)
		}
	}

	// 按类型统计（显示前10个）
	if len(stats.MessagesByType) > 0 {
		fmt.Println("\n按类型统计 (前10):")
		count := 0
		for msgType, msgCount := range stats.MessagesByType {
			if count >= 10 {
				break
			}
			percentage := float64(msgCount) / float64(stats.TotalMessages) * 100
			fmt.Printf("  %s: %d (%.1f%%)\n", msgType, msgCount, percentage)
			count++
		}
	}

	// 延迟统计
	if len(stats.MessageLatency) > 0 {
		var totalLatency time.Duration
		for _, lat := range stats.MessageLatency {
			totalLatency += lat
		}
		avgLatency := totalLatency / time.Duration(len(stats.MessageLatency))

		// 计算最小和最大延迟
		minLatency := stats.MessageLatency[0]
		maxLatency := stats.MessageLatency[0]
		for _, lat := range stats.MessageLatency {
			if lat < minLatency {
				minLatency = lat
			}
			if lat > maxLatency {
				maxLatency = lat
			}
		}

		fmt.Println("\n消息延迟统计:")
		fmt.Printf("  平均延迟: %v\n", avgLatency.Round(time.Millisecond))
		fmt.Printf("  最小延迟: %v\n", minLatency.Round(time.Millisecond))
		fmt.Printf("  最大延迟: %v\n", maxLatency.Round(time.Millisecond))
		fmt.Printf("  样本数: %d\n", len(stats.MessageLatency))
	}

	// 最后消息时间
	if !stats.LastMessageTime.IsZero() {
		timeSinceLastMsg := time.Since(stats.LastMessageTime)
		fmt.Printf("\n最后消息: %v 前\n", timeSinceLastMsg.Round(time.Second))
	}

	// 错误统计
	if stats.ErrorCount > 0 {
		fmt.Printf("\n错误数: %d\n", stats.ErrorCount)
	}
}

func displayFinalStats(stats *PerformanceStats) {
	fmt.Println("\n\n=== 最终性能报告 ===")
	uptime := time.Since(stats.StartTime)
	fmt.Printf("总运行时间: %v\n", uptime.Round(time.Second))
	fmt.Printf("总消息数: %d\n", stats.TotalMessages)
	if stats.TotalMessages > 0 {
		msgRate := float64(stats.TotalMessages) / uptime.Seconds()
		fmt.Printf("平均消息速率: %.2f 消息/秒\n", msgRate)
	}
	fmt.Printf("错误数: %d\n", stats.ErrorCount)
}
