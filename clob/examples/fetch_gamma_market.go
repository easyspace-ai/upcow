package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/betbot/gobet/clob/client"
)

// 示例：从 Gamma API 获取市场数据
// 使用方法：
//   export MARKET_SLUG="btc-updown-15m-1765960200"
//   go run fetch_gamma_market.go

func main() {
	// 从环境变量读取市场 slug
	marketSlug := os.Getenv("MARKET_SLUG")
	if marketSlug == "" {
		fmt.Fprintf(os.Stderr, "错误: 请设置 MARKET_SLUG 环境变量\n")
		fmt.Fprintf(os.Stderr, "示例: export MARKET_SLUG=\"btc-updown-15m-1765960200\"\n")
		os.Exit(1)
	}

	ctx := context.Background()

	fmt.Printf("正在从 Gamma API 获取市场数据: %s\n", marketSlug)

	// 从 Gamma API 获取市场数据（不需要认证）
	market, err := client.FetchMarketFromGamma(ctx, marketSlug)
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: 获取市场数据失败: %v\n", err)
		os.Exit(1)
	}

	// 格式化输出
	jsonData, err := json.MarshalIndent(market, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: 序列化数据失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("\n✅ 市场数据获取成功！\n")
	fmt.Println(string(jsonData))

	// 解析 clobTokenIds（通常是逗号分隔的 YES 和 NO token IDs）
	if market.ClobTokenIDs != "" {
		fmt.Printf("\nToken IDs: %s\n", market.ClobTokenIDs)
	}
}

