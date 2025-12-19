package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// WebSocket URL for Polymarket CLOB
	WebSocketURL = "wss://ws-subscriptions-clob.polymarket.com/ws/market"

	// WebSocket URL for Polymarket RTDS (Real Time Data Stream)
	RTDSWebSocketURL = "wss://ws-live-data.polymarket.com"

	// Binance REST API for historical klines
	BinanceKlineAPI = "https://api.binance.com/api/v3/klines"

	// Channel types
	MarketChannel = "market"
	UserChannel   = "user"

	// Ping interval
	PingInterval = 10 * time.Second
)

// getCurrentMarketSlug generates the market slug for the current hour in ET
func getCurrentMarketSlug() string {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		// Fallback to UTC-4 if tz not available
		now := time.Now().Add(-4 * time.Hour)
		return buildSlugFromTime(now)
	}
	nowET := time.Now().In(loc)
	return buildSlugFromTime(nowET)
}

func buildSlugFromTime(t time.Time) string {
	month := strings.ToLower(t.Format("January"))
	day := t.Day()
	hour := t.Hour()

	var hourStr string
	switch {
	case hour == 0:
		hourStr = "12am"
	case hour < 12:
		hourStr = fmt.Sprintf("%dam", hour)
	case hour == 12:
		hourStr = "12pm"
	default:
		hourStr = fmt.Sprintf("%dpm", hour-12)
	}

	return fmt.Sprintf("bitcoin-up-or-down-%s-%d-%s-et", month, day, hourStr)
}

// fetchConditionID resolves a market conditionId from a slug (with a fallback format try)
func fetchConditionID(slug string) (string, error) {
	url := fmt.Sprintf("https://gamma-api.polymarket.com/markets?slug=%s", slug)
	cond, ok, err := tryFetchConditionID(url)
	if err != nil {
		return "", err
	}
	if ok {
		log.Printf("✅ Found market with original slug: %s", slug)
		return cond, nil
	}

	// Try alternative format adjustments: "-am-" -> "am-", "-pm-" -> "pm-"
	alt := strings.ReplaceAll(strings.ReplaceAll(slug, "-am-", "am-"), "-pm-", "pm-")
	if alt != slug {
		altURL := fmt.Sprintf("https://gamma-api.polymarket.com/markets?slug=%s", alt)
		cond, ok, err = tryFetchConditionID(altURL)
		if err == nil && ok {
			log.Printf("✅ Found market with alternative slug: %s", alt)
			return cond, nil
		}
	}
	return "", fmt.Errorf("condition ID not found for slug '%s' or alternative formats", slug)
}

func tryFetchConditionID(url string) (string, bool, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", false, fmt.Errorf("failed to GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", false, fmt.Errorf("gamma API status %d: %s", resp.StatusCode, string(body))
	}
	var data []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", false, fmt.Errorf("failed to decode gamma API response: %w", err)
	}
	if len(data) == 0 {
		return "", false, nil
	}
	if cid, ok := data[0]["conditionId"].(string); ok && cid != "" {
		return cid, true, nil
	}
	return "", false, nil
}

