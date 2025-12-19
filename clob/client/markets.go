package client

import (
	"context"
	"fmt"

	"github.com/betbot/gobet/clob/signing"
	"github.com/betbot/gobet/clob/types"
)

// GetMarkets 获取市场列表
func (c *Client) GetMarkets(ctx context.Context, slug *string) ([]interface{}, error) {
	queryParams := make(map[string]string)
	if slug != nil {
		queryParams["slug"] = *slug
	}

	resp, err := c.httpClient.get(EndpointGetMarkets, nil, queryParams)
	if err != nil {
		return nil, fmt.Errorf("获取市场列表失败: %w", err)
	}

	var markets []interface{}
	if err := parseResponse(resp, &markets); err != nil {
		return nil, err
	}

	return markets, nil
}

// GetOrderBook 获取订单簿
func (c *Client) GetOrderBook(ctx context.Context, tokenID string, side *types.Side) (*types.OrderBookSummary, error) {
	queryParams := map[string]string{
		"token_id": tokenID,
	}
	if side != nil {
		queryParams["side"] = string(*side)
	}

	resp, err := c.httpClient.get(EndpointGetOrderBook, nil, queryParams)
	if err != nil {
		return nil, fmt.Errorf("获取订单簿失败: %w", err)
	}

	var book types.OrderBookSummary
	if err := parseResponse(resp, &book); err != nil {
		return nil, err
	}

	return &book, nil
}

// GetPrice 获取价格
func (c *Client) GetPrice(ctx context.Context, tokenID string) (*types.MarketPrice, error) {
	queryParams := map[string]string{
		"token_id": tokenID,
	}

	resp, err := c.httpClient.get(EndpointGetPrice, nil, queryParams)
	if err != nil {
		return nil, fmt.Errorf("获取价格失败: %w", err)
	}

	var price types.MarketPrice
	if err := parseResponse(resp, &price); err != nil {
		return nil, err
	}

	return &price, nil
}

// GetBalanceAllowance 获取余额和授权（参考 test/clob.go 的实现）
func (c *Client) GetBalanceAllowance(ctx context.Context, params *types.BalanceAllowanceParams) (*types.BalanceAllowanceResponse, error) {
	fmt.Printf("[HTTP DEBUG] GetBalanceAllowance 开始调用\n")

	if err := c.CanL2Auth(); err != nil {
		fmt.Printf("[HTTP DEBUG] CanL2Auth 失败: %v\n", err)
		return nil, err
	}
	fmt.Printf("[HTTP DEBUG] CanL2Auth 通过\n")

	queryParams := map[string]string{
		"asset_type": string(params.AssetType),
	}
	if params.TokenID != nil {
		queryParams["token_id"] = *params.TokenID
	}
	// 添加 signature_type 查询参数（参考 test/clob.go）
	if params.SignatureType != nil {
		queryParams["signature_type"] = fmt.Sprintf("%d", int(*params.SignatureType))
	}

	// 构建 L2 认证头
	l2HeaderArgs := &types.L2HeaderArgs{
		Method:      "GET",
		RequestPath: EndpointGetBalanceAllowance,
		Body:        nil,
	}

	fmt.Printf("[HTTP DEBUG] 准备创建 L2 认证头\n")
	headers, err := signing.CreateL2Headers(
		c.authConfig.PrivateKey,
		c.authConfig.Creds,
		l2HeaderArgs,
		nil,
	)
	if err != nil {
		fmt.Printf("[HTTP DEBUG] 创建 L2 认证头失败: %v\n", err)
		return nil, fmt.Errorf("创建 L2 认证头失败: %w", err)
	}
	fmt.Printf("[HTTP DEBUG] L2 认证头创建成功\n")

	// 调试：显示查询的地址
	fmt.Printf("[HTTP DEBUG] 查询余额的地址: %s\n", headers.PolyAddress)

	// 转换为 map
	headerMap := map[string]string{
		"POLY_ADDRESS":    headers.PolyAddress,
		"POLY_SIGNATURE":  headers.PolySignature,
		"POLY_TIMESTAMP":  headers.PolyTimestamp,
		"POLY_API_KEY":    headers.PolyAPIKey,
		"POLY_PASSPHRASE": headers.PolyPassphrase,
	}

	fmt.Printf("[HTTP DEBUG] 准备调用 httpClient.get, httpClient=%v\n", c.httpClient != nil)
	if c.httpClient == nil {
		return nil, fmt.Errorf("httpClient 为 nil")
	}

	resp, err := c.httpClient.get(EndpointGetBalanceAllowance, headerMap, queryParams)
	fmt.Printf("[HTTP DEBUG] httpClient.get 调用完成, err=%v\n", err)

	if err != nil {
		return nil, fmt.Errorf("获取余额和授权失败: %w", err)
	}

	var balance types.BalanceAllowanceResponse
	if err := parseResponse(resp, &balance); err != nil {
		return nil, err
	}

	// 调试：显示 API 返回的原始响应
	fmt.Printf("[HTTP DEBUG] 余额API响应: Balance=%q, Allowance=%q\n", balance.Balance, balance.Allowance)

	return &balance, nil
}
