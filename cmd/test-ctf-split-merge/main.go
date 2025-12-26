package main

import (
	"bufio"
	"context"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/betbot/gobet/clob/client"
	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/pkg/marketspec"
	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// EnvConfig 从 .env 文件读取的配置
type EnvConfig struct {
	PrivateKey string
	RPCURL     string
	Amount     string
	ChainID    string
	SkipSplit  string
	SkipMerge  string
}

// 测试 Demo: CTF 拆分与合并操作
// 功能：
//   1. 自动获取当前 BTC 15 分钟市场
//   2. 通过 Gamma API 获取市场信息和 conditionId
//   3. 执行 split 操作（USDC -> YES + NO）
//   4. 执行 merge 操作（YES + NO -> USDC）
//
// 使用方法：
//   1. 在当前目录（cmd/test-ctf-split-merge/）创建 .env 文件，包含以下配置：
//      PRIVATE_KEY=0x...           # 私钥（必需）
//      RPC_URL=https://polygon-rpc.com  # RPC节点URL（可选，默认根据链ID自动选择）
//      AMOUNT=1.0                   # 要拆分/合并的USDC数量（默认 1.0）
//      CHAIN_ID=137                 # Polygon主网（默认 137）
//      SKIP_SPLIT=false             # 是否跳过拆分操作（默认 false）
//      SKIP_MERGE=false             # 是否跳过合并操作（默认 false）
//   2. 运行：go run cmd/test-ctf-split-merge/main.go

func main() {
	fmt.Println("╔══════════════════════════════════════════════════════════════════════════╗")
	fmt.Println("║   CTF 拆分与合并测试 Demo                                                 ║")
	fmt.Println("╚══════════════════════════════════════════════════════════════════════════╝")
	fmt.Println()

	// 从 .env 文件读取配置
	envConfig, err := loadEnvConfig()
	if err != nil {
		fmt.Printf("错误: 加载 .env 配置失败: %v\n", err)
		fmt.Println("提示: 请在当前目录创建 .env 文件，包含 PRIVATE_KEY 等配置")
		os.Exit(1)
	}

	// 解析金额
	amountStr := envConfig.Amount
	if amountStr == "" {
		amountStr = "1.0"
	}
	var amount float64
	if _, err := fmt.Sscanf(amountStr, "%f", &amount); err != nil {
		fmt.Printf("错误: 无效的金额 %s: %v\n", amountStr, err)
		os.Exit(1)
	}

	// 解析链ID
	chainIDStr := envConfig.ChainID
	if chainIDStr == "" {
		chainIDStr = "137" // 默认Polygon主网
	}
	var chainIDInt int64
	if _, err := fmt.Sscanf(chainIDStr, "%d", &chainIDInt); err != nil {
		fmt.Printf("错误: 无效的链ID %s: %v\n", chainIDStr, err)
		os.Exit(1)
	}
	chainID := types.Chain(chainIDInt)

	skipSplit := envConfig.SkipSplit == "true"
	skipMerge := envConfig.SkipMerge == "true"

	// 检查私钥
	if envConfig.PrivateKey == "" {
		fmt.Println("错误: .env 文件中缺少 PRIVATE_KEY")
		os.Exit(1)
	}

	// 解析私钥
	privateKey, err := crypto.HexToECDSA(envConfig.PrivateKey)
	if err != nil {
		fmt.Printf("错误: 解析私钥失败: %v\n", err)
		os.Exit(1)
	}

	// 获取账户地址
	address := crypto.PubkeyToAddress(privateKey.PublicKey)
	fmt.Printf("账户地址: %s\n", address.Hex())

	// 获取RPC URL
	rpcURL := envConfig.RPCURL
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
	fmt.Printf("链ID: %d\n", chainID)
	fmt.Println()

	ctx := context.Background()

	// ===== 步骤 1: 获取下一个 BTC 15 分钟市场 =====
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("步骤 1: 获取下一个 BTC 15 分钟市场")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	// 创建市场规格（BTC 15分钟）
	marketSpec, err := marketspec.New("btc", "15m", "updown")
	if err != nil {
		fmt.Printf("错误: 创建市场规格失败: %v\n", err)
		os.Exit(1)
	}

	// 设置 slug 模板（与 config.yaml 保持一致）
	marketSpec.SlugTemplates = map[string]string{
		"15m": "{symbol}-{kind}-{timeframe}-{timestamp}",
	}

	// 获取当前周期的开始时间戳，然后计算下一个周期
	now := time.Now()
	currentPeriodStartUnix := marketSpec.CurrentPeriodStartUnix(now)
	nextPeriodStartUnix := marketSpec.NextPeriodStartUnix(currentPeriodStartUnix)
	slug := marketSpec.Slug(nextPeriodStartUnix)

	fmt.Printf("当前时间: %s\n", now.Format(time.RFC3339))
	fmt.Printf("当前周期开始时间戳: %d\n", currentPeriodStartUnix)
	fmt.Printf("下一个周期开始时间戳: %d\n", nextPeriodStartUnix)
	fmt.Printf("市场 Slug: %s\n", slug)
	fmt.Println()

	// ===== 步骤 2: 通过 Gamma API 获取市场信息 =====
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("步骤 2: 通过 Gamma API 获取市场信息")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	gammaMarket, err := client.FetchMarketFromGamma(ctx, slug)
	if err != nil {
		fmt.Printf("错误: 获取市场信息失败: %v\n", err)
		fmt.Printf("提示: 市场可能尚未创建，请稍后再试\n")
		os.Exit(1)
	}

	fmt.Printf("市场 ID: %s\n", gammaMarket.ID)
	fmt.Printf("问题: %s\n", gammaMarket.Question)
	fmt.Printf("Condition ID: %s\n", gammaMarket.ConditionID)
	fmt.Printf("Slug: %s\n", gammaMarket.Slug)
	fmt.Printf("结束时间: %s\n", gammaMarket.EndDate)
	fmt.Println()

	if gammaMarket.ConditionID == "" {
		fmt.Println("错误: 市场没有 conditionId")
		os.Exit(1)
	}

	// ===== 步骤 3: 创建 CTF 客户端 =====
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("步骤 3: 创建 CTF 客户端")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	ctfClient, err := client.NewCTFClient(rpcURL, chainID, privateKey)
	if err != nil {
		fmt.Printf("错误: 创建CTF客户端失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("CTF 合约地址: %s\n", ctfClient.GetCTFAddress().Hex())
	fmt.Printf("抵押品代币地址: %s\n", ctfClient.GetCollateralToken().Hex())
	fmt.Println()

	// ===== 步骤 4: 检查余额和授权 =====
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("步骤 4: 检查余额和授权")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	usdcBalance, err := ctfClient.GetUSDCBalance(ctx)
	if err != nil {
		fmt.Printf("警告: 检查USDC余额失败: %v\n", err)
	} else {
		fmt.Printf("USDC余额: %.6f USDC\n", usdcBalance)
	}

	usdcAllowance, err := ctfClient.CheckUSDCAllowance(ctx)
	if err != nil {
		fmt.Printf("警告: 检查USDC授权失败: %v\n", err)
	} else {
		fmt.Printf("USDC授权: %.6f USDC\n", usdcAllowance)
	}

	// 检查 YES 和 NO 代币余额
	conditionIdHash := common.HexToHash(gammaMarket.ConditionID)
	parentCollectionId := common.Hash{}

	var yesBalance, noBalance float64
	if yesCollectionId, err := ctfClient.GetCollectionId(parentCollectionId, conditionIdHash, big.NewInt(1)); err == nil {
		if yesPositionId, err := ctfClient.GetPositionId(ctfClient.GetCollateralToken(), yesCollectionId); err == nil {
			if balance, err := ctfClient.GetConditionalTokenBalance(ctx, yesPositionId); err == nil {
				yesBalance = balance
				fmt.Printf("YES代币余额: %.6f\n", yesBalance)
			}
		}
	}
	if noCollectionId, err := ctfClient.GetCollectionId(parentCollectionId, conditionIdHash, big.NewInt(2)); err == nil {
		if noPositionId, err := ctfClient.GetPositionId(ctfClient.GetCollateralToken(), noCollectionId); err == nil {
			if balance, err := ctfClient.GetConditionalTokenBalance(ctx, noPositionId); err == nil {
				noBalance = balance
				fmt.Printf("NO代币余额: %.6f\n", noBalance)
			}
		}
	}
	fmt.Println()

	// ===== 步骤 5: 执行 Split 操作（USDC -> YES + NO）=====
	if !skipSplit {
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		fmt.Println("步骤 5: 执行 Split 操作（USDC -> YES + NO）")
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

		fmt.Printf("准备拆分: %.6f USDC -> %.6f YES + %.6f NO\n", amount, amount, amount)

		// 创建拆分交易
		splitParams := client.SplitPositionParams{
			ConditionId: gammaMarket.ConditionID,
			Amount:      amount,
		}

		fmt.Println("\n创建拆分交易...")
		splitTx, err := ctfClient.SplitPosition(ctx, splitParams)
		if err != nil {
			fmt.Printf("错误: 创建拆分交易失败: %v\n", err)
			fmt.Println("提示: 请检查 USDC 余额和授权是否足够")
			os.Exit(1)
		}

		fmt.Printf("交易已创建: %s\n", splitTx.Hash().Hex())
		fmt.Printf("Gas Limit: %d\n", splitTx.Gas())
		fmt.Printf("Gas Price: %s wei\n", splitTx.GasPrice().String())

		// 询问是否发送
		fmt.Print("\n是否发送拆分交易? (y/n): ")
		var confirm string
		fmt.Scanln(&confirm)
		if confirm != "y" && confirm != "Y" {
			fmt.Println("已取消拆分操作")
		} else {
			// 发送交易
			fmt.Println("\n发送交易...")
			splitTxHash, err := ctfClient.SendTransaction(ctx, splitTx)
			if err != nil {
				fmt.Printf("错误: 发送交易失败: %v\n", err)
				os.Exit(1)
			}

			fmt.Printf("交易已发送: %s\n", splitTxHash.Hex())
			fmt.Println("等待确认...")

			// 等待交易确认（轮询）
			var receipt *ethtypes.Receipt
			maxAttempts := 60 // 最多等待 60 次（约 5 分钟）
			for i := 0; i < maxAttempts; i++ {
				var err error
				receipt, err = ctfClient.WaitForTransaction(ctx, splitTxHash)
				if err == nil && receipt != nil {
					break
				}
				if i < maxAttempts-1 {
					time.Sleep(5 * time.Second)
					fmt.Printf("等待中... (%d/%d)\n", i+1, maxAttempts)
				}
			}

			if receipt == nil {
				fmt.Printf("错误: 等待交易确认超时\n")
				fmt.Printf("交易哈希: %s\n", splitTxHash.Hex())
				fmt.Println("请稍后手动检查交易状态")
				os.Exit(1)
			}

			if receipt.Status == 1 {
				fmt.Printf("\n✓ 拆分交易成功确认!\n")
				fmt.Printf("  区块号: %d\n", receipt.BlockNumber.Uint64())
				fmt.Printf("  Gas使用: %d\n", receipt.GasUsed)
				fmt.Printf("  交易哈希: %s\n", splitTxHash.Hex())
				fmt.Printf("\n您现在拥有 %.6f YES 和 %.6f NO 代币\n", amount, amount)
			} else {
				fmt.Printf("\n✗ 拆分交易失败\n")
				fmt.Printf("  交易哈希: %s\n", splitTxHash.Hex())
				os.Exit(1)
			}
		}
		fmt.Println()
	} else {
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		fmt.Println("步骤 5: 跳过 Split 操作（SKIP_SPLIT=true）")
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		fmt.Println()
	}

	// ===== 步骤 6: 执行 Merge 操作（YES + NO -> USDC）=====
	if !skipMerge {
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		fmt.Println("步骤 6: 执行 Merge 操作（YES + NO -> USDC）")
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

		// 重新检查 YES 和 NO 余额
		if yesCollectionId, err := ctfClient.GetCollectionId(parentCollectionId, conditionIdHash, big.NewInt(1)); err == nil {
			if yesPositionId, err := ctfClient.GetPositionId(ctfClient.GetCollateralToken(), yesCollectionId); err == nil {
				if balance, err := ctfClient.GetConditionalTokenBalance(ctx, yesPositionId); err == nil {
					yesBalance = balance
				}
			}
		}
		if noCollectionId, err := ctfClient.GetCollectionId(parentCollectionId, conditionIdHash, big.NewInt(2)); err == nil {
			if noPositionId, err := ctfClient.GetPositionId(ctfClient.GetCollateralToken(), noCollectionId); err == nil {
				if balance, err := ctfClient.GetConditionalTokenBalance(ctx, noPositionId); err == nil {
					noBalance = balance
				}
			}
		}

		fmt.Printf("当前 YES余额: %.6f\n", yesBalance)
		fmt.Printf("当前 NO余额: %.6f\n", noBalance)

		// 确定可合并的数量（取两者最小值）
		mergeAmount := amount
		if yesBalance < mergeAmount {
			mergeAmount = yesBalance
		}
		if noBalance < mergeAmount {
			mergeAmount = noBalance
		}

		if mergeAmount <= 0 {
			fmt.Println("错误: 没有足够的 YES 和 NO 代币进行合并")
			fmt.Println("提示: 请先执行 split 操作或购买代币")
			os.Exit(1)
		}

		fmt.Printf("\n准备合并: %.6f YES + %.6f NO -> %.6f USDC\n", mergeAmount, mergeAmount, mergeAmount)

		// 创建合并交易
		mergeParams := client.MergePositionsParams{
			ConditionId: gammaMarket.ConditionID,
			Amount:      mergeAmount,
		}

		fmt.Println("\n创建合并交易...")
		mergeTx, err := ctfClient.MergePositions(ctx, mergeParams)
		if err != nil {
			fmt.Printf("错误: 创建合并交易失败: %v\n", err)
			fmt.Println("提示: 请检查 YES 和 NO 代币余额是否足够")
			os.Exit(1)
		}

		fmt.Printf("交易已创建: %s\n", mergeTx.Hash().Hex())
		fmt.Printf("Gas Limit: %d\n", mergeTx.Gas())
		fmt.Printf("Gas Price: %s wei\n", mergeTx.GasPrice().String())

		// 询问是否发送
		fmt.Print("\n是否发送合并交易? (y/n): ")
		var confirm string
		fmt.Scanln(&confirm)
		if confirm != "y" && confirm != "Y" {
			fmt.Println("已取消合并操作")
		} else {
			// 发送交易
			fmt.Println("\n发送交易...")
			mergeTxHash, err := ctfClient.SendTransaction(ctx, mergeTx)
			if err != nil {
				fmt.Printf("错误: 发送交易失败: %v\n", err)
				os.Exit(1)
			}

			fmt.Printf("交易已发送: %s\n", mergeTxHash.Hex())
			fmt.Println("等待确认...")

			// 等待交易确认（轮询）
			var receipt *ethtypes.Receipt
			maxAttempts := 60 // 最多等待 60 次（约 5 分钟）
			for i := 0; i < maxAttempts; i++ {
				var err error
				receipt, err = ctfClient.WaitForTransaction(ctx, mergeTxHash)
				if err == nil && receipt != nil {
					break
				}
				if i < maxAttempts-1 {
					time.Sleep(5 * time.Second)
					fmt.Printf("等待中... (%d/%d)\n", i+1, maxAttempts)
				}
			}

			if receipt == nil {
				fmt.Printf("错误: 等待交易确认超时\n")
				fmt.Printf("交易哈希: %s\n", mergeTxHash.Hex())
				fmt.Println("请稍后手动检查交易状态")
				os.Exit(1)
			}

			if receipt.Status == 1 {
				fmt.Printf("\n✓ 合并交易成功确认!\n")
				fmt.Printf("  区块号: %d\n", receipt.BlockNumber.Uint64())
				fmt.Printf("  Gas使用: %d\n", receipt.GasUsed)
				fmt.Printf("  交易哈希: %s\n", mergeTxHash.Hex())
				fmt.Printf("\n您已获得 %.6f USDC\n", mergeAmount)
			} else {
				fmt.Printf("\n✗ 合并交易失败\n")
				fmt.Printf("  交易哈希: %s\n", mergeTxHash.Hex())
				os.Exit(1)
			}
		}
		fmt.Println()
	} else {
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		fmt.Println("步骤 6: 跳过 Merge 操作（SKIP_MERGE=true）")
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		fmt.Println()
	}

	// ===== 最终状态 =====
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("最终状态")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	finalUSDCBalance, _ := ctfClient.GetUSDCBalance(ctx)
	fmt.Printf("USDC余额: %.6f USDC\n", finalUSDCBalance)

	if yesCollectionId, err := ctfClient.GetCollectionId(parentCollectionId, conditionIdHash, big.NewInt(1)); err == nil {
		if yesPositionId, err := ctfClient.GetPositionId(ctfClient.GetCollateralToken(), yesCollectionId); err == nil {
			if balance, err := ctfClient.GetConditionalTokenBalance(ctx, yesPositionId); err == nil {
				fmt.Printf("YES代币余额: %.6f\n", balance)
			}
		}
	}
	if noCollectionId, err := ctfClient.GetCollectionId(parentCollectionId, conditionIdHash, big.NewInt(2)); err == nil {
		if noPositionId, err := ctfClient.GetPositionId(ctfClient.GetCollateralToken(), noCollectionId); err == nil {
			if balance, err := ctfClient.GetConditionalTokenBalance(ctx, noPositionId); err == nil {
				fmt.Printf("NO代币余额: %.6f\n", balance)
			}
		}
	}

	fmt.Println("\n✓ 测试完成!")
}

// loadEnvConfig 从当前目录的 .env 文件加载配置
func loadEnvConfig() (*EnvConfig, error) {
	// 获取当前工作目录
	wd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("获取当前工作目录失败: %w", err)
	}

	// 尝试从当前工作目录读取 .env 文件
	envPath := filepath.Join(wd, ".env")

	// 如果当前目录没有，尝试从可执行文件所在目录读取
	if _, err := os.Stat(envPath); os.IsNotExist(err) {
		if execPath, err := os.Executable(); err == nil {
			execDir := filepath.Dir(execPath)
			envPath = filepath.Join(execDir, ".env")
		}
	}

	config := &EnvConfig{}

	// 如果文件不存在，返回错误
	if _, err := os.Stat(envPath); os.IsNotExist(err) {
		return config, fmt.Errorf(".env 文件不存在: %s (请在当前目录创建 .env 文件)", envPath)
	}

	file, err := os.Open(envPath)
	if err != nil {
		return nil, fmt.Errorf("打开 .env 文件失败: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// 跳过空行和注释
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// 解析 KEY=VALUE 格式
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		// 移除引号（如果存在）
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') ||
			(value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}

		switch key {
		case "PRIVATE_KEY":
			config.PrivateKey = value
		case "RPC_URL":
			config.RPCURL = value
		case "AMOUNT":
			config.Amount = value
		case "CHAIN_ID":
			config.ChainID = value
		case "SKIP_SPLIT":
			config.SkipSplit = value
		case "SKIP_MERGE":
			config.SkipMerge = value
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取 .env 文件失败: %w", err)
	}

	return config, nil
}
