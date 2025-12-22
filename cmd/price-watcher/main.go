package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/betbot/gobet/clob/client"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/infrastructure/websocket"
	"github.com/betbot/gobet/internal/services"
	"github.com/betbot/gobet/pkg/config"
)

func main() {
	// 加载配置（用于代理设置）
	config.SetConfigPath("config.yaml")
	cfg, err := config.Load()
	if err != nil {
		log.Printf("警告: 无法加载配置文件，使用默认配置: %v", err)
		cfg = &config.Config{}
	}

	ctx := context.Background()

	// 获取当前周期的市场
	currentTs := services.GetCurrent15MinTimestamp()
	currentSlug := services.Generate15MinSlug(currentTs)

	// 显示启动信息
	fmt.Printf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	fmt.Printf("🚀 WebSocket 价格监控程序\n")
	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	fmt.Printf("当前周期: %s\n", currentSlug)
	fmt.Printf("周期时间戳: %d\n", currentTs)
	fmt.Printf("周期开始时间: %s\n", time.Unix(currentTs, 0).Format("2006-01-02 15:04:05"))
	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")

	// 直接使用 Gamma API 获取市场信息（不需要认证）
	gammaMarket, err := client.FetchMarketFromGamma(ctx, currentSlug)
	if err != nil {
		log.Fatalf("获取市场信息失败: %v", err)
	}

	// 解析 token IDs
	yesAssetID, noAssetID := parseTokenIDs(gammaMarket.ClobTokenIDs)
	if yesAssetID == "" || noAssetID == "" {
		log.Fatalf("解析 token IDs 失败: %s", gammaMarket.ClobTokenIDs)
	}

	// 创建市场对象（使用互斥锁保护并发访问）
	var marketMu sync.RWMutex
	market := &domain.Market{
		Slug:        gammaMarket.Slug,
		ConditionID: gammaMarket.ConditionID,
		YesAssetID:  yesAssetID,
		NoAssetID:   noAssetID,
		Timestamp:   currentTs,
	}

	// 设置代理 URL
	proxyURL := ""
	if cfg.Proxy != nil {
		proxyURL = fmt.Sprintf("http://%s:%d", cfg.Proxy.Host, cfg.Proxy.Port)
	}

	// 创建 MarketStream
	marketStream := websocket.NewMarketStream()
	marketStream.SetProxyURL(proxyURL)

	// 创建价格处理器
	priceHandler := &priceChangeHandler{
		marketMu: &marketMu,
		market:   market,
	}

	// 注册价格变化处理器
	marketStream.OnPriceChanged(priceHandler)

	// 连接市场数据流
	fmt.Printf("正在连接市场数据 WebSocket...\n")
	if err := marketStream.Connect(ctx, market); err != nil {
		log.Fatalf("连接市场数据流失败: %v", err)
	}
	defer marketStream.Close()

	fmt.Printf("✅ 市场数据流连接成功\n\n")
	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	fmt.Printf("开始监控价格变化...\n")
	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")

	// 周期切换检测
	cycleCtx, cycleCancel := context.WithCancel(context.Background())
	defer cycleCancel()

	go func() {
		ticker := time.NewTicker(5 * time.Second) // 每5秒检查一次
		defer ticker.Stop()

		for {
			select {
			case <-cycleCtx.Done():
				return
			case <-ticker.C:
				// 检查是否需要切换到下一个周期（使用读锁）
				marketMu.RLock()
				currentMarket := market
				marketMu.RUnlock()

				now := time.Now().Unix()
				periodEnd := currentMarket.Timestamp + 900 // 15分钟 = 900秒

				if now >= periodEnd {
					// 切换到下一个周期
					nextTs := services.GetCurrent15MinTimestamp()
					if nextTs <= currentMarket.Timestamp {
						nextTs = currentMarket.Timestamp + 900
					}
					nextSlug := services.Generate15MinSlug(nextTs)

					fmt.Printf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
					fmt.Printf("🔄 周期切换检测到\n")
					fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
					fmt.Printf("旧周期: %s\n", currentMarket.Slug)
					fmt.Printf("新周期: %s\n", nextSlug)
					fmt.Printf("新周期时间戳: %d\n", nextTs)
					fmt.Printf("新周期开始时间: %s\n", time.Unix(nextTs, 0).Format("2006-01-02 15:04:05"))
					fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")

					// 获取新市场信息
					nextGammaMarket, err := client.FetchMarketFromGamma(ctx, nextSlug)
					if err != nil {
						log.Printf("获取新市场信息失败: %v", err)
						continue
					}

					// 解析 token IDs
					nextYesAssetID, nextNoAssetID := parseTokenIDs(nextGammaMarket.ClobTokenIDs)
					if nextYesAssetID == "" || nextNoAssetID == "" {
						log.Printf("解析新市场 token IDs 失败: %s", nextGammaMarket.ClobTokenIDs)
						continue
					}

					// 关闭旧连接
					marketStream.Close()

					// 创建新的 MarketStream
					newMarketStream := websocket.NewMarketStream()
					newMarketStream.SetProxyURL(proxyURL)

					// 更新市场（使用写锁）
					marketMu.Lock()
					market = &domain.Market{
						Slug:        nextGammaMarket.Slug,
						ConditionID: nextGammaMarket.ConditionID,
						YesAssetID:  nextYesAssetID,
						NoAssetID:   nextNoAssetID,
						Timestamp:   nextTs,
					}
					newMarket := market
					priceHandler.market = market
					marketMu.Unlock()

					// 注册价格变化处理器
					newMarketStream.OnPriceChanged(priceHandler)

					// 连接新市场
					if err := newMarketStream.Connect(ctx, newMarket); err != nil {
						log.Printf("连接新市场数据流失败: %v", err)
						continue
					}

					// 更新引用
					marketStream = newMarketStream

					fmt.Printf("✅ 已切换到新周期: %s\n", newMarket.Slug)
					fmt.Printf("✅ 已连接新市场数据流\n\n")
				}
			}
		}
	}()

	// 等待中断信号
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	fmt.Printf("\n正在关闭...\n")
}

