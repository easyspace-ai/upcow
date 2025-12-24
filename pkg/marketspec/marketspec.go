package marketspec

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Timeframe 表示市场周期（用于 polymarket updown market slug）。
// 支持：15m / 1h / 4h
type Timeframe string

const (
	Timeframe15m Timeframe = "15m"
	Timeframe1h  Timeframe = "1h"
	Timeframe4h  Timeframe = "4h"
)

func ParseTimeframe(v string) (Timeframe, error) {
	s := strings.ToLower(strings.TrimSpace(v))
	switch s {
	case "15m", "15min", "15mins", "15-minute", "15minutes":
		return Timeframe15m, nil
	case "1h", "1hour", "1-hour", "60m", "60min", "60mins":
		return Timeframe1h, nil
	case "4h", "4hour", "4-hour", "240m", "240min", "240mins":
		return Timeframe4h, nil
	default:
		return "", fmt.Errorf("不支持的 timeframe: %q（支持: 15m/1h/4h）", v)
	}
}

func (t Timeframe) String() string { return string(t) }

func (t Timeframe) Duration() time.Duration {
	switch t {
	case Timeframe15m:
		return 15 * time.Minute
	case Timeframe1h:
		return 1 * time.Hour
	case Timeframe4h:
		return 4 * time.Hour
	default:
		// 未知值按 15m 处理，避免 panic（Validate 会兜底）
		return 15 * time.Minute
	}
}

// MarketSpec 表示要交易/订阅的 polymarket updown 市场规格。
type MarketSpec struct {
	Symbol    string   // e.g. "btc", "eth"
	Kind      string   // e.g. "updown"
	Timeframe Timeframe
}

var symbolRe = regexp.MustCompile(`^[a-z0-9]+$`)

func New(symbol, timeframe, kind string) (MarketSpec, error) {
	tf, err := ParseTimeframe(timeframe)
	if err != nil {
		return MarketSpec{}, err
	}
	s := strings.ToLower(strings.TrimSpace(symbol))
	if s == "" {
		s = "btc"
	}
	if !symbolRe.MatchString(s) {
		return MarketSpec{}, fmt.Errorf("无效的 symbol: %q（仅允许小写字母/数字）", symbol)
	}
	k := strings.ToLower(strings.TrimSpace(kind))
	if k == "" {
		k = "updown"
	}
	return MarketSpec{Symbol: s, Kind: k, Timeframe: tf}, nil
}

func (m MarketSpec) Duration() time.Duration { return m.Timeframe.Duration() }

// CurrentPeriodStartUnix 返回当前周期起点（按本地时区对齐）。
func (m MarketSpec) CurrentPeriodStartUnix(now time.Time) int64 {
	loc := now.Location()
	switch m.Timeframe {
	case Timeframe15m:
		min := (now.Minute() / 15) * 15
		t := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), min, 0, 0, loc)
		return t.Unix()
	case Timeframe1h:
		t := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, loc)
		return t.Unix()
	case Timeframe4h:
		h := (now.Hour() / 4) * 4
		t := time.Date(now.Year(), now.Month(), now.Day(), h, 0, 0, 0, loc)
		return t.Unix()
	default:
		// 兜底：按 duration truncate（但对齐点可能不符合预期，因此只做 fallback）
		return now.Truncate(m.Duration()).Unix()
	}
}

func (m MarketSpec) Slug(periodStartUnix int64) string {
	// 约定：polymarket slug 使用小写 symbol / kind / timeframe
	return fmt.Sprintf("%s-%s-%s-%d", m.Symbol, m.Kind, m.Timeframe.String(), periodStartUnix)
}

func (m MarketSpec) SlugPrefix() string {
	return fmt.Sprintf("%s-%s-%s-", m.Symbol, m.Kind, m.Timeframe.String())
}

func (m MarketSpec) NextPeriodStartUnix(periodStartUnix int64) int64 {
	return periodStartUnix + int64(m.Duration().Seconds())
}

func (m MarketSpec) NextSlugs(count int) []string {
	if count <= 0 {
		return nil
	}
	start := m.CurrentPeriodStartUnix(time.Now())
	out := make([]string, 0, count)
	for i := 0; i < count; i++ {
		ts := start + int64(i)*int64(m.Duration().Seconds())
		out = append(out, m.Slug(ts))
	}
	return out
}