// fetchYesTokenID returns the YES/UP token_id for a conditionId
func fetchYesTokenID(conditionID string) (string, error) {
	url := fmt.Sprintf("https://clob.polymarket.com/markets/%s", conditionID)
	resp, err := http.Get(url)
	if err != nil {
		return "", fmt.Errorf("failed to GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("CLOB API status %d: %s", resp.StatusCode, string(body))
	}
	var payload struct {
		Tokens []struct {
			Outcome string `json:"outcome"`
			TokenID any    `json:"token_id"`
		} `json:"tokens"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("failed to decode CLOB response: %w", err)
	}
	for _, tok := range payload.Tokens {
		outcome := strings.ToLower(tok.Outcome)
		if outcome == "yes" || outcome == "up" {
			switch v := tok.TokenID.(type) {
			case string:
				return v, nil
			case float64:
				return strconv.FormatInt(int64(v), 10), nil
			default:
				// Fallback to JSON marshal then string
				b, _ := json.Marshal(v)
				return string(b), nil
			}
		}
	}
	return "", fmt.Errorf("YES/UP token_id not found for condition %s", conditionID)
}

// TokenIDs groups YES/NO token ids
type TokenIDs struct {
	Yes string
	No  string
}

// fetchTokenIDs returns both YES and NO token ids for a conditionId
func fetchTokenIDs(conditionID string) (TokenIDs, error) {
	url := fmt.Sprintf("https://clob.polymarket.com/markets/%s", conditionID)
	resp, err := http.Get(url)
	if err != nil {
		return TokenIDs{}, fmt.Errorf("failed to GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return TokenIDs{}, fmt.Errorf("CLOB API status %d: %s", resp.StatusCode, string(body))
	}
	var payload struct {
		Tokens []struct {
			Outcome string `json:"outcome"`
			TokenID any    `json:"token_id"`
		} `json:"tokens"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return TokenIDs{}, fmt.Errorf("failed to decode CLOB response: %w", err)
	}
	var ids TokenIDs
	for _, tok := range payload.Tokens {
		idStr := ""
		switch v := tok.TokenID.(type) {
		case string:
			idStr = v
		case float64:
			idStr = strconv.FormatInt(int64(v), 10)
		default:
			b, _ := json.Marshal(v)
			idStr = string(b)
		}
		outcome := strings.ToLower(tok.Outcome)
		if outcome == "yes" || outcome == "up" {
			ids.Yes = idStr
		} else if outcome == "no" || outcome == "down" {
			ids.No = idStr
		}
	}
	if ids.Yes == "" || ids.No == "" {
		return TokenIDs{}, fmt.Errorf("missing YES/NO token ids for condition %s", conditionID)
	}
	return ids, nil
}

// OrderSummary represents a price level in the orderbook
type OrderSummary struct {
	Price string `json:"price"`
	Size  string `json:"size"`
}

// BookMessage represents the full orderbook snapshot
type BookMessage struct {
	EventType          string         `json:"event_type"`
	AssetID            string         `json:"asset_id"`
	Market             string         `json:"market"`
	Timestamp          string         `json:"timestamp"`
	Hash               string         `json:"hash"`
	Bids               []OrderSummary `json:"bids"`
	Asks               []OrderSummary `json:"asks"`
	BTCPriceCurrent    float64        `json:"btc_price_current,omitempty"`     // Current BTC price (close)
	BTCPriceHourlyOpen float64        `json:"btc_price_hourly_open,omitempty"` // Open price of current 1H candle
}

// PriceChange represents a single price level change
type PriceChange struct {
	AssetID string `json:"asset_id"`
	Price   string `json:"price"`
	Size    string `json:"size"`
	Side    string `json:"side"`
	Hash    string `json:"hash"`
	BestBid string `json:"best_bid"`
	BestAsk string `json:"best_ask"`
}

// PriceChangeMessage represents price changes in the orderbook
type PriceChangeMessage struct {
	EventType    string        `json:"event_type"`
	Market       string        `json:"market"`
	PriceChanges []PriceChange `json:"price_changes"`
	Timestamp    string        `json:"timestamp"`
}

// TickSizeChangeMessage represents a tick size change event
type TickSizeChangeMessage struct {
	EventType   string `json:"event_type"`
	AssetID     string `json:"asset_id"`
	Market      string `json:"market"`
	OldTickSize string `json:"old_tick_size"`
	NewTickSize string `json:"new_tick_size"`
	Timestamp   string `json:"timestamp"`
}

// LastTradePriceMessage represents a trade execution event
type LastTradePriceMessage struct {
	EventType  string `json:"event_type"`
	AssetID    string `json:"asset_id"`
	Market     string `json:"market"`
	Price      string `json:"price"`
	Size       string `json:"size"`
	Side       string `json:"side"`
	FeeRateBps string `json:"fee_rate_bps"`
	Timestamp  string `json:"timestamp"`
}

// SubscribeMessage is the message sent to subscribe to markets
type SubscribeMessage struct {
	AssetsIDs []string `json:"assets_ids"`
	Type      string   `json:"type"`
}

// Auth holds authentication credentials (for user channel)
type Auth struct {
	APIKey     string `json:"apiKey"`
	Secret     string `json:"secret"`
	Passphrase string `json:"passphrase"`
}

// RTDSSubscribeMessage is the subscription message for RTDS
type RTDSSubscribeMessage struct {
	Action        string                  `json:"action"`
	Subscriptions []RTDSSubscriptionTopic `json:"subscriptions"`
}

// RTDSSubscriptionTopic represents a single subscription topic
type RTDSSubscriptionTopic struct {
	Topic string `json:"topic"`
	Type  string `json:"type"`
	// Filters not needed for crypto_prices - omit entirely
}

// RTDSCryptoPriceMessage represents crypto price updates from RTDS
type RTDSCryptoPriceMessage struct {
	Topic     string              `json:"topic"`
	Type      string              `json:"type"`
	Timestamp int64               `json:"timestamp"`
	Payload   RTDSCryptoPriceData `json:"payload"`
}

// RTDSCryptoPriceData contains the actual price data
type RTDSCryptoPriceData struct {
	Symbol    string  `json:"symbol"`
	Timestamp int64   `json:"timestamp"`
	Value     float64 `json:"value"`
}

// BTCPriceTracker tracks current and hourly open Bitcoin prices
type BTCPriceTracker struct {
	currentPrice     float64
	hourlyOpenPrice  float64 // Open price of the current 1H candle
	currentHourStart time.Time
	mu               sync.RWMutex
}

// NewBTCPriceTracker creates a new Bitcoin price tracker
func NewBTCPriceTracker() *BTCPriceTracker {
	return &BTCPriceTracker{
		currentHourStart: time.Now().Truncate(time.Hour),
	}
}

// UpdatePrice updates the current Bitcoin price and detects hour changes
func (bt *BTCPriceTracker) UpdatePrice(price float64) {
	bt.mu.Lock()
	bt.currentPrice = price
	currentHour := time.Now().Truncate(time.Hour)
	shouldFetch := currentHour.After(bt.currentHourStart) && bt.currentHourStart.Unix() > 0
	bt.mu.Unlock()

	// If we crossed into a new hour, fetch the official open from Binance
	if shouldFetch {
		log.Printf("🔔 Hour changed - fetching official open price from Binance...")
		if err := bt.FetchHourlyOpenFromBinance(currentHour); err != nil {
			log.Printf("⚠️  Failed to fetch Binance hourly open: %v", err)
			// Fallback to using the current price
			bt.mu.Lock()
			bt.hourlyOpenPrice = price
			bt.currentHourStart = currentHour
			bt.mu.Unlock()
			log.Printf("📊 Using fallback open price: $%.2f", price)
		}
	}
}

// GetPrices returns the current price and hourly open price
func (bt *BTCPriceTracker) GetPrices() (current, hourlyOpen float64) {
	bt.mu.RLock()
	defer bt.mu.RUnlock()
	return bt.currentPrice, bt.hourlyOpenPrice
}

// GetCurrentHourInfo returns detailed info about the current hour candle
func (bt *BTCPriceTracker) GetCurrentHourInfo() (hourStart time.Time, open, current float64) {
	bt.mu.RLock()
	defer bt.mu.RUnlock()
	return bt.currentHourStart, bt.hourlyOpenPrice, bt.currentPrice
}

// FetchHourlyOpenFromBinance fetches the actual hourly open price from Binance API
// This ensures we have the correct "Open" price that Binance uses for the 1H candle
func (bt *BTCPriceTracker) FetchHourlyOpenFromBinance(hourStart time.Time) error {
	// Binance API expects milliseconds timestamp
	startTime := hourStart.UnixMilli()

	// Build request URL for BTCUSDT 1h klines
	// We request 1 candle starting at the hour boundary
	url := fmt.Sprintf("%s?symbol=BTCUSDT&interval=1h&startTime=%d&limit=1",
		BinanceKlineAPI, startTime)

	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("failed to fetch Binance kline: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Binance API returned status %d: %s", resp.StatusCode, string(body))
	}

	// Parse response
	// Binance kline format: [openTime, open, high, low, close, volume, closeTime, ...]
	var klines [][]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&klines); err != nil {
		return fmt.Errorf("failed to decode Binance response: %w", err)
	}

	if len(klines) == 0 {
		return fmt.Errorf("no kline data returned from Binance")
	}

	// Extract open price (index 1)
	openPriceStr, ok := klines[0][1].(string)
	if !ok {
		return fmt.Errorf("unexpected kline format from Binance")
	}

	openPrice, err := strconv.ParseFloat(openPriceStr, 64)
	if err != nil {
		return fmt.Errorf("failed to parse open price: %w", err)
	}

	// Update the tracker with the official open price
	bt.mu.Lock()
	bt.hourlyOpenPrice = openPrice
	bt.currentHourStart = hourStart
	bt.mu.Unlock()

	log.Printf("📊 Fetched official hourly open from Binance for %s: $%.2f",
		hourStart.Format("15:04"), openPrice)

	return nil
}

