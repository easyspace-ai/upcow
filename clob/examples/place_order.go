//go:build ignore
// +build ignore

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/betbot/gobet/clob/client"
	"github.com/betbot/gobet/clob/signing"
	"github.com/betbot/gobet/clob/types"
)

// 示例：下单
// 使用方法：
//   export PRIVATE_KEY="your_private_key_hex"
//   export TOKEN_ID="token_id"  # 条件代币资产 ID
//   export PRICE="0.65"  # 订单价格（小数）
//   export SIZE="1.0"  # 订单数量（条件代币数量）
//   export SIDE="BUY"  # BUY 或 SELL
//   export ORDER_TYPE="GTC"  # 可选，GTC/FOK/FAK，默认 GTC
//   export TICK_SIZE="0.001"  # 可选，价格精度，默认 0.001
//   export NEG_RISK="false"  # 可选，是否为负风险市场，默认 false
//   export API_KEY="your_api_key"  # 可选，如果已创建过 API 密钥
//   export API_SECRET="your_api_secret"
//   export API_PASSPHRASE="your_api_passphrase"
//   export CHAIN_ID=137
//   export CLOB_API_URL="https://clob.polymarket.com"
//   go run place_order.go

func main() {
	// 从环境变量读取配置
	privateKeyHex := os.Getenv("PRIVATE_KEY")
	if privateKeyHex == "" {
		fmt.Fprintf(os.Stderr, "错误: 请设置 PRIVATE_KEY 环境变量\n")
		os.Exit(1)
	}

	tokenID := os.Getenv("TOKEN_ID")
	if tokenID == "" {
		fmt.Fprintf(os.Stderr, "错误: 请设置 TOKEN_ID 环境变量\n")
		os.Exit(1)
	}

	priceStr := os.Getenv("PRICE")
	if priceStr == "" {
		fmt.Fprintf(os.Stderr, "错误: 请设置 PRICE 环境变量\n")
		os.Exit(1)
	}
	price, err := strconv.ParseFloat(priceStr, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: PRICE 必须是数字: %v\n", err)
		os.Exit(1)
	}

	sizeStr := os.Getenv("SIZE")
	if sizeStr == "" {
		fmt.Fprintf(os.Stderr, "错误: 请设置 SIZE 环境变量\n")
		os.Exit(1)
	}
	size, err := strconv.ParseFloat(sizeStr, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: SIZE 必须是数字: %v\n", err)
		os.Exit(1)
	}

	sideStr := os.Getenv("SIDE")
	if sideStr == "" {
		fmt.Fprintf(os.Stderr, "错误: 请设置 SIDE 环境变量（BUY 或 SELL）\n")
		os.Exit(1)
	}
	var side types.Side
	if strings.ToUpper(sideStr) == "BUY" {
		side = types.SideBuy
	} else if strings.ToUpper(sideStr) == "SELL" {
		side = types.SideSell
	} else {
		fmt.Fprintf(os.Stderr, "错误: SIDE 必须是 BUY 或 SELL\n")
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

	// 获取地址
	address := signing.GetAddressFromPrivateKey(privateKey)
	fmt.Printf("钱包地址: %s\n", address.Hex())
	fmt.Printf("链 ID: %d\n", chainID)
	fmt.Printf("API 地址: %s\n\n", host)

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
		tempClient := client.NewClient(host, chainID, privateKey, nil)
		ctx := context.Background()
		fmt.Println("正在创建或推导 API 密钥...")
		creds, err = tempClient.CreateOrDeriveAPIKey(ctx, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "错误: 创建 API 密钥失败: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("✅ API 密钥已创建")
	}

	// 创建客户端
	clobClient := client.NewClient(host, chainID, privateKey, creds)

	// 解析订单类型
	orderTypeStr := os.Getenv("ORDER_TYPE")
	if orderTypeStr == "" {
		orderTypeStr = "GTC"
	}
	var orderType types.OrderType
	switch strings.ToUpper(orderTypeStr) {
	case "GTC":
		orderType = types.OrderTypeGTC
	case "FOK":
		orderType = types.OrderTypeFOK
	case "FAK":
		orderType = types.OrderTypeFAK
	case "GTD":
		orderType = types.OrderTypeGTD
	default:
		fmt.Fprintf(os.Stderr, "警告: 不支持的订单类型 %s，使用默认值 GTC\n", orderTypeStr)
		orderType = types.OrderTypeGTC
	}

	// 解析 tick size
	tickSizeStr := os.Getenv("TICK_SIZE")
	if tickSizeStr == "" {
		tickSizeStr = "0.001"
	}
	tickSize := types.TickSize(tickSizeStr)
	if tickSize != types.TickSize01 && tickSize != types.TickSize001 && 
		tickSize != types.TickSize0001 && tickSize != types.TickSize00001 {
		fmt.Fprintf(os.Stderr, "警告: 不支持的 tick size %s，使用默认值 0.001\n", tickSizeStr)
		tickSize = types.TickSize0001
	}

	// 解析 neg risk
	negRiskStr := os.Getenv("NEG_RISK")
	negRisk := false
	if negRiskStr != "" {
		negRisk, err = strconv.ParseBool(negRiskStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "警告: NEG_RISK 必须是 true 或 false，使用默认值 false\n")
			negRisk = false
		}
	}

	// 构建用户订单
	userOrder := &types.UserOrder{
		TokenID: tokenID,
		Price:   price,
		Size:    size,
		Side:    side,
	}

	// 构建订单选项
	options := &types.CreateOrderOptions{
		TickSize: tickSize,
		NegRisk:  &negRisk,
	}

	ctx := context.Background()

	// 打印订单信息
	fmt.Println("\n订单信息:")
	fmt.Printf("  Token ID: %s\n", tokenID)
	fmt.Printf("  价格: %.4f\n", price)
	fmt.Printf("  数量: %.2f\n", size)
	fmt.Printf("  方向: %s\n", side)
	fmt.Printf("  订单类型: %s\n", orderType)
	fmt.Printf("  Tick Size: %s\n", tickSize)
	fmt.Printf("  负风险: %v\n", negRisk)
	fmt.Println()

	// 创建签名订单
	fmt.Println("正在创建签名订单...")
	signedOrder, err := clobClient.CreateOrder(ctx, userOrder, options)
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: 创建订单失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("✅ 订单签名成功")
	fmt.Printf("  Salt: %d\n", signedOrder.Salt)
	fmt.Printf("  Maker: %s\n", signedOrder.Maker)
	fmt.Printf("  Signer: %s\n", signedOrder.Signer)
	fmt.Printf("  Maker Amount: %s\n", signedOrder.MakerAmount)
	fmt.Printf("  Taker Amount: %s\n", signedOrder.TakerAmount)
	fmt.Println()

	// 提交订单
	fmt.Println("正在提交订单...")
	orderResp, err := clobClient.PostOrder(ctx, signedOrder, orderType, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: 提交订单失败: %v\n", err)
		os.Exit(1)
	}

	if !orderResp.Success {
		fmt.Fprintf(os.Stderr, "错误: 订单提交失败: %s\n", orderResp.ErrorMsg)
		os.Exit(1)
	}

	// 输出结果
	fmt.Println("✅ 订单提交成功！")
	fmt.Println("\n订单响应:")
	jsonData, err := json.MarshalIndent(orderResp, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: 序列化数据失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(jsonData))

	if orderResp.OrderID != "" {
		fmt.Printf("\n订单 ID: %s\n", orderResp.OrderID)
		fmt.Println("\n提示: 可以使用以下命令查看订单状态：")
		fmt.Printf("  export ORDER_ID=%s\n", orderResp.OrderID)
		fmt.Println("  go run get_open_orders.go")
	}
}

