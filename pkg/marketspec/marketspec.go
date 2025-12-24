package marketspec

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// SlugStyle 控制不同市场的 slug 格式。
type SlugStyle string

const (
	// SlugStyleTimestamp: {symbol}-{kind}-{timeframe}-{periodStartUnix}
	SlugStyleTimestamp SlugStyle = "timestamp"
	// SlugStylePolymarketHourlyET: {coinName}-up-or-down-{month}-{day}-{hour}{am|pm}-et
	// 示例：bitcoin-up-or-down-december-24-5am-et
	SlugStylePolymarketHourlyET SlugStyle = "polymarket_hourly_et"
)

func ParseSlugStyle(v string) (SlugStyle, error) {
	s := strings.ToLower(strings.TrimSpace(v))
	switch s {
	case "", "timestamp", "unix", "epoch":
		return SlugStyleTimestamp, nil
	case "polymarket_hourly_et", "hourly_et", "hour_et", "et_hourly":
		return SlugStylePolymarketHourlyET, nil
	default:
		return "", fmt.Errorf("不支持的 slugStyle: %q（支持: timestamp/polymarket_hourly_et）", v)
	}
}

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
	Symbol        string            // e.g. "btc", "eth"
	Kind          string            // e.g. "updown"
	Timeframe     Timeframe
	SlugStyle     SlugStyle
	SlugTemplates map[string]string // 格式模板映射：timeframe -> template
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
	return MarketSpec{Symbol: s, Kind: k, Timeframe: tf, SlugStyle: SlugStyleTimestamp}, nil
}

func (m MarketSpec) Duration() time.Duration { return m.Timeframe.Duration() }

func (m MarketSpec) location(now time.Time) *time.Location {
	switch m.SlugStyle {
	case SlugStylePolymarketHourlyET:
		// 交易时间锚定到 ET（America/New_York）
		if loc, err := time.LoadLocation("America/New_York"); err == nil {
			return loc
		}
		// fallback：如果系统缺少 tzdata，则退回 local
		return now.Location()
	default:
		return now.Location()
	}
}

// CurrentPeriodStartUnix 返回当前周期起点（按 slugStyle 对应时区对齐）。
func (m MarketSpec) CurrentPeriodStartUnix(now time.Time) int64 {
	loc := m.location(now)
	now = now.In(loc)
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
	// 优先使用模板系统
	if m.SlugTemplates != nil {
		template, ok := m.SlugTemplates[m.Timeframe.String()]
		if ok && template != "" {
			return m.renderTemplate(template, periodStartUnix)
		}
	}
	
	// 兼容旧逻辑：使用 SlugStyle
	switch m.SlugStyle {
	case SlugStylePolymarketHourlyET:
		// 目前该格式主要用于 1h up-or-down 市场
		return m.slugPolymarketHourlyET(periodStartUnix)
	default:
		// 约定：polymarket slug 使用小写 symbol / kind / timeframe
		return fmt.Sprintf("%s-%s-%s-%d", m.Symbol, m.Kind, m.Timeframe.String(), periodStartUnix)
	}
}

// renderTemplate 渲染模板，替换变量
func (m MarketSpec) renderTemplate(template string, periodStartUnix int64) string {
	result := template
	
	// 基础变量
	result = strings.ReplaceAll(result, "{symbol}", m.Symbol)
	result = strings.ReplaceAll(result, "{coinName}", m.getHourlyETCoinName())
	result = strings.ReplaceAll(result, "{kind}", m.Kind)
	result = strings.ReplaceAll(result, "{timeframe}", m.Timeframe.String())
	result = strings.ReplaceAll(result, "{timestamp}", fmt.Sprintf("%d", periodStartUnix))
	result = strings.ReplaceAll(result, "{et}", "et")
	
	// 时间相关变量（需要解析时间戳）
	loc := m.location(time.Now())
	t := time.Unix(periodStartUnix, 0).In(loc)
	
	// 月份（全名小写）
	month := strings.ToLower(t.Month().String())
	result = strings.ReplaceAll(result, "{month}", month)
	
	// 日期
	result = strings.ReplaceAll(result, "{day}", fmt.Sprintf("%d", t.Day()))
	
	// 12小时制小时和 am/pm
	h := t.Hour()
	var h12 int
	var ampm string
	if h == 0 {
		h12 = 12
		ampm = "am"
	} else if h < 12 {
		h12 = h
		ampm = "am"
	} else if h == 12 {
		h12 = 12
		ampm = "pm"
	} else {
		h12 = h - 12
		ampm = "pm"
	}
	result = strings.ReplaceAll(result, "{hour}", fmt.Sprintf("%d", h12))
	result = strings.ReplaceAll(result, "{ampm}", ampm)
	
	return result
}

