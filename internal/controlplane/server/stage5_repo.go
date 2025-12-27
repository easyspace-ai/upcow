package server

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type AccountStats24h struct {
	AccountID        string  `json:"account_id"`
	Trades           int     `json:"trades"`
	VolumeUSDC       float64 `json:"volume_usdc"`
	BuyVolumeUSDC    float64 `json:"buy_volume_usdc"`
	SellVolumeUSDC   float64 `json:"sell_volume_usdc"`
	RedeemVolumeUSDC float64 `json:"redeem_volume_usdc"`
}

func (s *Server) listTradesByAccount(ctx context.Context, accountID string, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT trade_id, market, asset_id, side, size, price, outcome, status, match_time_ts, transaction_hash, volume_usdc
FROM clob_trades
WHERE account_id=?
ORDER BY match_time_ts DESC
LIMIT ?
`, accountID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []map[string]any
	for rows.Next() {
		var (
			id, market, asset, side, outcome, status, txhash sql.NullString
			size, price, vol                                 sql.NullFloat64
			matchTS                                          sql.NullInt64
		)
		if err := rows.Scan(&id, &market, &asset, &side, &size, &price, &outcome, &status, &matchTS, &txhash, &vol); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"trade_id":         id.String,
			"market":           market.String,
			"asset_id":         asset.String,
			"side":             side.String,
			"size":             size.Float64,
			"price":            price.Float64,
			"outcome":          outcome.String,
			"status":           status.String,
			"match_time_ts":    matchTS.Int64,
			"transaction_hash": txhash.String,
			"volume_usdc":      vol.Float64,
		})
	}
	return out, rows.Err()
}

func (s *Server) listPositionsByAccount(ctx context.Context, accountID string) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT asset, condition_id, outcome, size, avg_price, cur_price, realized_pnl, title, slug, outcome_index, event_slug, ts
FROM positions_current
WHERE account_id=?
ORDER BY size DESC
`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []map[string]any
	for rows.Next() {
		var (
			asset, cond, outcome, title, slug, eventSlug, ts sql.NullString
			size, avg, cur, rpnl                             sql.NullFloat64
			outcomeIndex                                     sql.NullInt64
		)
		if err := rows.Scan(&asset, &cond, &outcome, &size, &avg, &cur, &rpnl, &title, &slug, &outcomeIndex, &eventSlug, &ts); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"asset":         asset.String,
			"condition_id":  cond.String,
			"outcome":       outcome.String,
			"size":          size.Float64,
			"avg_price":     avg.Float64,
			"cur_price":     cur.Float64,
			"realized_pnl":  rpnl.Float64,
			"title":         title.String,
			"slug":          slug.String,
			"outcome_index": outcomeIndex.Int64,
			"event_slug":    eventSlug.String,
			"ts":            ts.String,
		})
	}
	return out, rows.Err()
}

func (s *Server) listOpenOrdersByAccount(ctx context.Context, accountID string) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT order_id, status, market, asset_id, side, original_size, size_matched, price, outcome, created_at_ts, order_type, ts
FROM open_orders_current
WHERE account_id=?
ORDER BY created_at_ts DESC
`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []map[string]any
	for rows.Next() {
		var (
			orderID, status, market, assetID, side, outcome, orderType, ts sql.NullString
			origSize, matched, price                                       sql.NullFloat64
			createdAtTS                                                    sql.NullInt64
		)
		if err := rows.Scan(&orderID, &status, &market, &assetID, &side, &origSize, &matched, &price, &outcome, &createdAtTS, &orderType, &ts); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"order_id":      orderID.String,
			"status":        status.String,
			"market":        market.String,
			"asset_id":      assetID.String,
			"side":          side.String,
			"original_size": origSize.Float64,
			"size_matched":  matched.Float64,
			"price":         price.Float64,
			"outcome":       outcome.String,
			"created_at_ts": createdAtTS.Int64,
			"order_type":    orderType.String,
			"ts":            ts.String,
		})
	}
	return out, rows.Err()
}

func (s *Server) stats24h(ctx context.Context, accountID string) (*AccountStats24h, error) {
	since := time.Now().Add(-24 * time.Hour).Unix()
	row := s.db.QueryRowContext(ctx, `
SELECT
  COUNT(*),
  COALESCE(SUM(volume_usdc), 0),
  COALESCE(SUM(CASE WHEN side='BUY' THEN volume_usdc ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN side='SELL' THEN volume_usdc ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN side='REDEEM' THEN volume_usdc ELSE 0 END), 0)
FROM clob_trades
WHERE account_id=? AND match_time_ts >= ?
`, accountID, since)

	var out AccountStats24h
	out.AccountID = accountID
	var trades int
	if err := row.Scan(&trades, &out.VolumeUSDC, &out.BuyVolumeUSDC, &out.SellVolumeUSDC, &out.RedeemVolumeUSDC); err != nil {
		return nil, fmt.Errorf("scan stats: %w", err)
	}
	out.Trades = trades
	return &out, nil
}
