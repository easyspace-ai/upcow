package paircostarb

import "math"

func isFinite(x float64) bool {
	return !math.IsInf(x, 0) && !math.IsNaN(x)
}
