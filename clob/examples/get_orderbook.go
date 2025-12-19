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

// 示例：获取订单簿
// 使用方法：
//   export TOKEN_ID="your_token_id"  # 必需
//   export SIDE="BUY"  # 可选，BUY 或 SELL
//   export PRIVATE_KEY="your_private_key_hex"  # 可选，用于认证
//   export CHAIN_ID=137
//   export CLOB_API_URL="https://clob.polymarket.com"
//   go run get_orderbook.go

func main() {
	// 从环境变量读取配置
	tokenID := os.Getenv("TOKEN_ID")
	if tokenID == "" {
		fmt.Fprintf(os.Stderr, "错误: 请设置 TOKEN_ID 环境变量\n")
		fmt.Fprintf(os.Stderr, "提示: 可以从 Gamma API 获取市场的 clobTokenIds\n")
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

	// 创建客户端（获取订单簿不需要认证）
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
		// 创建一个临时私钥用于初始化
		privateKey, _ := signing.PrivateKeyFromHex("0000000000000000000000000000000000000000000000000000000000000001")
		clobClient = client.NewClient(host, chainID, privateKey, nil)
	}

	// 解析 side（可选）
	var sidePtr *types.Side
	sideStr := os.Getenv("SIDE")
	if sideStr != "" {
		side := types.Side(sideStr)
		if side != types.SideBuy && side != types.SideSell {
			fmt.Fprintf(os.Stderr, "错误: SIDE 必须是 BUY 或 SELL\n")
			os.Exit(1)
		}
		sidePtr = &side
	}

	// 获取订单簿
	ctx := context.Background()
	fmt.Printf("正在获取订单簿: Token ID = %s", tokenID)
	if sidePtr != nil {
		fmt.Printf(", Side = %s", *sidePtr)
	}
	fmt.Println()

	orderbook, err := clobClient.GetOrderBook(ctx, tokenID, sidePtr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: 获取订单簿失败: %v\n", err)
		os.Exit(1)
	}

	// 格式化输出
	jsonData, err := json.MarshalIndent(orderbook, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: 序列化数据失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("\n✅ 订单簿获取成功！\n")
	fmt.Println(string(jsonData))
}

