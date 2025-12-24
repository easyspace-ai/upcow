package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

var binanceKlineLog = logrus.WithField("component", "binance_futures_klines")

// BinanceFuturesKlines 提供 Binance U 本位合约的 K 线（1s/1m）最新值缓存。
// 数据源：wss://fstream.binance.com（Binance Futures）
type BinanceFuturesKlines struct {
	symbol string // e.g. "btcusdt"

	mu sync.RWMutex
	// interval -> latest
	latest map[string]Kline
	// interval -> startMs -> kline
	history map[string]map[int64]Kline
	// interval -> ordered startMs (oldest -> newest)
	order map[string][]int64

	ctx    context.Context
	cancel context.CancelFunc

	connMu sync.Mutex
	conn   *websocket.Conn

	proxyURL string
}

// Kline 是一个标准 K 线（OHLCV）。
type Kline struct {
	Interval string
	Symbol   string

	StartTimeMs int64
	EndTimeMs   int64
	IsClosed    bool

	Open   float64
	High   float64
	Low    float64
	Close  float64
	Volume float64
}

func NewBinanceFuturesKlines(symbol string, proxyURL string) *BinanceFuturesKlines {
	s := strings.ToLower(strings.TrimSpace(symbol))
	if s == "" {
		s = "btcusdt"
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &BinanceFuturesKlines{
		symbol:   s,
		latest:   make(map[string]Kline),
		history:  make(map[string]map[int64]Kline),
		order:    make(map[string][]int64),
		ctx:      ctx,
		cancel:   cancel,
		proxyURL: strings.TrimSpace(proxyURL),
	}
}

func (b *BinanceFuturesKlines) Start() {
	go b.run()
}

func (b *BinanceFuturesKlines) Stop() {
	if b.cancel != nil {
		b.cancel()
	}
	b.connMu.Lock()
	if b.conn != nil {
		_ = b.conn.Close()
		b.conn = nil
	}
	b.connMu.Unlock()
}

func (b *BinanceFuturesKlines) Symbol() string { return b.symbol }

// Latest 返回某个 interval（如 "1s"/"1m"）的最新 K 线快照。
func (b *BinanceFuturesKlines) Latest(interval string) (Kline, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	kl, ok := b.latest[strings.ToLower(strings.TrimSpace(interval))]
	return kl, ok
}

// Get 返回某个 interval 在指定 startTimeMs 的 kline（如果缓存里有）。
func (b *BinanceFuturesKlines) Get(interval string, startTimeMs int64) (Kline, bool) {
	interval = strings.ToLower(strings.TrimSpace(interval))
	b.mu.RLock()
	defer b.mu.RUnlock()
	mp := b.history[interval]
	if mp == nil {
		return Kline{}, false
	}
	kl, ok := mp[startTimeMs]
	return kl, ok
}

// NearestAtOrBefore 返回 startTimeMs <= targetMs 的最近一根 kline（用于做“lookback window”）。
func (b *BinanceFuturesKlines) NearestAtOrBefore(interval string, targetMs int64) (Kline, bool) {
	interval = strings.ToLower(strings.TrimSpace(interval))
	b.mu.RLock()
	defer b.mu.RUnlock()
	ord := b.order[interval]
	if len(ord) == 0 {
		return Kline{}, false
	}
	mp := b.history[interval]
	if mp == nil {
		return Kline{}, false
	}
	for i := len(ord) - 1; i >= 0; i-- {
		st := ord[i]
		if st <= targetMs {
			kl, ok := mp[st]
			return kl, ok
		}
	}
	return Kline{}, false
}

func (b *BinanceFuturesKlines) setLatest(kl Kline) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.latest == nil {
		b.latest = make(map[string]Kline)
	}
	interval := strings.ToLower(strings.TrimSpace(kl.Interval))
	b.latest[interval] = kl

	// 写入历史（用于查“开盘 1m”等非 latest 的 kline）
	if b.history == nil {
		b.history = make(map[string]map[int64]Kline)
	}
	if b.order == nil {
		b.order = make(map[string][]int64)
	}
	if b.history[interval] == nil {
		b.history[interval] = make(map[int64]Kline)
	}
	if _, exists := b.history[interval][kl.StartTimeMs]; !exists {
		b.order[interval] = append(b.order[interval], kl.StartTimeMs)
	}
	b.history[interval][kl.StartTimeMs] = kl

	// 控制内存：不同 interval 保留不同长度
	maxLen := 0
	switch interval {
	case "1s":
		maxLen = 1200 // ~20分钟
	case "1m":
		maxLen = 240 // ~4小时
	default:
		maxLen = 256
	}
	if maxLen > 0 && len(b.order[interval]) > maxLen {
		overflow := len(b.order[interval]) - maxLen
		evict := b.order[interval][:overflow]
		b.order[interval] = b.order[interval][overflow:]
		for _, st := range evict {
			delete(b.history[interval], st)
		}
	}
}

