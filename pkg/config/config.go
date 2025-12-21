package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// UserJSON 表示 user.json 文件的结构
type UserJSON struct {
	PrivateKey       string `json:"private_key"`
	Proxy            string `json:"proxy"`
	Address          string `json:"address"`
	RecipientAddress string `json:"recipient_address"`
	ProxyAddress     string `json:"proxy_address"`
}

// WalletConfig 钱包配置
type WalletConfig struct {
	PrivateKey    string
	FunderAddress string
}

// ProxyConfig 代理配置
type ProxyConfig struct {
	Host string
	Port int
}

// ExchangeStrategyMount 按 bbgo main 的风格挂载策略：
//
// exchangeStrategies:
//   - on: polymarket
//     (grid strategy removed)
//     gridLevels: [62, 65]
//     orderSize: 3
//
// 其中：
// - on: 会话名（本项目通常是 "polymarket"；支持 string 或 []string）
// - 其余 key 必须且只能有一个：即策略 ID
// - value 是该策略的配置（任意结构，最终会被反序列化到策略 struct 上）
type ExchangeStrategyMount struct {
	On []string `yaml:"-" json:"-"`

	StrategyID string                 `yaml:"-" json:"-"`
	Config     map[string]interface{} `yaml:"-" json:"-"`
}

func (m *ExchangeStrategyMount) UnmarshalYAML(value *yaml.Node) error {
	if value == nil {
		return errors.New("exchangeStrategies entry is nil")
	}
	var raw map[string]interface{}
	if err := value.Decode(&raw); err != nil {
		return fmt.Errorf("decode exchangeStrategies entry failed: %w", err)
	}

	onVal, ok := raw["on"]
	if !ok {
		return fmt.Errorf("exchangeStrategies entry missing 'on'")
	}
	m.On = parseStringOrStringSlice(onVal)
	if len(m.On) == 0 {
		return fmt.Errorf("exchangeStrategies entry 'on' is empty")
	}

	// 找到策略 key（排除 on）
	var strategyKeys []string
	for k := range raw {
		if k == "on" {
			continue
		}
		strategyKeys = append(strategyKeys, k)
	}
	if len(strategyKeys) != 1 {
		return fmt.Errorf("exchangeStrategies entry must contain exactly 1 strategy key (got %d)", len(strategyKeys))
	}

	m.StrategyID = strategyKeys[0]
	cfgVal := raw[m.StrategyID]
	if cfgVal == nil {
		m.Config = map[string]interface{}{}
		return nil
	}

	if cfgMap, ok := cfgVal.(map[string]interface{}); ok {
		m.Config = cfgMap
		return nil
	}

	// 允许用户把配置写成非 map（例如标量/数组），这里统一包一层，交给后续 json/yaml 反序列化处理
	m.Config = map[string]interface{}{"_": cfgVal}
	return nil
}

func parseStringOrStringSlice(v interface{}) []string {
	switch t := v.(type) {
	case string:
		if strings.TrimSpace(t) == "" {
			return nil
		}
		return []string{t}
	case []interface{}:
		out := make([]string, 0, len(t))
		for _, it := range t {
			s, ok := it.(string)
			if !ok {
				continue
			}
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			out = append(out, s)
		}
		return out
	default:
		return nil
	}
}

