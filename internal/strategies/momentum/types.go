package momentum

import "time"

// Direction 表示外部信号方向
type Direction int

const (
	DirectionUp Direction = iota + 1
	DirectionDown
)

// MomentumSignal 是外部行情源发出的动量信号
type MomentumSignal struct {
	Asset     string
	MoveBps   int
	Dir       Direction
	FiredAt   time.Time
	Source    string
	WindowS   int
	Threshold int
}

