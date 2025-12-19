package services

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/betbot/gobet/clob/client"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/pkg/logger"
)

// MarketDataService 市场数据服务
type MarketDataService struct {
	clobClient *client.Client
	cache      *marketCache
	ctx        context.Context
	cancel     context.CancelFunc
	
	// 预加载任务去重：正在预加载的 slug
	preloadingSlugs map[string]bool
	preloadingMu    sync.RWMutex
}

// marketCache 内存缓存（线程安全）
type marketCache struct {
	markets map[string]*domain.Market // slug -> Market
	mu      sync.RWMutex
	loaded  bool // 是否已加载预加载数据
}

// NewMarketDataService 创建新的市场数据服务
func NewMarketDataService(clobClient *client.Client) *MarketDataService {
	ctx, cancel := context.WithCancel(context.Background())
	return &MarketDataService{
		clobClient: clobClient,
		cache: &marketCache{
			markets: make(map[string]*domain.Market),
		},
		ctx:             ctx,
		cancel:          cancel,
		preloadingSlugs: make(map[string]bool),
	}
}

// Start 启动市场数据服务（异步加载预加载数据，不阻塞）
func (s *MarketDataService) Start() {
	// 异步加载预加载数据（不阻塞启动）
	//go s.loadPreloadedData()

	// 启动后台预加载goroutine（定期预加载未来市场）
	go s.startBackgroundPreload()
}

// Stop 停止市场数据服务
func (s *MarketDataService) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
}

// loadPreloadedData 异步加载预加载数据（从文件）
func (s *MarketDataService) loadPreloadedData() {
	filePath := "data/market-data.json"

	// 检查文件是否存在
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		logger.Debugf("预加载数据文件不存在: %s，将使用 API 获取", filePath)
		return
	}

	// 读取文件
	data, err := os.ReadFile(filePath)
	if err != nil {
		logger.Warnf("读取预加载数据文件失败: %v，将使用 API 获取", err)
		return
	}

	// 解析 JSON
	var marketDataFile struct {
		Markets []struct {
			Slug        string `json:"slug"`
			YesAssetID  string `json:"yesAssetId"`
			NoAssetID   string `json:"noAssetId"`
			ConditionID string `json:"conditionId"`
			Question    string `json:"question"`
			Timestamp   int64  `json:"timestamp"`
		} `json:"markets"`
		GeneratedAt int64 `json:"generatedAt"`
	}

	if err := json.Unmarshal(data, &marketDataFile); err != nil {
		logger.Warnf("解析预加载数据文件失败: %v，将使用 API 获取", err)
		return
	}

	// 加载到内存缓存
	count := 0
	s.cache.mu.Lock()
	for _, m := range marketDataFile.Markets {
		market := &domain.Market{
			Slug:        m.Slug,
			YesAssetID:  m.YesAssetID,
			NoAssetID:   m.NoAssetID,
			ConditionID: m.ConditionID,
			Question:    m.Question,
			Timestamp:   m.Timestamp,
		}
		s.cache.markets[m.Slug] = market
		count++
	}
	s.cache.loaded = true
	s.cache.mu.Unlock()

	logger.Infof("✅ 已加载 %d 个预加载市场数据到内存缓存", count)
}

// startBackgroundPreload 后台预加载未来市场数据（定期运行）
func (s *MarketDataService) startBackgroundPreload() {
	// 每个周期（15分钟）检查一次，预加载接下来2个周期
	// 这样既保证数据充足，又不会过度预加载
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()

	// 立即执行一次（不等待15分钟）
	s.preloadFutureMarkets()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.preloadFutureMarkets()
		}
	}
}

