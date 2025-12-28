package client

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/betbot/gobet/clob/signing"
	"github.com/betbot/gobet/clob/types"
)

// CreateOrDeriveAPIKey 创建或推导 API 密钥（L1 方法）
func (c *Client) CreateOrDeriveAPIKey(ctx context.Context, nonce *int64) (*types.ApiKeyCreds, error) {
	if err := c.CanL1Auth(); err != nil {
		return nil, err
	}

	// 构建 L1 认证头
	var n int64 = 0
	if nonce != nil {
		n = *nonce
	}

	headers, err := signing.CreateL1Headers(
		c.authConfig.PrivateKey,
		c.authConfig.ChainID,
		&n,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("创建 L1 认证头失败: %w", err)
	}

	// 转换为 map
	headerMap := map[string]string{
		"POLY_ADDRESS":   headers.PolyAddress,
		"POLY_SIGNATURE":  headers.PolySignature,
		"POLY_TIMESTAMP":  headers.PolyTimestamp,
		"POLY_NONCE":     headers.PolyNonce,
	}

	// 先尝试推导现有 API 密钥
	endpoint := EndpointDeriveAPIKey
	resp, err := c.httpClient.get(endpoint, headerMap, nil)
	if err != nil {
		// 网络错误，直接尝试创建
	} else if resp != nil {
		// 检查状态码：400 表示没有现有 API 密钥，需要创建
		if resp.StatusCode == http.StatusOK {
			// 推导成功，解析响应
			var apiKeyRaw types.ApiKeyRaw
			if err := parseResponse(resp, &apiKeyRaw); err == nil {
				return &types.ApiKeyCreds{
					Key:       apiKeyRaw.ApiKey,
					Secret:    apiKeyRaw.Secret,
					Passphrase: apiKeyRaw.Passphrase,
				}, nil
			}
			// 解析失败，返回错误
			return nil, fmt.Errorf("解析 API 密钥响应失败: %w", err)
		} else if resp.StatusCode == http.StatusBadRequest {
			// 400 错误：没有现有 API 密钥，需要创建新的
			// 读取并关闭响应体（避免资源泄漏）
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			// 继续执行创建逻辑
		} else {
			// 其他错误，读取错误信息后返回
			bodyBytes, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return nil, fmt.Errorf("推导 API 密钥失败: HTTP %d: %s", resp.StatusCode, string(bodyBytes))
		}
	}

	// 推导失败（可能是账户还没有 API 密钥），尝试创建新的
	endpoint = EndpointCreateAPIKey
	createBody := map[string]interface{}{}
	resp, err = c.httpClient.post(endpoint, headerMap, createBody)
	if err != nil {
		return nil, fmt.Errorf("创建 API 密钥失败: %w", err)
	}

	var apiKeyRaw types.ApiKeyRaw
	if err := parseResponse(resp, &apiKeyRaw); err != nil {
		return nil, fmt.Errorf("解析 API 密钥响应失败: %w", err)
	}

	// 转换为 ApiKeyCreds
	creds := &types.ApiKeyCreds{
		Key:       apiKeyRaw.ApiKey,
		Secret:    apiKeyRaw.Secret,
		Passphrase: apiKeyRaw.Passphrase,
	}

	return creds, nil
}

// DeriveAPIKey 推导现有 API 密钥
func (c *Client) DeriveAPIKey(ctx context.Context, nonce int64) (*types.ApiKeyCreds, error) {
	return c.CreateOrDeriveAPIKey(ctx, &nonce)
}

// CreateAPIKey 创建新的 API 密钥
func (c *Client) CreateAPIKey(ctx context.Context) (*types.ApiKeyCreds, error) {
	return c.CreateOrDeriveAPIKey(ctx, nil)
}

