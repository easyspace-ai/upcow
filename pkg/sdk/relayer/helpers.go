package relayer

import (
	"encoding/hex"
	"fmt"
	"github.com/betbot/gobet/pkg/sdk/relayer/types"
	"github.com/betbot/gobet/pkg/sdk/signing"
	sdktypes "github.com/betbot/gobet/pkg/sdk/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"math/big"
)

func buildSafeCreateTransactionRequest(
	chainId *big.Int,
	signatureFunc signing.SignatureFunc,
	safeCfg string,
	args types.SafeCreateTransactionArgs,
) (*types.TransactionRequest, error) {
	owner := args.From

	signature, err := signing.BuildSafeCreateTransactionEip712Signature(signatureFunc, chainId, owner, safeCfg, args.PaymentToken, args.Payment, args.PaymentReceiver)
	if err != nil {
		return nil, err
	}

	safeAddr, err := deriveSafe(owner, safeCfg)
	if err != nil {
		return nil, fmt.Errorf("buildSafeCreateTransactionRequest: deriveSafe failed: %w", err)
	}

	req := &types.TransactionRequest{
		Type:        string(types.TransactionTypeSAFECreate),
		From:        owner,
		To:          safeCfg,
		ProxyWallet: &safeAddr,
		Data:        "0x",
		Signature:   signature,
		SignatureParams: types.SignatureParams{
			PaymentToken:    &args.PaymentToken,
			Payment:         &args.Payment,
			PaymentReceiver: &args.PaymentReceiver,
		},
	}
	return req, nil
}

func deriveSafe(ownerAddress string, safeFactory string) (string, error) {
	if !common.IsHexAddress(ownerAddress) {
		return "", fmt.Errorf("deriveSafe: invalid owner address: %s", ownerAddress)
	}
	if !common.IsHexAddress(safeFactory) {
		return "", fmt.Errorf("deriveSafe: invalid factory address: %s", safeFactory)
	}

	initCodeHashBytes, err := hex.DecodeString(types.SafeInitCodeHashHex)
	if err != nil {
		return "", fmt.Errorf("deriveSafe: invalid SAFE_INIT_CODE_HASH_HEX: %w", err)
	}
	if len(initCodeHashBytes) != 32 {
		return "", fmt.Errorf("deriveSafe: unexpected init code hash length %d", len(initCodeHashBytes))
	}

	paddedOwner := common.LeftPadBytes(common.HexToAddress(ownerAddress).Bytes(), 32)
	saltBytes := crypto.Keccak256(paddedOwner)
	factoryAddr := common.HexToAddress(safeFactory)
	salt32 := [32]byte{}
	copy(salt32[:], saltBytes[:32])

	computedAddress := crypto.CreateAddress2(factoryAddr, salt32, initCodeHashBytes)
	return computedAddress.Hex(), nil
}

func buildSafeTransactionRequest(signatureFunc signing.SignatureFunc, args types.SafeTransactionArgs, safeCfg *types.ContractConfig, metadata string) (*types.TransactionRequest, error) {
	safeAddr, err := deriveSafe(args.From, safeCfg.SafeFactory)
	if err != nil {
		return nil, fmt.Errorf("build safe transaction request: deriveSafe failed: %w", err)
	}

	transaction, err := aggregateTransaction(args.Transactions, safeCfg.SafeMultisend)
	if err != nil {
		return nil, fmt.Errorf("build safe transaction request: aggregateTransaction failed: %w", err)
	}

	sigBytes, err := signing.BuildSafeCreateSafeSignature(
		signatureFunc,
		args.ChainID,
		args.From,
		safeAddr,
		transaction.To,
		transaction.Value,
		transaction.Data,
		transaction.Operation,
		"0",
		"0",
		"0",
		sdktypes.ZeroAddress,
		sdktypes.ZeroAddress,
		args.Nonce,
	)
	if err != nil {
		return nil, fmt.Errorf("build safe transaction request: create safe signature failed: %w", err)
	}

	sig, err := types.SplitAndPackSignature("0x" + hex.EncodeToString(sigBytes))
	if err != nil {
		return nil, fmt.Errorf("build safe transaction request:  invalid signature: %w", err)
	}

	var (
		gasPrice       = "0"
		operation      = fmt.Sprintf("%d", transaction.Operation)
		safeTxnGas     = "0"
		baseGas        = "0"
		gasToken       = sdktypes.ZeroAddress
		refundReceiver = sdktypes.ZeroAddress
	)

	sigParams := types.SignatureParams{
		GasPrice:       &gasPrice,
		Operation:      &operation,
		SafeTxnGas:     &safeTxnGas,
		BaseGas:        &baseGas,
		GasToken:       &gasToken,
		RefundReceiver: &refundReceiver,
	}

	req := &types.TransactionRequest{
		Type:            string(types.TransactionTypeSAFE),
		From:            args.From,
		To:              transaction.To,
		ProxyWallet:     &safeAddr,
		Data:            transaction.Data,
		Nonce:           &args.Nonce,
		Signature:       sig,
		SignatureParams: sigParams,
		Metadata:        &metadata,
	}
	return req, nil
}

// aggregateTransaction
func aggregateTransaction(txns []types.SafeTransaction, multisendAddr string) (types.SafeTransaction, error) {
	if len(txns) == 1 {
		return txns[0], nil
	}
	return types.CreateSafeMultiSendTransaction(txns, multisendAddr)
}
