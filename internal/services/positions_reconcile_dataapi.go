package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
	apiURL := fmt.Sprintf("https://data-api.polymarket.com/positions?user=%s&sizeThreshold=0&limit=500", user)
	req, e := http.NewRequestWithContext(cctx, "GET", apiURL, nil)
	if e != nil {
		return 0, 0, e
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 20 * time.Second}
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

	for _, pos := range positions {
		asset, _ := pos["asset"].(string)
		if asset == "" {
			continue
		}
		sizeStr, _ := pos["size"].(string)
		if sizeStr == "" {
			continue
		}
		sz, _ := strconv.ParseFloat(sizeStr, 64)
		if sz <= 0 {
			continue
		}
		if asset == market.YesAssetID {
			yes = sz
		} else if asset == market.NoAssetID {
			no = sz
		}
	}
	return yes, no, nil
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
