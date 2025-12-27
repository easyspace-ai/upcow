package client

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// HTTP 调试输出默认关闭（开启方式：设置环境变量 GOBET_HTTP_DEBUG=1）
var httpDebug = os.Getenv("GOBET_HTTP_DEBUG") != ""

// HTTPClient HTTP 客户端接口
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// httpClient HTTP 客户端封装
type httpClient struct {
	client     *http.Client
	host       string
	authConfig *AuthConfig
	useProxy   bool
	proxyURL   *url.URL
}

// newHTTPClient 创建新的 HTTP 客户端（默认使用代理）
func newHTTPClient(host string, authConfig *AuthConfig, useProxy bool, proxyURL *url.URL) *httpClient {
	transport := &http.Transport{
		MaxIdleConns:        100,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}

	// 仅在 useProxy 为 true 且 proxyURL 不为 nil 时使用代理
	if useProxy && proxyURL != nil {
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	// 如果 useProxy 为 false 或 proxyURL 为 nil，则不设置代理（直接连接）

	client := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
	}

	return &httpClient{
		client:     client,
		host:       strings.TrimSuffix(host, "/"),
		authConfig: authConfig,
		useProxy:   useProxy,
		proxyURL:   proxyURL,
	}
}

// get 执行 GET 请求
func (h *httpClient) get(endpoint string, headers map[string]string, params map[string]string) (*http.Response, error) {
	reqURL := h.host + endpoint

	// 先打印初始 URL（在添加参数之前）
	if httpDebug {
		fmt.Printf("[HTTP DEBUG] GET 初始URL: %s\n", reqURL)
	}

	if len(params) > 0 {
		u, err := url.Parse(reqURL)
		if err != nil {
			return nil, fmt.Errorf("解析 URL 失败: %w", err)
		}
		q := u.Query()
		for k, v := range params {
			q.Set(k, v)
		}
		u.RawQuery = q.Encode()
		reqURL = u.String()
		// 打印最终 URL（包含查询参数）
		if httpDebug {
			fmt.Printf("[HTTP DEBUG] GET 最终URL: %s\n", reqURL)
		}
	}

	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}

	// 设置默认头
	h.setDefaultHeaders(req)

	// 为 balance-allowance 端点添加浏览器样式的 headers（参考 test/clob.go）
	if strings.Contains(endpoint, "balance-allowance") {
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Origin", "https://polymarket.com")
		req.Header.Set("Referer", "https://polymarket.com/")
	}

	// 设置自定义头
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	return h.client.Do(req)
}

// post 执行 POST 请求
func (h *httpClient) post(endpoint string, headers map[string]string, body interface{}) (*http.Response, error) {
	reqURL := h.host + endpoint

	var bodyReader io.Reader
	var bodyBytes []byte
	var err error
	if body != nil {
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("序列化请求体失败: %w", err)
		}
		bodyReader = bytes.NewReader(bodyBytes)

		// 调试：打印实际发送的请求
		if httpDebug {
			fmt.Printf("[HTTP DEBUG] POST %s\n", reqURL)
			fmt.Printf("[HTTP DEBUG] Body: %s\n", string(bodyBytes))
		}
	}

	req, err := http.NewRequest(http.MethodPost, reqURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}

	// 设置默认头
	h.setDefaultHeaders(req)

	// 设置自定义头
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	// 记录请求开始时间
	startTime := time.Now()
	resp, err := h.client.Do(req)
	duration := time.Since(startTime)

	if err != nil {
		if httpDebug {
			fmt.Printf("[HTTP DEBUG] 请求失败 (耗时: %v): %v\n", duration, err)
		}
		return nil, err
	}

	if httpDebug {
		fmt.Printf("[HTTP DEBUG] 请求完成 (耗时: %v), 状态码: %d\n", duration, resp.StatusCode)
	}
	return resp, nil
}

// delete 执行 DELETE 请求
func (h *httpClient) delete(endpoint string, headers map[string]string, params map[string]string) (*http.Response, error) {
	reqURL := h.host + endpoint
	if len(params) > 0 {
		u, err := url.Parse(reqURL)
		if err != nil {
			return nil, fmt.Errorf("解析 URL 失败: %w", err)
		}
		q := u.Query()
		for k, v := range params {
			q.Set(k, v)
		}
		u.RawQuery = q.Encode()
		reqURL = u.String()
	}

	req, err := http.NewRequest(http.MethodDelete, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}

	// 设置默认头
	h.setDefaultHeaders(req)

	// 设置自定义头
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	return h.client.Do(req)
}

// setDefaultHeaders 设置默认请求头
func (h *httpClient) setDefaultHeaders(req *http.Request) {
	req.Header.Set("User-Agent", "gobet-clob")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Content-Type", "application/json")

	if req.Method == http.MethodGet {
		req.Header.Set("Accept-Encoding", "gzip")
	}
}

// parseResponse 解析响应
func parseResponse(resp *http.Response, result interface{}) error {
	defer resp.Body.Close()

	// 处理 gzip 压缩的响应
	var reader io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gzipReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return fmt.Errorf("创建 gzip 读取器失败: %w", err)
		}
		defer gzipReader.Close()
		reader = gzipReader
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(reader)
		errorMsg := fmt.Sprintf("HTTP 错误 %d: %s", resp.StatusCode, string(bodyBytes))
		if httpDebug {
			fmt.Printf("[HTTP DEBUG] %s\n", errorMsg)
		}
		// 使用常量 format string，避免 go vet 报错
		return fmt.Errorf("%s", errorMsg)
	}

	if result != nil {
		// 读取响应体以便调试
		bodyBytes, err := io.ReadAll(reader)
		if err != nil {
			return fmt.Errorf("读取响应体失败: %w", err)
		}

		// 如果是余额查询，打印完整响应体
		if strings.Contains(resp.Request.URL.Path, "balance-allowance") {
			if httpDebug {
				fmt.Printf("[HTTP DEBUG] 余额API完整响应体: %s\n", string(bodyBytes))
			}
		}

		if err := json.Unmarshal(bodyBytes, result); err != nil {
			return fmt.Errorf("解析响应失败: %w, 响应体: %s", err, string(bodyBytes))
		}
	}

	return nil
}
