package luckbet

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/betbot/gobet/internal/common"
	"gopkg.in/yaml.v3"
)

const ID = "luckbet"

// Config LuckBet策略配置
// 遵循BBGO风格的配置设计，支持YAML配置文件和环境变量覆盖
type Config struct {
	// ===== 基础交易参数 =====
	OrderSize      float64 `yaml:"orderSize" json:"orderSize"`           // Entry订单大小（shares）
	HedgeOrderSize float64 `yaml:"hedgeOrderSize" json:"hedgeOrderSize"` // Hedge订单大小（shares，0表示与Entry相同）

	// ===== 速度参数 =====
	WindowSeconds              int     `yaml:"windowSeconds" json:"windowSeconds"`                           // 速度计算窗口大小（秒）
	MinMoveCents              int     `yaml:"minMoveCents" json:"minMoveCents"`                             // 最小价格变化（分）
	MinVelocityCentsPerSec    float64 `yaml:"minVelocityCentsPerSec" json:"minVelocityCentsPerSec"`         // 最小速度阈值（分/秒）
	CooldownMs                int     `yaml:"cooldownMs" json:"cooldownMs"`                                 // 交易冷却时间（毫秒）
	WarmupMs                  int     `yaml:"warmupMs" json:"warmupMs"`                                     // 策略预热时间（毫秒）
	MaxTradesPerCycle         int     `yaml:"maxTradesPerCycle" json:"maxTradesPerCycle"`                   // 每周期最大交易次数

	// ===== 安全参数 =====
	HedgeOffsetCents      int `yaml:"hedgeOffsetCents" json:"hedgeOffsetCents"`           // 对冲价格偏移（分）
	MinEntryPriceCents    int `yaml:"minEntryPriceCents" json:"minEntryPriceCents"`       // 最小入场价格（分）
	MaxEntryPriceCents    int `yaml:"maxEntryPriceCents" json:"maxEntryPriceCents"`       // 最大入场价格（分）
	MaxSpreadCents        int `yaml:"maxSpreadCents" json:"maxSpreadCents"`               // 最大价差（分）

	// ===== 执行模式 =====
	OrderExecutionMode           string `yaml:"orderExecutionMode" json:"orderExecutionMode"`                     // 订单执行模式（sequential/parallel）
	SequentialCheckIntervalMs    int    `yaml:"sequentialCheckIntervalMs" json:"sequentialCheckIntervalMs"`       // 顺序模式检查间隔（毫秒）
	SequentialMaxWaitMs          int    `yaml:"sequentialMaxWaitMs" json:"sequentialMaxWaitMs"`                   // 顺序模式最大等待时间（毫秒）

	// ===== 风险控制 =====
	CycleEndProtectionMinutes    int     `yaml:"cycleEndProtectionMinutes" json:"cycleEndProtectionMinutes"`       // 周期结束保护时间（分钟）
	HedgeReorderTimeoutSeconds   int     `yaml:"hedgeReorderTimeoutSeconds" json:"hedgeReorderTimeoutSeconds"`     // 对冲订单超时重下时间（秒）
	InventoryThreshold           float64 `yaml:"inventoryThreshold" json:"inventoryThreshold"`                     // 库存不平衡阈值

	// ===== 市场质量 =====
	EnableMarketQualityGate      bool `yaml:"enableMarketQualityGate" json:"enableMarketQualityGate"`           // 启用市场质量过滤
	MarketQualityMinScore        int  `yaml:"marketQualityMinScore" json:"marketQualityMinScore"`               // 市场质量最小评分
	MarketQualityMaxSpreadCents  int  `yaml:"marketQualityMaxSpreadCents" json:"marketQualityMaxSpreadCents"`   // 市场质量最大价差（分）
	MarketQualityMaxBookAgeMs    int  `yaml:"marketQualityMaxBookAgeMs" json:"marketQualityMaxBookAgeMs"`       // 订单簿最大年龄（毫秒）

	// ===== 退出策略 =====
	TakeProfitCents       int                   `yaml:"takeProfitCents" json:"takeProfitCents"`             // 止盈价格（分）
	StopLossCents         int                   `yaml:"stopLossCents" json:"stopLossCents"`                 // 止损价格（分）
	MaxHoldSeconds        int                   `yaml:"maxHoldSeconds" json:"maxHoldSeconds"`               // 最大持有时间（秒）
	ExitCooldownMs        int                   `yaml:"exitCooldownMs" json:"exitCooldownMs"`               // 退出冷却时间（毫秒）
	ExitBothSidesIfHedged bool                  `yaml:"exitBothSidesIfHedged" json:"exitBothSidesIfHedged"` // 如果已对冲则同时退出两边
	PartialTakeProfits    []PartialTakeProfit   `yaml:"partialTakeProfits" json:"partialTakeProfits"`       // 分批止盈配置

	// ===== 外部数据集成 =====
	UseBinanceOpen1mBias         bool    `yaml:"useBinanceOpen1mBias" json:"useBinanceOpen1mBias"`               // 使用Binance 1分钟开盘偏向
	BiasMode                     string  `yaml:"biasMode" json:"biasMode"`                                       // 偏向模式（hard/soft）
	UseBinanceMoveConfirm        bool    `yaml:"useBinanceMoveConfirm" json:"useBinanceMoveConfirm"`             // 使用Binance移动确认

	// ===== UI配置 =====
	EnableTerminalUI             bool `yaml:"enableTerminalUI" json:"enableTerminalUI"`                       // 启用终端UI
	UIUpdateIntervalMs           int  `yaml:"uiUpdateIntervalMs" json:"uiUpdateIntervalMs"`                   // UI更新间隔（毫秒）

	// ===== 自动合并配置 =====
	AutoMerge common.AutoMergeConfig `yaml:"autoMerge" json:"autoMerge"`
}