// DataStore manages JSON file storage for market data
type DataStore struct {
	dataDir               string
	bookFileYes           *os.File
	bookFileNo            *os.File
	priceChangeFile       *os.File
	tradeFileYes          *os.File
	tradeFileNo           *os.File
	tickSizeChangeFileYes *os.File
	tickSizeChangeFileNo  *os.File
	mu                    sync.Mutex
}

// NewDataStore creates a new data store with JSON files
func NewDataStore(dataDir string) (*DataStore, error) {
	// Create data directory if it doesn't exist
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create data directory: %w", err)
	}

	ds := &DataStore{
		dataDir: dataDir,
	}

	// Open/create JSON files - separate for YES and NO tokens
	var err error
	ds.bookFileYes, err = os.OpenFile(dataDir+"/orderbook_snapshots_yes.json", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open YES orderbook file: %w", err)
	}

	ds.bookFileNo, err = os.OpenFile(dataDir+"/orderbook_snapshots_no.json", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open NO orderbook file: %w", err)
	}

	// Do not create/store price_changes.json anymore
	ds.priceChangeFile = nil

	ds.tradeFileYes, err = os.OpenFile(dataDir+"/trades_yes.json", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open YES trade file: %w", err)
	}

	ds.tradeFileNo, err = os.OpenFile(dataDir+"/trades_no.json", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open NO trade file: %w", err)
	}

	ds.tickSizeChangeFileYes, err = os.OpenFile(dataDir+"/tick_size_changes_yes.json", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open YES tick size change file: %w", err)
	}

	ds.tickSizeChangeFileNo, err = os.OpenFile(dataDir+"/tick_size_changes_no.json", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open NO tick size change file: %w", err)
	}

	log.Printf("Data store initialized. Saving data to: %s", dataDir)
	return ds, nil
}

