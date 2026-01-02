package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/betbot/gobet/clob/client"
	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/pkg/marketspec"
	"github.com/betbot/gobet/pkg/sdk/api"
	"github.com/betbot/gobet/pkg/sdk/relayer"
	relayertypes "github.com/betbot/gobet/pkg/sdk/relayer/types"
	sdktypes "github.com/betbot/gobet/pkg/sdk/types"
	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/joho/godotenv"
)

// EnvConfig 从 .env 文件读取的配置
type EnvConfig struct {
	PrivateKey        string
	RPCURL            string
	Amount            string
	ChainID           string
	ProxyAddress      string
	BuilderAPIKey     string
	BuilderSecret     string
	BuilderPassPhrase string
}

// 测试 Demo: CTF 合并操作
// 功能：
//   1. 自动获取当前 BTC 15 分钟市场
//   2. 通过 Gamma API 获取市场信息和 conditionId
//   3. 执行 merge 操作（YES + NO -> USDC）
//
// 使用方法：
//   1. 在当前目录（cmd/test-ctf-merge/）创建 .env 文件，包含以下配置：
//      PRIVATE_KEY=0x...           # 私钥（必需）
//      RPC_URL=https://polygon-rpc.com  # RPC节点URL（可选，默认根据链ID自动选择）
//      AMOUNT=1.0                   # 要合并的USDC数量（默认 1.0）
//      CHAIN_ID=137                 # Polygon主网（默认 137）
//   2. 运行：go run cmd/test-ctf-merge/main.go

