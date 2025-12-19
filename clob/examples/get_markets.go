//go:build ignore
// +build ignore

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/betbot/gobet/clob/client"
	"github.com/betbot/gobet/clob/signing"
	"github.com/betbot/gobet/clob/types"
)

// 示例：获取市场信息
// 使用方法：
//   export PRIVATE_KEY="your_private_key_hex"  # 可选，用于认证
//   export CHAIN_ID=137  # 可选，默认 137
//   export CLOB_API_URL="https://clob.polymarket.com"  # 可选
//   export MARKET_SLUG="btc-updown-15m-1765960200"  # 可选，指定市场 slug
//   go run get_markets.go

func main() {
	// 从环境变量读取配置
	chainIDStr := os.Getenv("CHAIN_ID")
	if chainIDStr == "" {
		chainIDStr = "137"
	}
	chainIDInt, err := strconv.Atoi(chainIDStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: CHAIN_ID 必须是数字: %v\n", err)
		os.Exit(1)
	}
	chainID := types.Chain(chainIDInt)

	host := os.Getenv("CLOB_API_URL")
	if host == "" {
		host = "https://clob.polymarket.com"
	}

	marketSlug := os.Getenv("MARKET_SLUG")

	// 创建客户端（获取市场信息不需要认证）
	var clobClient *client.Client
	privateKeyHex := os.Getenv("PRIVATE_KEY")
	if privateKeyHex != "" {
		privateKey, err := signing.PrivateKeyFromHex(privateKeyHex)
		if err != nil {
			fmt.Fprintf(os.Stderr, "错误: 解析私钥失败: %v\n", err)
			os.Exit(1)
		}
		clobClient = client.NewClient(host, chainID, privateKey, nil)
	} else {
		// 创建一个临时私钥用于初始化（实际上不会用于认证）
		privateKey, _ := signing.PrivateKeyFromHex("0000000000000000000000000000000000000000000000000000000000000001")
		clobClient = client.NewClient(host, chainID, privateKey, nil)
	}

	ctx := context.Background()

	// 获取市场列表
	fmt.Println("正在获取市场列表...")
	var slugPtr *string
	if marketSlug != "" {
		slugPtr = &marketSlug
		fmt.Printf("查询市场: %s\n", marketSlug)
	}

	markets, err := clobClient.GetMarkets(ctx, slugPtr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: 获取市场列表失败: %v\n", err)
		os.Exit(1)
	}

	// 格式化输出
	jsonData, err := json.MarshalIndent(markets, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: 序列化数据失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n✅ 获取到 %d 个市场\n\n", len(markets))
	fmt.Println(string(jsonData))
}

