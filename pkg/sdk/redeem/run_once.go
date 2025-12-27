package redeem

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/betbot/gobet/pkg/sdk/api"
	"github.com/betbot/gobet/pkg/sdk/relayer"
	reltypes "github.com/betbot/gobet/pkg/sdk/relayer/types"
	sdktypes "github.com/betbot/gobet/pkg/sdk/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

type RunOnceOptions struct {
	PrivateKeyHex string
	FunderAddress string
	BuilderCreds  *sdktypes.BuilderApiKeyCreds
	RelayerURL    string
}

type RunOnceResult struct {
	Redeemed  int
	TotalUSDC float64
}

// RunOnce checks open positions and redeems resolved ones (gasless via Relayer).
// It is a pure function w.r.t. configuration: does NOT read env vars.
func RunOnce(ctx context.Context, apiClient *api.Client, opts RunOnceOptions) (RunOnceResult, error) {
	if apiClient == nil {
		return RunOnceResult{}, fmt.Errorf("apiClient is nil")
	}
	pkHex := strings.TrimSpace(opts.PrivateKeyHex)
	if pkHex == "" {
		return RunOnceResult{}, fmt.Errorf("PrivateKeyHex is required")
	}
	pkHex = strings.TrimPrefix(pkHex, "0x")
	privateKey, err := crypto.HexToECDSA(pkHex)
	if err != nil {
		return RunOnceResult{}, fmt.Errorf("invalid private key: %w", err)
	}

	publicKey := privateKey.Public()
	publicKeyECDSA, ok := publicKey.(*ecdsa.PublicKey)
	if !ok {
		return RunOnceResult{}, fmt.Errorf("error casting public key")
	}
	eoaAddr := crypto.PubkeyToAddress(*publicKeyECDSA)

	safeAddr := eoaAddr
	if strings.TrimSpace(opts.FunderAddress) != "" {
		safeAddr = common.HexToAddress(strings.TrimSpace(opts.FunderAddress))
	}

	if opts.BuilderCreds == nil || strings.TrimSpace(opts.BuilderCreds.Key) == "" || strings.TrimSpace(opts.BuilderCreds.Secret) == "" || strings.TrimSpace(opts.BuilderCreds.Passphrase) == "" {
		return RunOnceResult{}, fmt.Errorf("BuilderCreds is required")
	}

	signFn := func(signer string, digest []byte) ([]byte, error) {
		_ = signer
		sig, err := crypto.Sign(digest, privateKey)
		if err != nil {
			return nil, err
		}
		if sig[64] < 27 {
			sig[64] += 27
		}
		return sig, nil
	}

	relayerURL := strings.TrimSpace(opts.RelayerURL)
	if relayerURL == "" {
		relayerURL = "https://relayer-v2.polymarket.com"
	}

	chainID := big.NewInt(137) // Polygon
	relayerClient := relayer.NewClient(relayerURL, chainID, signFn, opts.BuilderCreds)

	positions, err := apiClient.GetOpenPositions(ctx, safeAddr.Hex())
	if err != nil {
		return RunOnceResult{}, fmt.Errorf("get open positions: %w", err)
	}
	if len(positions) == 0 {
		return RunOnceResult{}, nil
	}

	var redeemable []api.OpenPosition
	for _, pos := range positions {
		curPrice := pos.CurPrice.Float64()
		size := pos.Size.Float64()
		if size > 0.001 && (curPrice < 0.001 || curPrice > 0.999) {
			redeemable = append(redeemable, pos)
		}
	}
	if len(redeemable) == 0 {
		return RunOnceResult{}, nil
	}
	if len(redeemable) > maxRedeemsPerCycle {
		redeemable = redeemable[:maxRedeemsPerCycle]
	}

	var out RunOnceResult
	for i, pos := range redeemable {
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		default:
		}
		if i > 0 {
			select {
			case <-ctx.Done():
				return out, ctx.Err()
			case <-time.After(redeemDelay):
			}
		}

		if err := redeemPositionViaRelayer(ctx, relayerClient, eoaAddr, safeAddr, privateKey, pos); err != nil {
			// quota exceeded => stop early
			if strings.Contains(err.Error(), "quota exceeded") {
				break
			}
			// skip this position
			continue
		}
		out.Redeemed++
		if pos.CurPrice.Float64() > 0.5 {
			out.TotalUSDC += pos.Size.Float64()
		}
	}

	return out, nil
}

func redeemPositionViaRelayer(ctx context.Context, relayerClient *relayer.Client, eoaAddr common.Address, safeAddr common.Address, _ *ecdsa.PrivateKey, pos api.OpenPosition) error {
	conditionID := common.HexToHash(pos.ConditionID)
	indexSet := big.NewInt(1)
	if pos.OutcomeIndex == 1 {
		indexSet = big.NewInt(2)
	}

	apiTx, err := api.BuildRedeemTransaction(conditionID, indexSet)
	if err != nil {
		return fmt.Errorf("build redeem tx: %w", err)
	}

	relayerTx := reltypes.SafeTransaction{
		To:        apiTx.To.Hex(),
		Operation: reltypes.OperationType(apiTx.Operation),
		Data:      "0x" + hex.EncodeToString(apiTx.Data),
		Value:     apiTx.Value.String(),
	}

	metadata := fmt.Sprintf("Redeem: %s - %s", pos.Title, pos.Outcome)
	if len(metadata) > 500 {
		metadata = metadata[:497] + "..."
	}

	authOption := &sdktypes.AuthOption{
		SingerAddress: eoaAddr.Hex(),
		FunderAddress: safeAddr.Hex(),
	}

	_, err = relayerClient.Execute([]reltypes.SafeTransaction{relayerTx}, metadata, authOption)
	if err != nil {
		return fmt.Errorf("relayer execute: %w", err)
	}
	return nil
}