// SaveBookMessage saves an orderbook snapshot to JSON (separate files for YES/NO)
func (ds *DataStore) SaveBookMessage(msg BookMessage, tokenType string) error {
	ds.mu.Lock()
	defer ds.mu.Unlock()

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal book message: %w", err)
	}

	var targetFile *os.File
	if tokenType == "YES" {
		targetFile = ds.bookFileYes
	} else if tokenType == "NO" {
		targetFile = ds.bookFileNo
	} else {
		return fmt.Errorf("invalid token type: %s (must be YES or NO)", tokenType)
	}

	if _, err := targetFile.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("failed to write book message: %w", err)
	}

	return targetFile.Sync()
}

// SavePriceChangeMessage saves price changes to JSON
func (ds *DataStore) SavePriceChangeMessage(msg PriceChangeMessage) error {
	// Intentionally disabled: no longer storing price change data
	return nil
}

// SaveTradeMessage saves a trade execution to JSON (separate files for YES/NO)
func (ds *DataStore) SaveTradeMessage(msg LastTradePriceMessage, tokenType string) error {
	ds.mu.Lock()
	defer ds.mu.Unlock()

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal trade message: %w", err)
	}

	var targetFile *os.File
	if tokenType == "YES" {
		targetFile = ds.tradeFileYes
	} else if tokenType == "NO" {
		targetFile = ds.tradeFileNo
	} else {
		return fmt.Errorf("invalid token type: %s (must be YES or NO)", tokenType)
	}

	if _, err := targetFile.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("failed to write trade message: %w", err)
	}

	return targetFile.Sync()
}

// SaveTickSizeChangeMessage saves a tick size change to JSON (separate files for YES/NO)
func (ds *DataStore) SaveTickSizeChangeMessage(msg TickSizeChangeMessage, tokenType string) error {
	ds.mu.Lock()
	defer ds.mu.Unlock()

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal tick size change message: %w", err)
	}

	var targetFile *os.File
	if tokenType == "YES" {
		targetFile = ds.tickSizeChangeFileYes
	} else if tokenType == "NO" {
		targetFile = ds.tickSizeChangeFileNo
	} else {
		return fmt.Errorf("invalid token type: %s (must be YES or NO)", tokenType)
	}

	if _, err := targetFile.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("failed to write tick size change message: %w", err)
	}

	return targetFile.Sync()
}