// Config 应用配置
type Config struct {
	Wallet                               WalletConfig
	Proxy                                *ProxyConfig
	ExchangeStrategies                   []ExchangeStrategyMount // bbgo main 风格：动态策略挂载
	LogLevel                             string                  // 日志级别
	LogFile                              string                  // 日志文件路径（可选）
	LogByCycle                           bool                    // 是否按周期命名日志文件
	DirectModeDebounce                   int                     // 直接回调模式的防抖间隔（毫秒），默认100ms（BBGO风格：只支持直接模式）
	MinOrderSize                         float64                 // 全局最小下单金额（USDC），默认 1.1（交易所要求 >= 1）
	MinShareSize                         float64                 // 限价单最小 share 数量，默认 5.0（仅限价单 GTC 时应用）
	OrderStatusCheckTimeout              int                     // 订单状态检查超时时间（秒），如果WebSocket在此时长内没有更新，则启用API轮询，默认3秒
	OrderStatusCheckInterval             int                     // 订单状态API轮询间隔（秒），默认3秒
	OrderStatusSyncIntervalWithOrders    int                     // 有活跃订单时的订单状态同步间隔（秒），默认3秒（官方API限流：150请求/10秒，理论上可支持1秒，但建议3秒以上）
	OrderStatusSyncIntervalWithoutOrders int                     // 无活跃订单时的订单状态同步间隔（秒），默认30秒
	CancelOpenOrdersOnCycleStart         bool                    // 每个新周期开始时是否清空“本周期残留 open orders”（默认false）
	ConcurrentExecutorWorkers            int                     // 并发命令执行器 worker 数（套利等），默认 8
	DryRun                               bool                    // 纸交易模式（dry run），如果为 true，不进行真实交易，只在日志中打印订单信息
}

var globalConfig *Config
var configFilePath string

// SetConfigPath 设置配置文件路径
func SetConfigPath(path string) {
	configFilePath = path
}

// GetConfigPath 获取配置文件路径
func GetConfigPath() string {
	return configFilePath
}

// ConfigFile 配置文件结构（用于 YAML/JSON 解析）
type ConfigFile struct {
	Wallet struct {
		PrivateKey    string `yaml:"private_key" json:"private_key"`
		FunderAddress string `yaml:"funder_address" json:"funder_address"`
	} `yaml:"wallet" json:"wallet"`
	Proxy struct {
		Host string `yaml:"host" json:"host"`
		Port int    `yaml:"port" json:"port"`
	} `yaml:"proxy" json:"proxy"`
	ExchangeStrategies                   []ExchangeStrategyMount `yaml:"exchangeStrategies" json:"exchangeStrategies"`
	LogLevel                             string                  `yaml:"log_level" json:"log_level"`
	LogFile                              string                  `yaml:"log_file" json:"log_file"`
	LogByCycle                           bool                    `yaml:"log_by_cycle" json:"log_by_cycle"`
	DirectModeDebounce                   int                     `yaml:"direct_mode_debounce" json:"direct_mode_debounce"` // 直接回调模式的防抖间隔（毫秒），默认100ms（BBGO风格：只支持直接模式）
	MinOrderSize                         float64                 `yaml:"minOrderSize" json:"minOrderSize"`
	MinShareSize                         float64                 `yaml:"minShareSize" json:"minShareSize"`                                                           // 限价单最小 share 数量（仅限价单 GTC 时应用）
	OrderStatusCheckTimeout              int                     `yaml:"order_status_check_timeout" json:"order_status_check_timeout"`                               // WebSocket超时时间（秒），默认3秒
	OrderStatusCheckInterval             int                     `yaml:"order_status_check_interval" json:"order_status_check_interval"`                             // API轮询间隔（秒），默认3秒
	OrderStatusSyncIntervalWithOrders    int                     `yaml:"order_status_sync_interval_with_orders" json:"order_status_sync_interval_with_orders"`       // 有活跃订单时的同步间隔（秒），默认3秒
	OrderStatusSyncIntervalWithoutOrders int                     `yaml:"order_status_sync_interval_without_orders" json:"order_status_sync_interval_without_orders"` // 无活跃订单时的同步间隔（秒），默认30秒
	CancelOpenOrdersOnCycleStart         bool                    `yaml:"cancel_open_orders_on_cycle_start" json:"cancel_open_orders_on_cycle_start"`                 // 新周期开始时清空本周期残留 open orders（默认false）
	ConcurrentExecutorWorkers            int                     `yaml:"concurrent_executor_workers" json:"concurrent_executor_workers"`                             // 并发命令执行器 worker 数（套利等），默认8
	DryRun                               bool                    `yaml:"dry_run" json:"dry_run"`                                                                     // 纸交易模式（dry run），如果为 true，不进行真实交易，只在日志中打印订单信息
}