func (m MarketSpec) SlugPrefix() string {
	// 优先使用模板系统：从模板中提取前缀（移除时间相关变量）
	if m.SlugTemplates != nil {
		template, ok := m.SlugTemplates[m.Timeframe.String()]
		if ok && template != "" {
			// 找到第一个时间变量的位置，之前的部分就是前缀
			// 时间变量：{timestamp}, {month}, {day}, {hour}, {ampm}
			timeVarPattern := regexp.MustCompile(`\{timestamp\}|\{month\}|\{day\}|\{hour\}|\{ampm\}`)
			firstTimeVar := timeVarPattern.FindStringIndex(template)
			
			var prefix string
			if firstTimeVar != nil {
				// 提取时间变量之前的部分
				prefix = template[:firstTimeVar[0]]
			} else {
				// 如果没有时间变量，使用整个模板（但移除 {et} 等）
				prefix = template
			}
			
			// 替换静态变量
			prefix = strings.ReplaceAll(prefix, "{symbol}", m.Symbol)
			prefix = strings.ReplaceAll(prefix, "{coinName}", m.getHourlyETCoinName())
			prefix = strings.ReplaceAll(prefix, "{kind}", m.Kind)
			prefix = strings.ReplaceAll(prefix, "{timeframe}", m.Timeframe.String())
			prefix = strings.ReplaceAll(prefix, "{et}", "et")
			
			// 清理多余的连字符和尾部
			prefix = regexp.MustCompile(`-+`).ReplaceAllString(prefix, "-")
			prefix = strings.TrimSuffix(prefix, "-")
			if prefix != "" {
				return prefix + "-"
			}
		}
	}
	
	// 兼容旧逻辑
	switch m.SlugStyle {
	case SlugStylePolymarketHourlyET:
		// 使用硬编码映射获取币种名称（确保 BTC -> bitcoin, ETH -> ethereum）
		coinName := m.getHourlyETCoinName()
		return fmt.Sprintf("%s-up-or-down-", coinName)
	default:
		return fmt.Sprintf("%s-%s-%s-", m.Symbol, m.Kind, m.Timeframe.String())
	}
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

// TimestampFromSlug 尝试从 slug 解析周期起点时间戳（Unix seconds）。
// - timestamp 模式：解析末尾的 -{digits}
// - hourly_et 模式：解析 {month}-{day}-{hour}{am|pm}-et
func (m MarketSpec) TimestampFromSlug(slug string, now time.Time) (int64, bool) {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return 0, false
	}
	switch m.SlugStyle {
	case SlugStylePolymarketHourlyET:
		return parsePolymarketHourlyETSlug(slug, now)
	default:
		// -(\d+)$
		i := strings.LastIndex(slug, "-")
		if i < 0 || i+1 >= len(slug) {
			return 0, false
		}
		ts, err := strconv.ParseInt(slug[i+1:], 10, 64)
		if err != nil || ts <= 0 {
			return 0, false
		}
		return ts, true
	}
}

func (m MarketSpec) coinName() string {
	switch strings.ToLower(strings.TrimSpace(m.Symbol)) {
	case "btc", "bitcoin":
		return "bitcoin"
	case "eth", "ethereum":
		return "ethereum"
	case "sol", "solana":
		return "solana"
	case "xrp":
		return "xrp"
	default:
		// fallback：直接用 symbol
		return strings.ToLower(strings.TrimSpace(m.Symbol))
	}
}

// hourlyETSlugMapping 1小时市场的硬编码映射表
// 格式：{coinName}-up-or-down-{month}-{day}-{hour}{am|pm}-et
// 例如：bitcoin-up-or-down-december-24-11am-et
//       ethereum-up-or-down-december-24-11am-et
var hourlyETSlugMapping = map[string]string{
	// BTC 映射
	"bitcoin": "bitcoin",
	"btc":     "bitcoin",
	// ETH 映射
	"ethereum": "ethereum",
	"eth":      "ethereum",
	// 其他币种可以继续添加
	"solana": "solana",
	"sol":    "solana",
	"xrp":    "xrp",
}

