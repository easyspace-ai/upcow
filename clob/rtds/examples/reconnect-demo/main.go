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

func main() {
	fmt.Println("=== Polymarket RTDS 重连机制演示 ===\n")

	// 创建带自定义配置的客户端，启用重连
	config := &polymarketrtds.ClientConfig{
		URL:            polymarketrtds.RTDSWebSocketURL,
		PingInterval:   5 * time.Second,
		WriteTimeout:   10 * time.Second,
		ReadTimeout:    60 * time.Second,
		Reconnect:      true,            // 启用自动重连
		ReconnectDelay: 3 * time.Second, // 重连延迟
		MaxReconnect:   10,              // 最大重连次数
	}

	client := polymarketrtds.NewClientWithConfig(config)

	// 连接状态监控
	connectionCount := 0
	lastMessageTime := time.Now()

	// 注册全局处理器，监控连接状态
	client.RegisterHandler("*", func(msg *polymarketrtds.Message) error {
		lastMessageTime = time.Now()
		if connectionCount == 0 {
			connectionCount++
			fmt.Printf("✅ 首次连接成功！收到第一条消息\n")
		}
		return nil
	})

	// 价格处理器
	priceHandler := polymarketrtds.CreateCryptoPriceHandler(func(price *polymarketrtds.CryptoPrice) error {
		fmt.Printf("[%s] %s: $%.2f\n",
			time.Now().Format("15:04:05"),
			price.Symbol,
			price.Value)
		return nil
	})

	client.RegisterHandler("crypto_prices", priceHandler)

	// 连接
	fmt.Println("正在连接到 Polymarket RTDS...")
	fmt.Println("配置:")
	fmt.Printf("  - 自动重连: %v\n", config.Reconnect)
	fmt.Printf("  - 重连延迟: %v\n", config.ReconnectDelay)
	fmt.Printf("  - 最大重连次数: %d\n", config.MaxReconnect)
	fmt.Println()

	if err := client.Connect(); err != nil {
		log.Fatalf("连接失败: %v", err)
	}
	defer client.Disconnect()

	fmt.Println("✅ 连接成功！\n")

	// 订阅加密货币价格
	fmt.Println("订阅加密货币价格 (BTC, ETH)...")
	if err := client.SubscribeToCryptoPrices("binance", "btcusdt", "ethusdt"); err != nil {
		log.Fatalf("订阅失败: %v", err)
	}
	fmt.Println("✅ 订阅成功！\n")

	fmt.Println("监控中...")
	fmt.Println("提示: 可以断开网络连接来测试重连机制")
	fmt.Println("     按 Ctrl+C 退出\n")

	// 定期检查连接状态
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	// 等待中断信号
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	for {
		select {
		case <-sigChan:
			fmt.Println("\n\n正在关闭...")
			fmt.Printf("连接状态: %v\n", client.IsConnected())
			return
		case <-ticker.C:
			// 检查连接状态
			if client.IsConnected() {
				timeSinceLastMsg := time.Since(lastMessageTime)
				if timeSinceLastMsg > 30*time.Second {
					fmt.Printf("⚠️  警告: 已 %v 未收到消息\n", timeSinceLastMsg)
				} else {
					fmt.Printf("✅ 连接正常 (最后消息: %v 前)\n", timeSinceLastMsg)
				}
			} else {
				fmt.Println("❌ 连接已断开，等待重连...")
			}
		}
	}
}
