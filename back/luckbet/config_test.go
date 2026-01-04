package luckbet

import (
	"os"
	"testing"
	"time"
)

// TestConfigDefaults 测试配置默认值
func TestConfigDefaults(t *testing.T) {
	config := &Config{}
	config.ApplyDefaults()
	
	if config.OrderSize != DefaultOrderSize {
		t.Errorf("OrderSize 默认值应该为 %.2f，实际为 %.2f", DefaultOrderSize, config.OrderSize)
	}
	
	if config.WindowSeconds != DefaultWindowSeconds {
		t.Errorf("WindowSeconds 默认值应该为 %d，实际为 %d", DefaultWindowSeconds, config.WindowSeconds)
	}
	
	if config.MinVelocityCentsPerSec != DefaultMinVelocity {
		t.Errorf("MinVelocityCentsPerSec 默认值应该为 %.2f，实际为 %.2f", DefaultMinVelocity, config.MinVelocityCentsPerSec)
	}
	
	if config.OrderExecutionMode != string(SequentialMode) {
		t.Errorf("OrderExecutionMode 默认值应该为 %s，实际为 %s", SequentialMode, config.OrderExecutionMode)
	}
	
	if config.BiasMode != "soft" {
		t.Errorf("BiasMode 默认值应该为 'soft'，实际为 '%s'", config.BiasMode)
	}
}

// TestConfigValidation 测试配置验证
func TestConfigValidation(t *testing.T) {
	// 测试有效配置
	validConfig := &Config{}
	validConfig.ApplyDefaults()
	
	if err := validConfig.Validate(); err != nil {
		t.Errorf("有效配置验证失败: %v", err)
	}
	
	// 测试无效配置 - OrderSize <= 0
	invalidConfig1 := &Config{OrderSize: 0}
	invalidConfig1.ApplyDefaults()
	invalidConfig1.OrderSize = 0 // 重置为无效值
	
	if err := invalidConfig1.Validate(); err == nil {
		t.Error("OrderSize <= 0 应该验证失败")
	}
	
	// 测试无效配置 - WindowSeconds <= 0
	invalidConfig2 := &Config{}
	invalidConfig2.ApplyDefaults()
	invalidConfig2.WindowSeconds = 0
	
	if err := invalidConfig2.Validate(); err == nil {
		t.Error("WindowSeconds <= 0 应该验证失败")
	}
	
	// 测试无效配置 - 无效的执行模式
	invalidConfig3 := &Config{}
	invalidConfig3.ApplyDefaults()
	invalidConfig3.OrderExecutionMode = "invalid_mode"
	
	if err := invalidConfig3.Validate(); err == nil {
		t.Error("无效的 OrderExecutionMode 应该验证失败")
	}
	
	// 测试无效配置 - 无效的偏向模式
	invalidConfig4 := &Config{}
	invalidConfig4.ApplyDefaults()
	invalidConfig4.BiasMode = "invalid_bias"
	
	if err := invalidConfig4.Validate(); err == nil {
		t.Error("无效的 BiasMode 应该验证失败")
	}
}

// TestConfigPriceRangeValidation 测试价格范围验证
func TestConfigPriceRangeValidation(t *testing.T) {
	config := &Config{}
	config.ApplyDefaults()
	
	// 测试有效的价格范围
	config.MinEntryPriceCents = 10
	config.MaxEntryPriceCents = 90
	
	if err := config.Validate(); err != nil {
		t.Errorf("有效价格范围验证失败: %v", err)
	}
	
	// 测试无效的价格范围 - Max <= Min
	config.MinEntryPriceCents = 50
	config.MaxEntryPriceCents = 50
	
	if err := config.Validate(); err == nil {
		t.Error("MaxEntryPriceCents <= MinEntryPriceCents 应该验证失败")
	}
	
	config.MaxEntryPriceCents = 30
	
	if err := config.Validate(); err == nil {
		t.Error("MaxEntryPriceCents < MinEntryPriceCents 应该验证失败")
	}
}

