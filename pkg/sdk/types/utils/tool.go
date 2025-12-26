package utils

import (
	"github.com/shopspring/decimal"
	"math"
	"math/big"
	"strconv"
	"strings"
)

func StringToBool(value string) bool {
	b, err := strconv.ParseBool(value)
	if err != nil {
		panic(err)
	}
	return b
}

func StringToUint64(value string) uint64 {
	num, _ := strconv.ParseUint(value, 10, 64)
	return num
}

func StringToUint8(value string) uint8 {
	num, _ := strconv.ParseUint(value, 10, 8)
	return uint8(num)
}

func StringToDecimal(value string) decimal.Decimal {
	price, _ := decimal.NewFromString(value)
	return price
}

func Float64ToDecimal(value float64) decimal.Decimal {
	price := decimal.NewFromFloat(value)
	return price
}

func Uint64Decimal(value uint64) decimal.Decimal {
	return decimal.NewFromUint64(value)
}

func Pow(value decimal.Decimal, decimals uint8) *big.Int {
	return decimal.Decimal.Mul(value, decimal.NewFromInt(10).Pow(decimal.NewFromInt(int64(decimals)))).Abs().BigInt()
}

func Exp(value decimal.Decimal, decimals uint8) decimal.Decimal {
	return decimal.Decimal.Div(value, decimal.NewFromInt(10).Pow(decimal.NewFromInt(int64(decimals))))
}

func RoundNormal(num float64, decimals int) float64 {
	if DecimalPlaces(num) <= decimals {
		return num
	}
	pow := math.Pow(10, float64(decimals))
	return math.Round((num+1e-9)*pow) / pow
}

// RoundDown
func RoundDown(num float64, decimals int) float64 {
	if DecimalPlaces(num) <= decimals {
		return num
	}
	pow := math.Pow(10, float64(decimals))
	return math.Floor(num*pow) / pow
}

// RoundUp
func RoundUp(num float64, decimals int) float64 {
	if DecimalPlaces(num) <= decimals {
		return num
	}
	pow := math.Pow(10, float64(decimals))
	return math.Ceil(num*pow) / pow
}

func DecimalPlaces(num float64) int {
	if math.Mod(num, 1) == 0 {
		return 0
	}
	s := strconv.FormatFloat(num, 'f', -1, 64)
	parts := strings.Split(s, ".")
	if len(parts) < 2 {
		return 0
	}
	return len(parts[1])
}

func PriceValid(price float64, tickSize float64) bool {
	const epsilon = 1e-9
	return price >= tickSize-epsilon && price <= 1.0-tickSize+epsilon
}
