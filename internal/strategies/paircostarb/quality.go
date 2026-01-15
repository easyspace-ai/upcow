package paircostarb

import (
	"math"
	"time"

	"github.com/betbot/gobet/internal/domain"
)

type ewma struct {
	alpha float64
	init  bool
	v     float64
}

func (e *ewma) Update(x float64) {
	if e == nil {
		return
	}
	if !e.init {
		e.v = x
		e.init = true
		return
	}
	a := e.alpha
	if a <= 0 || a > 1 {
		a = 0.2
	}
	e.v = (1-a)*e.v + a*x
}

func (e *ewma) Value() (float64, bool) {
	if e == nil || !e.init {
		return 0, false
	}
	return e.v, true
}

// QualityState tracks execution quality (slippage and fill ratio) for adaptive buffers and risk gating.
type QualityState struct {
	SlipAbsUp   ewma
	SlipAbsDown ewma
	FillRatio   ewma

	LastUpdated time.Time
}

func (q *QualityState) InitDefaults() {
	if q == nil {
		return
	}
	if q.SlipAbsUp.alpha == 0 {
		q.SlipAbsUp.alpha = 0.2
	}
	if q.SlipAbsDown.alpha == 0 {
		q.SlipAbsDown.alpha = 0.2
	}
	if q.FillRatio.alpha == 0 {
		q.FillRatio.alpha = 0.2
	}
}

func (q *QualityState) UpdateSlipAbs(token domain.TokenType, absErr float64) {
	if q == nil {
		return
	}
	if absErr < 0 {
		absErr = -absErr
	}
	if math.IsNaN(absErr) || math.IsInf(absErr, 0) {
		return
	}
	switch token {
	case domain.TokenTypeUp:
		q.SlipAbsUp.Update(absErr)
	case domain.TokenTypeDown:
		q.SlipAbsDown.Update(absErr)
	}
	q.LastUpdated = time.Now()
}

func (q *QualityState) UpdateFillRatio(r float64) {
	if q == nil {
		return
	}
	if r < 0 {
		r = 0
	}
	if r > 1.0 {
		r = 1.0
	}
	q.FillRatio.Update(r)
	q.LastUpdated = time.Now()
}

func (q *QualityState) MaxSlipAbs() float64 {
	if q == nil {
		return 0
	}
	a, oka := q.SlipAbsUp.Value()
	b, okb := q.SlipAbsDown.Value()
	if !oka && !okb {
		return 0
	}
	if !oka {
		return b
	}
	if !okb {
		return a
	}
	if a >= b {
		return a
	}
	return b
}