// preloadFutureMarkets 预加载未来市场数据（异步，不阻塞）
// 精简策略：每个周期检查一次，只预加载接下来2个周期
// 这样既保证数据充足，又不会过度预加载，资源占用更少
func (s *MarketDataService) preloadFutureMarkets() {
	// 只预加载接下来2个周期（本周期之后的下一个、下下个）
	// 这样在切换时，下一个周期的数据已经准备好了
	slugs := GenerateNext15MinSlugs(2) // 接下来2个周期（30分钟）

	// 检查哪些市场数据缺失
	missingSlugs := make([]string, 0)

	s.cache.mu.RLock()
	for _, slug := range slugs {
		if _, exists := s.cache.markets[slug]; !exists {
			missingSlugs = append(missingSlugs, slug)
		}
	}
	cacheSize := len(s.cache.markets)
	s.cache.mu.RUnlock()

	if len(missingSlugs) == 0 {
		logger.Debugf("后台预加载检查: 接下来2个周期的市场数据已全部存在（缓存大小: %d）", cacheSize)
		return // 所有数据都已存在，无需预加载
	}

	// 过滤掉正在预加载的 slug（去重）
	s.preloadingMu.RLock()
	actualMissing := make([]string, 0)
	for _, slug := range missingSlugs {
		if !s.preloadingSlugs[slug] {
			actualMissing = append(actualMissing, slug)
		}
	}
	s.preloadingMu.RUnlock()

	if len(actualMissing) == 0 {
		logger.Debugf("后台预加载检查: 缺失的数据正在预加载中，跳过重复请求（缓存大小: %d）", cacheSize)
		return // 所有缺失数据都在预加载中，无需重复请求
	}

	logger.Infof("后台预加载: 发现 %d 个缺失的周期数据（需要预加载: %v，缓存大小: %d）",
		len(actualMissing), actualMissing, cacheSize)

	// 标记为正在预加载
	s.preloadingMu.Lock()
	for _, slug := range actualMissing {
		s.preloadingSlugs[slug] = true
	}
	s.preloadingMu.Unlock()

	// 异步预加载（不阻塞，闲时拉取）
	go func() {
		successCount := 0
		failCount := 0

		// 预加载完成后清除标记
		defer func() {
			s.preloadingMu.Lock()
			for _, slug := range actualMissing {
				delete(s.preloadingSlugs, slug)
			}
			s.preloadingMu.Unlock()
		}()

		for _, slug := range actualMissing {
			// 使用带超时的上下文，避免阻塞
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			market, err := s.fetchFromAPI(ctx, slug)
			cancel()

			if err != nil {
				// 提高日志级别，方便调试
				logger.Warnf("后台预加载市场数据失败 %s: %v", slug, err)
				failCount++
				continue
			}

			// 更新缓存
			s.cache.mu.Lock()
			s.cache.markets[slug] = market
			s.cache.mu.Unlock()

			successCount++
			logger.Debugf("✅ 已预加载周期: %s", slug)

			// 速率限制（闲时拉取，不着急）
			time.Sleep(200 * time.Millisecond)
		}

		// 记录预加载结果（无论成功或失败都记录，方便调试）
		if successCount > 0 || failCount > 0 {
			logger.Infof("后台预加载完成: 成功=%d, 失败=%d, 当前缓存大小=%d",
				successCount, failCount, cacheSize+successCount)
		}
	}()
}

// FetchMarketInfo 获取市场信息（优先使用内存缓存，缓存未命中时调用 API）
// 这是高频调用的方法，必须保证 O(1) 查找性能
func (s *MarketDataService) FetchMarketInfo(ctx context.Context, slug string) (*domain.Market, error) {
	// 1. 优先从内存缓存读取（O(1) 查找，极快）
	s.cache.mu.RLock()
	if market, exists := s.cache.markets[slug]; exists {
		s.cache.mu.RUnlock()
		logger.Debugf("从缓存获取市场信息: %s", slug)
		return market, nil
	}
	s.cache.mu.RUnlock()

	// 2. 缓存未命中，从 API 获取
	logger.Debugf("缓存未命中，从 API 获取市场信息: %s", slug)
	market, err := s.fetchFromAPI(ctx, slug)
	if err != nil {
		return nil, err
	}

	// 3. 异步更新缓存（不阻塞当前请求）
	go func() {
		s.cache.mu.Lock()
		s.cache.markets[slug] = market
		s.cache.mu.Unlock()

		// 4. 触发后台预加载（如果这是未来的市场，预加载更多未来市场）
		// 这样可以确保即使预加载数据用完，也能持续预加载未来市场
		s.triggerPreloadIfNeeded(slug)
	}()

	return market, nil
}

