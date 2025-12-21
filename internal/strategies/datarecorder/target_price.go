package datarecorder

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/betbot/gobet/clob/rtds"
	"github.com/betbot/gobet/pkg/logger"
)

// TargetPriceFetcher 目标价获取器
type TargetPriceFetcher struct {
	useRTDSFallback bool
	rtdsClient      *rtds.Client
}

// NewTargetPriceFetcher 创建新的目标价获取器
func NewTargetPriceFetcher(useRTDSFallback bool, rtdsClient *rtds.Client) *TargetPriceFetcher {
	return &TargetPriceFetcher{
		useRTDSFallback: useRTDSFallback,
		rtdsClient:      rtdsClient,
	}
}

// CryptoPriceAPIResponse API 响应结构
type CryptoPriceAPIResponse struct {
	OpenPrice  *float64 `json:"openPrice"`  // 开盘价（本周期目标价）
	ClosePrice *float64 `json:"closePrice"` // 收盘价（可能为 null）
	Timestamp  int64    `json:"timestamp"`
	Completed  bool     `json:"completed"`
	Incomplete bool     `json:"incomplete"`
	Cached     bool     `json:"cached"`
}

// FetchTargetPrice 获取目标价（上一个周期的收盘价）
func (tpf *TargetPriceFetcher) FetchTargetPrice(ctx context.Context, currentCycleStart int64) (float64, error) {
	// 计算上一个周期的开始和结束时间
	prevCycleStart := currentCycleStart - 900 // 15 分钟 = 900 秒
	prevCycleEnd := currentCycleStart

	// 方法 A：优先使用 API
	price, err := tpf.fetchFromAPI(ctx, prevCycleStart, prevCycleEnd)
	if err == nil {
		logger.Infof("从 API 获取目标价成功: %.2f (周期: %d-%d)", price, prevCycleStart, prevCycleEnd)
		return price, nil
	}

	logger.Warnf("从 API 获取目标价失败: %v", err)
	//
	//// 方法 B：尝试从 Binance API 获取（备选方案）
	//price, err = tpf.fetchFromBinance(ctx, prevCycleEnd)
	//if err == nil {
	//	logger.Infof("从 Binance API 获取目标价成功: %.2f (时间: %d)", price, prevCycleEnd)
	//	return price, nil
	//}
	//logger.Warnf("从 Binance API 获取目标价失败: %v", err)
	//
	//// 方法 C：如果启用 RTDS 备选方案，尝试从 RTDS 获取
	//if tpf.useRTDSFallback {
	//	price, err = tpf.fetchFromRTDS(ctx, prevCycleStart, prevCycleEnd)
	//	if err == nil {
	//		logger.Infof("从 RTDS 获取目标价成功: %.2f (周期: %d-%d)", price, prevCycleStart, prevCycleEnd)
	//		return price, nil
	//	}
	//	logger.Warnf("从 RTDS 获取目标价失败: %v", err)
	//}

	return 0, fmt.Errorf("无法获取目标价：API 失败，Binance 失败，RTDS 也失败或未启用")
}

// fetchFromAPI 从 API 获取目标价
func (tpf *TargetPriceFetcher) fetchFromAPI(ctx context.Context, startTime, endTime int64) (float64, error) {
	// 构建 API URL
	startTimeStr := time.Unix(startTime, 0).UTC().Format(time.RFC3339)
	endTimeStr := time.Unix(endTime, 0).UTC().Format(time.RFC3339)

	apiURL := fmt.Sprintf(
		"https://polymarket.com/api/crypto/crypto-price?symbol=BTC&eventStartTime=%s&variant=fifteen&endDate=%s",
		url.QueryEscape(startTimeStr),
		url.QueryEscape(endTimeStr),
	)

	//fmt.Println("====api url", apiURL)

	// 创建 HTTP 请求
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return 0, fmt.Errorf("创建请求失败: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "gobet-datarecorder")

	// 创建 HTTP 传输配置，支持代理
	transport := &http.Transport{
		MaxIdleConns:        100,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}

	// 使用环境变量中的代理配置（如果存在）
	proxyURL := os.Getenv("HTTP_PROXY")
	if proxyURL == "" {
		proxyURL = os.Getenv("HTTPS_PROXY")
	}
	if proxyURL != "" {
		if parsed, err := url.Parse(proxyURL); err == nil {
			transport.Proxy = http.ProxyURL(parsed)
		}
	}

	// 发送请求
	client := &http.Client{
		Transport: transport,
		Timeout:   10 * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("API 返回错误状态码 %d: %s", resp.StatusCode, string(body))
	}

	// 读取响应体（用于调试）
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("读取响应体失败: %w", err)
	}

	// 解析响应
	var apiResp CryptoPriceAPIResponse
	if err := json.Unmarshal(bodyBytes, &apiResp); err != nil {
		// 如果解析失败，记录原始响应用于调试
		logger.Warnf("API 响应解析失败，原始响应: %s", string(bodyBytes))
		return 0, fmt.Errorf("解析响应失败: %w", err)
	}

	// 使用 openPrice 作为本周期目标价
	if apiResp.OpenPrice == nil || *apiResp.OpenPrice <= 0 {
		// 记录原始响应用于调试
		logger.Warnf("API 返回的 openPrice 无效，原始响应: %s", string(bodyBytes))
		return 0, fmt.Errorf("API 返回的 openPrice 无效或为空")
	}

	return *apiResp.OpenPrice, nil
}

