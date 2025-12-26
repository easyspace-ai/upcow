package marketstate

import (
	"sync/atomic"
	"time"

	"github.com/betbot/gobet/internal/domain"
)

// AtomicBestBook 提供“锁自由的 top-of-book 快照”。
//
// 目标：
// - 高频写入（WS）与高频读取（策略/执行）解耦
// - 读取时拿到一致快照（避免多字段撕裂）
// - 只缓存策略最常用的数据：YES/NO 的 bid/ask（以及可选的 top size）
//
// 价格单位：domain.Price.Pips（= 价格 * 10000，通常 1~9999，对应 0.0001~0.9999）。
// size 单位：shares，按 1e4 缩放存储（uint32），即 sizeScaled = shares * 10000。
type AtomicBestBook struct {
	// pricesPacked: [yes_bid_pips:16][yes_ask_pips:16][no_bid_pips:16][no_ask_pips:16]
	pricesPacked atomic.Uint64

	// bidSizesPacked: [yes_bid_size:32][no_bid_size:32] (sizeScaled)
	bidSizesPacked atomic.Uint64
	// askSizesPacked: [yes_ask_size:32][no_ask_size:32] (sizeScaled)
	askSizesPacked atomic.Uint64

	updatedAtUnixMs atomic.Int64
}

type BestBookSnapshot struct {
	YesBidPips uint16
	YesAskPips uint16
	NoBidPips  uint16
	NoAskPips  uint16

	YesBidSizeScaled uint32
	YesAskSizeScaled uint32
	NoBidSizeScaled  uint32
	NoAskSizeScaled  uint32

	UpdatedAt time.Time
}

func NewAtomicBestBook() *AtomicBestBook {
	b := &AtomicBestBook{}
	b.updatedAtUnixMs.Store(0)
	return b
}

// Reset 清空所有缓存的 top-of-book 数据。
//
// 重要：必须“原地重置”，不能通过替换 *AtomicBestBook 指针来 reset。
// 因为上层（Session/策略）通常会缓存 BestBook 指针，替换指针会导致它们继续读到旧对象里的旧数据。
func (b *AtomicBestBook) Reset() {
	if b == nil {
		return
	}
	b.pricesPacked.Store(0)
	b.bidSizesPacked.Store(0)
	b.askSizesPacked.Store(0)
	b.updatedAtUnixMs.Store(0)
}

func (b *AtomicBestBook) Load() BestBookSnapshot {
	p := b.pricesPacked.Load()
	bids := b.bidSizesPacked.Load()
	asks := b.askSizesPacked.Load()
	ms := b.updatedAtUnixMs.Load()

	var t time.Time
	if ms > 0 {
		t = time.UnixMilli(ms)
	}

	return BestBookSnapshot{
		YesBidPips: uint16((p >> 48) & 0xFFFF),
		YesAskPips: uint16((p >> 32) & 0xFFFF),
		NoBidPips:  uint16((p >> 16) & 0xFFFF),
		NoAskPips:  uint16(p & 0xFFFF),

		YesBidSizeScaled: uint32((bids >> 32) & 0xFFFFFFFF),
		NoBidSizeScaled:  uint32(bids & 0xFFFFFFFF),
		YesAskSizeScaled: uint32((asks >> 32) & 0xFFFFFFFF),
		NoAskSizeScaled:  uint32(asks & 0xFFFFFFFF),

		UpdatedAt: t,
	}
}

func (b *AtomicBestBook) UpdatedAt() time.Time {
	ms := b.updatedAtUnixMs.Load()
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

func (b *AtomicBestBook) IsFresh(maxAge time.Duration) bool {
	if b == nil {
		return false
	}
	t := b.UpdatedAt()
	if t.IsZero() {
		return false
	}
	return time.Since(t) <= maxAge
}

// UpdateToken 更新某一侧（YES 或 NO）的 bid/ask 价格（pips=价格*10000），以及可选 top size。
//
// bid/ask 任意一侧传 0 表示“不更新该字段”（保留旧值）。
// sizeScaled 传 0 表示“不更新 size”。
func (b *AtomicBestBook) UpdateToken(token domain.TokenType, bidPips uint16, askPips uint16, bidSizeScaled uint32, askSizeScaled uint32) {
	if b == nil {
		return
	}

	for {
		cur := b.pricesPacked.Load()
		yesBid := uint16((cur >> 48) & 0xFFFF)
		yesAsk := uint16((cur >> 32) & 0xFFFF)
		noBid := uint16((cur >> 16) & 0xFFFF)
		noAsk := uint16(cur & 0xFFFF)

		switch token {
		case domain.TokenTypeUp:
			if bidPips != 0 {
				yesBid = bidPips
			}
			if askPips != 0 {
				yesAsk = askPips
			}
		case domain.TokenTypeDown:
			if bidPips != 0 {
				noBid = bidPips
			}
			if askPips != 0 {
				noAsk = askPips
			}
		default:
			// unknown tokenType: ignore
			return
		}

		next := packPrices(yesBid, yesAsk, noBid, noAsk)
		if b.pricesPacked.CompareAndSwap(cur, next) {
			break
		}
	}

	// size 更新可以与价格解耦（允许轻微不一致；策略一般优先读价格）
	if bidSizeScaled != 0 {
		for {
			cur := b.bidSizesPacked.Load()
			yes := uint32((cur >> 32) & 0xFFFFFFFF)
			no := uint32(cur & 0xFFFFFFFF)
			if token == domain.TokenTypeUp {
				yes = bidSizeScaled
			} else if token == domain.TokenTypeDown {
				no = bidSizeScaled
			}
			next := packSizes(yes, no)
			if b.bidSizesPacked.CompareAndSwap(cur, next) {
				break
			}
		}
	}

	if askSizeScaled != 0 {
		for {
			cur := b.askSizesPacked.Load()
			yes := uint32((cur >> 32) & 0xFFFFFFFF)
			no := uint32(cur & 0xFFFFFFFF)
			if token == domain.TokenTypeUp {
				yes = askSizeScaled
			} else if token == domain.TokenTypeDown {
				no = askSizeScaled
			}
			next := packSizes(yes, no)
			if b.askSizesPacked.CompareAndSwap(cur, next) {
				break
			}
		}
	}

	b.updatedAtUnixMs.Store(time.Now().UnixMilli())
}

func packPrices(yesBid, yesAsk, noBid, noAsk uint16) uint64 {
	return (uint64(yesBid) << 48) | (uint64(yesAsk) << 32) | (uint64(noBid) << 16) | uint64(noAsk)
}

func packSizes(yes, no uint32) uint64 {
	return (uint64(yes) << 32) | uint64(no)
}
