package paircostarb

import (
	"math"
	"time"

	"github.com/betbot/gobet/internal/domain"
)

// Snapshot 是“可复制/可模拟”的交易状态快照（用于 simulateBuy/simulatePair）。
// 注意：这里的 FIFO 成本是严格基于 fills 队列配对（qPair/costPair）。
type Snapshot struct {
	Qu, Qd float64
	Cu, Cd float64

	FillsUp   []Fill
	FillsDown []Fill
	UpCur     fifoCursor
	DownCur   fifoCursor

	QPair    float64
	CostPair float64
}

func (s Snapshot) Clone() Snapshot {
	cp := s
	if len(s.FillsUp) > 0 {
		cp.FillsUp = append([]Fill(nil), s.FillsUp...)
	}
	if len(s.FillsDown) > 0 {
		cp.FillsDown = append([]Fill(nil), s.FillsDown...)
	}
	return cp
}

func (s *Snapshot) AddFill(side domain.TokenType, f Fill) {
	if s == nil {
		return
	}
	if f.Qty <= 0 || f.CostUSD <= 0 {
		return
	}
	if f.Time.IsZero() {
		f.Time = time.Now()
	}

	switch side {
	case domain.TokenTypeUp:
		s.Qu += f.Qty
		s.Cu += f.CostUSD
		s.FillsUp = append(s.FillsUp, f)
	case domain.TokenTypeDown:
		s.Qd += f.Qty
		s.Cd += f.CostUSD
		s.FillsDown = append(s.FillsDown, f)
	default:
		return
	}

	s.pairAvailable()
}

func (s Snapshot) UnpairedUp() float64   { return math.Max(0, s.Qu-s.QPair) }
func (s Snapshot) UnpairedDown() float64 { return math.Max(0, s.Qd-s.QPair) }

func (s Snapshot) QPairValue() float64 { return s.QPair }

// GuaranteedProfitUSD 返回严格 FIFO 的保证利润（含 buffer 扣减）。
func (s Snapshot) GuaranteedProfitUSD(cfg Config) float64 {
	if s.QPair <= 0 || s.CostPair <= 0 {
		return 0
	}
	return s.QPair*1.0 - (s.CostPair + cfg.PairCostBuffer*s.QPair)
}

func (s Snapshot) PairCost(cfg Config) float64 {
	if s.QPair <= 0 || s.CostPair <= 0 {
		return math.Inf(1)
	}
	return (s.CostPair / s.QPair) + cfg.PairCostBuffer
}

func (s Snapshot) Imbalance() float64 {
	if s.Qu <= 0 || s.Qd <= 0 {
		return math.Inf(1)
	}
	imb := math.Max(s.Qu, s.Qd) / math.Min(s.Qu, s.Qd)
	if imb < 1.0 {
		imb = 1.0
	}
	return imb
}

// pairAvailable 将两边未配对库存按 FIFO 进行配对，累积到 QPair/CostPair，并推进游标。
func (s *Snapshot) pairAvailable() {
	for {
		upAvail, upCostPer, okUp := fifoAvailCostPer(s.FillsUp, s.UpCur)
		downAvail, downCostPer, okDown := fifoAvailCostPer(s.FillsDown, s.DownCur)
		if !okUp || !okDown {
			break
		}
		take := math.Min(upAvail, downAvail)
		if take <= 1e-9 {
			break
		}

		s.QPair += take
		s.CostPair += take*upCostPer + take*downCostPer

		s.UpCur = fifoConsumeQty(s.FillsUp, s.UpCur, take)
		s.DownCur = fifoConsumeQty(s.FillsDown, s.DownCur, take)

		s.FillsUp, s.UpCur = fifoCompact(s.FillsUp, s.UpCur)
		s.FillsDown, s.DownCur = fifoCompact(s.FillsDown, s.DownCur)
	}
}
