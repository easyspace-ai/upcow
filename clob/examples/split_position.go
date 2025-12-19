package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/betbot/gobet/clob/client"
	"github.com/betbot/gobet/clob/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// UserJSON 用户配置文件结构
type UserJSON struct {
	PrivateKey string `json:"private_key"`
	RPCURL     string `json:"rpc_url"`
}

// 示例：拆分USDC为完整的仓位集合（1 YES + 1 NO）
// 使用方法：
//   1. 创建 data/user.json 文件，包含 private_key 和 rpc_url
//   2. 设置环境变量：
//      export CONDITION_ID="0x..."  # 市场的conditionId
//      export AMOUNT="1.0"         # 要拆分的USDC数量
//      export CHAIN_ID=137          # Polygon主网
//      export RPC_URL="https://polygon-rpc.com"  # RPC节点URL（可选，会从user.json读取）
//   3. 运行：go run split_position.go
//
// 注意：
//   - 需要确保账户有足够的USDC余额
//   - 需要确保USDC已授权给CTF合约
//   - 拆分后，您将获得等量的YES和NO代币

func main() {
	// 从环境变量获取参数
	conditionId := os.Getenv("CONDITION_ID")
	if conditionId == "" {
		fmt.Println("错误: 请设置 CONDITION_ID 环境变量")
		os.Exit(1)
	}

	amountStr := os.Getenv("AMOUNT")
	if amountStr == "" {
		amountStr = "1.0"
	}
	var amount float64
	if _, err := fmt.Sscanf(amountStr, "%f", &amount); err != nil {
		fmt.Printf("错误: 无效的金额 %s: %v\n", amountStr, err)
		os.Exit(1)
	}

	chainIDStr := os.Getenv("CHAIN_ID")
	if chainIDStr == "" {
		chainIDStr = "137" // 默认Polygon主网
	}
	var chainIDInt int64
	if _, err := fmt.Sscanf(chainIDStr, "%d", &chainIDInt); err != nil {
		fmt.Printf("错误: 无效的链ID %s: %v\n", chainIDStr, err)
		os.Exit(1)
	}
	chainID := types.Chain(chainIDInt)

	// 读取用户配置
	userJSON, err := loadUserConfig()
	if err != nil {
		fmt.Printf("错误: 加载用户配置失败: %v\n", err)
		fmt.Println("提示: 请创建 data/user.json 文件，包含 private_key 和 rpc_url")
		os.Exit(1)
	}

	// 解析私钥
	privateKey, err := crypto.HexToECDSA(userJSON.PrivateKey)
	if err != nil {
		fmt.Printf("错误: 解析私钥失败: %v\n", err)
		os.Exit(1)
	}

	// 获取账户地址
	address := crypto.PubkeyToAddress(privateKey.PublicKey)
	fmt.Printf("账户地址: %s\n", address.Hex())

	// 获取RPC URL（优先使用环境变量）
	rpcURL := os.Getenv("RPC_URL")
	if rpcURL == "" {
		rpcURL = userJSON.RPCURL
	}
	if rpcURL == "" {
		// 默认RPC节点
		if chainID == types.ChainPolygon {
			rpcURL = "https://polygon-rpc.com"
		} else if chainID == types.ChainAmoy {
			rpcURL = "https://rpc-amoy.polygon.technology"
		} else {
			fmt.Printf("错误: 未指定RPC URL，且链ID %d 没有默认RPC\n", chainID)
			os.Exit(1)
		}
	}
	fmt.Printf("RPC节点: %s\n", rpcURL)

	// 创建CTF客户端
	ctfClient, err := client.NewCTFClient(rpcURL, chainID, privateKey)
	if err != nil {
		fmt.Printf("错误: 创建CTF客户端失败: %v\n", err)
		os.Exit(1)
	}

	// 检查余额和授权
	ctx := context.Background()
	fmt.Println("\n检查余额和授权...")
	balance, err := ctfClient.GetUSDCBalance(ctx)
	if err != nil {
		fmt.Printf("警告: 检查USDC余额失败: %v\n", err)
	} else {
		fmt.Printf("  USDC余额: %.6f USDC\n", balance)
	}

	allowance, err := ctfClient.CheckUSDCAllowance(ctx)
	if err != nil {
		fmt.Printf("警告: 检查USDC授权失败: %v\n", err)
	} else {
		fmt.Printf("  USDC授权: %.6f USDC\n", allowance)
	}

	fmt.Printf("\n准备拆分仓位:\n")
	fmt.Printf("  ConditionId: %s\n", conditionId)
	fmt.Printf("  金额: %.6f USDC\n", amount)
	fmt.Printf("  结果: %.6f YES + %.6f NO\n", amount, amount)

	// 创建拆分交易
	params := client.SplitPositionParams{
		ConditionId: conditionId,
		Amount:      amount,
	}

	fmt.Println("\n创建拆分交易...")
	tx, err := ctfClient.SplitPosition(ctx, params)
	if err != nil {
		fmt.Printf("错误: 创建拆分交易失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("交易已创建: %s\n", tx.Hash().Hex())
	fmt.Printf("Gas Limit: %d\n", tx.Gas())
	fmt.Printf("Gas Price: %s wei\n", tx.GasPrice().String())

	// 询问是否发送
	fmt.Print("\n是否发送交易? (y/n): ")
	var confirm string
	fmt.Scanln(&confirm)
	if confirm != "y" && confirm != "Y" {
		fmt.Println("已取消")
		os.Exit(0)
	}

	// 发送交易
	fmt.Println("\n发送交易...")
	txHash, err := ctfClient.SendTransaction(ctx, tx)
	if err != nil {
		fmt.Printf("错误: 发送交易失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("交易已发送: %s\n", txHash.Hex())
	fmt.Println("等待确认...")

	// 等待交易确认
	receipt, err := ctfClient.WaitForTransaction(ctx, txHash)
	if err != nil {
		fmt.Printf("错误: 等待交易确认失败: %v\n", err)
		fmt.Printf("交易哈希: %s\n", txHash.Hex())
		fmt.Println("请稍后手动检查交易状态")
		os.Exit(1)
	}

	if receipt.Status == 1 {
		fmt.Printf("\n✓ 交易成功确认!\n")
		fmt.Printf("  区块号: %d\n", receipt.BlockNumber.Uint64())
		fmt.Printf("  Gas使用: %d\n", receipt.GasUsed)
		fmt.Printf("  交易哈希: %s\n", txHash.Hex())
		fmt.Printf("\n您现在拥有 %.6f YES 和 %.6f NO 代币\n", amount, amount)
	} else {
		fmt.Printf("\n✗ 交易失败\n")
		fmt.Printf("  交易哈希: %s\n", txHash.Hex())
		os.Exit(1)
	}
}

// loadUserConfig 加载用户配置
func loadUserConfig() (*UserJSON, error) {
	configPath := "data/user.json"
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("配置文件不存在: %s", configPath)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	var config UserJSON
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}

	if config.PrivateKey == "" {
		return nil, fmt.Errorf("配置文件缺少 private_key")
	}

	return &config, nil
}

