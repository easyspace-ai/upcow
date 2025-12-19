package client

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/pkg/ratelimit"
)

// Client CLOB 客户端
type Client struct {
	host         string
	chainID      types.Chain
	authConfig   *AuthConfig
	httpClient   *httpClient
	tickSizes    types.TickSizes
	negRisk      types.NegRisk
	feeRates     types.FeeRates
	rateLimiter  *ratelimit.RateLimitManager
}

// NewClient 创建新的 CLOB 客户端
func NewClient(
	host string,
	chainID types.Chain,
	privateKey *ecdsa.PrivateKey,
	creds *types.ApiKeyCreds,
) *Client {
	authConfig := &AuthConfig{
		PrivateKey: privateKey,
		ChainID:    chainID,
		Creds:      creds,
	}

	// 解析代理 URL（默认使用代理）
	proxyStr := getProxyURL()
	var proxyURL *url.URL
	useProxy := true // 默认启用代理
	if parsed, err := url.Parse(proxyStr); err == nil {
		proxyURL = parsed
	} else {
		// 如果解析失败，使用默认代理
		if defaultProxy, err := url.Parse("http://127.0.0.1:15236"); err == nil {
			proxyURL = defaultProxy
		} else {
			useProxy = false
		}
	}

	httpClient := newHTTPClient(host, authConfig, useProxy, proxyURL)

	return &Client{
		host:        strings.TrimSuffix(host, "/"),
		chainID:     chainID,
		authConfig:  authConfig,
		httpClient:  httpClient,
		tickSizes:   make(types.TickSizes),
		negRisk:     make(types.NegRisk),
		feeRates:    make(types.FeeRates),
		rateLimiter: ratelimit.NewRateLimitManager(),
	}
}

// getProxyURL 从环境变量获取代理 URL，默认使用 http://127.0.0.1:15236
func getProxyURL() string {
	// 检查常见的代理环境变量
	proxyVars := []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"}
	for _, v := range proxyVars {
		if val := os.Getenv(v); val != "" {
			return val
		}
	}
	// 默认使用代理
	return "http://127.0.0.1:15236"
}

// GetHost 获取主机地址
func (c *Client) GetHost() string {
	return c.host
}

// GetChainID 获取链 ID
func (c *Client) GetChainID() types.Chain {
	return c.chainID
}

// FetchMarketFromGamma 从 Gamma API 获取市场数据（委托给 gamma.go）
func (c *Client) FetchMarketFromGamma(ctx context.Context, slug string) (*GammaMarket, error) {
	return FetchMarketFromGamma(ctx, slug)
}

// FetchMultipleMarketsFromGamma 批量获取市场数据（委托给 gamma.go）
func (c *Client) FetchMultipleMarketsFromGamma(ctx context.Context, slugs []string, delayMs int) ([]*GammaMarket, error) {
	return FetchMultipleMarketsFromGamma(ctx, slugs, delayMs)
}

// NewCTFClient 创建CTF客户端用于拆分和合并操作
// rpcURL: 以太坊RPC节点URL（例如：https://polygon-rpc.com）
func (c *Client) NewCTFClient(rpcURL string) (*CTFClient, error) {
	if c.authConfig == nil || c.authConfig.PrivateKey == nil {
		return nil, fmt.Errorf("客户端未配置私钥，无法创建CTF客户端")
	}
	return NewCTFClient(rpcURL, c.chainID, c.authConfig.PrivateKey)
}

