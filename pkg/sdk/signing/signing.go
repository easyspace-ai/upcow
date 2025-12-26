package signing

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"github.com/betbot/gobet/pkg/sdk/relayer/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"
	"math/big"
	"strings"
)

const (
	ClobAuthDomain          = "ClobAuthDomain"
	ClobAuthDomainVersion   = "1"
	ClobAuthDomainMsgToSign = "This message attests that I control the given wallet"

	SafeFactoryName = "Polymarket Contract Proxy Factory"
)

func BuildClobEip712Signature(signatureFunc SignatureFunc, chainId *big.Int, singer, ts string, nonce *big.Int) (sigHex string, err error) {
	typedData := apitypes.TypedData{
		Types: apitypes.Types{
			"EIP712Domain": []apitypes.Type{
				{Name: "name", Type: "string"},
				{Name: "version", Type: "string"},
				{Name: "chainId", Type: "uint256"},
			},
			"ClobAuth": []apitypes.Type{
				{Name: "address", Type: "address"},
				{Name: "timestamp", Type: "string"},
				{Name: "nonce", Type: "uint256"},
				{Name: "message", Type: "string"},
			},
		},
		PrimaryType: "ClobAuth",
		Domain: apitypes.TypedDataDomain{
			Name:    ClobAuthDomain,
			Version: ClobAuthDomainVersion,
			ChainId: math.NewHexOrDecimal256(chainId.Int64()),
		},
		Message: map[string]interface{}{
			"address":   singer,
			"timestamp": ts,
			"nonce":     nonce,
			"message":   ClobAuthDomainMsgToSign,
		},
	}

	hash, _, err := apitypes.TypedDataAndHash(typedData)
	if err != nil {
		return "", fmt.Errorf("TypedDataAndHash failed: %w", err)
	}

	sigBytes, err := signatureFunc(singer, hash)
	if err != nil {
		return "", fmt.Errorf("signature failed: %w", err)
	}

	return "0x" + hex.EncodeToString(sigBytes), nil
}

func BuildSafeCreateTransactionEip712Signature(signatureFunc SignatureFunc, chainId *big.Int, singer, safeCfg, paymentToken, payment, paymentReceiver string) (string, error) {
	typedData := apitypes.TypedData{
		Types: apitypes.Types{
			"EIP712Domain": []apitypes.Type{
				{Name: "name", Type: "string"},
				{Name: "chainId", Type: "uint256"},
				{Name: "verifyingContract", Type: "address"},
			},
			"CreateProxy": []apitypes.Type{
				{Name: "paymentToken", Type: "address"},
				{Name: "payment", Type: "uint256"},
				{Name: "paymentReceiver", Type: "address"},
			},
		},
		PrimaryType: "CreateProxy",
		Domain: apitypes.TypedDataDomain{
			Name:              SafeFactoryName,
			ChainId:           math.NewHexOrDecimal256(chainId.Int64()),
			VerifyingContract: common.HexToAddress(safeCfg).Hex(),
		},
		Message: map[string]interface{}{
			"paymentToken":    paymentToken,
			"payment":         payment,
			"paymentReceiver": paymentReceiver,
		},
	}

	hash, _, err := apitypes.TypedDataAndHash(typedData)
	if err != nil {
		return "", fmt.Errorf("build safe createTransaction TypedDataAndHash failed: %w", err)
	}

	sigBytes, err := signatureFunc(singer, hash)
	if err != nil {
		return "", fmt.Errorf("signature failed: %w", err)
	}

	return "0x" + hex.EncodeToString(sigBytes), nil
}

func BuildSafeCreateSafeSignature(
	signatureFunc SignatureFunc,
	chainID int64,
	from string,
	safeAddress string,
	to string,
	value string,
	data string,
	operation types.OperationType,
	safeTxGas string,
	baseGas string,
	gasPrice string,
	gasToken string,
	refundReceiver string,
	nonce string,
) ([]byte, error) {
	typedData := apitypes.TypedData{
		Types: apitypes.Types{
			"EIP712Domain": []apitypes.Type{
				{Name: "chainId", Type: "uint256"},
				{Name: "verifyingContract", Type: "address"},
			},
			"SafeTx": []apitypes.Type{
				{Name: "to", Type: "address"},
				{Name: "value", Type: "uint256"},
				{Name: "data", Type: "bytes"},
				{Name: "operation", Type: "uint8"},
				{Name: "safeTxGas", Type: "uint256"},
				{Name: "baseGas", Type: "uint256"},
				{Name: "gasPrice", Type: "uint256"},
				{Name: "gasToken", Type: "address"},
				{Name: "refundReceiver", Type: "address"},
				{Name: "nonce", Type: "uint256"},
			},
		},
		PrimaryType: "SafeTx",
		Domain: apitypes.TypedDataDomain{
			ChainId:           math.NewHexOrDecimal256(chainID),
			VerifyingContract: common.HexToAddress(safeAddress).Hex(),
		},
		Message: apitypes.TypedDataMessage{
			"to":             to,
			"value":          value,
			"data":           data,
			"operation":      big.NewInt(int64(operation)),
			"safeTxGas":      safeTxGas,
			"baseGas":        baseGas,
			"gasPrice":       gasPrice,
			"gasToken":       gasToken,
			"refundReceiver": refundReceiver,
			"nonce":          nonce,
		},
	}

	structHash, _, err := apitypes.TypedDataAndHash(typedData)
	if err != nil {
		return nil, fmt.Errorf("TypedDataAndHash failed: %w", err)
	}

	prefixedHash := crypto.Keccak256Hash([]byte(fmt.Sprintf("\x19Ethereum Signed Message:\n%d%s", len(structHash), structHash)))

	sigBytes, err := signatureFunc(from, prefixedHash.Bytes())
	if err != nil {
		return nil, fmt.Errorf("signature failed: %w", err)
	}
	return sigBytes, nil
}

func BuildPolyHmacSignature(secret string, timestamp string, method string, requestPath string, body *string) (string, error) {
	secretBytes, err := base64.URLEncoding.DecodeString(secret)
	if err != nil {
		return "", fmt.Errorf("failed to decode secret from base64 url: %w", err)
	}

	message := timestamp + method + requestPath
	if body != nil {
		processed := strings.ReplaceAll(*body, "'", `"`)
		message += processed
	}

	mac := hmac.New(sha256.New, secretBytes)
	mac.Write([]byte(message))
	digest := mac.Sum(nil)

	signature := base64.URLEncoding.EncodeToString(digest)
	return signature, nil
}
