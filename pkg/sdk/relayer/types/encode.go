package types

import (
	"encoding/hex"
	"fmt"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"math/big"
	"strings"
)

var multiSendABI, _ = abi.JSON(strings.NewReader(multiSendAbi))

const multiSendAbi = `[
    {
        "constant": false,
        "inputs": [
            {
                "internalType": "bytes",
                "name": "transactions",
                "type": "bytes"
            }
        ],
        "name": "multiSend",
        "outputs": [

        ],
        "payable": false,
        "stateMutability": "nonpayable",
        "type": "function"
    }
]`

// CreateSafeMultiSendTransaction
func CreateSafeMultiSendTransaction(txns []SafeTransaction, safeMultiSendAddress string) (SafeTransaction, error) {
	if len(txns) == 0 {
		return SafeTransaction{}, fmt.Errorf("createSafeMultisendTransaction: no transactions provided")
	}

	allPacked := packTransactions(txns)

	dataBytes, err := multiSendABI.Pack("multiSend", allPacked)
	if err != nil {
		return SafeTransaction{}, fmt.Errorf("createSafeMultisendTransaction: abi.Pack multiSend failed: %w", err)
	}

	tx := SafeTransaction{
		To:        safeMultiSendAddress,
		Value:     "0",
		Data:      "0x" + hex.EncodeToString(dataBytes),
		Operation: OperationDelegateCall,
	}
	return tx, nil
}

// packTransactions
func packTransactions(txns []SafeTransaction) []byte {
	var packedData []byte

	for _, tx := range txns {
		packedTx := PackSingleTransaction(tx)
		packedData = append(packedData, packedTx...)
	}
	return packedData
}

func PackSingleTransaction(tx SafeTransaction) []byte {
	var packed []byte

	packed = append(packed, byte(tx.Operation))

	toAddress := common.HexToAddress(tx.To)
	packed = append(packed, toAddress.Bytes()...)

	value := parseBigInt(tx.Value)
	valuePadded := common.LeftPadBytes(value.Bytes(), 32)
	packed = append(packed, valuePadded...)

	dataBytes := hexutil.MustDecode(tx.Data)
	dataLength := big.NewInt(int64(len(dataBytes)))
	dataLengthPadded := common.LeftPadBytes(dataLength.Bytes(), 32)
	packed = append(packed, dataLengthPadded...)

	packed = append(packed, dataBytes...)

	return packed
}

// parseBigInt
func parseBigInt(valueStr string) *big.Int {
	if valueStr == "" {
		return big.NewInt(0)
	}

	if strings.HasPrefix(valueStr, "0x") {
		if value, ok := new(big.Int).SetString(valueStr[2:], 16); ok {
			return value
		}
	}

	if value, ok := new(big.Int).SetString(valueStr, 10); ok {
		return value
	}

	return big.NewInt(0)
}

type splitSig struct {
	R *big.Int
	S *big.Int
	V uint8
}

func SplitAndPackSignature(sigBytes string) (string, error) {
	sig, err := splitSignature(sigBytes)
	if err != nil {
		return "", fmt.Errorf("split signature failed: %w", err)
	}

	packedSig := packSignature(sig.R, sig.S, sig.V)
	return packedSig, nil
}

func splitSignature(sig string) (*splitSig, error) {
	if len(sig) < 2 {
		return nil, fmt.Errorf("signature too short")
	}
	if sig[:2] == "0x" {
		sig = sig[2:]
	}

	if len(sig) != 130 {
		return nil, fmt.Errorf("invalid signature length: %d", len(sig))
	}

	vHex := sig[128:130]
	v, err := hex.DecodeString(vHex)
	if err != nil {
		return nil, fmt.Errorf("decode v failed: %w", err)
	}

	vValue := v[0]

	switch vValue {
	case 0, 1:
		vValue += 27
	case 27, 28:
		vValue += 4
		break
	default:
		return nil, fmt.Errorf("invalid signature v value: %d", vValue)
	}

	rHex := sig[0:64]
	sHex := sig[64:128]

	r := new(big.Int)
	r.SetString(rHex, 16)

	s := new(big.Int)
	s.SetString(sHex, 16)

	return &splitSig{R: r, S: s, V: vValue}, nil
}

func packSignature(r, s *big.Int, v uint8) string {
	rBytes := padBytes(r.Bytes(), 32)
	sBytes := padBytes(s.Bytes(), 32)
	vBytes := []byte{v}

	packed := append(rBytes, sBytes...)
	packed = append(packed, vBytes...)

	return "0x" + hex.EncodeToString(packed)
}

func padBytes(data []byte, length int) []byte {
	if len(data) >= length {
		return data[:length]
	}

	padded := make([]byte, length)
	copy(padded[length-len(data):], data)
	return padded
}
