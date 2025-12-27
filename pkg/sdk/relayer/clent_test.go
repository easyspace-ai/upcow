package relayer_test

import (
	"context"
	"encoding/hex"
	"fmt"
	"github.com/betbot/gobet/pkg/sdk/relayer"
	"github.com/betbot/gobet/pkg/sdk/relayer/types"
	sdktypes "github.com/betbot/gobet/pkg/sdk/types"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/polymarket/go-order-utils/pkg/model"
	"github.com/stretchr/testify/assert"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"
)

var (
	PolymarketRelayURL = "https://relayer-v2.polymarket.com"
	chaindId           = big.NewInt(137)
	privateKey, _      = crypto.ToECDSA(common.Hex2Bytes("3f26dbcf904a3542e5f54eed3381c740d6246d8bc81cefbfd0679ae4ce8d82c9"))
)

func signature(signer string, digest []byte) ([]byte, error) {
	sig, err := crypto.Sign(digest, privateKey)
	if err != nil {
		return nil, err
	}
	sig[64] += 27
	return sig, nil
}

func TestClient_GetNonce(t *testing.T) {
	if os.Getenv("RUN_INTEGRATION_TESTS") != "1" {
		t.Skip("integration test: set RUN_INTEGRATION_TESTS=1 to enable")
	}
	client := relayer.NewClient(PolymarketRelayURL, chaindId, signature, &sdktypes.BuilderApiKeyCreds{
		Key:        "019a4dec-fc6a-79ba-8937-d9bf3c2792ca",
		Secret:     "Q23ZHyR21V5_F8qVLvOvnXGhxtW6CmNCWDjHzFJQW7k=",
		Passphrase: "2a171196ddfe34aab62eea32ed63fe424fde8144413982dd90527c844cf2e8d3",
	})

	transactions, err := client.GetTransactions()
	assert.Equal(t, nil, err)
	t.Logf("transactions: %v", transactions)
}

func TestProxy(t *testing.T) {
	if os.Getenv("RUN_INTEGRATION_TESTS") != "1" {
		t.Skip("integration test: set RUN_INTEGRATION_TESTS=1 to enable")
	}
	client := relayer.NewClient(PolymarketRelayURL, chaindId, signature, &sdktypes.BuilderApiKeyCreds{
		Key:        "019a4dec-fc6a-79ba-8937-d9bf3c2792ca",
		Secret:     "Q23ZHyR21V5_F8qVLvOvnXGhxtW6CmNCWDjHzFJQW7k=",
		Passphrase: "2a171196ddfe34aab62eea32ed63fe424fde8144413982dd90527c844cf2e8d3",
	})
	response, err := client.Deploy(&sdktypes.AuthOption{
		SignatureType: model.POLY_GNOSIS_SAFE,
		SingerAddress: "0x2061452270d3E93A37B72062d10e68Ec46d4A304",
	})
	assert.NoError(t, err)
	state, err := client.PollUntilState(context.Background(), response.TransactionID, []types.RelayerTransactionState{types.RelayerStateConfirmed}, nil, 10, 5*time.Second)
	assert.Equal(t, nil, err)
	t.Logf("state: %v", state)
}