// Load 加载配置
func Load() (*Config, error) {
	return LoadFromFile(configFilePath)
}

// LoadFromFile 从指定文件加载配置
func LoadFromFile(filePath string) (*Config, error) {
	if globalConfig != nil && configFilePath == filePath {
		return globalConfig, nil
	}

	// 尝试加载配置文件
	var configFile *ConfigFile
	if filePath != "" {
		var err error
		configFile, err = loadConfigFile(filePath)
		if err != nil {
			return nil, fmt.Errorf("加载配置文件失败 %s: %w", filePath, err)
		}
	}

	// 尝试加载 user.json（必须从 /pm/data/user.json 加载）
	userJSON, err := loadUserJSON()
	if err != nil {
		// user.json 不存在或解析失败会严重影响程序运行，返回错误
		return nil, fmt.Errorf("加载用户配置失败（必须从 /pm/data/user.json 加载）: %w", err)
	}
	if userJSON == nil {
		return nil, fmt.Errorf("用户配置为空，请检查 /pm/data/user.json 文件")
	}

	// 解析代理配置（优先级：配置文件 > 环境变量 > user.json > 默认值）
	proxyConfig := parseProxyConfigFromSources(configFile, userJSON)

	// 构建配置（优先级：环境变量 > user.json > 配置文件 > 默认值）
	// 注意：钱包信息优先从 user.json 加载，配置文件中的钱包配置会被忽略
	config := &Config{
		Wallet: WalletConfig{
			PrivateKey:    getEnvOrUserJSON("WALLET_PRIVATE_KEY", userJSON.PrivateKey, ""),
			FunderAddress: getEnvOrUserJSON("WALLET_FUNDER_ADDRESS", userJSON.ProxyAddress, userJSON.RecipientAddress, userJSON.Address, ""),
		},
		Proxy: proxyConfig,
		ExchangeStrategies: func() []ExchangeStrategyMount {
			if configFile != nil && len(configFile.ExchangeStrategies) > 0 {
				return configFile.ExchangeStrategies
			}
			return nil
		}(),
		LogLevel: func() string {
			if configFile != nil && configFile.LogLevel != "" {
				return configFile.LogLevel
			}
			return getEnv("LOG_LEVEL", "info")
		}(),
		LogFile: func() string {
			if configFile != nil && configFile.LogFile != "" {
				return configFile.LogFile
			}
			return getEnv("LOG_FILE", "logs/combined.log")
		}(),
		LogByCycle: func() bool {
			if configFile != nil {
				return configFile.LogByCycle
			}
			// 从环境变量读取，默认为 true
			if envVal := getEnv("LOG_BY_CYCLE", ""); envVal != "" {
				return envVal == "true" || envVal == "1"
			}
			return true // 默认按周期命名
		}(),
		DirectModeDebounce: func() int {
			if configFile != nil && configFile.DirectModeDebounce > 0 {
				return configFile.DirectModeDebounce
			}
			if envVal := getEnv("DIRECT_MODE_DEBOUNCE", ""); envVal != "" {
				if val, err := strconv.Atoi(envVal); err == nil && val > 0 {
					return val
				}
			}
			return 100 // 默认100ms
		}(),
		MinOrderSize: func() float64 {
			// 优先级：config file > env > 默认 1.1
			if configFile != nil && configFile.MinOrderSize > 0 {
				return configFile.MinOrderSize
			}
			if envVal := getEnv("MIN_ORDER_SIZE", ""); envVal != "" {
				if v, err := strconv.ParseFloat(envVal, 64); err == nil && v > 0 {
					return v
				}
			}
			return 1.1
		}(),
		MinShareSize: func() float64 {
			// 优先级：config file > env > 默认 5.0
			if configFile != nil && configFile.MinShareSize > 0 {
				return configFile.MinShareSize
			}
			if envVal := getEnv("MIN_SHARE_SIZE", ""); envVal != "" {
				if v, err := strconv.ParseFloat(envVal, 64); err == nil && v > 0 {
					return v
				}
			}
			return 5.0 // 默认 5.0 shares（Polymarket 限价单要求）
		}(),
		OrderStatusCheckTimeout: func() int {
			if configFile != nil && configFile.OrderStatusCheckTimeout > 0 {
				return configFile.OrderStatusCheckTimeout
			}
			if envVal := getEnv("ORDER_STATUS_CHECK_TIMEOUT", ""); envVal != "" {
				if val, err := strconv.Atoi(envVal); err == nil && val > 0 {
					return val
				}
			}
			return 3 // 默认3秒
		}(),
		DryRun: func() bool {
			if configFile != nil {
				return configFile.DryRun
			}
			// 从环境变量读取，默认为 false
			if envVal := getEnv("DRY_RUN", ""); envVal != "" {
				return envVal == "true" || envVal == "1"
			}
			return false // 默认关闭纸交易模式
		}(),
		OrderStatusCheckInterval: func() int {
			if configFile != nil && configFile.OrderStatusCheckInterval > 0 {
				return configFile.OrderStatusCheckInterval
			}
			if envVal := getEnv("ORDER_STATUS_CHECK_INTERVAL", ""); envVal != "" {
				if val, err := strconv.Atoi(envVal); err == nil && val > 0 {
					return val
				}
			}
			return 3 // 默认3秒
		}(),
		OrderStatusSyncIntervalWithOrders: func() int {
			if configFile != nil && configFile.OrderStatusSyncIntervalWithOrders > 0 {
				return configFile.OrderStatusSyncIntervalWithOrders
			}
			if envVal := getEnv("ORDER_STATUS_SYNC_INTERVAL_WITH_ORDERS", ""); envVal != "" {
				if val, err := strconv.Atoi(envVal); err == nil && val > 0 {
					return val
				}
			}
			return 3 // 默认3秒（官方API限流：150请求/10秒，理论上可支持1秒，但建议3秒以上）
		}(),
		OrderStatusSyncIntervalWithoutOrders: func() int {
			if configFile != nil && configFile.OrderStatusSyncIntervalWithoutOrders > 0 {
				return configFile.OrderStatusSyncIntervalWithoutOrders
			}
			if envVal := getEnv("ORDER_STATUS_SYNC_INTERVAL_WITHOUT_ORDERS", ""); envVal != "" {
				if val, err := strconv.Atoi(envVal); err == nil && val > 0 {
					return val
				}
			}
			return 30 // 默认30秒
		}(),
		CancelOpenOrdersOnCycleStart: func() bool {
			if configFile != nil {
				return configFile.CancelOpenOrdersOnCycleStart
			}
			if envVal := getEnv("CANCEL_OPEN_ORDERS_ON_CYCLE_START", ""); envVal != "" {
				return envVal == "true" || envVal == "1"
			}
			return false
		}(),
		ConcurrentExecutorWorkers: func() int {
			if configFile != nil && configFile.ConcurrentExecutorWorkers > 0 {
				return configFile.ConcurrentExecutorWorkers
			}
			if envVal := getEnv("CONCURRENT_EXECUTOR_WORKERS", ""); envVal != "" {
				if val, err := strconv.Atoi(envVal); err == nil && val > 0 {
					return val
				}
			}
			return 8
		}(),
	}

	// 验证配置
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("配置验证失败: %w", err)
	}

	// 设置代理环境变量（供 HTTP 客户端使用）
	if proxyConfig != nil {
		proxyURL := fmt.Sprintf("http://%s:%d", proxyConfig.Host, proxyConfig.Port)
		os.Setenv("HTTP_PROXY", proxyURL)
		os.Setenv("HTTPS_PROXY", proxyURL)
		os.Setenv("http_proxy", proxyURL)
		os.Setenv("https_proxy", proxyURL)
	}

	globalConfig = config
	configFilePath = filePath
	return config, nil
}

