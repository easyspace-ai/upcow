package api

import (
	"log"
	"net/http"
	"net/url"
	"os"
	"time"
)

// createHTTPTransport creates an HTTP transport with optional proxy support
// 仅在环境变量 HTTP_PROXY/HTTPS_PROXY 设置时使用代理，否则直接连接
func createHTTPTransport() *http.Transport {
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 20,
		MaxConnsPerHost:     50,
		IdleConnTimeout:     90 * time.Second,
		DisableKeepAlives:   false,
		ForceAttemptHTTP2:   true,
	}

	// Check for proxy configuration from environment variables first
	proxyURL := os.Getenv("HTTP_PROXY")
	if proxyURL == "" {
		proxyURL = os.Getenv("http_proxy")
	}
	if proxyURL == "" {
		// Try HTTPS_PROXY as well
		proxyURL = os.Getenv("HTTPS_PROXY")
		if proxyURL == "" {
			proxyURL = os.Getenv("https_proxy")
		}
	}

	// 如果环境变量未设置，不使用代理（直接连接）
	if proxyURL == "" {
		log.Printf("[Proxy] 代理未启用，使用直接连接")
	} else {
		log.Printf("[Proxy] 使用代理: %s", proxyURL)
		// 如果代理 URL 已设置，配置代理
		parsedURL, err := url.Parse(proxyURL)
		if err == nil {
			transport.Proxy = http.ProxyURL(parsedURL)
			log.Printf("[Proxy] 代理已配置: %s", parsedURL.Host)
		} else {
			log.Printf("[Proxy] 警告: 解析代理 URL 失败 %s: %v", proxyURL, err)
		}
	}

	return transport
}

// getProxyURL returns the proxy URL to use
// 仅在环境变量 HTTP_PROXY/HTTPS_PROXY 设置时返回代理 URL，否则返回空字符串（不使用代理）
func getProxyURL() string {
	// Check for proxy configuration from environment variables first
	proxyURL := os.Getenv("HTTP_PROXY")
	if proxyURL == "" {
		proxyURL = os.Getenv("http_proxy")
	}
	if proxyURL == "" {
		// Try HTTPS_PROXY as well
		proxyURL = os.Getenv("HTTPS_PROXY")
		if proxyURL == "" {
			proxyURL = os.Getenv("https_proxy")
		}
	}

	// 如果环境变量未设置，返回空字符串（不使用代理）
	if proxyURL == "" {
		log.Printf("[Proxy] 代理未启用，使用直接连接")
	} else {
		log.Printf("[Proxy] 使用代理: %s", proxyURL)
	}

	return proxyURL
}

// getWebSocketProxy returns a proxy function for WebSocket dialer
func getWebSocketProxy() func(*http.Request) (*url.URL, error) {
	proxyURL := getProxyURL()
	if proxyURL == "" {
		return nil
	}

	parsedURL, err := url.Parse(proxyURL)
	if err != nil {
		log.Printf("[Proxy] Warning: Failed to parse proxy URL %s: %v", proxyURL, err)
		return nil
	}

	log.Printf("[Proxy] WebSocket proxy configured: %s", parsedURL.Host)
	return http.ProxyURL(parsedURL)
}