func TestGetExpectedSafe(t *testing.T) {
	if os.Getenv("RUN_INTEGRATION_TESTS") != "1" {
		t.Skip("integration test: set RUN_INTEGRATION_TESTS=1 to enable")
	}
	client := relayer.NewClient(PolymarketRelayURL, chaindId, signature, &sdktypes.BuilderApiKeyCreds{
		Key:        "019a4dec-fc6a-79ba-8937-d9bf3c2792ca",
		Secret:     "Q23ZHyR21V5_F8qVLvOvnXGhxtW6CmNCWDjHzFJQW7k=",
		Passphrase: "2a171196ddfe34aab62eea32ed63fe424fde8144413982dd90527c844cf2e8d3",
	})

	txn, err := createUsdcApproveTxn("0x2791Bca1f2de4661ED88A30C99A7a9449Aa84174", "0x4d97dcd97ec945f40cf65f87097ace5ea0476045")
	txn2, err := createUsdcApproveTxn("0x2791Bca1f2de4661ED88A30C99A7a9449Aa84174", "0x4bFb41d5B3570DeFd03C39a9A4D8dE6Bd8B8982E")
	txn3, err := createUsdcApproveTxn("0x2791Bca1f2de4661ED88A30C99A7a9449Aa84174", "0xC5d563A36AE78145C45a50134d48A1215220f80a")
	txn4, err := createUsdcApproveTxn("0x2791Bca1f2de4661ED88A30C99A7a9449Aa84174", "0xd91E80cF2E7be2e162c6513ceD06f1dD0dA35296")
	//txn5, err := createSetApproveAll("0x4d97dcd97ec945f40cf65f87097ace5ea0476045", "0x4bFb41d5B3570DeFd03C39a9A4D8dE6Bd8B8982E")
	//txn6, err := createSetApproveAll("0x4d97dcd97ec945f40cf65f87097ace5ea0476045", "0xC5d563A36AE78145C45a50134d48A1215220f80a")
	//txn7, err := createSetApproveAll("0x4d97dcd97ec945f40cf65f87097ace5ea0476045", "0xd91E80cF2E7be2e162c6513ceD06f1dD0dA35296")
	//var parent [32]byte
	//indexSets := []*big.Int{big.NewInt(1), big.NewInt(2)}
	//txn, err := createRedeemPositions("0x4d97dcd97ec945f40cf65f87097ace5ea0476045", common.HexToAddress("0x2791Bca1f2de4661ED88A30C99A7a9449Aa84174"), parent, common.HexToHash("0xee1091d8be46ae92f59d743694ff29e4fbd9d9b15151064ab4a4af7062aed6de"), indexSets)

	assert.Equal(t, nil, err)
	execute, err := client.Execute([]types.SafeTransaction{txn, txn2, txn3, txn4}, "approve USDC on CTF", &sdktypes.AuthOption{
		SingerAddress: "0x8c5f23249462e20C4a202Ad35275562075F37e09",
	})
	assert.Equal(t, nil, err)
	t.Logf("execute: %s", execute)
}

func createUsdcApproveTxn(tokenAddress string, spenderAddress string) (types.SafeTransaction, error) {
	// 校验地址
	if !common.IsHexAddress(tokenAddress) {
		return types.SafeTransaction{}, fmt.Errorf("CreateUsdcApproveTxn: invalid token address: %s", tokenAddress)
	}
	if !common.IsHexAddress(spenderAddress) {
		return types.SafeTransaction{}, fmt.Errorf("CreateUsdcApproveTxn: invalid spender address: %s", spenderAddress)
	}

	// ERC-20 approve 方法 ABI 定义（简化）
	const erc20ABIJSON = `[
      {
        "constant": false,
        "inputs": [
          {"name": "_spender","type": "address"},
          {"name": "_value","type": "uint256"}
        ],
        "name": "approve",
        "outputs": [
          {"name":"","type":"bool"}
        ],
        "payable": false,
        "stateMutability": "nonpayable",
        "type": "function"
      }
    ]`

	tokenABI, err := abi.JSON(strings.NewReader(erc20ABIJSON))
	if err != nil {
		return types.SafeTransaction{}, fmt.Errorf("CreateUsdcApproveTxn: parse abi failed: %w", err)
	}

	spender := common.HexToAddress(spenderAddress)
	maxValue := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))

	dataBytes, err := tokenABI.Pack("approve", spender, maxValue)
	if err != nil {
		return types.SafeTransaction{}, fmt.Errorf("CreateUsdcApproveTxn: ABI pack failed: %w", err)
	}

	txn := types.SafeTransaction{
		To:        tokenAddress,
		Operation: types.OperationCall,
		Data:      "0x" + hex.EncodeToString(dataBytes),
		Value:     "0",
	}
	return txn, nil
}
