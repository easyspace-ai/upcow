package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/betbot/gobet/clob/rtds"
	"github.com/betbot/gobet/pkg/config"
	"github.com/betbot/gobet/pkg/logger"
)

var (
	proxyURL      = flag.String("proxy", "", "代理 URL (例如: http://127.0.0.1:15236)")
	cryptoSource  = flag.String("crypto-source", "", "加密货币价格源 (binance 或 chainlink)")
	cryptoSymbols = flag.String("crypto-symbols", "", "加密货币符号，逗号分隔 (例如: btc/usd,eth/usd)")
	comments      = flag.Bool("comments", false, "订阅评论数据")
	trades        = flag.Bool("trades", false, "订阅交易数据")
	orderbook     = flag.Bool("orderbook", false, "订阅订单簿数据")
	verbose       = flag.Bool("verbose", false, "显示详细日志")
	raw           = flag.Bool("raw", false, "显示原始 JSON 消息")
)

func main() {
	flag.Parse()

	// 初始化 logger
	logLevel := "info"
	if *verbose {
		logLevel = "debug"
	}

	if err := logger.Init(logger.Config{
		Level:      logLevel,
		OutputFile: "", // 只输出到控制台
	}); err != nil {
		log.Fatalf("初始化日志失败: %v", err)
	}

	fmt.Printf("\n🚀 RTDS 监控工具启动\n")
	if *verbose {
		logger.Infof("配置: proxy=%s, crypto-source=%s, crypto-symbols=%s",
			*proxyURL, *cryptoSource, *cryptoSymbols)
	}

	// 获取代理 URL（优先级：命令行参数 > 全局配置 > 环境变量）
	proxy := *proxyURL
	if proxy == "" {
		if globalConfig := config.Get(); globalConfig != nil && globalConfig.Proxy != nil {
			proxy = fmt.Sprintf("http://%s:%d", globalConfig.Proxy.Host, globalConfig.Proxy.Port)
			logger.Infof("从全局配置获取代理: %s", proxy)
		} else {
			if envProxy := os.Getenv("HTTP_PROXY"); envProxy != "" {
				proxy = envProxy
				logger.Infof("从环境变量获取代理: %s", proxy)
			} else if envProxy := os.Getenv("HTTPS_PROXY"); envProxy != "" {
				proxy = envProxy
				logger.Infof("从环境变量获取代理: %s", proxy)
			}
		}
	}

	// 创建 RTDS 客户端配置
	rtdsConfig := &rtds.ClientConfig{
		URL:            rtds.RTDSWebSocketURL,
		ProxyURL:       proxy,
		PingInterval:   5 * time.Second,
		WriteTimeout:   10 * time.Second,
		ReadTimeout:    60 * time.Second,
		Reconnect:      true,
		ReconnectDelay: 5 * time.Second,
		MaxReconnect:   10,
		Logger:         &rtdsLoggerAdapter{},
	}

	client := rtds.NewClientWithConfig(rtdsConfig)

	// 注册通用消息处理器（用于显示所有消息）
	if *raw {
		client.RegisterHandler("*", func(msg *rtds.Message) error {
			jsonData, _ := json.MarshalIndent(msg, "", "  ")
			fmt.Printf("\n[%s] 原始消息:\n%s\n", time.Now().Format("15:04:05"), string(jsonData))
			return nil
		})
	}

	// 注册加密货币价格处理器
	if *cryptoSource != "" && *cryptoSymbols != "" {
		topic := "crypto_prices"
		if *cryptoSource == "chainlink" {
			topic = "crypto_prices_chainlink"
		}

		handler := rtds.CreateCryptoPriceHandler(func(price *rtds.CryptoPrice) error {
			timestamp := time.Unix(price.Timestamp/1000, (price.Timestamp%1000)*1000000)
			// 如果是 BTC，使用醒目格式
			if strings.ToLower(price.Symbol) == "btc/usd" || strings.ToLower(price.Symbol) == "btcusdt" {
				fmt.Printf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
				fmt.Printf("💰 BTC 实时价格: $%.2f\n", price.Value.Float64())
				fmt.Printf("   时间: %s | 数据源: %s\n", timestamp.Format("2006-01-02 15:04:05"), strings.ToUpper(*cryptoSource))
				fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
			} else {
				fmt.Printf("[%s] 💰 %s %s: $%.2f (时间: %s)\n",
					time.Now().Format("15:04:05"),
					strings.ToUpper(*cryptoSource),
					price.Symbol,
					price.Value.Float64(),
					timestamp.Format("15:04:05"))
			}
			return nil
		})

		client.RegisterHandler(topic, handler)
	}

	// 注册评论处理器
	if *comments {
		handler := rtds.CreateCommentHandler(func(comment *rtds.Comment) error {
			fmt.Printf("[%s] 💬 评论: ID=%s, Body=%s, EntityID=%d\n",
				time.Now().Format("15:04:05"),
				comment.ID,
				truncate(comment.Body, 50),
				comment.ParentEntityID)
			return nil
		})
		client.RegisterHandler("comments", handler)
	}

	// 注册交易处理器
	if *trades {
		handler := rtds.CreateTradeHandler(func(trade *rtds.Trade) error {
			price, _ := trade.Price.Float64()
			size, _ := trade.Size.Float64()
			fmt.Printf("[%s] 📊 交易: Market=%s, AssetID=%s, Side=%s, Price=%.4f, Size=%.4f\n",
				time.Now().Format("15:04:05"),
				trade.Market,
				truncate(trade.AssetID, 20),
				trade.Side,
				price,
				size)
			return nil
		})
		client.RegisterHandler("trades", handler)
	}

	// 注册订单簿处理器
	if *orderbook {
		handler := rtds.CreateAggOrderbookHandler(func(book *rtds.AggOrderbook) error {
			fmt.Printf("[%s] 📖 订单簿: Market=%s, AssetID=%s, Bids=%d, Asks=%d\n",
				time.Now().Format("15:04:05"),
				book.Market,
				truncate(book.AssetID, 20),
				len(book.Bids),
				len(book.Asks))
			return nil
		})
		client.RegisterHandler("orderbook", handler)
	}

	// 连接 RTDS
	logger.Infof("正在连接 RTDS...")
	if err := client.Connect(); err != nil {
		log.Fatalf("连接 RTDS 失败: %v", err)
	}
	defer client.Disconnect()

	logger.Infof("✅ RTDS 连接成功")

	// 检查是否有任何订阅
	hasAnySubscription := (*cryptoSource != "" && *cryptoSymbols != "") || *comments || *trades || *orderbook

	// 如果没有指定任何订阅，默认订阅 Chainlink BTC 价格
	if !hasAnySubscription {
		logger.Infof("未指定订阅参数，使用默认配置：订阅 Chainlink BTC 价格")
		*cryptoSource = "chainlink"
		*cryptoSymbols = "btc/usd"

		// 注册默认的 BTC 价格处理器
		handler := rtds.CreateCryptoPriceHandler(func(price *rtds.CryptoPrice) error {
			timestamp := time.Unix(price.Timestamp/1000, (price.Timestamp%1000)*1000000)
			// 使用醒目的格式显示 BTC 价格
			fmt.Printf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
			fmt.Printf("💰 BTC 实时价格: $%.2f\n", price.Value.Float64())
			fmt.Printf("   时间: %s | 数据源: Chainlink\n", timestamp.Format("2006-01-02 15:04:05"))
			fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
			return nil
		})
		client.RegisterHandler("crypto_prices_chainlink", handler)
	}

	// 订阅加密货币价格
	if *cryptoSource != "" && *cryptoSymbols != "" {
		symbols := strings.Split(*cryptoSymbols, ",")
		for i := range symbols {
			symbols[i] = strings.TrimSpace(symbols[i])
		}
		logger.Infof("订阅 %s 加密货币价格: %v", *cryptoSource, symbols)
		if err := client.SubscribeToCryptoPrices(*cryptoSource, symbols...); err != nil {
			log.Fatalf("订阅加密货币价格失败: %v", err)
		}
		logger.Infof("✅ 加密货币价格订阅成功")
	}

	// 订阅评论
	if *comments {
		logger.Infof("订阅评论数据...")
		if err := client.SubscribeToComments(nil, "Event", "*"); err != nil {
			log.Fatalf("订阅评论失败: %v", err)
		}
		logger.Infof("✅ 评论订阅成功")
	}

	// 订阅交易（需要市场 slug）
	if *trades {
		logger.Warnf("交易订阅需要指定市场 slug，当前未实现")
	}

	// 订阅订单簿（需要市场 slug）
	if *orderbook {
		logger.Warnf("订单簿订阅需要指定市场 slug，当前未实现")
	}

	fmt.Printf("\n📡 开始监控 RTDS 数据...\n")
	fmt.Printf("按 Ctrl+C 停止监控\n\n")

	// 等待中断信号
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// 定期显示连接状态和统计信息
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if client.IsConnected() {
					logger.Debugf("RTDS 连接状态: 已连接 - %s", client.DebugSnapshot())
				} else {
					logger.Warnf("RTDS 连接状态: 未连接（可能正在重连中）")
					// 显示快照以便诊断
					logger.Debugf("RTDS 快照: %s", client.DebugSnapshot())
				}
			}
		}
	}()

	<-sigChan
	logger.Infof("\n正在关闭...")
}

// rtdsLoggerAdapter 适配器，将 RTDS 日志输出到我们的 logger 系统
type rtdsLoggerAdapter struct{}

func (l *rtdsLoggerAdapter) Printf(format string, v ...interface{}) {
	logger.Debugf("[RTDS] "+format, v...)
}

// truncate 截断字符串
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
