# prepare-data 工具改进建议

## 当前问题分析

### 1. 数据未被使用
- 预加载的 `market-data.json` 文件在主程序中未被使用
- 主程序仍直接调用 Gamma API，失去了预加载的意义

### 2. 功能限制
- 硬编码 100 个周期，无法配置
- 缺少命令行参数支持
- 没有增量更新机制

### 3. 错误处理
- 单个市场失败仅记录警告，可能导致数据不完整
- 没有重试机制
- 没有失败统计

## 改进方案

### 方案 1: 增强 MarketDataService 支持缓存（推荐）

**修改 `internal/services/market_data.go`**：

```go
// MarketDataService 市场数据服务
type MarketDataService struct {
    clobClient *client.Client
    cache      *MarketDataCache // 新增缓存
}

// MarketDataCache 市场数据缓存
type MarketDataCache struct {
    markets map[string]*domain.Market
    mu      sync.RWMutex
    loaded  bool
}

// FetchMarketInfo 获取市场信息（优先使用缓存）
func (s *MarketDataService) FetchMarketInfo(ctx context.Context, slug string) (*domain.Market, error) {
    // 1. 先尝试从缓存读取
    if s.cache != nil {
        if market := s.cache.Get(slug); market != nil {
            logger.Debugf("从缓存获取市场信息: %s", slug)
            return market, nil
        }
    }
    
    // 2. 缓存未命中，从 API 获取
    return s.fetchFromAPI(ctx, slug)
}

// LoadCacheFromFile 从文件加载缓存
func (s *MarketDataService) LoadCacheFromFile(filePath string) error {
    // 读取 market-data.json 并加载到缓存
}
```

**修改 `cmd/bot/main.go`**：

```go
// 创建市场数据服务
marketDataService := services.NewMarketDataService(clobClient)

// 尝试加载预加载的数据文件
if err := marketDataService.LoadCacheFromFile("data/market-data.json"); err != nil {
    logger.Warnf("加载预加载市场数据失败: %v，将使用 API 获取", err)
}
```

### 方案 2: 增强 prepare-data 工具

**添加命令行参数**：

```go
func main() {
    var (
        count     = flag.Int("count", 100, "要预加载的市场周期数量")
        output    = flag.String("output", "data/market-data.json", "输出文件路径")
        retries   = flag.Int("retries", 3, "每个市场失败时的重试次数")
        delay     = flag.Duration("delay", 200*time.Millisecond, "请求之间的延迟")
        incremental = flag.Bool("incremental", false, "增量更新模式（只获取缺失的市场）")
    )
    flag.Parse()
    
    // ... 使用参数
}
```

**添加增量更新逻辑**：

```go
// 如果启用增量更新，先读取现有文件
var existingMarkets map[string]MarketInfo
if *incremental {
    existingMarkets = loadExistingMarkets(*output)
}

for i, slug := range slugs {
    // 增量更新：跳过已存在的市场
    if *incremental && existingMarkets[slug] != nil {
        logger.Debugf("跳过已存在的市场: %s", slug)
        continue
    }
    
    // 带重试的获取逻辑
    market, err := fetchWithRetry(ctx, marketDataService, slug, *retries)
    // ...
}
```

**添加失败统计**：

```go
type FetchStats struct {
    Total    int
    Success  int
    Failed   int
    Skipped  int
}

stats := &FetchStats{Total: len(slugs)}
// ... 统计逻辑
logger.Infof("获取完成: 总计=%d, 成功=%d, 失败=%d, 跳过=%d", 
    stats.Total, stats.Success, stats.Failed, stats.Skipped)
```

### 方案 3: 添加数据验证

```go
// ValidateMarketData 验证市场数据有效性
func ValidateMarketData(market *domain.Market) error {
    if market.Slug == "" {
        return fmt.Errorf("slug 不能为空")
    }
    if market.YesAssetID == "" || market.NoAssetID == "" {
        return fmt.Errorf("asset ID 不能为空")
    }
    if market.Timestamp <= 0 {
        return fmt.Errorf("timestamp 无效")
    }
    
    // 检查时间戳是否在未来（市场应该还未开始）
    now := time.Now().Unix()
    if market.Timestamp < now-3600 { // 允许1小时的误差
        return fmt.Errorf("市场时间戳过旧: %d (当前: %d)", market.Timestamp, now)
    }
    
    return nil
}
```

### 方案 4: 添加数据过期检查

```go
// IsMarketDataExpired 检查市场数据是否过期
func IsMarketDataExpired(filePath string, maxAge time.Duration) (bool, error) {
    info, err := os.Stat(filePath)
    if err != nil {
        return true, err // 文件不存在，视为过期
    }
    
    age := time.Since(info.ModTime())
    return age > maxAge, nil
}

// 在主程序中使用
if expired, _ := IsMarketDataExpired("data/market-data.json", 24*time.Hour); expired {
    logger.Warnf("预加载的市场数据已过期，将使用 API 获取")
}
```

## 实施优先级

1. **高优先级**：方案 1（让预加载数据真正被使用）
2. **中优先级**：方案 2（增强 prepare-data 工具）
3. **低优先级**：方案 3 和 4（数据验证和过期检查）

## 使用场景

### 场景 1: 开发/测试环境
- 预加载数据可以避免频繁调用 API
- 提高开发效率

### 场景 2: 生产环境
- 预加载数据作为缓存，减少 API 调用
- API 失败时可以作为备用数据源

### 场景 3: 离线测试
- 使用预加载数据进行离线测试
- 不依赖外部 API