func main() {
	fmt.Println("╔══════════════════════════════════════════════════════════════════════════╗")
	fmt.Println("║   CTF 合并测试 Demo                                                      ║")
	fmt.Println("╚══════════════════════════════════════════════════════════════════════════╝")
	fmt.Println()

	// 加载 .env 文件（使用绝对路径）
	envPath := "/pm/data/.env"
	if err := godotenv.Load(envPath); err != nil {
		// .env 文件不存在也没关系，使用环境变量
		fmt.Printf("提示: 未找到 .env 文件 (%s)，将从环境变量读取配置\n", envPath)
	} else {
		fmt.Printf("✓ 已加载配置文件: %s\n", envPath)
	}

	// 从 .env 文件和环境变量读取配置
	envConfig, err := loadEnvConfig()
	if err != nil {
		fmt.Printf("错误: 加载配置失败: %v\n", err)
		fmt.Println("提示: 请创建 .env 文件或设置环境变量")
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

	// 获取账户地址（用于交易）
	address := crypto.PubkeyToAddress(privateKey.PublicKey)
	fmt.Printf("交易账户地址: %s\n", address.Hex())

	// 获取代理地址（用于查询余额）
	var proxyAddress common.Address
	if envConfig.ProxyAddress != "" {
		proxyAddress = common.HexToAddress(envConfig.ProxyAddress)
		fmt.Printf("代理地址（余额查询）: %s\n", proxyAddress.Hex())
	} else {
		// 如果没有配置代理地址，使用交易账户地址
		proxyAddress = address
		fmt.Printf("未配置代理地址，使用交易账户地址查询余额\n")
	}
	fmt.Println()

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

	// ===== 步骤 1: 获取当前 BTC 15 分钟市场 =====
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("步骤 1: 获取当前 BTC 15 分钟市场")
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

	// 获取当前周期的开始时间戳
	now := time.Now()
	currentPeriodStartUnix := marketSpec.CurrentPeriodStartUnix(now)
	slug := marketSpec.Slug(currentPeriodStartUnix)

	fmt.Printf("当前时间: %s\n", now.Format(time.RFC3339))
	fmt.Printf("当前周期开始时间戳: %d\n", currentPeriodStartUnix)
	fmt.Printf("市场 Slug: %s\n", slug)
	fmt.Println()

	// ===== 步骤 2: 通过 Gamma API 获取市场信息 =====
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("步骤 2: 通过 Gamma API 获取市场信息")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	slug = "xrp-updown-15m-1767199500"

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

	// ===== 步骤 4: 检查余额 =====
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("步骤 4: 检查余额")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	// 使用代理地址查询余额
	usdcBalance, err := ctfClient.GetUSDCBalanceForAddress(ctx, proxyAddress)
	if err != nil {
		fmt.Printf("警告: 检查USDC余额失败: %v\n", err)
	} else {
		fmt.Printf("USDC余额（地址 %s）: %.6f USDC\n", proxyAddress.Hex(), usdcBalance)
	}

	// 检查 YES 和 NO 代币余额
	conditionIdHash := common.HexToHash(gammaMarket.ConditionID)
	parentCollectionId := common.Hash{}

	var yesBalance, noBalance float64
	if yesCollectionId, err := ctfClient.GetCollectionId(parentCollectionId, conditionIdHash, big.NewInt(1)); err == nil {
		if yesPositionId, err := ctfClient.GetPositionId(ctfClient.GetCollateralToken(), yesCollectionId); err == nil {
			if balance, err := ctfClient.GetConditionalTokenBalanceForAddress(ctx, proxyAddress, yesPositionId); err == nil {
				yesBalance = balance
				fmt.Printf("YES代币余额（地址 %s）: %.6f\n", proxyAddress.Hex(), yesBalance)
			}
		}
	}
	if noCollectionId, err := ctfClient.GetCollectionId(parentCollectionId, conditionIdHash, big.NewInt(2)); err == nil {
		if noPositionId, err := ctfClient.GetPositionId(ctfClient.GetCollateralToken(), noCollectionId); err == nil {
			if balance, err := ctfClient.GetConditionalTokenBalanceForAddress(ctx, proxyAddress, noPositionId); err == nil {
				noBalance = balance
				fmt.Printf("NO代币余额（地址 %s）: %.6f\n", proxyAddress.Hex(), noBalance)
			}
		}
	}
	fmt.Println()

	// ===== 步骤 5: 执行 Merge 操作（YES + NO -> USDC）=====
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("步骤 5: 执行 Merge 操作（YES + NO -> USDC）")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

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

	// 检查是否使用 relayer（需要代理地址和 Builder API 凭证）
	// Builder API 凭证从环境变量读取（和 redeem 一样）
	builderAPIKey := envConfig.BuilderAPIKey
	if builderAPIKey == "" {
		builderAPIKey = os.Getenv("BUILDER_API_KEY")
	}
	builderSecret := envConfig.BuilderSecret
	if builderSecret == "" {
		builderSecret = os.Getenv("BUILDER_SECRET")
	}
	builderPassPhrase := envConfig.BuilderPassPhrase
	if builderPassPhrase == "" {
		builderPassPhrase = os.Getenv("BUILDER_PASS_PHRASE")
	}

	useRelayer := envConfig.ProxyAddress != "" &&
		builderAPIKey != "" &&
		builderSecret != "" &&
		builderPassPhrase != ""

	if useRelayer {
		// 使用 Relayer 执行交易（通过代理钱包）
		fmt.Println("\n使用 Relayer 通过代理钱包执行合并交易（gasless）...")

		// 转换金额为6位小数精度
		mergeAmountBigInt := new(big.Int)
		mergeAmountFloat := new(big.Float).SetFloat64(mergeAmount)
		decimals := new(big.Float).SetInt64(1000000) // 10^6
		mergeAmountFloat.Mul(mergeAmountFloat, decimals)
		mergeAmountBigInt, _ = mergeAmountFloat.Int(nil)

		// 构建 merge 交易
		conditionIdHash := common.HexToHash(gammaMarket.ConditionID)
		apiTx, err := api.BuildMergeTransaction(conditionIdHash, mergeAmountBigInt)
		if err != nil {
			fmt.Printf("错误: 构建合并交易失败: %v\n", err)
			os.Exit(1)
		}

		// 转换为 relayer 交易格式
		relayerTx := relayertypes.SafeTransaction{
			To:        apiTx.To.Hex(),
			Operation: relayertypes.OperationType(apiTx.Operation),
			Data:      "0x" + hex.EncodeToString(apiTx.Data),
			Value:     apiTx.Value.String(),
		}

		// 创建签名函数
		signFn := func(signer string, digest []byte) ([]byte, error) {
			sig, err := crypto.Sign(digest, privateKey)
			if err != nil {
				return nil, err
			}
			// Adjust v value for Ethereum (add 27)
			if sig[64] < 27 {
				sig[64] += 27
			}
			return sig, nil
		}

		// 创建 relayer 客户端
		relayerURL := "https://relayer-v2.polymarket.com"
		builderCreds := &sdktypes.BuilderApiKeyCreds{
			Key:        builderAPIKey,
			Secret:     builderSecret,
			Passphrase: builderPassPhrase,
		}
		chainIDBigInt := big.NewInt(int64(chainID))
		relayerClient := relayer.NewClient(relayerURL, chainIDBigInt, signFn, builderCreds)

		// 创建 auth option
		authOption := &sdktypes.AuthOption{
			SingerAddress: address.Hex(),
			FunderAddress: proxyAddress.Hex(),
		}

		// 通过 Relayer 执行交易（默认使用，无需确认）
		fmt.Println("\n通过 Relayer 发送交易...")
		metadata := fmt.Sprintf("Merge %.6f positions for %s", mergeAmount, gammaMarket.Slug)
		if len(metadata) > 500 {
			metadata = metadata[:497] + "..."
		}

		resp, err := relayerClient.Execute([]relayertypes.SafeTransaction{relayerTx}, metadata, authOption)
		if err != nil {
			fmt.Printf("错误: Relayer 执行失败: %v\n", err)
			os.Exit(1)
		}

		txHash := resp.TransactionHash
		if txHash == "" {
			txHash = resp.Hash
		}

		fmt.Printf("\n✓ 合并交易已通过 Relayer 提交!\n")
		fmt.Printf("  交易ID: %s\n", resp.TransactionID)
		fmt.Printf("  交易哈希: %s\n", txHash)
		fmt.Printf("  状态: %s\n", resp.State)
		fmt.Printf("\n您已获得 %.6f USDC（在代理地址 %s）\n", mergeAmount, proxyAddress.Hex())
	} else {
		// 使用直接调用 CTF 合约的方式
		// 如果配置了代理地址但没有 Builder API 凭证，提示用户
		if envConfig.ProxyAddress != "" {
			fmt.Printf("\n⚠️  提示: 检测到代理地址，但未配置 Builder API 凭证\n")
			fmt.Println("  配置 Builder API 凭证（BUILDER_API_KEY, BUILDER_SECRET, BUILDER_PASS_PHRASE）")
			fmt.Println("  可以使用 Relayer 通过代理钱包执行交易（gasless，不需要 MATIC）")
			fmt.Println("  当前将使用直接调用模式（需要交易账户地址有代币和 MATIC）")
			fmt.Println()
		}

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

		// 发送交易（默认执行，无需确认）
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

	// ===== 最终状态 =====
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("最终状态")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	finalUSDCBalance, _ := ctfClient.GetUSDCBalanceForAddress(ctx, proxyAddress)
	fmt.Printf("USDC余额（地址 %s）: %.6f USDC\n", proxyAddress.Hex(), finalUSDCBalance)

	if yesCollectionId, err := ctfClient.GetCollectionId(parentCollectionId, conditionIdHash, big.NewInt(1)); err == nil {
		if yesPositionId, err := ctfClient.GetPositionId(ctfClient.GetCollateralToken(), yesCollectionId); err == nil {
			if balance, err := ctfClient.GetConditionalTokenBalanceForAddress(ctx, proxyAddress, yesPositionId); err == nil {
				fmt.Printf("YES代币余额（地址 %s）: %.6f\n", proxyAddress.Hex(), balance)
			}
		}
	}
	if noCollectionId, err := ctfClient.GetCollectionId(parentCollectionId, conditionIdHash, big.NewInt(2)); err == nil {
		if noPositionId, err := ctfClient.GetPositionId(ctfClient.GetCollateralToken(), noCollectionId); err == nil {
			if balance, err := ctfClient.GetConditionalTokenBalanceForAddress(ctx, proxyAddress, noPositionId); err == nil {
				fmt.Printf("NO代币余额（地址 %s）: %.6f\n", proxyAddress.Hex(), balance)
			}
		}
	}

	fmt.Println("\n✓ 测试完成!")
}

// loadEnvConfig 从 .env 文件和环境变量加载配置
// 注意：godotenv.Load() 已经将 .env 文件加载到环境变量中，这里直接读取环境变量即可
func loadEnvConfig() (*EnvConfig, error) {
	config := &EnvConfig{}

	// 从环境变量读取（godotenv.Load() 已经加载了 .env 文件）
	config.PrivateKey = strings.TrimSpace(os.Getenv("PRIVATE_KEY"))
	config.RPCURL = strings.TrimSpace(os.Getenv("RPC_URL"))
	config.Amount = strings.TrimSpace(os.Getenv("AMOUNT"))
	config.ChainID = strings.TrimSpace(os.Getenv("CHAIN_ID"))
	config.ProxyAddress = strings.TrimSpace(os.Getenv("PROXY_ADDRESS"))
	config.BuilderAPIKey = strings.TrimSpace(os.Getenv("BUILDER_API_KEY"))
	config.BuilderSecret = strings.TrimSpace(os.Getenv("BUILDER_SECRET"))
	config.BuilderPassPhrase = strings.TrimSpace(os.Getenv("BUILDER_PASS_PHRASE"))

	// 如果私钥为空，返回错误
	if config.PrivateKey == "" {
		return config, fmt.Errorf("PRIVATE_KEY 未设置（请在 .env 文件或环境变量中设置）")
	}

	return config, nil
}
