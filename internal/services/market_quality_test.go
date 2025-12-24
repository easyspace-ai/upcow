package services

import (
	"context"
	"testing"
	"time"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/marketstate"
)

func TestMarketQuality_WSBestBook_CompleteAndFresh(t *testing.T) {
	ts := NewTradingService(nil, true)

	m := &domain.Market{
		Slug:       "test-1766322000",
		YesAssetID: "YES",
		NoAssetID:  "NO",
		Timestamp:  time.Now().Unix(),
	}
	ts.SetCurrentMarketInfo(m)

	book := marketstate.NewAtomicBestBook()
	// 0.6000/0.6100 and 0.3900/0.4000
	book.UpdateToken(domain.TokenTypeUp, 6000, 6100, 0, 0)
	book.UpdateToken(domain.TokenTypeDown, 3900, 4000, 0, 0)
	ts.SetBestBook(book)

	mq, err := ts.GetMarketQuality(context.Background(), m, &MarketQualityOptions{
		MaxBookAge:      3 * time.Second,
		MaxSpreadPips:   1000,
		PreferWS:        true,
		FallbackToREST:  false,
		AllowPartialWS:  true,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if mq == nil {
		t.Fatalf("mq is nil")
	}
	if mq.Source != "ws.bestbook" {
		t.Fatalf("unexpected source: %s", mq.Source)
	}
	if !mq.Complete {
		t.Fatalf("expected complete")
	}
	if !mq.Fresh {
		t.Fatalf("expected fresh")
	}
	if mq.Score <= 0 {
		t.Fatalf("expected score>0 got=%d", mq.Score)
	}
	if mq.Effective.EffectiveBuyYesPips <= 0 || mq.Effective.EffectiveBuyNoPips <= 0 {
		t.Fatalf("expected effective prices computed: %+v", mq.Effective)
	}
}

func TestMarketQuality_WSBestBook_Stale(t *testing.T) {
	ts := NewTradingService(nil, true)
	m := &domain.Market{Slug: "test-1766322001", YesAssetID: "YES", NoAssetID: "NO", Timestamp: time.Now().Unix()}
	ts.SetCurrentMarketInfo(m)

	book := marketstate.NewAtomicBestBook()
	book.UpdateToken(domain.TokenTypeUp, 6000, 6100, 0, 0)
	book.UpdateToken(domain.TokenTypeDown, 3900, 4000, 0, 0)
	ts.SetBestBook(book)

	// 极小 MaxBookAge：确保被判定 stale（避免 sleep）
	mq, err := ts.GetMarketQuality(context.Background(), m, &MarketQualityOptions{
		MaxBookAge:      1 * time.Nanosecond,
		PreferWS:        true,
		FallbackToREST:  false,
		AllowPartialWS:  true,
		MaxSpreadPips:   1000,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if mq == nil {
		t.Fatalf("mq is nil")
	}
	if mq.Fresh {
		t.Fatalf("expected stale/fresh=false")
	}
}

