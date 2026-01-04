package momentum

import (
	"context"
	"encoding/json"
	"math"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

const polygonWSURL = "wss://socket.polygon.io/crypto"

type polygonMessage struct {
	Event string  `json:"ev"`
	Pair  string  `json:"pair"`
	Price float64 `json:"p"`
}

type priceTick struct {
	price float64
	ts    time.Time
}

type priceHistory struct {
	ticks     []priceTick
	lastPrice float64
	hasLast   bool
}

func (h *priceHistory) add(price float64, now time.Time) {
	h.ticks = append(h.ticks, priceTick{price: price, ts: now})
	h.lastPrice = price
	h.hasLast = true

	// 保留近 60 秒
	cutoff := now.Add(-60 * time.Second)
	// 简单线性清理（tick 频率不高时足够；若后续需要可改 ring）
	i := 0
	for ; i < len(h.ticks); i++ {
		if h.ticks[i].ts.After(cutoff) || h.ticks[i].ts.Equal(cutoff) {
			break
		}
	}
	if i > 0 {
		h.ticks = h.ticks[i:]
	}
}

func (h *priceHistory) changeBps(windowSecs int, now time.Time) (int, bool) {
	if !h.hasLast || len(h.ticks) == 0 || windowSecs <= 0 {
		return 0, false
	}
	cutoff := now.Add(-time.Duration(windowSecs) * time.Second)
	// 找窗口内最早的 tick
	var old *priceTick
	for i := 0; i < len(h.ticks); i++ {
		if h.ticks[i].ts.After(cutoff) || h.ticks[i].ts.Equal(cutoff) {
			old = &h.ticks[i]
			break
		}
	}
	if old == nil || old.price <= 0 {
		return 0, false
	}
	change := (h.lastPrice - old.price) / old.price * 10000.0
	return int(math.Round(change)), true
}

// runPolygonFeed 连接 Polygon WS，产生动量信号并写入 outC（非阻塞丢弃）。
func runPolygonFeed(ctx context.Context, assetFilter string, thresholdBps int, windowSecs int, outC chan<- MomentumSignal, log *logrus.Entry) {
	apiKey := strings.TrimSpace(os.Getenv("POLYGON_API_KEY"))
	if apiKey == "" {
		log.Warnf("POLYGON_API_KEY 未设置，动量策略将不会产生外部信号（仅运行空循环）")
		<-ctx.Done()
		return
	}

	filter := strings.ToUpper(strings.TrimSpace(assetFilter))

	hist := map[string]*priceHistory{
		"BTC": {},
		"ETH": {},
		"SOL": {},
		"XRP": {},
	}
	for k := range hist {
		hist[k] = &priceHistory{}
	}

	// 防抖：同一资产的信号最小间隔 300ms，避免震荡重复触发
	lastSignalAt := map[string]time.Time{}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		u, _ := url.Parse(polygonWSURL)
		q := u.Query()
		q.Set("apiKey", apiKey)
		u.RawQuery = q.Encode()

		log.Infof("[Polygon] 连接外部行情源...")
		conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
		if err != nil {
			log.Warnf("[Polygon] 连接失败: %v，2s 后重试", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
				continue
			}
		}

		// 订阅 4 个资产（Polygon 端过滤粒度较粗，客户端再过滤）
		sub := map[string]string{
			"action": "subscribe",
			"params": "XT.BTC-USD,XT.ETH-USD,XT.SOL-USD,XT.XRP-USD",
		}
		_ = conn.WriteJSON(sub)
		log.Infof("[Polygon] 已订阅 BTC/ETH/SOL/XRP")

		_ = conn.SetReadDeadline(time.Now().Add(45 * time.Second))
		conn.SetPongHandler(func(string) error {
			_ = conn.SetReadDeadline(time.Now().Add(45 * time.Second))
			return nil
		})

		readLoop := func() error {
			for {
				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
				}

				_, msg, err := conn.ReadMessage()
				if err != nil {
					return err
				}

				// Polygon 可能返回 array
				var arr []polygonMessage
				if err := json.Unmarshal(msg, &arr); err != nil {
					continue
				}

				now := time.Now()
				for _, m := range arr {
					if m.Event != "XT" || m.Price <= 0 {
						continue
					}
					asset := strings.ToUpper(strings.TrimSpace(m.Pair))
					// m.Pair 形如 BTC-USD
					if strings.Contains(asset, "-") {
						asset = strings.Split(asset, "-")[0]
					}
					if asset == "" {
						continue
					}
					if filter != "" && filter != asset {
						continue
					}
					h := hist[asset]
					if h == nil {
						h = &priceHistory{}
						hist[asset] = h
					}

					h.add(m.Price, now)
					moveBps, ok := h.changeBps(windowSecs, now)
					if !ok || int(math.Abs(float64(moveBps))) < thresholdBps {
						continue
					}

					if t, ok := lastSignalAt[asset]; ok && now.Sub(t) < 300*time.Millisecond {
						continue
					}
					lastSignalAt[asset] = now

					dir := DirectionUp
					if moveBps < 0 {
						dir = DirectionDown
					}

					sig := MomentumSignal{
						Asset:    asset,
						MoveBps:  moveBps,
						Dir:      dir,
						FiredAt:  now,
						Source:   "polygon",
						WindowS:  windowSecs,
						Threshold: thresholdBps,
					}

					select {
					case outC <- sig:
					default:
						// 策略 loop 忙时直接丢弃，避免积压
					}
				}
			}
		}

		err = readLoop()
		_ = conn.Close()

		if err != nil && !errorsIsContext(err, ctx) {
			log.Warnf("[Polygon] 断开: %v，2s 后重连", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func errorsIsContext(err error, ctx context.Context) bool {
	if err == nil {
		return false
	}
	if ctx == nil {
		return false
	}
	if ctx.Err() == nil {
		return false
	}
	// gorilla/websocket 的错误类型较多，这里做最宽松判断
	return strings.Contains(strings.ToLower(err.Error()), "context canceled") ||
		strings.Contains(strings.ToLower(err.Error()), "use of closed network connection")
}

