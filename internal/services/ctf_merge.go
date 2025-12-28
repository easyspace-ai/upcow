package services

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	sdkapi "github.com/betbot/gobet/pkg/sdk/api"
	sdkrelayer "github.com/betbot/gobet/pkg/sdk/relayer"
	relayertypes "github.com/betbot/gobet/pkg/sdk/relayer/types"
	sdktypes "github.com/betbot/gobet/pkg/sdk/types"
	"github.com/ethereum/go-ethereum/common"
)

// MergeCompleteSetsViaRelayer merges YES+NO complete sets back to USDC for a given conditionId.
//
// Requirements:
// - TradingService must have wallet.funder_address set (Safe/proxy wallet).
// - Environment must provide Builder credentials:
//   BUILDER_API_KEY, BUILDER_SECRET, BUILDER_PASS_PHRASE
//
// Notes:
// - This is a "manual/explicit action" API by default. Balance checks are not performed here
//   because they require RPC access; callers should decide the amount to merge.
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
	if e := s.allowPlaceOrder(); e != nil {
		return "", e
	}

	// in-flight gate (avoid repeated merge clicks / repeated triggers)
	key := fmt.Sprintf("merge|%s|%0.6f", strings.ToLower(conditionID), round6(amount))
	if s.inFlightDeduper != nil {
		if e := s.inFlightDeduper.TryAcquire(key); e != nil {
			return "", e
		}
		defer func() {
			if err != nil {
				s.inFlightDeduper.Release(key)
			}
		}()
	}

	if s.dryRun {
		return "dry_run_merge", nil
	}

	builderKey := strings.TrimSpace(os.Getenv("BUILDER_API_KEY"))
	builderSecret := strings.TrimSpace(os.Getenv("BUILDER_SECRET"))
	builderPass := strings.TrimSpace(os.Getenv("BUILDER_PASS_PHRASE"))
	if builderKey == "" || builderSecret == "" || builderPass == "" {
		return "", fmt.Errorf("missing builder credentials (BUILDER_API_KEY/BUILDER_SECRET/BUILDER_PASS_PHRASE)")
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

