package paircostarb

import "time"

type Fill struct {
	OrderID string
	Time    time.Time
	Qty     float64
	Price   float64
	FeeUSD  float64
	CostUSD float64 // 含手续费的成本（USD）
}

type fifoCursor struct {
	idx  int     // 当前 fill index
	used float64 // 当前 fill 已被“配对消耗”的 qty
}
