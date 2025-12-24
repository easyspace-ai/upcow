package common

import "math"

// NormCdf 计算标准正态分布的累积分布函数（CDF）
// 使用误差函数（erf）实现：Φ(z) = 0.5 * (1 + erf(z / sqrt(2)))
func NormCdf(z float64) float64 {
	return 0.5 * (1 + math.Erf(z/math.Sqrt2))
}

