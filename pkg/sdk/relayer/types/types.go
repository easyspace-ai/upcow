package types

import "time"

type TransactionType string

const (
	TransactionTypeSAFE       TransactionType = "SAFE"
	TransactionTypeSAFECreate TransactionType = "SAFE-CREATE"
)

type SignatureParams struct {
	GasPrice *string `json:"gasPrice,omitempty"`

	// SAFE sig parameters
	Operation      *string `json:"operation,omitempty"`
	SafeTxnGas     *string `json:"safeTxnGas,omitempty"`
	BaseGas        *string `json:"baseGas,omitempty"`
	GasToken       *string `json:"gasToken,omitempty"`
	RefundReceiver *string `json:"refundReceiver,omitempty"`

	// SAFE CREATE sig parameters
	PaymentToken    *string `json:"paymentToken,omitempty"`
	Payment         *string `json:"payment,omitempty"`
	PaymentReceiver *string `json:"paymentReceiver,omitempty"`
}

type NoncePayload struct {
	Nonce string `json:"nonce"`
}

type TransactionRequest struct {
	Type            string          `json:"type"`
	From            string          `json:"from"`
	To              string          `json:"to"`
	ProxyWallet     *string         `json:"proxyWallet,omitempty"`
	Data            string          `json:"data"`
	Nonce           *string         `json:"nonce,omitempty"`
	Signature       string          `json:"signature"`
	SignatureParams SignatureParams `json:"signatureParams"`
	Metadata        *string         `json:"metadata,omitempty"`
}

type OperationType uint8

const (
	OperationCall         OperationType = iota // 0
	OperationDelegateCall                      // 1
)

type SafeTransaction struct {
	To        string        `json:"to"`
	Operation OperationType `json:"operation"` // 0/1
	Data      string        `json:"data"`
	Value     string        `json:"value"`
}

type SafeTransactionArgs struct {
	From         string            `json:"from"`
	Nonce        string            `json:"nonce"`
	ChainID      int64             `json:"chainId"`
	Transactions []SafeTransaction `json:"transactions"`
}

type SafeCreateTransactionArgs struct {
	From            string `json:"from"`
	ChainID         int64  `json:"chainId"`
	PaymentToken    string `json:"paymentToken"`
	Payment         string `json:"payment"`
	PaymentReceiver string `json:"paymentReceiver"`
}

type RelayerTransactionState string

const (
	RelayerStateNew       RelayerTransactionState = "STATE_NEW"
	RelayerStateExecuted  RelayerTransactionState = "STATE_EXECUTED"
	RelayerStateMined     RelayerTransactionState = "STATE_MINED"
	RelayerStateInvalid   RelayerTransactionState = "STATE_INVALID"
	RelayerStateConfirmed RelayerTransactionState = "STATE_CONFIRMED"
	RelayerStateFailed    RelayerTransactionState = "STATE_FAILED"
)

type RelayerTransaction struct {
	TransactionID   string                  `json:"transactionID"`
	TransactionHash string                  `json:"transactionHash"`
	From            string                  `json:"from"`
	To              string                  `json:"to"`
	ProxyAddress    string                  `json:"proxyAddress"`
	Data            string                  `json:"data"`
	Nonce           string                  `json:"nonce"`
	Value           string                  `json:"value"`
	State           RelayerTransactionState `json:"state"`
	Type            string                  `json:"type"`
	Metadata        string                  `json:"metadata"`
	CreatedAt       time.Time               `json:"createdAt"`
	UpdatedAt       time.Time               `json:"updatedAt"`
}

type RelayerTransactionResponse struct {
	TransactionID   string `json:"transactionID"`
	State           string `json:"state"`
	Hash            string `json:"hash"`
	TransactionHash string `json:"transactionHash"`
}

type GetDeployedResponse struct {
	Deployed bool `json:"deployed"`
}