// TestPartialTakeProfitValidation 测试分批止盈配置验证
func TestPartialTakeProfitValidation(t *testing.T) {
	config := &Config{}
	config.ApplyDefaults()
	
	// 测试有效的分批止盈配置
	config.PartialTakeProfits = []PartialTakeProfit{
		{ProfitCents: 10, Percentage: 0.5},
		{ProfitCents: 20, Percentage: 0.3},
	}
	
	if err := config.Validate(); err != nil {
		t.Errorf("有效分批止盈配置验证失败: %v", err)
	}
	
	// 测试无效的分批止盈配置 - ProfitCents <= 0
	config.PartialTakeProfits = []PartialTakeProfit{
		{ProfitCents: 0, Percentage: 0.5},
	}
	
	if err := config.Validate(); err == nil {
		t.Error("ProfitCents <= 0 应该验证失败")
	}
	
	// 测试无效的分批止盈配置 - Percentage <= 0
	config.PartialTakeProfits = []PartialTakeProfit{
		{ProfitCents: 10, Percentage: 0},
	}
	
	if err := config.Validate(); err == nil {
		t.Error("Percentage <= 0 应该验证失败")
	}
	
	// 测试无效的分批止盈配置 - Percentage > 1
	config.PartialTakeProfits = []PartialTakeProfit{
		{ProfitCents: 10, Percentage: 1.5},
	}
	
	if err := config.Validate(); err == nil {
		t.Error("Percentage > 1 应该验证失败")
	}
}

// TestConfigManager 测试配置管理器
func TestConfigManager(t *testing.T) {
	// 创建临时配置文件
	tmpFile := "/tmp/test_luckbet_config.yaml"
	defer os.Remove(tmpFile)
	
	// 创建配置管理器
	cm := NewConfigManager(tmpFile)
	
	if cm == nil {
		t.Fatal("NewConfigManager 返回了 nil")
	}
	
	if cm.configPath != tmpFile {
		t.Errorf("配置文件路径应该为 %s，实际为 %s", tmpFile, cm.configPath)
	}
}

// TestConfigManagerLoadNonExistentFile 测试加载不存在的配置文件
func TestConfigManagerLoadNonExistentFile(t *testing.T) {
	cm := NewConfigManager("/non/existent/file.yaml")
	
	config, err := cm.LoadConfig()
	if err != nil {
		t.Errorf("加载不存在的配置文件应该使用默认配置，但返回错误: %v", err)
	}
	
	if config == nil {
		t.Fatal("配置不应该为 nil")
	}
	
	// 验证使用了默认值
	if config.OrderSize != DefaultOrderSize {
		t.Errorf("应该使用默认 OrderSize %.2f，实际为 %.2f", DefaultOrderSize, config.OrderSize)
	}
}

// TestEnvironmentOverrides 测试环境变量覆盖
func TestEnvironmentOverrides(t *testing.T) {
	// 设置环境变量
	os.Setenv("LUCKBET_ORDER_SIZE", "25.5")
	os.Setenv("LUCKBET_WINDOW_SECONDS", "45")
	os.Setenv("LUCKBET_ENABLE_TERMINAL_UI", "true")
	os.Setenv("LUCKBET_ORDER_EXECUTION_MODE", "parallel")
	
	defer func() {
		os.Unsetenv("LUCKBET_ORDER_SIZE")
		os.Unsetenv("LUCKBET_WINDOW_SECONDS")
		os.Unsetenv("LUCKBET_ENABLE_TERMINAL_UI")
		os.Unsetenv("LUCKBET_ORDER_EXECUTION_MODE")
	}()
	
	cm := NewConfigManager("")
	config, err := cm.LoadConfig()
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	
	if config.OrderSize != 25.5 {
		t.Errorf("环境变量 ORDER_SIZE 应该覆盖为 25.5，实际为 %.2f", config.OrderSize)
	}
	
	if config.WindowSeconds != 45 {
		t.Errorf("环境变量 WINDOW_SECONDS 应该覆盖为 45，实际为 %d", config.WindowSeconds)
	}
	
	if !config.EnableTerminalUI {
		t.Error("环境变量 ENABLE_TERMINAL_UI 应该覆盖为 true")
	}
	
	if config.OrderExecutionMode != "parallel" {
		t.Errorf("环境变量 ORDER_EXECUTION_MODE 应该覆盖为 'parallel'，实际为 '%s'", config.OrderExecutionMode)
	}
}

