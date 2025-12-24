package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/betbot/gobet/clob/client"
	"github.com/betbot/gobet/clob/signing"
	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/services"
	"github.com/betbot/gobet/pkg/config"
	"github.com/betbot/gobet/pkg/logger"
)

// MarketInfo 市场信息（用于 JSON 序列化）
type MarketInfo struct {
	Slug        string `json:"slug"`
	YesAssetID  string `json:"yesAssetId"`
	NoAssetID   string `json:"noAssetId"`
	ConditionID string `json:"conditionId"`
	Question    string `json:"question"`
	Timestamp   int64  `json:"timestamp"`
}

// MarketDataFile 市场数据文件结构
type MarketDataFile struct {
	Markets      []MarketInfo `json:"markets"`
	GeneratedAt  int64        `json:"generatedAt"`
	TotalMarkets int          `json:"totalMarkets"`
}

func main() {
	// 初始化日志
	if err := logger.InitDefault(); err != nil {
		fmt.Printf("初始化日志失败: %v\n", err)
		os.Exit(1)
	}

	// 设置配置文件路径（如果存在默认配置文件）
	defaultConfigPath := "config.yaml"
	if _, err := os.Stat(defaultConfigPath); err == nil {
		config.SetConfigPath(defaultConfigPath)
		logger.Infof("使用默认配置文件: %s", defaultConfigPath)
	}

	// 加载配置
	cfg, err := config.Load()
	if err != nil {
		logger.Errorf("加载配置失败: %v", err)
		os.Exit(1)
	}

	logger.Info("开始准备市场数据...")
	logger.Info("注意：此工具是可选的，主程序会自动预加载数据。")
	logger.Info("运行此工具可以提升启动速度和稳定性。")

	// 创建数据目录
	dataDir := "data"
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		logger.Errorf("创建数据目录失败: %v", err)
		os.Exit(1)
	}

	// 初始化 CLOB 客户端
	privateKey, err := signing.PrivateKeyFromHex(cfg.Wallet.PrivateKey)
	if err != nil {
		logger.Errorf("解析私钥失败: %v", err)
		os.Exit(1)
	}

	// 创建临时客户端用于推导 API 凭证（市场数据获取不需要认证）
	tempClient := client.NewClient(
		"https://clob.polymarket.com",
		types.ChainPolygon,
		privateKey,
		nil,
	)

	// 创建市场数据服务（使用临时客户端，Gamma API 是公开的）
	clobClient := tempClient

	// 创建市场数据服务
	spec, err := cfg.Market.Spec()
	if err != nil {
		logger.Errorf("market 配置无效: %v", err)
		os.Exit(1)
	}
	marketDataService := services.NewMarketDataService(clobClient, spec)

	// 生成 slugs
	logger.Info("生成接下来 100 个周期的 slugs...")
	slugs := spec.NextSlugs(100)
	logger.Infof("已生成 %d 个 slugs", len(slugs))

	// 获取市场数据
	ctx := context.Background()
	markets := make([]MarketInfo, 0)

	for i, slug := range slugs {
		logger.Infof("获取市场数据 (%d/%d): %s", i+1, len(slugs), slug)

		market, err := marketDataService.FetchMarketInfo(ctx, slug)
		if err != nil {
			logger.Warnf("获取市场数据失败 %s: %v", slug, err)
			continue
		}

		markets = append(markets, MarketInfo{
			Slug:        market.Slug,
			YesAssetID:  market.YesAssetID,
			NoAssetID:   market.NoAssetID,
			ConditionID: market.ConditionID,
			Question:    market.Question,
			Timestamp:   market.Timestamp,
		})

		// 速率限制
		if i < len(slugs)-1 {
			time.Sleep(200 * time.Millisecond)
		}

		// 进度日志
		if (i+1)%10 == 0 {
			logger.Infof("进度: %d/%d", i+1, len(slugs))
		}
	}

	// 保存到文件
	marketDataFile := MarketDataFile{
		Markets:      markets,
		GeneratedAt:  time.Now().UnixMilli(),
		TotalMarkets: len(markets),
	}

	filePath := filepath.Join(dataDir, "market-data.json")
	data, err := json.MarshalIndent(marketDataFile, "", "  ")
	if err != nil {
		logger.Errorf("序列化市场数据失败: %v", err)
		os.Exit(1)
	}

	if err := os.WriteFile(filePath, data, 0644); err != nil {
		logger.Errorf("保存市场数据失败: %v", err)
		os.Exit(1)
	}

	logger.Infof("市场数据已保存到: %s", filePath)
	logger.Infof("总计: %d 个市场", len(markets))
}