// Validate 验证配置有效性
func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("config不能为空")
	}

	// 标准化AutoMerge配置
	c.AutoMerge.Normalize()

	// 基础交易参数验证
	if c.OrderSize <= 0 {
		return fmt.Errorf("orderSize必须大于0，当前值: %.6f", c.OrderSize)
	}
	if c.HedgeOrderSize < 0 {
		return fmt.Errorf("hedgeOrderSize不能为负数，当前值: %.6f", c.HedgeOrderSize)
	}

	// 速度参数验证
	if c.WindowSeconds <= 0 {
		return fmt.Errorf("windowSeconds必须大于0，当前值: %d", c.WindowSeconds)
	}
	if c.MinMoveCents < 0 {
		return fmt.Errorf("minMoveCents不能为负数，当前值: %d", c.MinMoveCents)
	}
	if c.MinVelocityCentsPerSec < 0 {
		return fmt.Errorf("minVelocityCentsPerSec不能为负数，当前值: %.6f", c.MinVelocityCentsPerSec)
	}
	if c.MaxTradesPerCycle < 0 {
		return fmt.Errorf("maxTradesPerCycle不能为负数，当前值: %d", c.MaxTradesPerCycle)
	}

	// 安全参数验证
	if c.HedgeOffsetCents < 0 {
		return fmt.Errorf("hedgeOffsetCents不能为负数，当前值: %d", c.HedgeOffsetCents)
	}
	if c.MinEntryPriceCents < 0 {
		return fmt.Errorf("minEntryPriceCents不能为负数，当前值: %d", c.MinEntryPriceCents)
	}
	if c.MaxEntryPriceCents > 0 && c.MaxEntryPriceCents <= c.MinEntryPriceCents {
		return fmt.Errorf("maxEntryPriceCents必须大于minEntryPriceCents，当前值: max=%d, min=%d", 
			c.MaxEntryPriceCents, c.MinEntryPriceCents)
	}

	// 执行模式验证
	if c.OrderExecutionMode != "" && 
		c.OrderExecutionMode != string(SequentialMode) && 
		c.OrderExecutionMode != string(ParallelMode) {
		return fmt.Errorf("orderExecutionMode必须是'sequential'或'parallel'，当前值: %s", c.OrderExecutionMode)
	}

	// 偏向模式验证
	if c.BiasMode != "" && c.BiasMode != "hard" && c.BiasMode != "soft" {
		return fmt.Errorf("biasMode必须是'hard'或'soft'，当前值: %s", c.BiasMode)
	}

	// 分批止盈验证
	for i, ptp := range c.PartialTakeProfits {
		if ptp.ProfitCents <= 0 {
			return fmt.Errorf("partialTakeProfits[%d].profitCents必须大于0，当前值: %d", i, ptp.ProfitCents)
		}
		if ptp.Percentage <= 0 || ptp.Percentage > 1 {
			return fmt.Errorf("partialTakeProfits[%d].percentage必须在(0,1]范围内，当前值: %.6f", i, ptp.Percentage)
		}
	}

	return nil
}