// TestCreateDefaultConfig 测试创建默认配置文件
func TestCreateDefaultConfig(t *testing.T) {
	tmpFile := "/tmp/test_default_config.yaml"
	defer os.Remove(tmpFile)
	
	err := CreateDefaultConfig(tmpFile)
	if err != nil {
		t.Fatalf("创建默认配置文件失败: %v", err)
	}
	
	// 验证文件是否存在
	if _, err := os.Stat(tmpFile); os.IsNotExist(err) {
		t.Error("默认配置文件应该被创建")
	}
	
	// 加载并验证配置
	cm := NewConfigManager(tmpFile)
	config, err := cm.LoadConfig()
	if err != nil {
		t.Fatalf("加载默认配置文件失败: %v", err)
	}
	
	if config.OrderSize != DefaultOrderSize {
		t.Errorf("默认配置文件中的 OrderSize 应该为 %.2f，实际为 %.2f", DefaultOrderSize, config.OrderSize)
	}
}

// TestConfigHotReload 测试配置热重载
func TestConfigHotReload(t *testing.T) {
	tmpFile := "/tmp/test_hot_reload_config.yaml"
	defer os.Remove(tmpFile)
	
	// 创建初始配置文件
	err := CreateDefaultConfig(tmpFile)
	if err != nil {
		t.Fatalf("创建配置文件失败: %v", err)
	}
	
	cm := NewConfigManager(tmpFile)
	
	// 首次加载
	config1, err := cm.LoadConfig()
	if err != nil {
		t.Fatalf("首次加载配置失败: %v", err)
	}
	
	// 等待一小段时间确保文件时间戳不同
	time.Sleep(10 * time.Millisecond)
	
	// 修改配置文件
	config1.OrderSize = 99.99
	err = cm.SaveConfig(config1)
	if err != nil {
		t.Fatalf("保存配置失败: %v", err)
	}
	
	// 检查是否检测到变化
	if !cm.CheckForConfigChanges() {
		t.Error("应该检测到配置文件变化")
	}
	
	// 重新加载配置
	config2, err := cm.ReloadConfig()
	if err != nil {
		t.Fatalf("重新加载配置失败: %v", err)
	}
	
	if config2.OrderSize != 99.99 {
		t.Errorf("重新加载后 OrderSize 应该为 99.99，实际为 %.2f", config2.OrderSize)
	}
}

// TestConfigManagerWithEmptyPath 测试空路径的配置管理器
func TestConfigManagerWithEmptyPath(t *testing.T) {
	cm := NewConfigManager("")
	
	config, err := cm.LoadConfig()
	if err != nil {
		t.Errorf("空路径配置管理器应该使用默认配置，但返回错误: %v", err)
	}
	
	if config == nil {
		t.Fatal("配置不应该为 nil")
	}
	
	// 验证使用了默认值
	if config.OrderSize != DefaultOrderSize {
		t.Errorf("应该使用默认 OrderSize %.2f，实际为 %.2f", DefaultOrderSize, config.OrderSize)
	}
	
	// 测试检查配置变化（空路径应该返回 false）
	if cm.CheckForConfigChanges() {
		t.Error("空路径配置管理器不应该检测到配置变化")
	}
}