// loadConfigFile 加载配置文件（支持 YAML 和 JSON）
func loadConfigFile(filePath string) (*ConfigFile, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	var configFile ConfigFile
	ext := strings.ToLower(filepath.Ext(filePath))

	switch ext {
	case ".yaml", ".yml":
		if err := yaml.Unmarshal(data, &configFile); err != nil {
			return nil, fmt.Errorf("解析 YAML 配置文件失败: %w", err)
		}
	case ".json":
		if err := json.Unmarshal(data, &configFile); err != nil {
			return nil, fmt.Errorf("解析 JSON 配置文件失败: %w", err)
		}
	default:
		return nil, fmt.Errorf("不支持的配置文件格式: %s (支持 .yaml, .yml, .json)", ext)
	}

	return &configFile, nil
}

// parseProxyConfigFromSources 从多个源解析代理配置
func parseProxyConfigFromSources(configFile *ConfigFile, userJSON *UserJSON) *ProxyConfig {
	// 优先级：配置文件 > 环境变量 > user.json > 默认值
	var proxyHost, proxyPortStr string

	if configFile != nil && configFile.Proxy.Host != "" {
		proxyHost = configFile.Proxy.Host
		proxyPortStr = fmt.Sprintf("%d", configFile.Proxy.Port)
	} else {
		proxyHost = getEnv("PROXY_HOST", "")
		proxyPortStr = getEnv("PROXY_PORT", "")

		if proxyHost == "" && userJSON != nil && userJSON.Proxy != "" {
			if strings.HasPrefix(userJSON.Proxy, "http://") {
				parts := strings.Split(strings.TrimPrefix(userJSON.Proxy, "http://"), ":")
				if len(parts) == 2 {
					proxyHost = parts[0]
					proxyPortStr = parts[1]
				}
			}
		}
	}

	// 如果仍未设置，使用默认值
	if proxyHost == "" {
		proxyHost = "127.0.0.1"
	}
	if proxyPortStr == "" {
		proxyPortStr = "15236"
	}

	proxyPort, err := strconv.Atoi(proxyPortStr)
	if err != nil {
		return nil
	}

	return &ProxyConfig{
		Host: proxyHost,
		Port: proxyPort,
	}
}

