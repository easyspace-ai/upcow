package types

import (
	"github.com/betbot/gobet/pkg/sdk/types"
	"github.com/polymarket/go-order-utils/pkg/model"
)

// L2HeaderArgs L2头部参数
type L2HeaderArgs struct {
	Method      string `json:"method"`
	RequestPath string `json:"requestPath"`
	Body        string `json:"body,omitempty"`
}

// L1PolyHeader L1多签名头部
type L1PolyHeader struct {
	PolyAddress   string `json:"POLY_ADDRESS"`
	PolySignature string `json:"POLY_SIGNATURE"`
	PolyTimestamp string `json:"POLY_TIMESTAMP"`
	PolyNonce     string `json:"POLY_NONCE"`
}

// L2PolyHeader L2 API密钥头部
type L2PolyHeader struct {
	PolyAddress    string `json:"POLY_ADDRESS"`
	PolySignature  string `json:"POLY_SIGNATURE"`
	PolyTimestamp  string `json:"POLY_TIMESTAMP"`
	PolyApiKey     string `json:"POLY_API_KEY"`
	PolyPassphrase string `json:"POLY_PASSPHRASE"`
}

// L2WithBuilderHeader 带Builder的L2头部
type L2WithBuilderHeader struct {
	L2PolyHeader
	PolyBuilderApiKey     string `json:"POLY_BUILDER_API_KEY"`
	PolyBuilderTimestamp  string `json:"POLY_BUILDER_TIMESTAMP"`
	PolyBuilderPassphrase string `json:"POLY_BUILDER_PASSPHRASE"`
	PolyBuilderSignature  string `json:"POLY_BUILDER_SIGNATURE"`
}

type Side string

const (
	BUY  Side = "BUY"
	SELL      = "SELL"
)

type OrderType string

const (
	OrderTypeGTC OrderType = "GTC"
	OrderTypeFOK OrderType = "FOK"
	OrderTypeGTD OrderType = "GTD"
	OrderTypeFAK OrderType = "FAK"
)

type PostOrdersArgs struct {
	Order     model.SignedOrder `json:"order"`
	OrderType OrderType         `json:"orderType"`
}

type NewOrder struct {
	Order     Order     `json:"order"`
	Owner     string    `json:"owner"`
	OrderType OrderType `json:"orderType"`
	DeferExec bool      `json:"deferExec"`
}

type Order struct {
	Salt          int64               `json:"salt"`
	Maker         string              `json:"maker"`
	Signer        string              `json:"signer"`
	Taker         string              `json:"taker"`
	TokenID       string              `json:"tokenId"`
	MakerAmount   string              `json:"makerAmount"`
	TakerAmount   string              `json:"takerAmount"`
	Expiration    string              `json:"expiration"`
	Nonce         string              `json:"nonce"`
	FeeRateBps    string              `json:"feeRateBps"`
	Side          Side                `json:"side"`
	SignatureType model.SignatureType `json:"signatureType"`
	Signature     string              `json:"signature"`
}

type UserOrder struct {
	TokenID    string   `json:"tokenID"`              // TokenID of the Conditional token asset being traded
	Price      float64  `json:"price"`                // Price used to create the order
	Size       float64  `json:"size"`                 // Size in terms of the ConditionalToken in limit, 	// market BUY orders: $$$ Amount to buy // SELL orders: Shares to sell
	Side       Side     `json:"side"`                 // Side of the order
	FeeRateBps *float64 `json:"feeRateBps,omitempty"` // Fee rate, in basis points, charged to the order maker, charged on proceeds
	Nonce      *int64   `json:"nonce,omitempty"`      // Nonce used for onchain cancellations
	Expiration *int64   `json:"expiration,omitempty"` // Timestamp after which the order is expired.
	Taker      *string  `json:"taker,omitempty"`      // Address of the order taker. The zero address is used to indicate a public order
}

type CreateOrderOptions struct {
	AuthOption *types.AuthOption

	TickSize TickSize
	NegRisk  bool
}

type OrderPayload struct {
	OrderID string `json:"orderID"`
}

type ApiKeysResponse struct {
	ApiKeys []types.ApiKeyCreds `json:"apiKeys"`
}

type OrderResponse struct {
	Success            bool     `json:"success"`
	ErrorMsg           string   `json:"errorMsg"`
	OrderID            string   `json:"orderID"`
	TransactionsHashes []string `json:"transactionsHashes"`
	Status             string   `json:"status"`
	TakingAmount       string   `json:"takingAmount"`
	MakingAmount       string   `json:"makingAmount"`
}

type TickSize string

const (
	TickSize01    TickSize = "0.1"
	TickSize001   TickSize = "0.01"
	TickSize0001  TickSize = "0.001"
	TickSize00001 TickSize = "0.0001"
)

type TickSizes map[string]TickSize
type NegRisks map[string]bool
type FeeRates map[string]float64
type RewardsPercentages map[string]float64

type OrderBookSummary struct {
	Market       string         `json:"market"`
	AssetID      string         `json:"asset_id"`
	Timestamp    string         `json:"timestamp"`
	Bids         []OrderSummary `json:"bids"`
	Asks         []OrderSummary `json:"asks"`
	MinOrderSize string         `json:"min_order_size"`
	TickSize     string         `json:"tick_size"`
	NegRisk      bool           `json:"neg_risk"`
	Hash         string         `json:"hash"`
}

type OrderSummary struct {
	Price string `json:"price"`
	Size  string `json:"size"`
}

type GetOrderRequest struct {
	ID string `json:"id,omitempty"`
}

// OpenOrder 对象
type OpenOrder struct {
	AssociateTrades []string `json:"associate_trades"`
	ID              string   `json:"id"`
	Status          string   `json:"status"`
	Market          string   `json:"market"`
	OriginalSize    string   `json:"original_size"`
	Outcome         string   `json:"outcome"`
	Owner           string   `json:"owner"`
	Price           string   `json:"price"`
	Side            string   `json:"side"`
	SizeMatched     string   `json:"size_matched"`
	AssetID         string   `json:"asset_id"`
	Expiration      string   `json:"expiration"`
	Type            string   `json:"type"`
	CreatedAt       uint64   `json:"created_at"`
}

type GetActiveOrdersRequest struct {
	ID      string `json:"id,omitempty" url:"id,omitempty"`
	Market  string `json:"market,omitempty" url:"market,omitempty"`
	AssetID string `json:"asset_id,omitempty" url:"asset_id,omitempty"`
}

// OpenOrders 响应体
type OpenOrders struct {
	Data       []OpenOrder `json:"data"`
	NextCursor string      `json:"next_cursor"`
	Limit      int         `json:"limit"`
	Count      int         `json:"count"`
}