// fetchFromBinance 从 Binance API 获取 BTC 价格（备选方案）
func (tpf *TargetPriceFetcher) fetchFromBinance(ctx context.Context, timestamp int64) (float64, error) {
	// Binance K线 API：获取指定时间点的 BTC/USDT 价格
	// 使用 1 分钟 K线，获取最接近的时间点
	apiURL := fmt.Sprintf("https://api.binance.com/api/v3/klines?symbol=BTCUSDT&interval=1m&limit=1&endTime=%d", timestamp*1000)

	// 创建 HTTP 请求
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return 0, fmt.Errorf("创建请求失败: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "gobet-datarecorder")

	// 创建 HTTP 传输配置，支持代理
	transport := &http.Transport{
		MaxIdleConns:        100,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}

	// 使用环境变量中的代理配置（如果存在）
	proxyURL := os.Getenv("HTTP_PROXY")
	if proxyURL == "" {
		proxyURL = os.Getenv("HTTPS_PROXY")
	}
	if proxyURL != "" {
		if parsed, err := url.Parse(proxyURL); err == nil {
			transport.Proxy = http.ProxyURL(parsed)
		}
	}

	// 发送请求
	client := &http.Client{
		Transport: transport,
		Timeout:   10 * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("Binance API 返回错误状态码 %d: %s", resp.StatusCode, string(body))
	}

	// 解析响应：Binance K线数据格式为二维数组
	var klines [][]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&klines); err != nil {
		return 0, fmt.Errorf("解析响应失败: %w", err)
	}

	if len(klines) == 0 || len(klines[0]) < 5 {
		return 0, fmt.Errorf("Binance API 返回的数据格式无效")
	}

	// K线数据格式：[开盘时间, 开盘价, 最高价, 最低价, 收盘价, ...]
	// 使用收盘价（索引 4）
	closePriceStr, ok := klines[0][4].(string)
	if !ok {
		return 0, fmt.Errorf("Binance API 返回的价格格式无效")
	}

	var closePrice float64
	if _, err := fmt.Sscanf(closePriceStr, "%f", &closePrice); err != nil {
		return 0, fmt.Errorf("解析价格失败: %w", err)
	}

	if closePrice <= 0 {
		return 0, fmt.Errorf("Binance API 返回的价格无效: %.2f", closePrice)
	}

	return closePrice, nil
}

// fetchFromRTDS 从 RTDS 获取目标价（备选方案）
// 注意：RTDS 主要提供实时数据，历史数据支持可能有限
// 这里尝试从 RTDS 获取，如果失败则返回错误
func (tpf *TargetPriceFetcher) fetchFromRTDS(ctx context.Context, startTime, endTime int64) (float64, error) {
	// RTDS 主要提供实时数据流，历史数据支持有限
	// 这里可以尝试从 RTDS 的历史数据中获取，但需要检查 RTDS 是否支持
	// 目前 RTDS 客户端主要支持实时订阅，历史数据可能需要通过其他方式获取

	// 如果 RTDS 客户端未连接或未提供历史数据支持，返回错误
	if tpf.rtdsClient == nil || !tpf.rtdsClient.IsConnected() {
		return 0, fmt.Errorf("RTDS 客户端未连接")
	}

	// 注意：RTDS 的 crypto_prices 主题主要提供实时更新
	// 历史数据可能需要通过其他 API 获取
	// 这里暂时返回错误，表示 RTDS 不支持历史数据获取
	return 0, fmt.Errorf("RTDS 不支持历史数据获取，请使用 API 方法")
}