// ApplyDefaults 应用默认值
func (c *Config) ApplyDefaults() {
	if c.OrderSize == 0 {
		c.OrderSize = DefaultOrderSize
	}
	if c.HedgeOrderSize == 0 {
		c.HedgeOrderSize = c.OrderSize // 默认与Entry相同
	}
	if c.WindowSeconds == 0 {
		c.WindowSeconds = DefaultWindowSeconds
	}
	if c.MinMoveCents == 0 {
		c.MinMoveCents = DefaultMinMoveCents
	}
	if c.MinVelocityCentsPerSec == 0 {
		c.MinVelocityCentsPerSec = DefaultMinVelocity
	}
	if c.HedgeOffsetCents == 0 {
		c.HedgeOffsetCents = DefaultHedgeOffsetCents
	}
	if c.MaxTradesPerCycle == 0 {
		c.MaxTradesPerCycle = DefaultMaxTradesPerCycle
	}
	if c.TakeProfitCents == 0 {
		c.TakeProfitCents = DefaultTakeProfitCents
	}
	if c.StopLossCents == 0 {
		c.StopLossCents = DefaultStopLossCents
	}
	if c.MaxHoldSeconds == 0 {
		c.MaxHoldSeconds = DefaultMaxHoldSeconds
	}
	if c.UIUpdateIntervalMs == 0 {
		c.UIUpdateIntervalMs = DefaultUIUpdateIntervalMs
	}
	if c.OrderExecutionMode == "" {
		c.OrderExecutionMode = string(SequentialMode)
	}
	if c.BiasMode == "" {
		c.BiasMode = "soft"
	}
	if c.SequentialCheckIntervalMs == 0 {
		c.SequentialCheckIntervalMs = 100
	}
	if c.SequentialMaxWaitMs == 0 {
		c.SequentialMaxWaitMs = 5000
	}
	if c.HedgeReorderTimeoutSeconds == 0 {
		c.HedgeReorderTimeoutSeconds = 30
	}
	if c.CycleEndProtectionMinutes == 0 {
		c.CycleEndProtectionMinutes = 2
	}
	if c.InventoryThreshold == 0 {
		c.InventoryThreshold = 50.0
	}
	if c.MarketQualityMinScore == 0 {
		c.MarketQualityMinScore = 70
	}
	if c.MarketQualityMaxSpreadCents == 0 {
		c.MarketQualityMaxSpreadCents = 10
	}
	if c.MarketQualityMaxBookAgeMs == 0 {
		c.MarketQualityMaxBookAgeMs = 5000
	}
}

// ConfigManager 配置管理器
// 负责配置的加载、验证、热重载和环境变量覆盖
type ConfigManager struct {
	config     *Config
	configPath string
	lastModTime time.Time
}

// NewConfigManager 创建配置管理器
func NewConfigManager(configPath string) *ConfigManager {
	return &ConfigManager{
		configPath: configPath,
	}
}

// LoadConfig 加载配置文件
func (cm *ConfigManager) LoadConfig() (*Config, error) {
	config := &Config{}
	
	// 如果配置文件存在，则加载
	if cm.configPath != "" {
		if err := cm.loadFromFile(config); err != nil {
			return nil, fmt.Errorf("加载配置文件失败: %w", err)
		}
	}
	
	// 应用默认值
	config.ApplyDefaults()
	
	// 应用环境变量覆盖
	cm.applyEnvironmentOverrides(config)
	
	// 验证配置
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("配置验证失败: %w", err)
	}
	
	cm.config = config
	return config, nil
}

// loadFromFile 从文件加载配置
func (cm *ConfigManager) loadFromFile(config *Config) error {
	data, err := os.ReadFile(cm.configPath)
	if err != nil {
		if os.IsNotExist(err) {
			// 配置文件不存在，使用默认配置
			return nil
		}
		return fmt.Errorf("读取配置文件失败: %w", err)
	}
	
	if err := yaml.Unmarshal(data, config); err != nil {
		return fmt.Errorf("解析YAML配置失败: %w", err)
	}
	
	// 记录文件修改时间用于热重载
	if stat, err := os.Stat(cm.configPath); err == nil {
		cm.lastModTime = stat.ModTime()
	}
	
	return nil
}

