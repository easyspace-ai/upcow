package services

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/betbot/gobet/clob/types"
)

// initializeBalance 初始化余额（优先从链上查询，然后从 API 获取授权）
func (s *TradingService) initializeBalance(ctx context.Context) {
	// 等待一小段时间，确保 OrderEngine 已启动
	time.Sleep(100 * time.Millisecond)

	// 获取账号地址（优先使用 funderAddress，如果没有则从私钥计算）
	accountAddress := s.funderAddress
	if accountAddress == "" {
		// 尝试从 Client 获取地址
		if addr, err := s.clobClient.GetAddress(); err == nil {
			accountAddress = addr.Hex()
		} else {
			accountAddress = "未设置（无法获取地址）"
		}
	}

	// 优先从链上查询余额（直接查询代理钱包地址的余额）
	var balance float64
	var balanceStr string
	var balanceRaw int64
	var balanceInfo *types.BalanceAllowanceResponse // 用于存储 API 响应，避免重复调用

	if accountAddress != "" && accountAddress != "未设置（无法获取地址）" {
		onChainBalance, err := s.getOnChainUSDCBalance(ctx, accountAddress)
		if err != nil {
			log.Warnf("⚠️ [余额初始化] 链上余额查询失败: %v，将尝试从 API 获取", err)
		} else {
			balance = onChainBalance
			balanceRaw = int64(balance * 1e6)
			balanceStr = fmt.Sprintf("%d", balanceRaw) // 转换为6位小数字符串
			log.Infof("✅ [余额初始化] 从链上查询到余额: %.6f USDC (地址: %s)", balance, accountAddress)
		}
	}

	// 无论链上查询是否成功，都需要从 API 获取授权额度，所以统一调用一次 API
	sigType := s.signatureType
	params := &types.BalanceAllowanceParams{
		AssetType:     types.AssetTypeCollateral,
		SignatureType: &sigType,
	}
	balanceInfo, err := s.clobClient.GetBalanceAllowance(ctx, params)
	if err != nil {
		log.Errorf("❌ [余额初始化] 获取余额和授权失败: %v", err)
		return
	}

	log.Debugf("📊 [余额API响应] Balance=%q, Allowance=%q, CollateralBalance=%q, CollateralAllowance=%q",
		balanceInfo.Balance, balanceInfo.Allowance, balanceInfo.CollateralBalance, balanceInfo.CollateralAllowance)

	// 如果链上查询失败（balance == 0），使用 API 返回的余额
	if balance == 0 {
		balanceStr = balanceInfo.CollateralBalance
		if balanceStr == "" {
			balanceStr = balanceInfo.Balance
		}
		if balanceStr == "" {
			balanceStr = "0"
			log.Debugf("余额字段为空，使用默认值 0")
		}

		var parseErr error
		balanceRaw, parseErr = strconv.ParseInt(balanceStr, 10, 64)
		if parseErr != nil {
			log.Errorf("❌ [余额初始化] 解析余额失败 (值: %q): %v", balanceStr, parseErr)
			return
		}
		balance = float64(balanceRaw) / 1e6
		log.Debugf("📊 [余额解析] 原始字符串: %q, 解析为整数: %d, 除以 1e6: %.6f USDC",
			balanceStr, balanceRaw, balance)
	}

	// 获取授权额度（复用同一份 API 响应）
	var allowance float64
	var allowanceStr string
	if balanceInfo != nil {
		allowanceStr = balanceInfo.CollateralAllowance
		if allowanceStr == "" {
			allowanceStr = balanceInfo.Allowance
		}

		if allowanceStr == "" && balanceInfo.Allowances != nil && len(balanceInfo.Allowances) > 0 {
			log.Debugf("📊 [授权额度] Allowances map 包含 %d 个条目", len(balanceInfo.Allowances))
			maxAllowance := ""
			allZero := true
			for spenderAddr, v := range balanceInfo.Allowances {
				log.Debugf("📊 [授权额度] Spender=%s, Allowance=%s", spenderAddr, v)
				if v != "" && v != "0" {
					allZero = false
					if maxAllowance == "" || v > maxAllowance {
						maxAllowance = v
					}
				}
			}

			if !allZero && maxAllowance != "" {
				allowanceStr = maxAllowance
				log.Debugf("📊 [授权额度] 使用 Allowances map 中的最大值: %s", allowanceStr)
			} else if allZero {
				log.Warnf("⚠️ [授权额度] Allowances map 中所有值都是 0，可能表示授权足够大（unlimited）或查询方式不对")
				allowanceStr = "999999999999" // 999,999,999.999 USDC，足够大
				log.Infof("💡 [授权额度] 由于可以在其他平台下单，假设授权足够大，使用默认值: %s", allowanceStr)
			}
		}

		if allowanceStr == "" {
			allowanceStr = "0"
			log.Debugf("授权字段为空，使用默认值 0")
		}

		allowanceBig := new(big.Int)
		allowanceBig, ok := allowanceBig.SetString(allowanceStr, 10)
		if !ok {
			log.Warnf("⚠️ [余额初始化] 解析授权失败 (值: %q): 无法转换为 big.Int", allowanceStr)
			allowance = 0
		} else {
			maxUint256 := new(big.Int)
			maxUint256.SetString("115792089237316195423570985008687907853269984665640564039457584007913129639935", 10)
			threshold := new(big.Int).Sub(maxUint256, big.NewInt(1000))
			if allowanceBig.Cmp(threshold) >= 0 {
				log.Infof("✅ [授权额度] 检测到无限授权（uint256 最大值），设置为足够大的值")
				allowance = 999999999.999
			} else {
				allowanceFloat := new(big.Float).SetInt(allowanceBig)
				divisor := new(big.Float).SetFloat64(1e6)
				allowanceFloat.Quo(allowanceFloat, divisor)
				allowance, _ = allowanceFloat.Float64()
			}
		}
	} else {
		log.Warnf("⚠️ [余额初始化] balanceInfo 为 nil，无法获取授权")
		allowance = 0
		allowanceStr = "0"
	}

	// 更新 OrderEngine 余额
	s.orderEngine.SubmitCommand(&UpdateBalanceCommand{
		id:       fmt.Sprintf("init_balance_%d", time.Now().UnixNano()),
		Balance:  balance,
		Currency: "USDC",
	})

	// 格式化显示账号信息、余额和授权额度
	log.Infof("═══════════════════════════════════════════════════════════")
	log.Infof("📋 [账号信息]")
	log.Infof("   账号地址: %s", accountAddress)
	log.Infof("   余额:     %.6f USDC (原始值: %s, 整数: %d)", balance, balanceStr, balanceRaw)
	log.Infof("   授权额度: %.6f USDC (原始值: %s)", allowance, allowanceStr)
	if allowance < balance {
		log.Warnf("   ⚠️  授权额度小于余额，可能需要增加授权才能下单")
	}
	if balance < 0.01 {
		log.Warnf("   ⚠️  余额非常低 (%.6f USDC)，可能无法下单", balance)
	}
	log.Infof("═══════════════════════════════════════════════════════════")
}

