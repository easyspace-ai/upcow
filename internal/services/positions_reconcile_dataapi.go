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
)

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
	// bounded request
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// sizeThreshold=0 to not miss small balances; limit=500 should be enough for single-bot
	apiURL := fmt.Sprintf("https://data-api.polymarket.com/positions?user=%s&sizeThreshold=0&limit=500", user)
	req, e := http.NewRequestWithContext(cctx, "GET", apiURL, nil)
	if e != nil {
		return 0, 0, e
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
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
		return err
	}

	// Helper: upsert a position size
	upsert := func(token domain.TokenType, desired float64) error {
		assetID := market.YesAssetID
		if token == domain.TokenTypeDown {
			assetID = market.NoAssetID
		}
		positionID := fmt.Sprintf("%s_%s_%s", market.Slug, assetID, token)

		// If desired <= 0: close if exists
		if desired <= 0 {
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