// priceChangeHandler 价格变化处理器
type priceChangeHandler struct {
	marketMu *sync.RWMutex
	market   *domain.Market
}

// OnPriceChanged 实现 PriceChangeHandler 接口
func (h *priceChangeHandler) OnPriceChanged(ctx context.Context, event *events.PriceChangedEvent) error {
	if event == nil || event.Market == nil {
		return nil
	}

	// 检查市场是否匹配
	h.marketMu.RLock()
	currentMarket := h.market
	h.marketMu.RUnlock()

	if event.Market.Slug != currentMarket.Slug {
		return nil // 不是当前市场的消息，忽略
	}

	// 打印价格信息
	priceCents := float64(event.NewPrice.Cents) / 100.0
	tokenTypeStr := strings.ToUpper(string(event.TokenType))
	printPrice(tokenTypeStr, priceCents, event.Timestamp)

	return nil
}

// parseTokenIDs 解析 token IDs（支持 JSON 数组格式: ["token1", "token2"] 或逗号分隔格式）
func parseTokenIDs(clobTokenIDs string) (yesAssetID, noAssetID string) {
	// 尝试解析为 JSON 数组
	var tokenArray []string
	if err := json.Unmarshal([]byte(clobTokenIDs), &tokenArray); err == nil {
		if len(tokenArray) >= 2 {
			return tokenArray[0], tokenArray[1]
		}
		return "", ""
	}

	// 如果不是 JSON 数组，尝试用正则表达式解析（兼容旧格式）
	// 移除 JSON 数组标记和引号
	re := regexp.MustCompile(`["'\[\]]`)
	cleaned := re.ReplaceAllString(clobTokenIDs, "")
	parts := regexp.MustCompile(`[,\-]\s*`).Split(cleaned, -1)
	if len(parts) >= 2 {
		yesAssetID = strings.TrimSpace(parts[0])
		noAssetID = strings.TrimSpace(parts[1])
		if yesAssetID != "" && noAssetID != "" {
			return yesAssetID, noAssetID
		}
	}

	return "", ""
}

// printPrice 打印价格信息
func printPrice(tokenType string, price float64, updateTime time.Time) {
	// 使用 ANSI 颜色代码美化输出
	var colorReset = "\033[0m"
	var colorUp = "\033[32m"   // 绿色
	var colorDown = "\033[31m" // 红色
	var colorBold = "\033[1m"

	color := colorUp
	if tokenType == "DOWN" {
		color = colorDown
	}

	fmt.Printf("%s[%s]%s %s%s%s 价格: %s%.2f%s\n",
		colorReset,
		updateTime.Format("15:04:05"),
		colorReset,
		colorBold,
		tokenType,
		colorReset,
		color,
		price*100, // 转换为分显示
		colorReset,
	)
}