// Close closes all open file handles
func (ds *DataStore) Close() error {
	var errs []error

	if ds.bookFileYes != nil {
		if err := ds.bookFileYes.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if ds.bookFileNo != nil {
		if err := ds.bookFileNo.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if ds.priceChangeFile != nil {
		if err := ds.priceChangeFile.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if ds.tradeFileYes != nil {
		if err := ds.tradeFileYes.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if ds.tradeFileNo != nil {
		if err := ds.tradeFileNo.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if ds.tickSizeChangeFileYes != nil {
		if err := ds.tickSizeChangeFileYes.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if ds.tickSizeChangeFileNo != nil {
		if err := ds.tickSizeChangeFileNo.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors closing files: %v", errs)
	}
	return nil
}

// PolymarketClient manages the WebSocket connection
type PolymarketClient struct {
	conn         *websocket.Conn
	assetIDs     []string
	auth         *Auth
	dataStore    *DataStore
	btcTracker   *BTCPriceTracker
	done         chan struct{}
	interrupt    chan os.Signal
	tokenTypeMap map[string]string // Maps assetID to "YES" or "NO"
}

// NewPolymarketClient creates a new client instance
func NewPolymarketClient(assetIDs []string, tokenIDs TokenIDs, auth *Auth, dataStore *DataStore, btcTracker *BTCPriceTracker) *PolymarketClient {
	// Build map from assetID to token type (YES/NO)
	tokenTypeMap := make(map[string]string)
	tokenTypeMap[tokenIDs.Yes] = "YES"
	tokenTypeMap[tokenIDs.No] = "NO"

	return &PolymarketClient{
		assetIDs:     assetIDs,
		auth:         auth,
		dataStore:    dataStore,
		btcTracker:   btcTracker,
		done:         make(chan struct{}),
		interrupt:    make(chan os.Signal, 1),
		tokenTypeMap: tokenTypeMap,
	}
}

// Connect establishes the WebSocket connection
func (c *PolymarketClient) Connect() error {
	log.Printf("Connecting to %s", WebSocketURL)

	var err error
	c.conn, _, err = websocket.DefaultDialer.Dial(WebSocketURL, nil)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}

	log.Println("Connected successfully")
	return nil
}

// Subscribe sends the subscription message for the market channel
func (c *PolymarketClient) Subscribe() error {
	subscribeMsg := SubscribeMessage{
		AssetsIDs: c.assetIDs,
		Type:      MarketChannel,
	}

	msgBytes, err := json.Marshal(subscribeMsg)
	if err != nil {
		return fmt.Errorf("failed to marshal subscribe message: %w", err)
	}

	log.Printf("Subscribing to asset IDs: %v", c.assetIDs)
	err = c.conn.WriteMessage(websocket.TextMessage, msgBytes)
	if err != nil {
		return fmt.Errorf("failed to send subscribe message: %w", err)
	}

	log.Println("Subscription message sent")
	return nil
}

// startPingLoop sends periodic PING messages to keep the connection alive
func (c *PolymarketClient) startPingLoop() {
	ticker := time.NewTicker(PingInterval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				err := c.conn.WriteMessage(websocket.TextMessage, []byte("PING"))
				if err != nil {
					log.Printf("Error sending ping: %v", err)
					return
				}
				log.Println("Sent PING")
			case <-c.done:
				return
			}
		}
	}()
}

// handleMessage processes incoming WebSocket messages
func (c *PolymarketClient) handleMessage(message []byte) {
	// First, check if it's a PONG response
	if string(message) == "PONG" {
		log.Println("Received PONG")
		return
	}

	// Parse the message to determine its type
	var eventType struct {
		EventType string `json:"event_type"`
	}

	err := json.Unmarshal(message, &eventType)
	if err != nil {
		log.Printf("Failed to parse message type: %v", err)
		log.Printf("Raw message: %s", string(message))
		return
	}

	switch eventType.EventType {
	case "book":
		var bookMsg BookMessage
		if err := json.Unmarshal(message, &bookMsg); err != nil {
			log.Printf("Failed to parse book message: %v", err)
			return
		}
		c.handleBookMessage(bookMsg)

	case "price_change":
		var priceChangeMsg PriceChangeMessage
		if err := json.Unmarshal(message, &priceChangeMsg); err != nil {
			log.Printf("Failed to parse price_change message: %v", err)
			return
		}
		c.handlePriceChangeMessage(priceChangeMsg)

	case "tick_size_change":
		var tickSizeMsg TickSizeChangeMessage
		if err := json.Unmarshal(message, &tickSizeMsg); err != nil {
			log.Printf("Failed to parse tick_size_change message: %v", err)
			return
		}
		c.handleTickSizeChangeMessage(tickSizeMsg)

	case "last_trade_price":
		var tradeMsg LastTradePriceMessage
		if err := json.Unmarshal(message, &tradeMsg); err != nil {
			log.Printf("Failed to parse last_trade_price message: %v", err)
			return
		}
		c.handleLastTradePriceMessage(tradeMsg)

	default:
		log.Printf("Unknown event type: %s", eventType.EventType)
		log.Printf("Raw message: %s", string(message))
	}
}

// handleBookMessage processes orderbook snapshot messages
func (c *PolymarketClient) handleBookMessage(msg BookMessage) {
	// Add Bitcoin prices if tracker is available
	if c.btcTracker != nil {
		current, hourlyOpen := c.btcTracker.GetPrices()
		msg.BTCPriceCurrent = current
		msg.BTCPriceHourlyOpen = hourlyOpen
	}

	log.Println("========== ORDERBOOK SNAPSHOT ==========")
	log.Printf("Asset ID: %s", msg.AssetID)
	log.Printf("Market: %s", msg.Market)
	log.Printf("Timestamp: %s", msg.Timestamp)
	log.Printf("Hash: %s", msg.Hash)

	if msg.BTCPriceCurrent > 0 {
		hourStart, open, current := c.btcTracker.GetCurrentHourInfo()
		movement := ""
		if current > open {
			movement = "📈 UP"
		} else if current < open {
			movement = "📉 DOWN"
		} else {
			movement = "➡️ FLAT"
		}
		log.Printf("BTC 1H Candle [%s]: Open=$%.2f Current=$%.2f %s",
			hourStart.Format("15:04"), open, current, movement)
	}

	log.Println("\nBids (Buy Orders):")
	for i, bid := range msg.Bids {
		if i < 5 { // Show top 5 levels
			log.Printf("  Price: %s, Size: %s", bid.Price, bid.Size)
		}
	}

	log.Println("\nAsks (Sell Orders):")
	for i, ask := range msg.Asks {
		if i < 5 { // Show top 5 levels
			log.Printf("  Price: %s, Size: %s", ask.Price, ask.Size)
		}
	}
	log.Println("=======================================\n")

	// Determine token type (YES/NO) from assetID
	tokenType, ok := c.tokenTypeMap[msg.AssetID]
	if !ok {
		log.Printf("Warning: Unknown asset ID %s, cannot determine token type", msg.AssetID)
		return
	}

	// Save to JSON file (separate for YES/NO)
	if c.dataStore != nil {
		if err := c.dataStore.SaveBookMessage(msg, tokenType); err != nil {
			log.Printf("Error saving orderbook snapshot: %v", err)
		}
	}
}

// handlePriceChangeMessage processes price change messages
func (c *PolymarketClient) handlePriceChangeMessage(msg PriceChangeMessage) {
	log.Println("---------- PRICE CHANGE ----------")
	log.Printf("Market: %s", msg.Market)
	log.Printf("Timestamp: %s", msg.Timestamp)

	for _, change := range msg.PriceChanges {
		log.Printf("\nAsset: %s", change.AssetID)
		log.Printf("  Side: %s", change.Side)
		log.Printf("  Price: %s, Size: %s", change.Price, change.Size)
		log.Printf("  Best Bid: %s, Best Ask: %s", change.BestBid, change.BestAsk)
	}
	log.Println("----------------------------------\n")

	// Storage disabled for price changes
}

// handleTickSizeChangeMessage processes tick size change messages
func (c *PolymarketClient) handleTickSizeChangeMessage(msg TickSizeChangeMessage) {
	log.Println("---------- TICK SIZE CHANGE ----------")
	log.Printf("Asset ID: %s", msg.AssetID)
	log.Printf("Market: %s", msg.Market)
	log.Printf("Old Tick Size: %s -> New Tick Size: %s", msg.OldTickSize, msg.NewTickSize)
	log.Printf("Timestamp: %s", msg.Timestamp)
	log.Println("--------------------------------------\n")

	// Determine token type (YES/NO) from assetID
	tokenType, ok := c.tokenTypeMap[msg.AssetID]
	if !ok {
		log.Printf("Warning: Unknown asset ID %s, cannot determine token type", msg.AssetID)
		return
	}

	// Save to JSON file (separate for YES/NO)
	if c.dataStore != nil {
		if err := c.dataStore.SaveTickSizeChangeMessage(msg, tokenType); err != nil {
			log.Printf("Error saving tick size change: %v", err)
		}
	}
}

// handleLastTradePriceMessage processes trade execution messages
func (c *PolymarketClient) handleLastTradePriceMessage(msg LastTradePriceMessage) {
	log.Println("---------- TRADE EXECUTED ----------")
	log.Printf("Asset ID: %s", msg.AssetID)
	log.Printf("Market: %s", msg.Market)
	log.Printf("Side: %s", msg.Side)
	log.Printf("Price: %s, Size: %s", msg.Price, msg.Size)
	log.Printf("Fee Rate (bps): %s", msg.FeeRateBps)
	log.Printf("Timestamp: %s", msg.Timestamp)
	log.Println("------------------------------------\n")

	// Determine token type (YES/NO) from assetID
	tokenType, ok := c.tokenTypeMap[msg.AssetID]
	if !ok {
		log.Printf("Warning: Unknown asset ID %s, cannot determine token type", msg.AssetID)
		return
	}

	// Save to JSON file (separate for YES/NO)
	if c.dataStore != nil {
		if err := c.dataStore.SaveTradeMessage(msg, tokenType); err != nil {
			log.Printf("Error saving trade: %v", err)
		}
	}
}

// Listen starts listening for messages from the WebSocket
func (c *PolymarketClient) Listen() {
	signal.Notify(c.interrupt, os.Interrupt)

	// Start the ping loop
	c.startPingLoop()

	// Listen for messages
	go func() {
		defer close(c.done)
		for {
			_, message, err := c.conn.ReadMessage()
			if err != nil {
				log.Printf("Error reading message: %v", err)
				return
			}
			c.handleMessage(message)
		}
	}()

	// Wait for interrupt signal
	select {
	case <-c.done:
		log.Println("Connection closed")
	case <-c.interrupt:
		log.Println("Interrupt received, closing connection...")

		// Send close message
		err := c.conn.WriteMessage(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		)
		if err != nil {
			log.Printf("Error sending close message: %v", err)
		}

		select {
		case <-c.done:
		case <-time.After(time.Second):
		}
	}
}

// Close closes the WebSocket connection
func (c *PolymarketClient) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// StartBTCPriceStream connects to RTDS and streams Bitcoin prices
func StartBTCPriceStream(btcTracker *BTCPriceTracker, done chan struct{}) {
	for {
		select {
		case <-done:
			log.Println("BTC price stream shutting down")
			return
		default:
			if err := connectAndStreamBTCPrices(btcTracker, done); err != nil {
				log.Printf("BTC price stream error: %v, reconnecting in 5s...", err)
				time.Sleep(5 * time.Second)
			}
		}
	}
}

func connectAndStreamBTCPrices(btcTracker *BTCPriceTracker, done chan struct{}) error {
	log.Printf("Connecting to RTDS at %s", RTDSWebSocketURL)

	conn, _, err := websocket.DefaultDialer.Dial(RTDSWebSocketURL, nil)
	if err != nil {
		return fmt.Errorf("failed to connect to RTDS: %w", err)
	}
	defer conn.Close()

	log.Println("Connected to RTDS successfully")

	// Subscribe to all crypto prices (will filter for BTC in the handler)
	subscribeMsg := RTDSSubscribeMessage{
		Action: "subscribe",
		Subscriptions: []RTDSSubscriptionTopic{
			{
				Topic: "crypto_prices",
				Type:  "update",
				// No filters - receive all crypto prices
			},
		},
	}

	msgBytes, err := json.Marshal(subscribeMsg)
	if err != nil {
		return fmt.Errorf("failed to marshal RTDS subscribe message: %w", err)
	}

	if err := conn.WriteMessage(websocket.TextMessage, msgBytes); err != nil {
		return fmt.Errorf("failed to send RTDS subscribe message: %w", err)
	}

	log.Println("Subscribed to crypto prices (filtering for Bitcoin)")

	// Read messages
	for {
		select {
		case <-done:
			return nil
		default:
			_, message, err := conn.ReadMessage()
			if err != nil {
				return fmt.Errorf("error reading RTDS message: %w", err)
			}

			// Parse the message
			var priceMsg RTDSCryptoPriceMessage
			if err := json.Unmarshal(message, &priceMsg); err != nil {
				// Skip non-JSON messages (like confirmation messages)
				continue
			}

			// Update the tracker with the new price
			if priceMsg.Topic == "crypto_prices" && priceMsg.Payload.Symbol == "btcusdt" {
				btcTracker.UpdatePrice(priceMsg.Payload.Value)
				log.Printf("₿ BTC: $%.2f", priceMsg.Payload.Value)
			}
		}
	}
}

func main() {
	// Create Bitcoin price tracker (global for the session)
	btcTracker := NewBTCPriceTracker()
	log.Println("Bitcoin price tracker initialized")

	// Fetch the official hourly open price from Binance at startup
	currentHour := time.Now().Truncate(time.Hour)
	log.Printf("Fetching official BTC hourly open for %s from Binance API...", currentHour.Format("15:04"))
	if err := btcTracker.FetchHourlyOpenFromBinance(currentHour); err != nil {
		log.Printf("⚠️  Warning: Failed to fetch initial hourly open from Binance: %v", err)
		log.Println("Will use real-time price as fallback when available")
	}

	// Start Bitcoin price stream in background (single stream for entire run)
	btcDone := make(chan struct{})
	go StartBTCPriceStream(btcTracker, btcDone)

	// Global interrupt handling to stop all goroutines (including BTC stream)
	stopAll := make(chan struct{})
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("Interrupt received, shutting down...")
		close(stopAll)
		close(btcDone)
	}()

	// Run hourly loop: resolve market -> get YES token -> start client -> run until hour end -> rotate
	for {
		slug := getCurrentMarketSlug()
		log.Printf("Resolved current market slug: %s", slug)

		conditionID, err := fetchConditionID(slug)
		if err != nil {
			log.Printf("Retrying in 15s - failed to resolve conditionId: %v", err)
			time.Sleep(15 * time.Second)
			continue
		}
		tokenIDs, err := fetchTokenIDs(conditionID)
		if err != nil {
			log.Printf("Retrying in 15s - failed to resolve token IDs: %v", err)
			time.Sleep(15 * time.Second)
			continue
		}
		log.Printf("Using token IDs YES=%s NO=%s (condition: %s)", tokenIDs.Yes, tokenIDs.No, conditionID)

		// Prepare hour-specific data directory (ET label)
		loc, _ := time.LoadLocation("America/New_York")
		nowET := time.Now().In(loc).Truncate(time.Hour)
		dirName := nowET.Format("2006-01-02_15-00_ET")
		dataDir := fmt.Sprintf("data/%s", dirName)

		dataStore, err := NewDataStore(dataDir)
		if err != nil {
			log.Printf("Retrying in 15s - failed to create data store: %v", err)
			time.Sleep(15 * time.Second)
			continue
		}

		client := NewPolymarketClient([]string{tokenIDs.Yes, tokenIDs.No}, tokenIDs, nil, dataStore, btcTracker)
		if err := client.Connect(); err != nil {
			log.Printf("Retrying in 15s - failed to connect CLOB: %v", err)
			dataStore.Close()
			time.Sleep(15 * time.Second)
			continue
		}

		if err := client.Subscribe(); err != nil {
			log.Printf("Retrying in 15s - failed to subscribe: %v", err)
			client.Close()
			dataStore.Close()
			time.Sleep(15 * time.Second)
			continue
		}

		log.Printf("Listening for orderbook updates... Hour folder: %s", dataDir)
		go client.Listen()

		// Run until end of the hour (ET) or until global stop
		nextHour := nowET.Add(time.Hour)
		sleepDur := time.Until(nextHour)
		if sleepDur < 0 {
			sleepDur = 0
		}
		select {
		case <-time.After(sleepDur):
			// hour completed
		case <-stopAll:
			client.Close()
			dataStore.Close()
			log.Println("Shutdown requested. Exiting...")
			return
		}

		// Rotate: close current client and datastore, then loop to next
		client.Close()
		dataStore.Close()
		log.Println("Hour complete. Rotating to next hour...")
	}
}
