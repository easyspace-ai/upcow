package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	clobclient "github.com/betbot/gobet/clob/client"
	clobsigning "github.com/betbot/gobet/clob/signing"
	clobtypes "github.com/betbot/gobet/clob/types"
	sdkapi "github.com/betbot/gobet/pkg/sdk/api"
)

// ---- trades sync (CLOB /data/trades) ----

func (s *Server) syncAccountTrades(ctx context.Context, a Account, privateKeyHex string) (inserted int, lastAfter int64, err error) {
	// state: last_clob_trade_after (unix seconds)
	after := int64(0)
	if v, ok, err := s.getSyncState(ctx, a.ID, "last_clob_trade_after"); err == nil && ok {
		if n, e := strconv.ParseInt(v, 10, 64); e == nil {
			after = n
		}
	}
	// overlap 60s to avoid missing same-second events
	if after > 60 {
		after = after - 60
	}

	auth, err := sdkapi.NewAuthFromKey(privateKeyHex)
	if err != nil {
		return 0, 0, err
	}
	baseURL := strings.TrimSpace(osGetenv("POLYMARKET_API_URL", "https://clob.polymarket.com"))
	cc, err := sdkapi.NewClobClient(baseURL, auth)
	if err != nil {
		return 0, 0, err
	}
	// funder is where positions live; use as maker filter
	cc.SetFunder(a.FunderAddress)
	cc.SetSignatureType(2) // proxy-style (best effort)

	// query both maker and taker, then dedupe by trade ID
	all := make(map[string]sdkapi.CLOBTrade)
	trades1, err1 := cc.GetCLOBTrades(ctx, sdkapi.CLOBTradeParams{Maker: a.FunderAddress, After: after})
	if err1 == nil {
		for _, t := range trades1 {
			all[t.ID] = t
		}
	}
	trades2, err2 := cc.GetCLOBTrades(ctx, sdkapi.CLOBTradeParams{Taker: a.FunderAddress, After: after})
	if err2 == nil {
		for _, t := range trades2 {
			all[t.ID] = t
		}
	}
	if err1 != nil && err2 != nil {
		return 0, 0, fmt.Errorf("maker query error: %v; taker query error: %v", err1, err2)
	}

	var maxTS int64 = 0
	for _, t := range all {
		ts := parseInt64(t.MatchTime)
		if ts > maxTS {
			maxTS = ts
		}
		ok, err := s.insertCLOBTrade(ctx, a.ID, t)
		if err != nil {
			return inserted, maxTS, err
		}
		if ok {
			inserted++
		}
	}

	if maxTS > 0 {
		_ = s.setSyncState(ctx, a.ID, "last_clob_trade_after", strconv.FormatInt(maxTS, 10))
	}
	return inserted, maxTS, nil
}

func (s *Server) insertCLOBTrade(ctx context.Context, accountID string, t sdkapi.CLOBTrade) (bool, error) {
	// parse
	size := parseFloat64(t.Size)
	price := parseFloat64(t.Price)
	ts := parseInt64(t.MatchTime)
	vol := size * price
	raw, _ := json.Marshal(t)
	rawStr := string(raw)

	res, err := s.db.ExecContext(ctx, `
INSERT OR IGNORE INTO clob_trades
(trade_id, account_id, maker_address, owner, market, asset_id, side, size, price, outcome, status, match_time_ts, transaction_hash, volume_usdc, raw_json, created_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
`, t.ID, accountID, t.MakerAddress, t.Owner, t.Market, t.AssetID, t.Side, size, price, t.Outcome, t.Status, ts, t.TransactionHash, vol, rawStr, time.Now().Format(time.RFC3339Nano))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ---- positions sync (data-api /positions) ----

func (s *Server) syncAccountPositions(ctx context.Context, a Account) error {
	client := sdkapi.NewClient(osGetenv("POLYMARKET_API_URL", "https://clob.polymarket.com"))
	positions, err := client.GetOpenPositions(ctx, a.FunderAddress)
	if err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM positions_current WHERE account_id=?`, a.ID); err != nil {
		return err
	}
	now := time.Now().Format(time.RFC3339Nano)
	for _, p := range positions {
		_, err := tx.ExecContext(ctx, `
INSERT INTO positions_current
(account_id, asset, condition_id, outcome, size, avg_price, cur_price, realized_pnl, title, slug, outcome_index, event_slug, ts)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
`, a.ID, p.Asset, p.ConditionID, p.Outcome, p.Size.Float64(), p.AvgPrice.Float64(), p.CurPrice.Float64(), p.RealizedPNL.Float64(), p.Title, p.Slug, p.OutcomeIndex, p.EventSlug, now)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ---- open orders sync (CLOB /data/orders via clob/client) ----

func (s *Server) syncAccountOpenOrders(ctx context.Context, a Account, privateKeyHex string) error {
	pk, err := clobsigning.PrivateKeyFromHex(privateKeyHex)
	if err != nil {
		return err
	}
	host := osGetenv("CLOB_API_URL", "https://clob.polymarket.com")
	temp := clobclient.NewClient(host, clobtypes.ChainPolygon, pk, nil)
	creds, err := temp.CreateOrDeriveAPIKey(ctx, nil)
	if err != nil {
		return err
	}
	c := clobclient.NewClient(host, clobtypes.ChainPolygon, pk, creds)
	orders, err := c.GetOpenOrders(ctx, nil)
	if err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM open_orders_current WHERE account_id=?`, a.ID); err != nil {
		return err
	}
	now := time.Now().Format(time.RFC3339Nano)
	for _, o := range orders {
		_, err := tx.ExecContext(ctx, `
INSERT INTO open_orders_current
(account_id, order_id, status, owner, maker_address, market, asset_id, side, original_size, size_matched, price, outcome, created_at_ts, expiration, order_type, ts)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
`, a.ID, o.ID, o.Status, o.Owner, o.MakerAddress, o.Market, o.AssetID, o.Side, parseFloat64(o.OriginalSize), parseFloat64(o.SizeMatched), parseFloat64(o.Price), o.Outcome, o.CreatedAt, o.Expiration, o.OrderType, now)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func parseFloat64(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

func parseInt64(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	v, _ := strconv.ParseInt(s, 10, 64)
	return v
}

func osGetenv(key string, def string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	return v
}
