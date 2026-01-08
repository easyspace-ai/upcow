package services

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/pkg/config"
	sdkapi "github.com/betbot/gobet/pkg/sdk/api"
	sdkrelayer "github.com/betbot/gobet/pkg/sdk/relayer"
	relayertypes "github.com/betbot/gobet/pkg/sdk/relayer/types"
	sdktypes "github.com/betbot/gobet/pkg/sdk/types"
	"github.com/ethereum/go-ethereum/common"
)

// MergeCompleteSetsViaRelayer merges YES+NO complete sets back to USDC for a given conditionId.
//
// Requirements:
//   - TradingService must have wallet.funder_address set (Safe/proxy wallet).
//   - Builder credentials must be provided via:
//     - Environment variables: BUILDER_API_KEY, BUILDER_SECRET, BUILDER_PASS_PHRASE (highest priority)
//     - Config file: config.yaml builder section (fallback)
//
// Notes:
//   - This is a "manual/explicit action" API by default. Balance checks are not performed here
//     because they require RPC access; callers should decide the amount to merge.
func (s *TradingService) MergeCompleteSetsViaRelayer(ctx context.Context, conditionID string, amount float64, metadata string) (txHash string, err error) {
	if s == nil || s.clobClient == nil {
		return "", fmt.Errorf("trading service not initialized")
	}
	conditionID = strings.TrimSpace(conditionID)
	if conditionID == "" {
		return "", fmt.Errorf("conditionID is empty")
	}
	if amount <= 0 {
		return "", fmt.Errorf("amount must be > 0")
	}
	if strings.TrimSpace(s.funderAddress) == "" {
		return "", fmt.Errorf("funder_address not configured (required for relayer merge)")
	}

	// fail-safe: do not merge while system paused (same spirit as other "trade-like" actions)
	// ✅ 修复：合并操作是平仓操作（将 tokens 换回 USDC），应该允许绕过 risk-off
	// 传入一个虚拟订单对象，设置 BypassRiskOff=true，允许在 risk-off 期间执行合并
	virtualOrder := &domain.Order{
		BypassRiskOff: true, // 合并操作允许绕过 risk-off（平仓操作）
	}
	if e := s.allowPlaceOrder(virtualOrder); e != nil {
		return "", e
	}

	// in-flight gate (avoid repeated merge clicks / repeated triggers)
	// 注意：允许并发 merge，但防止完全相同的 merge 请求（相同的 conditionID 和 amount）
	// 如果希望完全允许并发，可以注释掉下面的代码
	key := fmt.Sprintf("merge|%s|%0.6f", strings.ToLower(conditionID), round6(amount))
	if s.inFlightDeduper != nil {
		if e := s.inFlightDeduper.TryAcquire(key); e != nil {
			// 允许并发：如果遇到 duplicate in-flight，记录日志但不阻止
			// 这样可以允许不同 amount 的 merge 并发执行
			// 如果希望完全阻止重复请求，可以取消下面的注释并 return
			// return "", e
		} else {
			defer func() {
				if err != nil {
					s.inFlightDeduper.Release(key)
				}
			}()
		}
	}

	if s.dryRun {
		return "dry_run_merge", nil
	}

	// 获取 Builder 凭证（优先级：环境变量 > 配置文件）
	builderKey := strings.TrimSpace(os.Getenv("BUILDER_API_KEY"))
	builderSecret := strings.TrimSpace(os.Getenv("BUILDER_SECRET"))
	builderPass := strings.TrimSpace(os.Getenv("BUILDER_PASS_PHRASE"))

	// 如果环境变量未设置，尝试从配置文件读取
	if builderKey == "" || builderSecret == "" || builderPass == "" {
		if gc := config.Get(); gc != nil {
			if builderKey == "" && strings.TrimSpace(gc.Builder.APIKey) != "" {
				builderKey = strings.TrimSpace(gc.Builder.APIKey)
			}
			if builderSecret == "" && strings.TrimSpace(gc.Builder.Secret) != "" {
				builderSecret = strings.TrimSpace(gc.Builder.Secret)
			}
			if builderPass == "" && strings.TrimSpace(gc.Builder.PassPhrase) != "" {
				builderPass = strings.TrimSpace(gc.Builder.PassPhrase)
			}
		}
	}

	if builderKey == "" || builderSecret == "" || builderPass == "" {
		return "", fmt.Errorf("missing builder credentials (BUILDER_API_KEY/BUILDER_SECRET/BUILDER_PASS_PHRASE or config.yaml builder section)")
	}

	// amount float -> 6 decimals integer
	amountBig := floatToUSDC6(amount)

	condHash := common.HexToHash(conditionID)
	apiTx, e := sdkapi.BuildMergeTransaction(condHash, amountBig)
	if e != nil {
		return "", fmt.Errorf("build merge tx failed: %w", e)
	}

	relayerTx := relayertypes.SafeTransaction{
		To:        apiTx.To.Hex(),
		Operation: relayertypes.OperationType(apiTx.Operation),
		Data:      "0x" + hex.EncodeToString(apiTx.Data),
		Value:     apiTx.Value.String(),
	}

	signerAddr, e := s.clobClient.GetAddress()
	if e != nil {
		return "", fmt.Errorf("get signer address failed: %w", e)
	}

	signFn := func(_ string, digest []byte) ([]byte, error) {
		return s.clobClient.SignDigest(digest)
	}

	relayerURL := strings.TrimSpace(os.Getenv("POLYMARKET_RELAYER_URL"))
	if relayerURL == "" {
		relayerURL = "https://relayer-v2.polymarket.com"
	}

	builderCreds := &sdktypes.BuilderApiKeyCreds{
		Key:        builderKey,
		Secret:     builderSecret,
		Passphrase: builderPass,
	}
	chainID := big.NewInt(int64(s.clobClient.GetChainID()))
	rc := sdkrelayer.NewClient(relayerURL, chainID, signFn, builderCreds)

	if strings.TrimSpace(metadata) == "" {
		metadata = fmt.Sprintf("Merge %.6f complete sets for %s", amount, conditionID)
	}
	if len(metadata) > 500 {
		metadata = metadata[:497] + "..."
	}

	auth := &sdktypes.AuthOption{
		SingerAddress: signerAddr.Hex(),
		FunderAddress: strings.TrimSpace(s.funderAddress),
	}

	_ = ctx // relayer SDK currently does not accept context for Execute()
	resp, e := rc.Execute([]relayertypes.SafeTransaction{relayerTx}, metadata, auth)
	if e != nil {
		// risk-off on relayer errors (avoid repeated submits)
		s.TriggerRiskOff(5*time.Second, "merge_relayer_error")
		return "", e
	}
	txHash = resp.TransactionHash
	if txHash == "" {
		txHash = resp.Hash
	}
	if txHash == "" {
		txHash = resp.TransactionID
	}
	return txHash, nil
}

func floatToUSDC6(v float64) *big.Int {
	if v <= 0 {
		return big.NewInt(0)
	}
	// amount * 1e6 with rounding
	f := new(big.Float).SetFloat64(v)
	f.Mul(f, new(big.Float).SetInt64(1000000))
	out, _ := f.Int(nil)
	if out == nil {
		out = big.NewInt(0)
	}
	return out
}

func round6(v float64) float64 {
	// best-effort rounding to 6 decimals for dedup keys
	f := floatToUSDC6(v)
	if f == nil {
		return 0
	}
	ff := new(big.Float).SetInt(f)
	ff.Quo(ff, new(big.Float).SetInt64(1000000))
	out, _ := ff.Float64()
	return out
}
