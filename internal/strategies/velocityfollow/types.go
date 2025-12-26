package velocityfollow

import (
	"time"
)

type sample struct {
	ts         time.Time
	priceCents int
}

type metrics struct {
	ok       bool
	delta    int
	seconds  float64
	velocity float64 // cents/sec
}

type trailState struct {
	Armed        bool
	HighBidCents int
	StopCents    int
}