// applyEnvironmentOverrides 应用环境变量覆盖
// 环境变量格式: LUCKBET_FIELD_NAME
func (cm *ConfigManager) applyEnvironmentOverrides(config *Config) {
	prefix := "LUCKBET_"
	
	// 基础交易参数
	if val := os.Getenv(prefix + "ORDER_SIZE"); val != "" {
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			config.OrderSize = f
		}
	}
	if val := os.Getenv(prefix + "HEDGE_ORDER_SIZE"); val != "" {
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			config.HedgeOrderSize = f
		}
	}
	
	// 速度参数
	if val := os.Getenv(prefix + "WINDOW_SECONDS"); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			config.WindowSeconds = i
		}
	}
	if val := os.Getenv(prefix + "MIN_MOVE_CENTS"); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			config.MinMoveCents = i
		}
	}
	if val := os.Getenv(prefix + "MIN_VELOCITY_CENTS_PER_SEC"); val != "" {
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			config.MinVelocityCentsPerSec = f
		}
	}
	if val := os.Getenv(prefix + "MAX_TRADES_PER_CYCLE"); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			config.MaxTradesPerCycle = i
		}
	}
	
	// 安全参数
	if val := os.Getenv(prefix + "HEDGE_OFFSET_CENTS"); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			config.HedgeOffsetCents = i
		}
	}
	if val := os.Getenv(prefix + "MIN_ENTRY_PRICE_CENTS"); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			config.MinEntryPriceCents = i
		}
	}
	if val := os.Getenv(prefix + "MAX_ENTRY_PRICE_CENTS"); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			config.MaxEntryPriceCents = i
		}
	}
	
	// 执行模式
	if val := os.Getenv(prefix + "ORDER_EXECUTION_MODE"); val != "" {
		config.OrderExecutionMode = val
	}
	
	// 风险控制
	if val := os.Getenv(prefix + "INVENTORY_THRESHOLD"); val != "" {
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			config.InventoryThreshold = f
		}
	}
	
	// 退出策略
	if val := os.Getenv(prefix + "TAKE_PROFIT_CENTS"); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			config.TakeProfitCents = i
		}
	}
	if val := os.Getenv(prefix + "STOP_LOSS_CENTS"); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			config.StopLossCents = i
		}
	}
	if val := os.Getenv(prefix + "MAX_HOLD_SECONDS"); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			config.MaxHoldSeconds = i
		}
	}
	
	// 外部数据
	if val := os.Getenv(prefix + "USE_BINANCE_OPEN_1M_BIAS"); val != "" {
		config.UseBinanceOpen1mBias = strings.ToLower(val) == "true"
	}
	if val := os.Getenv(prefix + "BIAS_MODE"); val != "" {
		config.BiasMode = val
	}
	if val := os.Getenv(prefix + "USE_BINANCE_MOVE_CONFIRM"); val != "" {
		config.UseBinanceMoveConfirm = strings.ToLower(val) == "true"
	}
	
	// UI配置
	if val := os.Getenv(prefix + "ENABLE_TERMINAL_UI"); val != "" {
		config.EnableTerminalUI = strings.ToLower(val) == "true"
	}
	if val := os.Getenv(prefix + "UI_UPDATE_INTERVAL_MS"); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			config.UIUpdateIntervalMs = i
		}
	}
	
	// 市场质量
	if val := os.Getenv(prefix + "ENABLE_MARKET_QUALITY_GATE"); val != "" {
		config.EnableMarketQualityGate = strings.ToLower(val) == "true"
	}
	if val := os.Getenv(prefix + "MARKET_QUALITY_MIN_SCORE"); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			config.MarketQualityMinScore = i
		}
	}
}

// CheckForConfigChanges 检查配置文件是否有变化（用于热重载）
func (cm *ConfigManager) CheckForConfigChanges() bool {
	if cm.configPath == "" {
		return false
	}
	
	stat, err := os.Stat(cm.configPath)
	if err != nil {
		return false
	}
	
	return stat.ModTime().After(cm.lastModTime)
}

// ReloadConfig 重新加载配置
func (cm *ConfigManager) ReloadConfig() (*Config, error) {
	return cm.LoadConfig()
}

// GetConfig 获取当前配置
func (cm *ConfigManager) GetConfig() *Config {
	return cm.config
}

// SaveConfig 保存配置到文件
func (cm *ConfigManager) SaveConfig(config *Config) error {
	if cm.configPath == "" {
		return fmt.Errorf("未指定配置文件路径")
	}
	
	data, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("序列化配置失败: %w", err)
	}
	
	if err := os.WriteFile(cm.configPath, data, 0644); err != nil {
		return fmt.Errorf("写入配置文件失败: %w", err)
	}
	
	return nil
}

// CreateDefaultConfig 创建默认配置文件
func CreateDefaultConfig(configPath string) error {
	config := &Config{}
	config.ApplyDefaults()
	
	cm := NewConfigManager(configPath)
	return cm.SaveConfig(config)
}