// NOTE: 旧版通过 enabled 策略列表+硬编码策略结构体加载。
// 当前已切换到 bbgo main 风格的 exchangeStrategies（动态挂载），不再需要 parseEnabledStrategies。

// getValueFromSources 从多个源获取字符串值（优先级：配置文件 > 环境变量/默认值）
func getValueFromSources(hasConfigValue bool, configValue, envValue string) string {
	if hasConfigValue && configValue != "" {
		return configValue
	}
	return envValue
}

// getIntFromSources 从多个源获取整数值
// 如果配置文件存在，使用配置文件的值（包括0），否则使用环境变量/默认值
func getIntFromSources(hasConfigValue bool, configValue, envValue int) int {
	if hasConfigValue {
		return configValue
	}
	return envValue
}

// getFloatFromSources 从多个源获取浮点数值
// 如果配置文件存在，使用配置文件的值（包括0），否则使用环境变量/默认值
func getFloatFromSources(hasConfigValue bool, configValue, envValue float64) float64 {
	if hasConfigValue {
		return configValue
	}
	return envValue
}

// getBoolFromSources 从多个源获取布尔值
func getBoolFromSources(hasConfigValue bool, configValue, envValue bool) bool {
	if hasConfigValue {
		return configValue
	}
	return envValue
}