// triggerPreloadIfNeeded 如果需要，触发后台预加载
// 当获取到未来市场数据时，自动触发预加载更多未来市场
func (s *MarketDataService) triggerPreloadIfNeeded(currentSlug string) {
	// 从 slug 提取时间戳
	currentTs := extractTimestampFromSlug(currentSlug)
	now := time.Now().Unix()

	// 如果这是未来的市场（时间戳 > 当前时间），触发预加载
	// 这样可以确保在预加载数据用完后，仍能持续预加载
	if currentTs > now {
		// 延迟触发，避免频繁调用
		time.AfterFunc(2*time.Second, func() {
			s.preloadFutureMarkets()
		})
	}
}

// fetchFromAPI 从 Gamma API 获取市场数据（内部方法）
func (s *MarketDataService) fetchFromAPI(ctx context.Context, slug string) (*domain.Market, error) {
	// 使用 Gamma API 获取市场数据
	gammaMarket, err := s.clobClient.FetchMarketFromGamma(ctx, slug)
	if err != nil {
		return nil, fmt.Errorf("从 Gamma API 获取市场失败: %w", err)
	}

	// 解析 token IDs
	yesAssetID, noAssetID := parseTokenIDs(gammaMarket.ClobTokenIDs)
	if yesAssetID == "" || noAssetID == "" {
		return nil, fmt.Errorf("无法解析 token IDs: clobTokenIds=%s", gammaMarket.ClobTokenIDs)
	}

	// 从 slug 提取时间戳
	timestamp := extractTimestampFromSlug(slug)

	market := &domain.Market{
		Slug:        slug,
		YesAssetID:  yesAssetID,
		NoAssetID:   noAssetID,
		ConditionID: gammaMarket.ConditionID,
		Question:    gammaMarket.Question,
		Timestamp:   timestamp,
	}

	logger.Infof("从 API 获取市场信息成功: %s (YES: %s..., NO: %s...)",
		slug, yesAssetID[:12], noAssetID[:12])
	return market, nil
}

// parseTokenIDs 解析 token IDs
func parseTokenIDs(clobTokenIDs string) (yesAssetID, noAssetID string) {
	// 简化解析逻辑
	re := regexp.MustCompile(`["'\[\]]`)
	cleaned := re.ReplaceAllString(clobTokenIDs, "")
	parts := regexp.MustCompile(`,\s*`).Split(cleaned, -1)
	if len(parts) >= 2 {
		yesAssetID = parts[0]
		noAssetID = parts[1]
	}
	return
}

// extractTimestampFromSlug 从 slug 提取时间戳
func extractTimestampFromSlug(slug string) int64 {
	re := regexp.MustCompile(`-(\d+)$`)
	matches := re.FindStringSubmatch(slug)
	if len(matches) >= 2 {
		if ts, err := strconv.ParseInt(matches[1], 10, 64); err == nil {
			return ts
		}
	}
	return time.Now().Unix()
}

// getString 从 map 获取字符串值
func getString(m map[string]interface{}, key string) string {
	if val, ok := m[key].(string); ok {
		return val
	}
	return ""
}

// Generate15MinSlug 生成 15 分钟周期的 slug
func Generate15MinSlug(timestamp int64) string {
	return fmt.Sprintf("btc-updown-15m-%d", timestamp)
}

// GetCurrent15MinTimestamp 获取当前 15 分钟周期的时间戳
func GetCurrent15MinTimestamp() int64 {
	now := time.Now()
	minutes := now.Minute()
	roundedMinutes := (minutes / 15) * 15

	periodStart := time.Date(now.Year(), now.Month(), now.Day(),
		now.Hour(), roundedMinutes, 0, 0, now.Location())

	return periodStart.Unix()
}

// GenerateNext15MinSlugs 生成接下来 N 个 15 分钟周期的 slugs
func GenerateNext15MinSlugs(count int) []string {
	currentTs := GetCurrent15MinTimestamp()
	slugs := make([]string, 0, count)

	for i := 0; i < count; i++ {
		periodTs := currentTs + int64(i*900) // 15 分钟 = 900 秒
		slugs = append(slugs, Generate15MinSlug(periodTs))
	}

	return slugs
}
