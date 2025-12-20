package config

import (
	"encoding/json"
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

// GridConfig 网格策略配置
type GridConfig struct {
	GridLevels              []int   // 手工定义的网格层级列表（分），例如 [62, 65, 71, ...]，第一个值即为最小交易价格
	OrderSize               float64 // 订单大小
	MinOrderSize            float64 // 最小下单金额（USDC），默认1.1，交易所要求不能小于1
	EnableRebuy             bool    // 允许重新买入
	EnableDoubleSide        bool    // 双向交易
	ProfitTarget            int     // 止盈目标（分），默认 3 cents，用于对冲锁定利润
	MaxUnhedgedLoss         int     // 最大未对冲损失（分），默认 10 cents
	HardStopPrice           int     // 硬止损价格（分），默认 50c - 价格跌到此价格以下必须止损（因为50以上才是win）
	ElasticStopPrice        int     // 弹性止损价格（分），默认 40c - 弹性止损价格，考虑波动性
	MaxRoundsPerPeriod      int     // 每个 15 分钟周期内允许开启的「网格轮数」上限，默认 1
	PriceDeviationThreshold int     // 价格偏差阈值（分），默认 2 cents，订单价格与订单簿价格偏差超过此值则撤单重新下单
	EntryMaxBuySlippageCents      int // 入场买入允许的最大滑点（分），相对 gridLevel 上限（默认0=关闭）
	SupplementMaxBuySlippageCents int // 补仓/强对冲买入允许的最大滑点（分），相对当前价上限（默认0=关闭）
}

// ThresholdConfig 价格阈值策略配置
type ThresholdConfig struct {
	BuyThreshold      float64 // 买入阈值（小数，例如 0.62）
	SellThreshold     float64 // 卖出阈值（小数，可选，如果为 0 则不卖出）
	OrderSize         float64 // 订单大小
	TokenType         string  // Token 类型：YES 或 NO，空字符串表示两者都监控
	ProfitTargetCents int     // 止盈目标（分），例如 3 表示 +3 cents
	StopLossCents     int     // 止损目标（分），例如 10 表示 -10 cents
	MaxBuySlippageCents  int  // 买入允许的最大滑点（分），相对触发价上限（默认0=关闭）
	MaxSellSlippageCents int  // 卖出允许的最大滑点（分），相对触发价下限（默认0=关闭）
}

// ArbitrageConfig 套利策略配置
type ArbitrageConfig struct {
	LockStartMinutes        int     // 锁盈阶段起始时间（分钟，默认12）
	EarlyLockPriceThreshold float64 // 提前锁盈价格阈值（默认0.85，当UP或DOWN价格达到此阈值时提前进入锁盈）
	TargetUpBase            float64 // UP胜目标利润（USDC，默认100）
	TargetDownBase          float64 // DOWN胜目标利润（USDC，默认60）
	BaseTarget              float64 // 基础建仓目标持仓量（默认1500）
	BuildLotSize            float64 // 建仓阶段单次下单量（默认18）
	MaxUpIncrement          float64 // 锁盈阶段单次最大UP加仓量（默认100）
	MaxDownIncrement        float64 // 锁盈阶段单次最大DOWN加仓量（默认100）
	SmallIncrement          float64 // 反向保险小额加仓量（默认20）
	MinOrderSize            float64 // 最小下单规模（默认1.0）
	MaxBuySlippageCents     int     // 买入允许的最大滑点（分），相对当前观测价上限（默认0=关闭）
}

// DataRecorderConfig 数据记录策略配置
type DataRecorderConfig struct {
	OutputDir       string // CSV 文件保存目录
	UseRTDSFallback bool   // 是否使用 RTDS 作为目标价备选方案
}

// StrategyConfig 策略配置（支持多策略）
type StrategyConfig struct {
	EnabledStrategies []string            // 启用的策略列表，例如 ["grid", "threshold", "arbitrage", "datarecorder"]
	Grid              *GridConfig         // 网格策略配置（如果启用）
	Threshold         *ThresholdConfig    // 价格阈值策略配置（如果启用）
	Arbitrage         *ArbitrageConfig    // 套利策略配置（如果启用）
	DataRecorder      *DataRecorderConfig // 数据记录策略配置（如果启用）
}

// Config 应用配置
type Config struct {
	Wallet                               WalletConfig
	Proxy                                *ProxyConfig
	Strategies                           StrategyConfig // 多策略配置
	LogLevel                             string         // 日志级别
	LogFile                              string         // 日志文件路径（可选）
	LogByCycle                           bool           // 是否按周期命名日志文件
	DirectModeDebounce                   int            // 直接回调模式的防抖间隔（毫秒），默认100ms（BBGO风格：只支持直接模式）
	OrderStatusCheckTimeout              int            // 订单状态检查超时时间（秒），如果WebSocket在此时长内没有更新，则启用API轮询，默认3秒
	OrderStatusCheckInterval             int            // 订单状态API轮询间隔（秒），默认3秒
	OrderStatusSyncIntervalWithOrders    int            // 有活跃订单时的订单状态同步间隔（秒），默认3秒（官方API限流：150请求/10秒，理论上可支持1秒，但建议3秒以上）
	OrderStatusSyncIntervalWithoutOrders int            // 无活跃订单时的订单状态同步间隔（秒），默认30秒
	CancelOpenOrdersOnCycleStart         bool           // 每个新周期开始时是否清空“本周期残留 open orders”（默认false）
	DryRun                               bool           // 纸交易模式（dry run），如果为 true，不进行真实交易，只在日志中打印订单信息
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
	Strategies struct {
		Enabled []string `yaml:"enabled" json:"enabled"`
		Grid    struct {
			GridLevels         []int   `yaml:"grid_levels" json:"grid_levels"` // 手工定义的网格层级列表，例如 [62, 65, 71]，第一个值即为最小交易价格
			OrderSize          float64 `yaml:"order_size" json:"order_size"`
			MinOrderSize       float64 `yaml:"min_order_size" json:"min_order_size"` // 最小下单金额（USDC），默认1.1
			EnableRebuy        bool    `yaml:"enable_rebuy" json:"enable_rebuy"`
			EnableDoubleSide   bool    `yaml:"enable_double_side" json:"enable_double_side"`
			ProfitTarget       int     `yaml:"profit_target" json:"profit_target"`
			MaxUnhedgedLoss    int     `yaml:"max_unhedged_loss" json:"max_unhedged_loss"`
			HardStopPrice      int     `yaml:"hard_stop_price" json:"hard_stop_price"`
			ElasticStopPrice   int     `yaml:"elastic_stop_price" json:"elastic_stop_price"`
			MaxRoundsPerPeriod int     `yaml:"max_rounds_per_period" json:"max_rounds_per_period"`
			EntryMaxBuySlippageCents      int `yaml:"entry_max_buy_slippage_cents" json:"entry_max_buy_slippage_cents"`
			SupplementMaxBuySlippageCents int `yaml:"supplement_max_buy_slippage_cents" json:"supplement_max_buy_slippage_cents"`
		} `yaml:"grid" json:"grid"`
		Threshold struct {
			BuyThreshold      float64 `yaml:"buy_threshold" json:"buy_threshold"`
			SellThreshold     float64 `yaml:"sell_threshold" json:"sell_threshold"`
			OrderSize         float64 `yaml:"order_size" json:"order_size"`
			TokenType         string  `yaml:"token_type" json:"token_type"`
			ProfitTargetCents int     `yaml:"profit_target_cents" json:"profit_target_cents"`
			StopLossCents     int     `yaml:"stop_loss_cents" json:"stop_loss_cents"`
			MaxBuySlippageCents  int  `yaml:"max_buy_slippage_cents" json:"max_buy_slippage_cents"`
			MaxSellSlippageCents int  `yaml:"max_sell_slippage_cents" json:"max_sell_slippage_cents"`
		} `yaml:"threshold" json:"threshold"`
		Arbitrage struct {
			LockStartMinutes        int     `yaml:"lock_start_minutes" json:"lock_start_minutes"`
			EarlyLockPriceThreshold float64 `yaml:"early_lock_price_threshold" json:"early_lock_price_threshold"`
			TargetUpBase            float64 `yaml:"target_up_base" json:"target_up_base"`
			TargetDownBase          float64 `yaml:"target_down_base" json:"target_down_base"`
			BaseTarget              float64 `yaml:"base_target" json:"base_target"`
			BuildLotSize            float64 `yaml:"build_lot_size" json:"build_lot_size"`
			MaxUpIncrement          float64 `yaml:"max_up_increment" json:"max_up_increment"`
			MaxDownIncrement        float64 `yaml:"max_down_increment" json:"max_down_increment"`
			SmallIncrement          float64 `yaml:"small_increment" json:"small_increment"`
			MinOrderSize            float64 `yaml:"min_order_size" json:"min_order_size"`
			MaxBuySlippageCents     int     `yaml:"max_buy_slippage_cents" json:"max_buy_slippage_cents"`
		} `yaml:"arbitrage" json:"arbitrage"`
		DataRecorder struct {
			OutputDir       string `yaml:"output_dir" json:"output_dir"`
			UseRTDSFallback bool   `yaml:"use_rtds_fallback" json:"use_rtds_fallback"`
		} `yaml:"datarecorder" json:"datarecorder"`
	} `yaml:"strategies" json:"strategies"`
	LogLevel                             string `yaml:"log_level" json:"log_level"`
	LogFile                              string `yaml:"log_file" json:"log_file"`
	LogByCycle                           bool   `yaml:"log_by_cycle" json:"log_by_cycle"`
	DirectModeDebounce                   int    `yaml:"direct_mode_debounce" json:"direct_mode_debounce"`                                           // 直接回调模式的防抖间隔（毫秒），默认100ms（BBGO风格：只支持直接模式）
	OrderStatusCheckTimeout              int    `yaml:"order_status_check_timeout" json:"order_status_check_timeout"`                               // WebSocket超时时间（秒），默认3秒
	OrderStatusCheckInterval             int    `yaml:"order_status_check_interval" json:"order_status_check_interval"`                             // API轮询间隔（秒），默认3秒
	OrderStatusSyncIntervalWithOrders    int    `yaml:"order_status_sync_interval_with_orders" json:"order_status_sync_interval_with_orders"`       // 有活跃订单时的同步间隔（秒），默认3秒
	OrderStatusSyncIntervalWithoutOrders int    `yaml:"order_status_sync_interval_without_orders" json:"order_status_sync_interval_without_orders"` // 无活跃订单时的同步间隔（秒），默认30秒
	CancelOpenOrdersOnCycleStart         bool   `yaml:"cancel_open_orders_on_cycle_start" json:"cancel_open_orders_on_cycle_start"`                 // 新周期开始时清空本周期残留 open orders（默认false）
	DryRun                               bool   `yaml:"dry_run" json:"dry_run"`                                                                     // 纸交易模式（dry run），如果为 true，不进行真实交易，只在日志中打印订单信息
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

	// 解析启用的策略列表（优先级：配置文件 > 环境变量 > 默认值）
	enabledStrategies := parseEnabledStrategies(configFile)

	// 构建配置（优先级：环境变量 > user.json > 配置文件 > 默认值）
	// 注意：钱包信息优先从 user.json 加载，配置文件中的钱包配置会被忽略
	config := &Config{
		Wallet: WalletConfig{
			PrivateKey:    getEnvOrUserJSON("WALLET_PRIVATE_KEY", userJSON.PrivateKey, ""),
			FunderAddress: getEnvOrUserJSON("WALLET_FUNDER_ADDRESS", userJSON.ProxyAddress, userJSON.RecipientAddress, userJSON.Address, ""),
		},
		Proxy: proxyConfig,
		Strategies: StrategyConfig{
			EnabledStrategies: enabledStrategies,
			Grid: &GridConfig{
				GridLevels:         safeGetGridIntSlice(configFile, func(cf *ConfigFile) []int { return cf.Strategies.Grid.GridLevels }),
				OrderSize:          getFloatFromSources(configFile != nil, safeGetGridFloat(configFile, func(cf *ConfigFile) float64 { return cf.Strategies.Grid.OrderSize }), parseFloatEnv("ORDER_SIZE", 1)),
				MinOrderSize:       getFloatFromSources(configFile != nil, safeGetGridFloat(configFile, func(cf *ConfigFile) float64 { return cf.Strategies.Grid.MinOrderSize }), parseFloatEnv("GRID_MIN_ORDER_SIZE", 1.1)),
				EnableRebuy:        getBoolFromSources(configFile != nil, safeGetGridBool(configFile, func(cf *ConfigFile) bool { return cf.Strategies.Grid.EnableRebuy }), parseBoolEnv("ENABLE_REBUY", true)),
				EnableDoubleSide:   getBoolFromSources(configFile != nil, safeGetGridBool(configFile, func(cf *ConfigFile) bool { return cf.Strategies.Grid.EnableDoubleSide }), parseBoolEnv("ENABLE_DOUBLE_SIDE", true)),
				ProfitTarget:       getIntFromSources(configFile != nil, safeGetGridInt(configFile, func(cf *ConfigFile) int { return cf.Strategies.Grid.ProfitTarget }), parseIntEnv("PROFIT_TARGET", 3)),
				MaxUnhedgedLoss:    getIntFromSources(configFile != nil, safeGetGridInt(configFile, func(cf *ConfigFile) int { return cf.Strategies.Grid.MaxUnhedgedLoss }), parseIntEnv("MAX_UNHEDGED_LOSS", 10)),
				HardStopPrice:      getIntFromSources(configFile != nil, safeGetGridInt(configFile, func(cf *ConfigFile) int { return cf.Strategies.Grid.HardStopPrice }), parseIntEnv("HARD_STOP_PRICE", 50)),
				ElasticStopPrice:   getIntFromSources(configFile != nil, safeGetGridInt(configFile, func(cf *ConfigFile) int { return cf.Strategies.Grid.ElasticStopPrice }), parseIntEnv("ELASTIC_STOP_PRICE", 40)),
				MaxRoundsPerPeriod: getIntFromSources(configFile != nil, safeGetGridInt(configFile, func(cf *ConfigFile) int { return cf.Strategies.Grid.MaxRoundsPerPeriod }), parseIntEnv("MAX_ROUNDS_PER_PERIOD", 1)),
				EntryMaxBuySlippageCents: getIntFromSources(
					configFile != nil,
					safeGetGridInt(configFile, func(cf *ConfigFile) int { return cf.Strategies.Grid.EntryMaxBuySlippageCents }),
					parseIntEnv("GRID_ENTRY_MAX_BUY_SLIPPAGE_CENTS", 0),
				),
				SupplementMaxBuySlippageCents: getIntFromSources(
					configFile != nil,
					safeGetGridInt(configFile, func(cf *ConfigFile) int { return cf.Strategies.Grid.SupplementMaxBuySlippageCents }),
					parseIntEnv("GRID_SUPPLEMENT_MAX_BUY_SLIPPAGE_CENTS", 0),
				),
			},
			Threshold: &ThresholdConfig{
				BuyThreshold:      getFloatFromSources(configFile != nil, safeGetThresholdFloat(configFile, func(cf *ConfigFile) float64 { return cf.Strategies.Threshold.BuyThreshold }), parseFloatEnv("THRESHOLD_BUY_THRESHOLD", 0.62)),
				SellThreshold:     getFloatFromSources(configFile != nil, safeGetThresholdFloat(configFile, func(cf *ConfigFile) float64 { return cf.Strategies.Threshold.SellThreshold }), parseFloatEnv("THRESHOLD_SELL_THRESHOLD", 0)),
				OrderSize:         getFloatFromSources(configFile != nil, safeGetThresholdFloat(configFile, func(cf *ConfigFile) float64 { return cf.Strategies.Threshold.OrderSize }), parseFloatEnv("THRESHOLD_ORDER_SIZE", 3.0)),
				TokenType:         getValueFromSources(configFile != nil && safeGetThresholdString(configFile, func(cf *ConfigFile) string { return cf.Strategies.Threshold.TokenType }) != "", safeGetThresholdString(configFile, func(cf *ConfigFile) string { return cf.Strategies.Threshold.TokenType }), getEnv("THRESHOLD_TOKEN_TYPE", "")),
				ProfitTargetCents: getIntFromSources(configFile != nil, safeGetThresholdInt(configFile, func(cf *ConfigFile) int { return cf.Strategies.Threshold.ProfitTargetCents }), parseIntEnv("THRESHOLD_PROFIT_TARGET_CENTS", 3)),
				StopLossCents:     getIntFromSources(configFile != nil, safeGetThresholdInt(configFile, func(cf *ConfigFile) int { return cf.Strategies.Threshold.StopLossCents }), parseIntEnv("THRESHOLD_STOP_LOSS_CENTS", 10)),
				MaxBuySlippageCents: getIntFromSources(
					configFile != nil,
					safeGetThresholdInt(configFile, func(cf *ConfigFile) int { return cf.Strategies.Threshold.MaxBuySlippageCents }),
					parseIntEnv("THRESHOLD_MAX_BUY_SLIPPAGE_CENTS", 0),
				),
				MaxSellSlippageCents: getIntFromSources(
					configFile != nil,
					safeGetThresholdInt(configFile, func(cf *ConfigFile) int { return cf.Strategies.Threshold.MaxSellSlippageCents }),
					parseIntEnv("THRESHOLD_MAX_SELL_SLIPPAGE_CENTS", 0),
				),
			},
			Arbitrage: &ArbitrageConfig{
				LockStartMinutes:        getIntFromSources(configFile != nil, safeGetArbitrageInt(configFile, func(cf *ConfigFile) int { return cf.Strategies.Arbitrage.LockStartMinutes }), parseIntEnv("ARBITRAGE_LOCK_START_MINUTES", 12)),
				EarlyLockPriceThreshold: getFloatFromSources(configFile != nil, safeGetArbitrageFloat(configFile, func(cf *ConfigFile) float64 { return cf.Strategies.Arbitrage.EarlyLockPriceThreshold }), parseFloatEnv("ARBITRAGE_EARLY_LOCK_PRICE_THRESHOLD", 0.85)),
				TargetUpBase:            getFloatFromSources(configFile != nil, safeGetArbitrageFloat(configFile, func(cf *ConfigFile) float64 { return cf.Strategies.Arbitrage.TargetUpBase }), parseFloatEnv("ARBITRAGE_TARGET_UP_BASE", 100.0)),
				TargetDownBase:          getFloatFromSources(configFile != nil, safeGetArbitrageFloat(configFile, func(cf *ConfigFile) float64 { return cf.Strategies.Arbitrage.TargetDownBase }), parseFloatEnv("ARBITRAGE_TARGET_DOWN_BASE", 60.0)),
				BaseTarget:              getFloatFromSources(configFile != nil, safeGetArbitrageFloat(configFile, func(cf *ConfigFile) float64 { return cf.Strategies.Arbitrage.BaseTarget }), parseFloatEnv("ARBITRAGE_BASE_TARGET", 1500.0)),
				BuildLotSize:            getFloatFromSources(configFile != nil, safeGetArbitrageFloat(configFile, func(cf *ConfigFile) float64 { return cf.Strategies.Arbitrage.BuildLotSize }), parseFloatEnv("ARBITRAGE_BUILD_LOT_SIZE", 18.0)),
				MaxUpIncrement:          getFloatFromSources(configFile != nil, safeGetArbitrageFloat(configFile, func(cf *ConfigFile) float64 { return cf.Strategies.Arbitrage.MaxUpIncrement }), parseFloatEnv("ARBITRAGE_MAX_UP_INCREMENT", 100.0)),
				MaxDownIncrement:        getFloatFromSources(configFile != nil, safeGetArbitrageFloat(configFile, func(cf *ConfigFile) float64 { return cf.Strategies.Arbitrage.MaxDownIncrement }), parseFloatEnv("ARBITRAGE_MAX_DOWN_INCREMENT", 100.0)),
				SmallIncrement:          getFloatFromSources(configFile != nil, safeGetArbitrageFloat(configFile, func(cf *ConfigFile) float64 { return cf.Strategies.Arbitrage.SmallIncrement }), parseFloatEnv("ARBITRAGE_SMALL_INCREMENT", 20.0)),
				MinOrderSize:            getFloatFromSources(configFile != nil, safeGetArbitrageFloat(configFile, func(cf *ConfigFile) float64 { return cf.Strategies.Arbitrage.MinOrderSize }), parseFloatEnv("ARBITRAGE_MIN_ORDER_SIZE", 1.2)),
				MaxBuySlippageCents: getIntFromSources(
					configFile != nil,
					safeGetArbitrageInt(configFile, func(cf *ConfigFile) int { return cf.Strategies.Arbitrage.MaxBuySlippageCents }),
					parseIntEnv("ARBITRAGE_MAX_BUY_SLIPPAGE_CENTS", 0),
				),
			},
			DataRecorder: &DataRecorderConfig{
				OutputDir:       getValueFromSources(configFile != nil && safeGetDataRecorderString(configFile, func(cf *ConfigFile) string { return cf.Strategies.DataRecorder.OutputDir }) != "", safeGetDataRecorderString(configFile, func(cf *ConfigFile) string { return cf.Strategies.DataRecorder.OutputDir }), getEnv("DATARECORDER_OUTPUT_DIR", "data/recordings")),
				UseRTDSFallback: getBoolFromSources(configFile != nil, safeGetDataRecorderBool(configFile, func(cf *ConfigFile) bool { return cf.Strategies.DataRecorder.UseRTDSFallback }), parseBoolEnv("DATARECORDER_USE_RTDS_FALLBACK", true)),
			},
		},
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

// parseEnabledStrategies 解析启用的策略列表
func parseEnabledStrategies(configFile *ConfigFile) []string {
	if configFile != nil && len(configFile.Strategies.Enabled) > 0 {
		return configFile.Strategies.Enabled
	}
	// 如果没有配置文件或配置文件中没有设置 enabled，检查环境变量
	// 如果环境变量也没有设置，返回空列表（不启用任何策略，避免默认启用 threshold）
	enabledStrategiesStr := getEnv("ENABLED_STRATEGIES", "")
	if enabledStrategiesStr == "" {
		return []string{} // 返回空列表，而不是默认启用 threshold
	}
	return parseStrategyList(enabledStrategiesStr)
}

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

// safeGetGridInt 安全地获取 Grid 配置的整数值
func safeGetGridInt(cf *ConfigFile, getter func(*ConfigFile) int) int {
	if cf == nil {
		return 0
	}
	return getter(cf)
}

// safeGetGridFloat 安全地获取 Grid 配置的浮点数值
func safeGetGridFloat(cf *ConfigFile, getter func(*ConfigFile) float64) float64 {
	if cf == nil {
		return 0
	}
	return getter(cf)
}

// safeGetGridBool 安全地获取 Grid 配置的布尔值
func safeGetGridBool(cf *ConfigFile, getter func(*ConfigFile) bool) bool {
	if cf == nil {
		return false
	}
	return getter(cf)
}

// safeGetGridIntSlice 安全地获取 Grid 配置的整数切片
func safeGetGridIntSlice(cf *ConfigFile, getter func(*ConfigFile) []int) []int {
	if cf == nil {
		return nil
	}
	return getter(cf)
}

// safeGetThresholdInt 安全地获取 Threshold 配置的整数值
func safeGetThresholdInt(cf *ConfigFile, getter func(*ConfigFile) int) int {
	if cf == nil {
		return 0
	}
	return getter(cf)
}

// safeGetThresholdFloat 安全地获取 Threshold 配置的浮点数值
func safeGetThresholdFloat(cf *ConfigFile, getter func(*ConfigFile) float64) float64 {
	if cf == nil {
		return 0
	}
	return getter(cf)
}

// safeGetThresholdString 安全地获取 Threshold 配置的字符串值
func safeGetThresholdString(cf *ConfigFile, getter func(*ConfigFile) string) string {
	if cf == nil {
		return ""
	}
	return getter(cf)
}

// safeGetArbitrageInt 安全地获取 Arbitrage 配置的整数值
func safeGetArbitrageInt(cf *ConfigFile, getter func(*ConfigFile) int) int {
	if cf == nil {
		return 0
	}
	return getter(cf)
}

// safeGetArbitrageFloat 安全地获取 Arbitrage 配置的浮点数值
func safeGetArbitrageFloat(cf *ConfigFile, getter func(*ConfigFile) float64) float64 {
	if cf == nil {
		return 0
	}
	return getter(cf)
}

// safeGetDataRecorderString 安全地获取 DataRecorder 配置的字符串值
func safeGetDataRecorderString(cf *ConfigFile, getter func(*ConfigFile) string) string {
	if cf == nil {
		return ""
	}
	return getter(cf)
}

// safeGetDataRecorderBool 安全地获取 DataRecorder 配置的布尔值
func safeGetDataRecorderBool(cf *ConfigFile, getter func(*ConfigFile) bool) bool {
	if cf == nil {
		return false
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
	if len(c.Strategies.EnabledStrategies) == 0 {
		return fmt.Errorf("至少需要启用一个策略")
	}

	// 验证启用的策略配置
	for _, strategyName := range c.Strategies.EnabledStrategies {
		switch strategyName {
		case "grid":
			if c.Strategies.Grid == nil {
				return fmt.Errorf("网格策略已启用但配置为空")
			}
			if len(c.Strategies.Grid.GridLevels) == 0 {
				return fmt.Errorf("网格层级列表 grid_levels 不能为空")
			}
			if c.Strategies.Grid.OrderSize <= 0 {
				return fmt.Errorf("ORDER_SIZE 必须大于 0")
			}
		case "threshold":
			if c.Strategies.Threshold == nil {
				return fmt.Errorf("价格阈值策略已启用但配置为空")
			}
			if c.Strategies.Threshold.BuyThreshold <= 0 || c.Strategies.Threshold.BuyThreshold >= 1 {
				return fmt.Errorf("THRESHOLD_BUY_THRESHOLD 必须在 0 到 1 之间")
			}
			if c.Strategies.Threshold.OrderSize <= 0 {
				return fmt.Errorf("THRESHOLD_ORDER_SIZE 必须大于 0")
			}
			if c.Strategies.Threshold.ProfitTargetCents < 0 {
				return fmt.Errorf("THRESHOLD_PROFIT_TARGET_CENTS 不能为负数")
			}
			if c.Strategies.Threshold.StopLossCents < 0 {
				return fmt.Errorf("THRESHOLD_STOP_LOSS_CENTS 不能为负数")
			}
		case "arbitrage":
			if c.Strategies.Arbitrage == nil {
				return fmt.Errorf("套利策略已启用但配置为空")
			}
			if c.Strategies.Arbitrage.LockStartMinutes <= 0 || c.Strategies.Arbitrage.LockStartMinutes >= 15 {
				return fmt.Errorf("ARBITRAGE_LOCK_START_MINUTES 必须在 0 到 15 之间")
			}
			if c.Strategies.Arbitrage.EarlyLockPriceThreshold <= 0 || c.Strategies.Arbitrage.EarlyLockPriceThreshold >= 1 {
				return fmt.Errorf("ARBITRAGE_EARLY_LOCK_PRICE_THRESHOLD 必须在 0 到 1 之间")
			}
			if c.Strategies.Arbitrage.TargetUpBase < 0 {
				return fmt.Errorf("ARBITRAGE_TARGET_UP_BASE 不能为负数")
			}
			if c.Strategies.Arbitrage.TargetDownBase < 0 {
				return fmt.Errorf("ARBITRAGE_TARGET_DOWN_BASE 不能为负数")
			}
			if c.Strategies.Arbitrage.BaseTarget <= 0 {
				return fmt.Errorf("ARBITRAGE_BASE_TARGET 必须大于 0")
			}
			if c.Strategies.Arbitrage.BuildLotSize <= 0 {
				return fmt.Errorf("ARBITRAGE_BUILD_LOT_SIZE 必须大于 0")
			}
			if c.Strategies.Arbitrage.MinOrderSize <= 0 {
				return fmt.Errorf("ARBITRAGE_MIN_ORDER_SIZE 必须大于 0")
			}
		case "datarecorder":
			if c.Strategies.DataRecorder == nil {
				return fmt.Errorf("数据记录策略已启用但配置为空")
			}
			if c.Strategies.DataRecorder.OutputDir == "" {
				return fmt.Errorf("DATARECORDER_OUTPUT_DIR 不能为空")
			}
		default:
			return fmt.Errorf("未知的策略: %s", strategyName)
		}
	}

	return nil
}

// parseStrategyList 解析策略列表（逗号分隔）
func parseStrategyList(str string) []string {
	if str == "" {
		return []string{}
	}
	parts := strings.Split(str, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
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
