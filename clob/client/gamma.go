package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/betbot/gobet/pkg/ratelimit"
)

var (
	gammaRateLimiter *ratelimit.RateLimitManager
	gammaRateLimitOnce sync.Once
)

// GammaMarket Gamma API 市场数据结构
type GammaMarket struct {
	ID            string `json:"id"`
	Question      string `json:"question"`
	ConditionID   string `json:"conditionId"`
	Slug          string `json:"slug"`
	ClobTokenIDs  string `json:"clobTokenIds"`
	EndDate       string `json:"endDate"`
	StartDate     string `json:"startDate"`
	Category      string `json:"category"`
}

// getGammaRateLimiter 获取 Gamma API 速率限制器（单例）
func getGammaRateLimiter() *ratelimit.RateLimitManager {
	gammaRateLimitOnce.Do(func() {
		gammaRateLimiter = ratelimit.NewRateLimitManager()
	})
	return gammaRateLimiter
}

// getProxyFromEnv 从环境变量获取代理 URL，默认使用 http://127.0.0.1:15236
func getProxyFromEnv() string {
	proxyVars := []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"}
	for _, v := range proxyVars {
		if proxy := os.Getenv(v); proxy != "" {
			return proxy
		}
	}
	// 默认使用代理
	return "http://127.0.0.1:15236"
}

// FetchMarketFromGamma 从 Gamma API 获取市场数据（独立函数，不依赖 Client）
func FetchMarketFromGamma(ctx context.Context, slug string) (*GammaMarket, error) {
	// 速率限制：等待直到允许请求
	rateLimiter := getGammaRateLimiter()
	if err := rateLimiter.Wait(ctx, "gamma:markets:get"); err != nil {
		return nil, fmt.Errorf("速率限制等待失败: %w", err)
	}

	gammaURL := "https://gamma-api.polymarket.com/markets"
	
	// 构建查询参数
	params := url.Values{}
	params.Set("slug", slug)
	params.Set("closed", "false")
	
	fullURL := fmt.Sprintf("%s?%s", gammaURL, params.Encode())
	
	// 创建 HTTP 传输配置，默认使用代理
	transport := &http.Transport{
		MaxIdleConns:        100,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	
	// 必须使用代理
	proxyURL := getProxyFromEnv()
	proxyURLParsed, parseErr := url.Parse(proxyURL)
	if parseErr == nil {
		transport.Proxy = http.ProxyURL(proxyURLParsed)
		log.Printf("使用代理获取市场数据: %s", proxyURL)
	} else {
		// 如果解析失败，使用默认代理
		if defaultProxy, err := url.Parse("http://127.0.0.1:15236"); err == nil {
			transport.Proxy = http.ProxyURL(defaultProxy)
			log.Printf("警告: 解析代理 URL 失败，使用默认代理: http://127.0.0.1:15236")
		} else {
			log.Printf("错误: 无法设置代理，请求可能失败")
		}
	}
	
	// 创建 HTTP 客户端（增加超时时间到 30 秒）
	client := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
	}
	
	// 重试机制（最多 3 次）
	maxRetries := 3
	var resp *http.Response
	var err error
	
	for i := 0; i < maxRetries; i++ {
		if i > 0 {
			log.Printf("重试获取市场数据 (第 %d/%d 次): %s", i+1, maxRetries, slug)
			// 递增延迟：2秒、4秒
			time.Sleep(time.Duration(i) * 2 * time.Second)
		}
		
		// 创建 HTTP 请求
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
		if err != nil {
			return nil, fmt.Errorf("创建请求失败: %w", err)
		}
		
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "gobet-clob")
		
		resp, err = client.Do(req)
		if err == nil && resp != nil {
			// 检查响应状态码
			if resp.StatusCode == http.StatusOK {
				break
			}
			// 如果状态码不是 200，关闭响应体并继续重试
			statusCode := resp.StatusCode
			resp.Body.Close()
			resp = nil
			err = fmt.Errorf("HTTP 错误 %d", statusCode)
		}
		
		if err != nil {
			log.Printf("获取市场数据失败 (尝试 %d/%d): %v", i+1, maxRetries, err)
		}
	}
	
	if err != nil || resp == nil {
		return nil, fmt.Errorf("请求失败（已重试 %d 次）: %w", maxRetries, err)
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP 错误 %d: %s", resp.StatusCode, string(bodyBytes))
	}
	
	// 解析响应（Gamma API 返回数组）
	var markets []GammaMarket
	if err := json.NewDecoder(resp.Body).Decode(&markets); err != nil {
		return nil, fmt.Errorf("解析响应失败: %w", err)
	}
	
	if len(markets) == 0 {
		return nil, fmt.Errorf("未找到市场: %s", slug)
	}
	
	return &markets[0], nil
}

// FetchMultipleMarketsFromGamma 批量获取市场数据（独立函数）
func FetchMultipleMarketsFromGamma(ctx context.Context, slugs []string, delayMs int) ([]*GammaMarket, error) {
	markets := make([]*GammaMarket, 0)
	
	for i, slug := range slugs {
		market, err := FetchMarketFromGamma(ctx, slug)
		if err != nil {
			log.Printf("警告: 获取市场失败 %s: %v", slug, err)
			continue
		}
		
		markets = append(markets, market)
		
		// 速率限制
		if i < len(slugs)-1 && delayMs > 0 {
			time.Sleep(time.Duration(delayMs) * time.Millisecond)
		}
		
		// 进度日志
		if (i+1)%10 == 0 {
			log.Printf("进度: %d/%d", i+1, len(slugs))
		}
	}
	
	return markets, nil
}

