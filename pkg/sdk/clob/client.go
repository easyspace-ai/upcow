package clob

import (
	"github.com/betbot/gobet/pkg/sdk/clob/types"
	http2 "github.com/betbot/gobet/pkg/sdk/http"
	"github.com/betbot/gobet/pkg/sdk/signing"
	sdktypes "github.com/betbot/gobet/pkg/sdk/types"
	"github.com/betbot/gobet/pkg/sdk/types/utils"
	"github.com/pkg/errors"
	"math/big"
	"net/http"
	"strings"
)

type Client struct {
	client *http2.Client

	chainId *big.Int
	signFn  signing.SignatureFunc

	builderApiKeyCreds *sdktypes.BuilderApiKeyCreds

	orderBuilder *OrderBuilder

	tickSizes types.TickSizes
	negRisk   types.NegRisks
	feeRates  types.FeeRates
}

func NewClient(host string, chainId *big.Int, signFn signing.SignatureFunc, builderApiKeyCreds *sdktypes.BuilderApiKeyCreds) *Client {
	if strings.HasSuffix(host, "/") {
		host = host[:len(host)-1]
	}
	return &Client{
		client:             http2.NewClient(host),
		chainId:            chainId,
		builderApiKeyCreds: builderApiKeyCreds,
		orderBuilder:       NewOrderBuilder(chainId, signFn),
		signFn:             signFn,
		tickSizes:          make(types.TickSizes, 500),
		negRisk:            make(types.NegRisks, 500),
		feeRates:           make(types.FeeRates, 500),
	}
}

func (c *Client) WithSignatureFunc(signFn signing.SignatureFunc) error {
	if c.signFn != nil {
		return errors.New("signFn already set")
	}
	c.signFn = signFn
	return c.orderBuilder.WithSignatureFunc(signFn)
}

func (c *Client) GetTickSize(tokenID string) (string, error) {
	if size, ok := c.tickSizes[tokenID]; ok {
		return string(size), nil
	}
	var resp map[string]float64
	res, err := c.client.DoRequest(http.MethodGet, types.GET_TICK_SIZE, &http2.RequestOptions{
		Params: map[string]any{"token_id": tokenID},
	}, &resp)
	if _, e := http2.ParseHTTPError(res, err); e != nil {
		return "", e
	}
	if c.tickSizes == nil {
		c.tickSizes = make(types.TickSizes)
	}

	tickSize := utils.Float64ToDecimal(resp["minimum_tick_size"]).String()
	c.tickSizes[tokenID] = types.TickSize(tickSize)

	return tickSize, nil
}

// GetNegRisk
func (c *Client) GetNegRisk(tokenID string) (bool, error) {
	if neg, ok := c.negRisk[tokenID]; ok {
		return neg, nil
	}

	var resp map[string]bool
	res, err := c.client.DoRequest(http.MethodGet, types.GET_NEG_RISK, &http2.RequestOptions{
		Params: map[string]any{"token_id": tokenID},
	}, &resp)
	if _, e := http2.ParseHTTPError(res, err); e != nil {
		return false, e
	}

	if c.negRisk == nil {
		c.negRisk = make(types.NegRisks)
	}

	negRisk := resp["neg_risk"]
	c.negRisk[tokenID] = negRisk

	return negRisk, nil
}

// GetFeeRateBps
func (c *Client) GetFeeRateBps(tokenID string) (float64, error) {
	if fee, ok := c.feeRates[tokenID]; ok {
		return fee, nil
	}

	var resp map[string]float64
	res, err := c.client.DoRequest(http.MethodGet, types.GET_FEE_RATE, &http2.RequestOptions{
		Params: map[string]any{"token_id": tokenID},
	}, &resp)
	if _, e := http2.ParseHTTPError(res, err); e != nil {
		return 0, e
	}

	if c.feeRates == nil {
		c.feeRates = make(types.FeeRates)
	}

	baseFee := resp["base_fee"]
	c.feeRates[tokenID] = baseFee

	return baseFee, nil
}

func (c *Client) GetOrderBook(tokenID string) (*types.OrderBookSummary, error) {
	var resp types.OrderBookSummary
	res, err := c.client.DoRequest(http.MethodGet, types.GET_ORDER_BOOK, &http2.RequestOptions{
		Params: map[string]any{"token_id": tokenID},
	}, &resp)
	if _, e := http2.ParseHTTPError(res, err); e != nil {
		return nil, errors.Wrap(e, "get order book")
	}
	return &resp, nil
}

func (c *Client) GetMarketPrice(tokenID, side string) (string, error) {
	var resp map[string]string
	res, err := c.client.DoRequest(http.MethodGet, types.GET_PRICE, &http2.RequestOptions{
		Params: map[string]any{"token_id": tokenID, "side": side},
	}, &resp)
	if _, e := http2.ParseHTTPError(res, err); e != nil {
		return "", errors.Wrap(e, "get market price")
	}
	return resp["price"], nil
}
