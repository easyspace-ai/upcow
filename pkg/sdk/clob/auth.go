package clob

import (
	"github.com/betbot/gobet/pkg/sdk/clob/types"
	"github.com/betbot/gobet/pkg/sdk/headers"
	http2 "github.com/betbot/gobet/pkg/sdk/http"
	sdktypes "github.com/betbot/gobet/pkg/sdk/types"
	"math/big"
	"net/http"
	"time"
)

func (c *Client) EnsureAPIKey(nonce *big.Int, option *sdktypes.AuthOption) (*sdktypes.ApiKeyCreds, error) {
	if creds, err := c.DeriveAPIKey(nonce, option); err == nil {
		option.ApiKeyCreds = creds
		return creds, nil
	}
	creds, err := c.CreateApiKey(nonce, option)
	if err != nil {
		return nil, err
	}
	option.ApiKeyCreds = creds
	return creds, nil
}

func (c *Client) CreateApiKey(nonce *big.Int, option *sdktypes.AuthOption) (*sdktypes.ApiKeyCreds, error) {
	ts := time.Now().Unix()
	l1Headers, err := headers.CreateL1Headers(option.SingerAddress, c.signFn, c.chainId, nonce, &ts)
	if err != nil {
		return nil, err
	}
	var raw *sdktypes.ApiKeyCreds
	resp, err := c.client.DoRequest(http.MethodPost, types.CREATE_API_KEY, &http2.RequestOptions{
		Headers: l1Headers,
	}, &raw)
	if _, e := http2.ParseHTTPError(resp, err); e != nil {
		return nil, e
	}
	return raw, nil
}

func (c *Client) DeriveAPIKey(nonce *big.Int, option *sdktypes.AuthOption) (*sdktypes.ApiKeyCreds, error) {
	ts := time.Now().Unix()
	l1Headers, err := headers.CreateL1Headers(option.SingerAddress, c.signFn, c.chainId, nonce, &ts)
	if err != nil {
		return nil, err
	}
	var raw *sdktypes.ApiKeyCreds
	resp, err := c.client.DoRequest(http.MethodGet, types.DERIVE_API_KEY, &http2.RequestOptions{
		Headers: l1Headers,
	}, &raw)
	if _, e := http2.ParseHTTPError(resp, err); e != nil {
		return nil, e
	}
	return raw, nil
}