func (b *BinanceFuturesKlines) run() {
	// 同时订阅 1s + 1m
	streams := []string{
		fmt.Sprintf("%s@kline_1s", b.symbol),
		fmt.Sprintf("%s@kline_1m", b.symbol),
	}
	wsURL := "wss://fstream.binance.com/stream?streams=" + strings.Join(streams, "/")

	for {
		select {
		case <-b.ctx.Done():
			return
		default:
		}

		conn, err := b.dial(wsURL)
		if err != nil {
			binanceKlineLog.Warnf("连接 Binance Futures WS 失败: %v", err)
			select {
			case <-time.After(2 * time.Second):
				continue
			case <-b.ctx.Done():
				return
			}
		}

		b.connMu.Lock()
		b.conn = conn
		b.connMu.Unlock()

		binanceKlineLog.Infof("✅ Binance Futures klines 已连接: symbol=%s streams=%v", b.symbol, streams)

		if err := b.readLoop(conn); err != nil {
			binanceKlineLog.Warnf("Binance Futures WS readLoop 退出: %v", err)
		}

		b.connMu.Lock()
		if b.conn == conn {
			b.conn = nil
		}
		_ = conn.Close()
		b.connMu.Unlock()

		select {
		case <-time.After(1 * time.Second):
		case <-b.ctx.Done():
			return
		}
	}
}

func (b *BinanceFuturesKlines) dial(wsURL string) (*websocket.Conn, error) {
	dialer := websocket.Dialer{
		HandshakeTimeout: 15 * time.Second,
	}
	if b.proxyURL != "" {
		if p, err := url.Parse(b.proxyURL); err == nil {
			dialer.Proxy = http.ProxyURL(p)
		}
	}
	conn, _, err := dialer.Dial(wsURL, nil)
	return conn, err
}

func (b *BinanceFuturesKlines) readLoop(conn *websocket.Conn) error {
	type payload struct {
		Stream string          `json:"stream"`
		Data   json.RawMessage `json:"data"`
	}

	for {
		select {
		case <-b.ctx.Done():
			return b.ctx.Err()
		default:
		}

		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return err
		}

		var p payload
		if err := json.Unmarshal(msg, &p); err != nil {
			continue
		}
		if len(p.Data) == 0 {
			continue
		}
		b.handleKlineEvent(p.Data)
	}
}

func (b *BinanceFuturesKlines) handleKlineEvent(data json.RawMessage) {
	// Binance futures kline payload
	// https://binance-docs.github.io/apidocs/futures/en/#kline-candlestick-streams
	type klinePayload struct {
		EventType string `json:"e"`
		EventTime int64  `json:"E"`
		Symbol    string `json:"s"`
		K         struct {
			StartTime int64  `json:"t"`
			EndTime   int64  `json:"T"`
			Symbol    string `json:"s"`
			Interval  string `json:"i"`
			Open      string `json:"o"`
			Close     string `json:"c"`
			High      string `json:"h"`
			Low       string `json:"l"`
			Volume    string `json:"v"`
			IsClosed  bool   `json:"x"`
		} `json:"k"`
		_ json.RawMessage `json:"-"`
	}

	var ev klinePayload
	if err := json.Unmarshal(data, &ev); err != nil {
		return
	}
	if ev.EventType != "kline" {
		return
	}

	// 解析 float（Binance 字符串数字）
	open, ok1 := parseFloat(ev.K.Open)
	high, ok2 := parseFloat(ev.K.High)
	low, ok3 := parseFloat(ev.K.Low)
	closep, ok4 := parseFloat(ev.K.Close)
	vol, ok5 := parseFloat(ev.K.Volume)
	if !(ok1 && ok2 && ok3 && ok4 && ok5) {
		return
	}

	interval := strings.ToLower(strings.TrimSpace(ev.K.Interval)) // "1s"/"1m"
	kl := Kline{
		Interval:    interval,
		Symbol:      strings.ToLower(strings.TrimSpace(ev.K.Symbol)),
		StartTimeMs: ev.K.StartTime,
		EndTimeMs:   ev.K.EndTime,
		IsClosed:    ev.K.IsClosed,
		Open:        open,
		High:        high,
		Low:         low,
		Close:       closep,
		Volume:      vol,
	}
	b.setLatest(kl)

	_ = ev.EventTime
}

func parseFloat(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	// 手写 parse，避免引入 strconv 重复开销？这里用 fmt.Sscanf 足够简单。
	var v float64
	if _, err := fmt.Sscanf(s, "%f", &v); err != nil {
		return 0, false
	}
	return v, true
}