// getOnChainUSDCBalance 从 Polygon 链上查询 USDC 余额（参考 test/clob.go）
// 直接查询指定地址的链上余额，不需要认证
func (s *TradingService) getOnChainUSDCBalance(ctx context.Context, walletAddress string) (float64, error) {
	const USDCContractPolygon = "0x2791Bca1f2de4661ED88A30C99A7a9449Aa84174"

	walletAddress = strings.ToLower(strings.TrimSpace(walletAddress))
	if !strings.HasPrefix(walletAddress, "0x") {
		walletAddress = "0x" + walletAddress
	}

	paddedAddr := strings.TrimPrefix(walletAddress, "0x")
	paddedAddr = fmt.Sprintf("%064s", paddedAddr)

	// balanceOf(address) selector: 0x70a08231
	data := "0x70a08231" + paddedAddr

	reqBody := fmt.Sprintf(`{
		"jsonrpc": "2.0",
		"method": "eth_call",
		"params": [{
			"to": "%s",
			"data": "%s"
		}, "latest"],
		"id": 1
	}`, USDCContractPolygon, data)

	req, err := http.NewRequestWithContext(ctx, "POST", "https://polygon-rpc.com", strings.NewReader(reqBody))
	if err != nil {
		return 0, fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("RPC 请求失败: %w", err)
	}
	defer resp.Body.Close()

	var rpcResp struct {
		Result string `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return 0, fmt.Errorf("解析响应失败: %w", err)
	}
	if rpcResp.Error != nil {
		return 0, fmt.Errorf("RPC 错误: %s", rpcResp.Error.Message)
	}

	result := strings.TrimPrefix(rpcResp.Result, "0x")
	if result == "" || result == "0" {
		return 0, nil
	}
	balance := new(big.Int)
	balance.SetString(result, 16)

	balanceFloat := new(big.Float).SetInt(balance)
	divisor := new(big.Float).SetFloat64(1e6)
	balanceFloat.Quo(balanceFloat, divisor)
	result64, _ := balanceFloat.Float64()
	return result64, nil
}

