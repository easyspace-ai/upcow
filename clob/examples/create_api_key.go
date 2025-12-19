package main

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/betbot/gobet/clob/client"
	"github.com/betbot/gobet/clob/signing"
	"github.com/betbot/gobet/clob/types"
)

// 示例：创建或推导 API 密钥
// 使用方法：
//   export PRIVATE_KEY="your_private_key_hex"
//   export CHAIN_ID=137  # 137 for Polygon, 80002 for Amoy
//   export CLOB_API_URL="https://clob.polymarket.com"  # 可选，默认为生产环境
//   go run create_api_key.go

func main() {
	// 从环境变量读取配置
	privateKeyHex := os.Getenv("PRIVATE_KEY")
	if privateKeyHex == "" {
		fmt.Fprintf(os.Stderr, "错误: 请设置 PRIVATE_KEY 环境变量\n")
		os.Exit(1)
	}

	chainIDStr := os.Getenv("CHAIN_ID")
	if chainIDStr == "" {
		chainIDStr = "137" // 默认使用 Polygon
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

	// 创建客户端（不需要 API 凭证，因为我们要创建它们）
	clobClient := client.NewClient(host, chainID, privateKey, nil)

	// 创建或推导 API 密钥
	ctx := context.Background()
	fmt.Println("正在创建或推导 API 密钥...")
	creds, err := clobClient.CreateOrDeriveAPIKey(ctx, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: 创建 API 密钥失败: %v\n", err)
		os.Exit(1)
	}

	// 输出 API 凭证
	fmt.Println("\n✅ API 密钥创建成功！")
	fmt.Println("\n请保存以下凭证（不要泄露）：")
	fmt.Printf("API Key:      %s\n", creds.Key)
	fmt.Printf("Secret:       %s\n", creds.Secret)
	fmt.Printf("Passphrase:   %s\n", creds.Passphrase)
	fmt.Println("\n提示: 可以将这些凭证保存到环境变量或配置文件中，用于后续的 API 调用。")
}

