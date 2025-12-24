package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// BridgeClient 对齐 poly-sdk 的 BridgeClient：用于“跨链入金（自动 bridge+swap 到 Polygon 的 USDC.e）”。
//
// 文档：
// - Base URL: https://bridge.polymarket.com
// - POST /deposit
// - GET  /supported-assets
type BridgeClient struct {
	baseURL string
	http    *http.Client
}

func NewBridgeClient() *BridgeClient {
	return NewBridgeClientWithBaseURL("https://bridge.polymarket.com")
}

func NewBridgeClientWithBaseURL(baseURL string) *BridgeClient {
	return &BridgeClient{
		baseURL: baseURL,
		http: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

type BridgeSupportedAssetsResponse struct {
	SupportedAssets []struct {
		ChainID   string `json:"chainId"`
		ChainName string `json:"chainName"`
		Token     struct {
			Name     string `json:"name"`
			Symbol   string `json:"symbol"`
			Address  string `json:"address"`
			Decimals int    `json:"decimals"`
		} `json:"token"`
		MinCheckoutUsd float64 `json:"minCheckoutUsd"`
	} `json:"supportedAssets"`
}

func (c *BridgeClient) GetSupportedAssets(ctx context.Context) (*BridgeSupportedAssetsResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/supported-assets", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("bridge supported-assets http %d", resp.StatusCode)
	}
	var out BridgeSupportedAssetsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

type BridgeCreateDepositRequest struct {
	Address string `json:"address"`
}

type BridgeCreateDepositResponse struct {
	Address          string `json:"address"`
	DepositAddresses []struct {
		ChainID       string `json:"chainId"`
		ChainName     string `json:"chainName"`
		TokenAddress  string `json:"tokenAddress"`
		TokenSymbol   string `json:"tokenSymbol"`
		DepositAddress string `json:"depositAddress"`
	} `json:"depositAddresses"`
}

func (c *BridgeClient) CreateDeposit(ctx context.Context, address string) (*BridgeCreateDepositResponse, error) {
	body, _ := json.Marshal(BridgeCreateDepositRequest{Address: address})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/deposit", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("bridge deposit http %d", resp.StatusCode)
	}
	var out BridgeCreateDepositResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