// safeGet 安全地从 ConfigFile 取值（cf 为 nil 时返回零值）。
func safeGet[T any](cf *ConfigFile, getter func(*ConfigFile) T) (zero T) {
	if cf == nil {
		return zero
	}
	return getter(cf)
}

// Get 获取全局配置（如果已加载）
func Get() *Config {
	return globalConfig
}

// Validate 验证配置
func (c *Config) Validate() error {
	if c.Wallet.PrivateKey == "" {
		return fmt.Errorf("WALLET_PRIVATE_KEY 未配置")
	}
	if c.Wallet.FunderAddress == "" {
		return fmt.Errorf("WALLET_FUNDER_ADDRESS 未配置")
	}
	if len(c.ExchangeStrategies) == 0 {
		return fmt.Errorf("exchangeStrategies 不能为空（请按 bbgo main 风格配置策略）")
	}
	if c.MinOrderSize > 0 && c.MinOrderSize < 1.0 {
		return fmt.Errorf("minOrderSize 必须 >= 1.0")
	}
	return nil
}

// loadUserJSON 加载 user.json 文件
func loadUserJSON() (*UserJSON, error) {
	// 只从 /pm/data/user.json 加载（不再使用 botuser.json）
	possiblePaths := []string{
		"/pm/data/user.json", // 绝对路径（唯一路径）
	}

	for _, path := range possiblePaths {
		if _, err := os.Stat(path); err == nil {
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}

			var userJSON UserJSON
			if err := json.Unmarshal(data, &userJSON); err != nil {
				return nil, fmt.Errorf("解析 %s 失败: %w", path, err)
			}

			fmt.Printf("✅ 从 %s 加载钱包配置\n", path)
			return &userJSON, nil
		}
	}

	return nil, fmt.Errorf("未找到 /pm/data/user.json 文件")
}

// parseProxyConfig 解析代理配置
func parseProxyConfig(userJSON *UserJSON) *ProxyConfig {
	// 优先使用环境变量
	proxyHost := getEnv("PROXY_HOST", "")
	proxyPortStr := getEnv("PROXY_PORT", "")

	// 如果环境变量未设置，尝试从 user.json 解析
	if proxyHost == "" && userJSON != nil && userJSON.Proxy != "" {
		// user.json 中的 proxy 格式为 "http://host:port"
		if strings.HasPrefix(userJSON.Proxy, "http://") {
			parts := strings.Split(strings.TrimPrefix(userJSON.Proxy, "http://"), ":")
			if len(parts) == 2 {
				proxyHost = parts[0]
				proxyPortStr = parts[1]
			}
		}
	}

	// 如果仍未设置，使用默认值
	if proxyHost == "" {
		proxyHost = "127.0.0.1"
	}
	if proxyPortStr == "" {
		proxyPortStr = "15236"
	}

	proxyPort, err := strconv.Atoi(proxyPortStr)
	if err != nil {
		return nil
	}

	return &ProxyConfig{
		Host: proxyHost,
		Port: proxyPort,
	}
}

// getEnv 获取环境变量，如果不存在则返回默认值
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// getEnvOrUserJSON 获取环境变量或 user.json 中的值，按优先级返回第一个非空值
func getEnvOrUserJSON(envKey string, userJSONValues ...string) string {
	if value := os.Getenv(envKey); value != "" {
		return value
	}
	for _, v := range userJSONValues {
		if v != "" {
			return v
		}
	}
	return ""
}

// parseIntEnv 解析整数环境变量
func parseIntEnv(key string, defaultValue int) int {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return defaultValue
	}
	return parsed
}

// parseFloatEnv 解析浮点数环境变量
func parseFloatEnv(key string, defaultValue float64) float64 {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return defaultValue
	}
	return parsed
}

// parseBoolEnv 解析布尔环境变量
func parseBoolEnv(key string, defaultValue bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return defaultValue
	}
	return parsed
}
