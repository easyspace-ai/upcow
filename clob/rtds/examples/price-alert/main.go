//go:build ignore
// +build ignore

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

// PriceAlert 价格告警配置
type PriceAlert struct {
	Symbol    string
	Threshold float64
	Direction string // "above" 或 "below"
	Triggered bool
}

func main() {
	// 配置价格告警
	alerts := []PriceAlert{
		{Symbol: "btcusdt", Threshold: 70000.0, Direction: "above"},
		{Symbol: "ethusdt", Threshold: 4000.0, Direction: "above"},
		{Symbol: "btcusdt", Threshold: 60000.0, Direction: "below"},
	}

	fmt.Println("=== Polymarket 价格告警系统 ===\n")
	fmt.Println("配置的告警:")
	for i, alert := range alerts {
		fmt.Printf("  %d. %s %s $%.2f\n", i+1, alert.Symbol, alert.Direction, alert.Threshold)
	}
	fmt.Println()

	// 创建客户端
	client := polymarketrtds.NewClient()

	// 创建价格处理器
	priceHandler := polymarketrtds.CreateCryptoPriceHandler(func(price *polymarketrtds.CryptoPrice) error {
		v := price.Value.Float64()
		// 检查每个告警
		for i := range alerts {
			alert := &alerts[i]
			if alert.Symbol != price.Symbol || alert.Triggered {
				continue
			}

			shouldAlert := false
			if alert.Direction == "above" && v >= alert.Threshold {
				shouldAlert = true
			} else if alert.Direction == "below" && v <= alert.Threshold {
				shouldAlert = true
			}

			if shouldAlert {
				alert.Triggered = true
				fmt.Printf("\n🚨 价格告警触发！\n")
				fmt.Printf("   币种: %s\n", price.Symbol)
				fmt.Printf("   当前价格: $%.2f\n", v)
				fmt.Printf("   阈值: $%.2f (%s)\n", alert.Threshold, alert.Direction)
				fmt.Printf("   时间: %s\n\n", time.Now().Format(time.RFC3339))
			}
		}

		// 显示当前价格（每10秒一次）
		if time.Now().Second()%10 == 0 {
			fmt.Printf("[%s] %s: $%.2f\n",
				time.Now().Format("15:04:05"),
				price.Symbol,
				v)
		}

		return nil
	})

	// 注册处理器
	client.RegisterHandler("crypto_prices", priceHandler)

	// 连接
	fmt.Println("正在连接到 Polymarket RTDS...")
	if err := client.Connect(); err != nil {
		log.Fatalf("连接失败: %v", err)
	}
	defer client.Disconnect()

	fmt.Println("✅ 连接成功！\n")

	// 订阅所有配置的币种
	symbols := make(map[string]bool)
	for _, alert := range alerts {
		symbols[alert.Symbol] = true
	}

	symbolList := make([]string, 0, len(symbols))
	for symbol := range symbols {
		symbolList = append(symbolList, symbol)
	}

	fmt.Printf("订阅币种: %v\n\n", symbolList)
	if err := client.SubscribeToCryptoPrices("binance", symbolList...); err != nil {
		log.Fatalf("订阅失败: %v", err)
	}

	fmt.Println("监控中... (按 Ctrl+C 退出)\n")

	// 等待中断信号
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	fmt.Println("\n\n=== 告警统计 ===")
	triggeredCount := 0
	for _, alert := range alerts {
		if alert.Triggered {
			triggeredCount++
			fmt.Printf("✅ %s %s $%.2f - 已触发\n", alert.Symbol, alert.Direction, alert.Threshold)
		} else {
			fmt.Printf("⏳ %s %s $%.2f - 未触发\n", alert.Symbol, alert.Direction, alert.Threshold)
		}
	}
	fmt.Printf("\n总计: %d/%d 告警已触发\n", triggeredCount, len(alerts))
}
