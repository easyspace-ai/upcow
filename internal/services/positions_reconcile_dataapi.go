package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/betbot/gobet/internal/domain"
	"github.com/sirupsen/logrus"
)

var reconcileLog = logrus.WithField("component", "positions_reconcile")

// FetchMarketTokenSizesFromDataAPI returns the current YES/NO token sizes (shares) for the given market,
// as observed by Polymarket Data API positions endpoint.
//
// This is useful for:
// - post-merge reconciliation (YES+NO merged into USDC)
// - sanity checking holdings before auto merge
//
// NOTE: Data API data may lag. Callers should treat it as eventually consistent.
func (s *TradingService) FetchMarketTokenSizesFromDataAPI(ctx context.Context, market *domain.Market) (yes float64, no float64, err error) {
	if s == nil {
		return 0, 0, fmt.Errorf("trading service is nil")
	}
	if market == nil || strings.TrimSpace(market.YesAssetID) == "" || strings.TrimSpace(market.NoAssetID) == "" {
		return 0, 0, fmt.Errorf("market invalid")
	}
	user := strings.TrimSpace(s.funderAddress)
	if user == "" {
		// fallback to signer address is possible but in this project the funder is the true inventory owner
		return 0, 0, fmt.Errorf("funder address not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// bounded request (增加超时时间到20秒，因为 Data API 可能响应较慢)
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	// sizeThreshold=0 to not miss small balances; limit=500 should be enough for single-bot
	apiURL := fmt.Sprintf("https://data-api.polymarket.com/positions?user=%s&sizeThreshold=0&limit=10", user)
	req, e := http.NewRequestWithContext(cctx, "GET", apiURL, nil)
	if e != nil {
		return 0, 0, e
	}
	req.Header.Set("Content-Type", "application/json")

	// 配置 HTTP 客户端，支持代理
	transport := &http.Transport{
		MaxIdleConns:        100,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}

	// 从环境变量获取代理配置（与 clob/client 保持一致）
	proxyURLStr := getProxyURLFromEnv()
	if proxyURLStr != "" {
		if parsedURL, err := url.Parse(proxyURLStr); err == nil {
			transport.Proxy = http.ProxyURL(parsedURL)
			reconcileLog.Debugf("📡 [FetchMarketTokenSizesFromDataAPI] 使用代理: %s", parsedURL.Host)
		} else {
			reconcileLog.Warnf("⚠️ [FetchMarketTokenSizesFromDataAPI] 解析代理 URL 失败: %s err=%v", proxyURLStr, err)
		}
	} else {
		reconcileLog.Debugf("📡 [FetchMarketTokenSizesFromDataAPI] 未配置代理，使用直接连接")
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   20 * time.Second,
	}
	resp, e := client.Do(req)
	if e != nil {
		return 0, 0, e
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, 0, fmt.Errorf("data api status=%d", resp.StatusCode)
	}

	var positions []map[string]any
	if e := json.NewDecoder(resp.Body).Decode(&positions); e != nil {
		return 0, 0, e
	}

	reconcileLog.Debugf("📊 [FetchMarketTokenSizesFromDataAPI] API 返回 %d 个持仓，查找市场: slug=%s YesAssetID=%s NoAssetID=%s",
		len(positions), market.Slug, market.YesAssetID, market.NoAssetID)

	for _, pos := range positions {
		asset, _ := pos["asset"].(string)
		if asset == "" {
			continue
		}

		// 解析 size：API 可能返回字符串或数字类型
		var sz float64
		switch v := pos["size"].(type) {
		case string:
			if v == "" {
				continue
			}
			var err error
			sz, err = strconv.ParseFloat(v, 64)
			if err != nil {
				reconcileLog.Debugf("⚠️ [FetchMarketTokenSizesFromDataAPI] 无法解析 size 字符串: asset=%s size=%v err=%v", asset, v, err)
				continue
			}
		case float64:
			sz = v
		case float32:
			sz = float64(v)
		case int:
			sz = float64(v)
		case int64:
			sz = float64(v)
		default:
			// 尝试转换为 float64
			if vFloat, ok := v.(float64); ok {
				sz = vFloat
			} else {
				reconcileLog.Debugf("⚠️ [FetchMarketTokenSizesFromDataAPI] size 类型不支持: asset=%s size=%v (type=%T)", asset, v, v)
				continue
			}
		}

		if sz <= 0 {
			continue
		}

		// 匹配 asset ID（不区分大小写，因为 API 可能返回小写）
		assetLower := strings.ToLower(strings.TrimSpace(asset))
		yesAssetLower := strings.ToLower(strings.TrimSpace(market.YesAssetID))
		noAssetLower := strings.ToLower(strings.TrimSpace(market.NoAssetID))

		if assetLower == yesAssetLower {
			yes = sz
			reconcileLog.Infof("✅ [FetchMarketTokenSizesFromDataAPI] 匹配 UP 持仓: asset=%s size=%.4f market=%s", asset, sz, market.Slug)
		} else if assetLower == noAssetLower {
			no = sz
			reconcileLog.Infof("✅ [FetchMarketTokenSizesFromDataAPI] 匹配 DOWN 持仓: asset=%s size=%.4f market=%s", asset, sz, market.Slug)
		} else {
			// 检查是否是同一个市场的其他资产（通过 slug 匹配）
			posSlug, _ := pos["slug"].(string)
			if posSlug == market.Slug {
				reconcileLog.Debugf("⚠️ [FetchMarketTokenSizesFromDataAPI] 同一市场但 asset 不匹配: asset=%s size=%.4f market=%s (期望 YesAssetID=%s NoAssetID=%s)",
					asset, sz, market.Slug, market.YesAssetID, market.NoAssetID)
			}
		}
	}

	if yes == 0 && no == 0 && len(positions) > 0 {
		reconcileLog.Warnf("⚠️ [FetchMarketTokenSizesFromDataAPI] 未找到匹配的持仓: market=%s YesAssetID=%s NoAssetID=%s (API 返回 %d 个持仓)",
			market.Slug, market.YesAssetID, market.NoAssetID, len(positions))
	}

	return yes, no, nil
}

// getProxyURLFromEnv 从环境变量获取代理 URL（与 clob/client 保持一致）
func getProxyURLFromEnv() string {
	// 检查常见的代理环境变量
	proxyVars := []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"}
	for _, v := range proxyVars {
		if val := os.Getenv(v); val != "" {
			return val
		}
	}
	// 如果环境变量未设置，返回空字符串（不使用代理）
	return ""
}

// ReconcileMarketPositionsFromDataAPI updates OrderEngine positions for this market (best-effort)
// using the Data API sizes (YES/NO).
//
// It only touches positions in the given market and token types (UP/DOWN).
// CostBasis/AvgPrice are not reconstructed here.
func (s *TradingService) ReconcileMarketPositionsFromDataAPI(ctx context.Context, market *domain.Market) error {
	if s == nil {
		return fmt.Errorf("trading service is nil")
	}
	if market == nil || strings.TrimSpace(market.Slug) == "" {
		return fmt.Errorf("market invalid")
	}

	yesSz, noSz, err := s.FetchMarketTokenSizesFromDataAPI(ctx, market)
	if err != nil {
		reconcileLog.Warnf("⚠️ [ReconcileMarketPositions] 从 Data API 获取持仓失败: market=%s err=%v", market.Slug, err)
		return err
	}
	reconcileLog.Infof("📊 [ReconcileMarketPositions] Data API 返回持仓: market=%s UP=%.4f DOWN=%.4f", market.Slug, yesSz, noSz)

	// Helper: upsert a position size
	upsert := func(token domain.TokenType, desired float64) error {
		assetID := market.YesAssetID
		if token == domain.TokenTypeDown {
			assetID = market.NoAssetID
		}
		positionID := fmt.Sprintf("%s_%s_%s", market.Slug, assetID, token)

		// If desired <= 0: check if local position exists
		// If local position exists and has size > 0, preserve it (Data API may not be synced yet)
		if desired <= 0 {
			if p, e := s.GetPosition(positionID); e == nil && p != nil && p.IsOpen() && p.Size > 0 {
				reconcileLog.Warnf("⚠️ [ReconcileMarketPositions] Data API 返回 0，但本地有持仓，保留本地持仓: positionID=%s tokenType=%s localSize=%.4f",
					positionID, token, p.Size)
				return nil // 保留本地持仓，不覆盖
			}
			// Only close if local position doesn't exist or is already closed
			if p, e := s.GetPosition(positionID); e == nil && p != nil && p.IsOpen() {
				return s.UpdatePosition(ctx, positionID, func(pp *domain.Position) {
					pp.Size = 0
					pp.Status = domain.PositionStatusClosed
				})
			}
			return nil
		}

		// If exists: update size
		if p, e := s.GetPosition(positionID); e == nil && p != nil {
			oldSize := p.Size
			oldStatus := p.Status
			reconcileLog.Infof("📝 [ReconcileMarketPositions] 更新持仓: positionID=%s tokenType=%s oldSize=%.4f oldStatus=%s newSize=%.4f",
				positionID, token, oldSize, oldStatus, desired)
			return s.UpdatePosition(ctx, positionID, func(pp *domain.Position) {
				pp.MarketSlug = market.Slug
				pp.TokenType = token
				pp.Size = desired
				pp.Status = domain.PositionStatusOpen
				// Keep Market pointer best-effort
				cp := *market
				pp.Market = &cp
			})
		}

		// Otherwise: create
		reconcileLog.Infof("📝 [ReconcileMarketPositions] 创建新持仓: positionID=%s tokenType=%s size=%.4f",
			positionID, token, desired)
		cp := *market
		return s.CreatePosition(ctx, &domain.Position{
			ID:         positionID,
			MarketSlug: market.Slug,
			Market:     &cp,
			EntryTime:  time.Now(),
			Size:       desired,
			TokenType:  token,
			Status:     domain.PositionStatusOpen,
		})
	}

	if e := upsert(domain.TokenTypeUp, yesSz); e != nil {
		return e
	}
	if e := upsert(domain.TokenTypeDown, noSz); e != nil {
		return e
	}
	return nil
}