// getHourlyETCoinName 获取1小时市场使用的币种名称（硬编码映射）
func (m MarketSpec) getHourlyETCoinName() string {
	symbol := strings.ToLower(strings.TrimSpace(m.Symbol))
	if coinName, ok := hourlyETSlugMapping[symbol]; ok {
		return coinName
	}
	// fallback：使用 coinName() 方法
	return m.coinName()
}

func (m MarketSpec) slugPolymarketHourlyET(periodStartUnix int64) string {
	loc := m.location(time.Now())
	t := time.Unix(periodStartUnix, 0).In(loc)

	month := strings.ToLower(t.Month().String())
	day := t.Day()

	h := t.Hour()
	ampm := "am"
	h12 := h
	if h == 0 {
		h12 = 12
		ampm = "am"
	} else if h < 12 {
		h12 = h
		ampm = "am"
	} else if h == 12 {
		h12 = 12
		ampm = "pm"
	} else {
		h12 = h - 12
		ampm = "pm"
	}
	// 使用硬编码映射获取币种名称（确保 BTC -> bitcoin, ETH -> ethereum）
	coinName := m.getHourlyETCoinName()
	return fmt.Sprintf("%s-up-or-down-%s-%d-%d%s-et", coinName, month, day, h12, ampm)
}

var hourTokenRe = regexp.MustCompile(`^(\d{1,2})(am|pm)$`)

func parsePolymarketHourlyETSlug(slug string, now time.Time) (int64, bool) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(slug)), "-")
	// 预期：{coin}-up-or-down-{month}-{day}-{hour}{am|pm}-et
	// 示例：bitcoin-up-or-down-december-24-11am-et
	//       ethereum-up-or-down-december-24-11am-et
	// split 后：coin, up, or, down, month, day, hourToken, et
	if len(parts) < 8 {
		return 0, false
	}
	// 验证格式：up-or-down
	if parts[1] != "up" || parts[2] != "or" || parts[3] != "down" {
		return 0, false
	}
	// 验证结尾：et
	if parts[len(parts)-1] != "et" {
		return 0, false
	}
	// 验证币种名称（支持 bitcoin, ethereum 等硬编码映射）
	coinName := parts[0]
	if _, ok := hourlyETSlugMapping[coinName]; !ok {
		// 如果不在映射表中，也允许（可能是其他币种）
		// 但确保是已知的格式
	}
	monthToken := parts[4]
	dayToken := parts[5]
	hourToken := parts[6]

	month, ok := parseMonthName(monthToken)
	if !ok {
		return 0, false
	}
	day, err := strconv.Atoi(dayToken)
	if err != nil || day < 1 || day > 31 {
		return 0, false
	}
	mm := hourTokenRe.FindStringSubmatch(hourToken)
	if len(mm) != 3 {
		return 0, false
	}
	hh, _ := strconv.Atoi(mm[1])
	if hh < 1 || hh > 12 {
		return 0, false
	}
	ampm := mm[2]
	h24 := hh % 12
	if ampm == "pm" {
		h24 += 12
	}

	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = now.Location()
	}
	nowET := now.In(loc)
	year := closestYear(nowET, month, day, h24)
	t := time.Date(year, month, day, h24, 0, 0, 0, loc)
	return t.Unix(), true
}

func parseMonthName(s string) (time.Month, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "january":
		return time.January, true
	case "february":
		return time.February, true
	case "march":
		return time.March, true
	case "april":
		return time.April, true
	case "may":
		return time.May, true
	case "june":
		return time.June, true
	case "july":
		return time.July, true
	case "august":
		return time.August, true
	case "september":
		return time.September, true
	case "october":
		return time.October, true
	case "november":
		return time.November, true
	case "december":
		return time.December, true
	default:
		return 0, false
	}
}

func closestYear(now time.Time, month time.Month, day int, hour int) int {
	y := now.Year()
	loc := now.Location()
	candidates := []time.Time{
		time.Date(y-1, month, day, hour, 0, 0, 0, loc),
		time.Date(y, month, day, hour, 0, 0, 0, loc),
		time.Date(y+1, month, day, hour, 0, 0, 0, loc),
	}
	bestY := y
	bestD := time.Duration(1<<63 - 1)
	for _, c := range candidates {
		d := c.Sub(now)
		if d < 0 {
			d = -d
		}
		if d < bestD {
			bestD = d
			bestY = c.Year()
		}
	}
	return bestY
}

