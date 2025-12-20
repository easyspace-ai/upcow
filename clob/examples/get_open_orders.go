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

// 示例：获取开放订单
// 使用方法：
//   export PRIVATE_KEY="your_private_key_hex"
//   export API_KEY="your_api_key"  # 可选，如果已创建过 API 密钥
//   export API_SECRET="your_api_secret"
//   export API_PASSPHRASE="your_api_passphrase"
//   export CHAIN_ID=137
//   export CLOB_API_URL="https://clob.polymarket.com"
//   export MARKET_SLUG="btc-updown-15m-1765960200"  # 可选，过滤特定市场
//   export TOKEN_ID="token_id"  # 可选，过滤特定 token
//   go run get_open_orders.go

func main() {
	// 从环境变量读取配置
	privateKeyHex := os.Getenv("PRIVATE_KEY")
	if privateKeyHex == "" {
		fmt.Fprintf(os.Stderr, "错误: 请设置 PRIVATE_KEY 环境变量\n")
		os.Exit(1)
	}

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

	// 解析私钥
	privateKey, err := signing.PrivateKeyFromHex(privateKeyHex)
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: 解析私钥失败: %v\n", err)
		os.Exit(1)
	}

	// 获取或创建 API 凭证
	var creds *types.ApiKeyCreds
	apiKey := os.Getenv("API_KEY")
	apiSecret := os.Getenv("API_SECRET")
	apiPassphrase := os.Getenv("API_PASSPHRASE")

	if apiKey != "" && apiSecret != "" && apiPassphrase != "" {
		// 使用现有的 API 凭证
		creds = &types.ApiKeyCreds{
			Key:       apiKey,
			Secret:    apiSecret,
			Passphrase: apiPassphrase,
		}
		fmt.Println("使用现有的 API 凭证")
	} else {
		// 创建或推导 API 密钥
		clobClient := client.NewClient(host, chainID, privateKey, nil)
		ctx := context.Background()
		fmt.Println("正在创建或推导 API 密钥...")
		creds, err = clobClient.CreateOrDeriveAPIKey(ctx, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "错误: 创建 API 密钥失败: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("✅ API 密钥已创建")
	}

	// 创建客户端
	clobClient := client.NewClient(host, chainID, privateKey, creds)

	// 构建查询参数
	params := &types.OpenOrderParams{}
	marketSlug := os.Getenv("MARKET_SLUG")
	if marketSlug != "" {
		params.Market = &marketSlug
	}
	tokenID := os.Getenv("TOKEN_ID")
	if tokenID != "" {
		params.AssetID = &tokenID
	}

	// 获取开放订单
	ctx := context.Background()
	fmt.Println("\n正在获取开放订单...")
	orders, err := clobClient.GetOpenOrders(ctx, params)
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: 获取开放订单失败: %v\n", err)
		os.Exit(1)
	}

	// 格式化输出
	jsonData, err := json.MarshalIndent(orders, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: 序列化数据失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n✅ 获取到 %d 个开放订单\n\n", len(orders))
	fmt.Println(string(jsonData))
}

