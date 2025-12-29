package velocityhedgehold

import "time"

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
