package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/betbot/gobet/clob/client"
	"github.com/betbot/gobet/clob/rtds"
	"github.com/betbot/gobet/clob/signing"
	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/infrastructure/websocket"
	"github.com/betbot/gobet/internal/marketstate"
	"github.com/betbot/gobet/internal/services"
	_ "github.com/betbot/gobet/internal/strategies/all"
	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/betbot/gobet/pkg/config"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	gorillaWS "github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

const (
	orderbookDepth = 5 // 显示订单薄的深度（买五、卖五）
)

var enableTradingInPageMode = true

// 全局 BTC 价格存储（简化处理）
var (
	globalBTCPrice   float64
	globalBTCPriceMu sync.RWMutex
)

// 全局订单薄存储（用于存储完整的买五、卖五数据）
var (
	globalOrderbook     = make(map[domain.TokenType][]orderLevel) // UP/DOWN 的 bids
	globalOrderbookAsks = make(map[domain.TokenType][]orderLevel) // UP/DOWN 的 asks
	globalOrderbookMu   sync.RWMutex
)

// 文件日志记录器（只写入文件，不输出到终端）
var (
	fileLogger     *log.Logger
	fileLoggerOnce sync.Once
)

// initFileLogger 初始化文件日志记录器
func initFileLogger() {
	fileLoggerOnce.Do(func() {
		// 创建日志目录
		logDir := "logs"
		if err := os.MkdirAll(logDir, 0755); err != nil {
			// 如果创建失败，使用临时文件
			logDir = os.TempDir()
		}

		// 日志文件路径
		logFile := filepath.Join(logDir, "price-watcher-tui.log")

		// 打开日志文件（追加模式）
		file, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			// 如果打开失败，使用空设备（丢弃日志）
			file = os.NewFile(0, os.DevNull)
		}

		// 创建日志记录器（只写入文件，不输出到终端）
		fileLogger = log.New(file, "", log.LstdFlags)
	})
}

// fileLoggerRTDS 实现 RTDS Logger 接口，将日志写入文件
type fileLoggerRTDS struct{}

func (l *fileLoggerRTDS) Printf(format string, v ...interface{}) {
	initFileLogger()
	fileLogger.Printf(format, v...)
}

var (
	// 样式定义
	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("15")).
			Background(lipgloss.Color("62")).
			Padding(0, 1)

	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("15"))

	upStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("2")) // 绿色

	downStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("1")) // 红色

	borderStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("238"))

	bidStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("2")) // 绿色

	askStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("1")) // 红色

	priceStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("15"))
)

// model 是应用程序的状态
type model struct {
	// 市场信息
	currentCycle string
	cycleStart   time.Time

	// UP/DOWN 订单薄数据
	upBids   []orderLevel
	upAsks   []orderLevel
	downBids []orderLevel
	downAsks []orderLevel

	// BTC 价格
	btcPrice    float64
	btcPriceStr string

	// 连接状态
	connected bool
	err       error

	// 内部状态
	enableTrading bool
	scheduler     *bbgo.MarketScheduler
	cleanup       func()

	marketStream *websocket.MarketStream
	rtdsClient   *rtds.Client
	market       *domain.Market
	bestBook     *marketstate.AtomicBestBook
	marketMu     sync.RWMutex
	ctx          context.Context
	cancel       context.CancelFunc
}

// orderLevel 订单薄层级
type orderLevel struct {
	Price float64
	Size  float64
}

// tickMsg 定时器消息
type tickMsg time.Time

// priceUpdateMsg 价格更新消息
type priceUpdateMsg struct {
	TokenType domain.TokenType
	Price     float64
}

// btcPriceMsg BTC 价格更新消息
type btcPriceMsg float64

// orderbookUpdateMsg 订单薄更新消息
type orderbookUpdateMsg struct {
	TokenType domain.TokenType
	Bids      []orderLevel
	Asks      []orderLevel
}

// cycleChangeMsg 周期切换消息
type cycleChangeMsg struct {
	Cycle string
	Start time.Time
}

// connectedMsg 连接成功消息
type connectedMsg struct {
	marketStream *websocket.MarketStream
	rtdsClient   *rtds.Client
	market       *domain.Market
	bestBook     *marketstate.AtomicBestBook
	cycle        string
	start        time.Time

	// page-mode trading
	scheduler *bbgo.MarketScheduler
	cleanup   func()
}

func initialModel() model {
	ctx, cancel := context.WithCancel(context.Background())
	return model{
		upBids:        make([]orderLevel, 0),
		upAsks:        make([]orderLevel, 0),
		downBids:      make([]orderLevel, 0),
		downAsks:      make([]orderLevel, 0),
		connected:     false,
		enableTrading: enableTradingInPageMode,
		ctx:           ctx,
		cancel:        cancel,
		bestBook:      marketstate.NewAtomicBestBook(),
		btcPrice:      0,
		btcPriceStr:   "N/A",
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(
		tickCmd(),
		connectCmd(m.ctx, m.enableTrading),
	)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			if m.cleanup != nil {
				m.cleanup()
			}
			if m.cancel != nil {
				m.cancel()
			}
			return m, tea.Quit
		}

	case tickMsg:
		// page 模式：如果启用交易，则由 MarketScheduler 驱动周期切换（UI 只读当前 market）
		if m.enableTrading && m.scheduler != nil {
			if mk := m.scheduler.CurrentMarket(); mk != nil {
				if m.currentCycle != mk.Slug {
					m.currentCycle = mk.Slug
					m.cycleStart = time.Unix(mk.Timestamp, 0)
				}
				// bestBook 会在 session 切换时更新，保险起见 tick 时也读一次
				if sess := m.scheduler.CurrentSession(); sess != nil && sess.BestBook() != nil {
					m.bestBook = sess.BestBook()
				}
			}
		} else {
			// 监控模式：按本地时间推算周期并重连
			currentTs := services.GetCurrent15MinTimestamp()
			currentSlug := services.Generate15MinSlug(currentTs)
			if m.currentCycle != currentSlug {
				return m, tea.Batch(
					tickCmd(),
					switchCycleCmd(currentSlug, time.Unix(currentTs, 0)),
				)
			}
		}

		// 每次 tick 都更新订单薄和 BTC 价格
		if m.bestBook != nil && m.connected {
			snap := m.bestBook.Load()
			// 检查数据是否新鲜（60秒内，放宽限制）
			if !snap.UpdatedAt.IsZero() && time.Since(snap.UpdatedAt) < 60*time.Second {
				// 更新 UP 订单薄
				if snap.YesBidCents > 0 && snap.YesAskCents > 0 {
					m.upBids = []orderLevel{{
						Price: float64(snap.YesBidCents) / 100.0,
						Size:  float64(snap.YesBidSizeScaled) / 10000.0,
					}}
					m.upAsks = []orderLevel{{
						Price: float64(snap.YesAskCents) / 100.0,
						Size:  float64(snap.YesAskSizeScaled) / 10000.0,
					}}
				}
				// 更新 DOWN 订单薄
				if snap.NoBidCents > 0 && snap.NoAskCents > 0 {
					m.downBids = []orderLevel{{
						Price: float64(snap.NoBidCents) / 100.0,
						Size:  float64(snap.NoBidSizeScaled) / 10000.0,
					}}
					m.downAsks = []orderLevel{{
						Price: float64(snap.NoAskCents) / 100.0,
						Size:  float64(snap.NoAskSizeScaled) / 10000.0,
					}}
				}
			}
		}

		// 更新 BTC 价格
		globalBTCPriceMu.RLock()
		if globalBTCPrice > 0 {
			m.btcPrice = globalBTCPrice
			m.btcPriceStr = fmt.Sprintf("$%.2f", globalBTCPrice)
		}
		globalBTCPriceMu.RUnlock()

		return m, tickCmd()

	case priceUpdateMsg:
		// 价格更新（从 BestBook 读取）
		return m, nil

	case btcPriceMsg:
		m.btcPrice = float64(msg)
		m.btcPriceStr = fmt.Sprintf("$%.2f", m.btcPrice)
		return m, nil

	case orderbookUpdateMsg:
		if msg.TokenType == domain.TokenTypeUp {
			m.upBids = msg.Bids
			m.upAsks = msg.Asks
		} else if msg.TokenType == domain.TokenTypeDown {
			m.downBids = msg.Bids
			m.downAsks = msg.Asks
		}
		return m, tickCmd() // 触发下一次更新

	case cycleChangeMsg:
		m.currentCycle = msg.Cycle
		m.cycleStart = msg.Start
		// 监控模式才需要 reconnect
		if !m.enableTrading {
			return m, reconnectCmd(m.ctx, m.enableTrading)
		}
		return m, nil

	case connectedMsg:
		m.marketStream = msg.marketStream
		m.rtdsClient = msg.rtdsClient
		m.market = msg.market
		m.bestBook = msg.bestBook
		m.currentCycle = msg.cycle
		m.cycleStart = msg.start
		m.connected = true
		m.scheduler = msg.scheduler
		m.cleanup = msg.cleanup
		// 启动 BTC 价格更新 goroutine
		return m, startBTCPriceUpdateCmd(msg.rtdsClient)

	case error:
		m.err = msg
		return m, nil
	}

	return m, nil
}

func (m model) View() string {
	if m.err != nil {
		return fmt.Sprintf("错误: %v\n\n按 q 退出", m.err)
	}

	if !m.connected {
		return "正在连接...\n\n按 q 退出"
	}

	// 每次 View 调用时都读取最新数据
	var upBids, upAsks, downBids, downAsks []orderLevel
	var btcPriceStr = m.btcPriceStr
	var statusInfo string

	// 从 BestBook 读取最新数据
	if m.bestBook != nil {
		snap := m.bestBook.Load()
		if !snap.UpdatedAt.IsZero() {
			age := time.Since(snap.UpdatedAt)
			if age < 60*time.Second {
				// 更新 UP 订单薄
				if snap.YesBidCents > 0 && snap.YesAskCents > 0 {
					upBids = []orderLevel{{
						Price: float64(snap.YesBidCents) / 100.0,
						Size:  float64(snap.YesBidSizeScaled) / 10000.0,
					}}
					upAsks = []orderLevel{{
						Price: float64(snap.YesAskCents) / 100.0,
						Size:  float64(snap.YesAskSizeScaled) / 10000.0,
					}}
				}
				// 更新 DOWN 订单薄
				if snap.NoBidCents > 0 && snap.NoAskCents > 0 {
					downBids = []orderLevel{{
						Price: float64(snap.NoBidCents) / 100.0,
						Size:  float64(snap.NoBidSizeScaled) / 10000.0,
					}}
					downAsks = []orderLevel{{
						Price: float64(snap.NoAskCents) / 100.0,
						Size:  float64(snap.NoAskSizeScaled) / 10000.0,
					}}
				}
				statusInfo = fmt.Sprintf("数据更新: %v前", age.Round(time.Second))
			} else {
				statusInfo = fmt.Sprintf("数据过期: %v前", age.Round(time.Second))
			}
		} else {
			statusInfo = "等待数据..."
		}
	} else {
		statusInfo = "BestBook 未初始化"
	}

	// 从全局订单薄读取数据（买五、卖五）
	globalOrderbookMu.RLock()
	if upBidsFromGlobal, ok := globalOrderbook[domain.TokenTypeUp]; ok && len(upBidsFromGlobal) > 0 {
		upBids = upBidsFromGlobal
	}
	if upAsksFromGlobal, ok := globalOrderbookAsks[domain.TokenTypeUp]; ok && len(upAsksFromGlobal) > 0 {
		upAsks = upAsksFromGlobal
	}
	if downBidsFromGlobal, ok := globalOrderbook[domain.TokenTypeDown]; ok && len(downBidsFromGlobal) > 0 {
		downBids = downBidsFromGlobal
	}
	if downAsksFromGlobal, ok := globalOrderbookAsks[domain.TokenTypeDown]; ok && len(downAsksFromGlobal) > 0 {
		downAsks = downAsksFromGlobal
	}
	globalOrderbookMu.RUnlock()

	// 如果没有全局数据，使用缓存的数据
	if len(upBids) == 0 {
		upBids = m.upBids
		upAsks = m.upAsks
	}
	if len(downBids) == 0 {
		downBids = m.downBids
		downAsks = m.downAsks
	}

	// 读取 BTC 价格
	globalBTCPriceMu.RLock()
	if globalBTCPrice > 0 {
		btcPriceStr = fmt.Sprintf("$%.2f", globalBTCPrice)
	}
	globalBTCPriceMu.RUnlock()

	var s strings.Builder

	// 头部：显示当前周期和 BTC 价格
	cycleInfo := fmt.Sprintf("周期: %s | 开始时间: %s",
		m.currentCycle,
		m.cycleStart.Format("2006-01-02 15:04:05"))
	btcInfo := fmt.Sprintf("BTC: %s", btcPriceStr)
	header := headerStyle.Render(fmt.Sprintf("%s | %s | %s", cycleInfo, btcInfo, statusInfo))
	s.WriteString(header)
	s.WriteString("\n\n")

	// 订单薄区域
	upBook := renderOrderbook("UP", upBids, upAsks)
	downBook := renderOrderbook("DOWN", downBids, downAsks)

	// 并排显示 UP 和 DOWN 订单薄
	orderbooks := lipgloss.JoinHorizontal(lipgloss.Top, upBook, "  ", downBook)
	s.WriteString(orderbooks)
	s.WriteString("\n\n")

	// 底部提示
	s.WriteString("按 q 退出")

	return s.String()
}

func renderOrderbook(title string, bids []orderLevel, asks []orderLevel) string {
	var s strings.Builder

	titleStyled := titleStyle.Render(title)
	if title == "UP" {
		titleStyled = upStyle.Render(title)
	} else {
		titleStyled = downStyle.Render(title)
	}

	s.WriteString(titleStyled)
	s.WriteString("\n\n")

	// 显示卖单（asks）- 从低到高（价格低的在上，最接近盘口）
	s.WriteString(askStyle.Render("卖单 (Asks)"))
	s.WriteString("\n")
	if len(asks) > 0 {
		// asks 已经按价格从低到高排序（最接近盘口的在前），直接显示前5档
		for i := 0; i < len(asks) && i < orderbookDepth; i++ {
			ask := asks[i]
			s.WriteString(fmt.Sprintf("  %6.2f  %8.2f\n", ask.Price*100, ask.Size))
		}
	} else {
		s.WriteString("  --\n")
	}

	s.WriteString("\n")

	// 显示中间价
	// 注意：asks[0] 是最低的卖价（最接近盘口），bids[0] 是最高的买价（最接近盘口）
	var midPrice float64
	if len(bids) > 0 && len(asks) > 0 {
		// 中间价 = (最高买价 + 最低卖价) / 2
		// asks 是从低到高排序，asks[0] 是最低卖价（最接近盘口）
		// bids 是从高到低排序，bids[0] 是最高买价（最接近盘口）
		lowestAsk := asks[0].Price
		highestBid := bids[0].Price
		midPrice = (highestBid + lowestAsk) / 2.0
	} else if len(bids) > 0 {
		midPrice = bids[0].Price
	} else if len(asks) > 0 {
		// asks 从低到高，最低卖价在最前
		midPrice = asks[0].Price
	}
	if midPrice > 0 {
		s.WriteString(priceStyle.Render(fmt.Sprintf("中间价: %.2f\n", midPrice*100)))
	} else {
		s.WriteString("中间价: --\n")
	}

	s.WriteString("\n")

	// 显示买单（bids）- 从高到低（价格高的在上）
	s.WriteString(bidStyle.Render("买单 (Bids)"))
	s.WriteString("\n")
	if len(bids) > 0 {
		// bids 按价格从高到低显示（价格高的在上）
		for i := 0; i < len(bids) && i < orderbookDepth; i++ {
			bid := bids[i]
			s.WriteString(fmt.Sprintf("  %6.2f  %8.2f\n", bid.Price*100, bid.Size))
		}
	} else {
		s.WriteString("  --\n")
	}

	return borderStyle.Render(s.String())
}

// Commands

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func connectCmd(ctx context.Context, enableTrading bool) tea.Cmd {
	return func() tea.Msg {
		// 加载配置
		config.SetConfigPath("config.yaml")
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("加载配置失败: %w", err)
		}

		// 设置代理
		proxyURL := ""
		if cfg.Proxy != nil {
			proxyURL = fmt.Sprintf("http://%s:%d", cfg.Proxy.Host, cfg.Proxy.Port)
		}

		// page 模式：启动交易机器人（修复：此前仅监控，不会真正跑 grid 下单）
		var (
			scheduler      *bbgo.MarketScheduler
			tradingCleanup func()
			market         *domain.Market
			bestBook       *marketstate.AtomicBestBook
			marketStream   *websocket.MarketStream
			currentSlug    string
			currentTs      int64
		)
		if enableTrading {
			scheduler, market, bestBook, tradingCleanup, err = startTradingBotForPageMode(ctx, cfg, proxyURL)
			if err != nil {
				return err
			}
			currentSlug = market.Slug
			currentTs = market.Timestamp
		} else {
			// 监控模式：仅连接 MarketStream
			currentTs = services.GetCurrent15MinTimestamp()
			currentSlug = services.Generate15MinSlug(currentTs)

			gammaMarket, err := client.FetchMarketFromGamma(ctx, currentSlug)
			if err != nil {
				return fmt.Errorf("获取市场信息失败: %w", err)
			}

			yesAssetID, noAssetID := parseTokenIDs(gammaMarket.ClobTokenIDs)
			if yesAssetID == "" || noAssetID == "" {
				return fmt.Errorf("解析 token IDs 失败: %s", gammaMarket.ClobTokenIDs)
			}

			market = &domain.Market{
				Slug:        gammaMarket.Slug,
				ConditionID: gammaMarket.ConditionID,
				YesAssetID:  yesAssetID,
				NoAssetID:   noAssetID,
				Timestamp:   currentTs,
			}

			marketStream = websocket.NewMarketStream()
			marketStream.SetProxyURL(proxyURL)
			handler := &priceHandler{}
			marketStream.OnPriceChanged(handler)
			if err := marketStream.Connect(ctx, market); err != nil {
				return fmt.Errorf("连接市场数据流失败: %w", err)
			}
			bestBook = marketStream.BestBook()
			if bestBook == nil {
				return fmt.Errorf("MarketStream BestBook 为空")
			}
		}

		// 启动 goroutine 监听 book 消息以获取完整的订单薄数据（买五、卖五）
		if market != nil {
			go startBookListener(ctx, market, proxyURL)
		}

		// 连接 RTDS 获取 BTC 价格
		// 使用文件日志记录器，避免日志输出到终端干扰 TUI
		initFileLogger()
		rtdsConfig := &rtds.ClientConfig{
			URL:            rtds.RTDSWebSocketURL,
			ProxyURL:       proxyURL,
			PingInterval:   5 * time.Second,
			WriteTimeout:   10 * time.Second,
			ReadTimeout:    60 * time.Second,
			Reconnect:      true,
			ReconnectDelay: 5 * time.Second,
			MaxReconnect:   10,
			Logger:         &fileLoggerRTDS{}, // 使用文件日志记录器
		}
		rtdsClient := rtds.NewClientWithConfig(rtdsConfig)

		if err := rtdsClient.Connect(); err != nil {
			return fmt.Errorf("连接 RTDS 失败: %w", err)
		}

		// 订阅 BTC 价格（在 startBTCPriceUpdateCmd 中处理）

		return connectedMsg{
			marketStream: marketStream,
			rtdsClient:   rtdsClient,
			market:       market,
			bestBook:     bestBook,
			cycle:        currentSlug,
			start:        time.Unix(currentTs, 0),
			scheduler:    scheduler,
			cleanup:      tradingCleanup,
		}
	}
}

func startBTCPriceUpdateCmd(rtdsClient *rtds.Client) tea.Cmd {
	// 订阅 BTC 价格（使用 binance 源，符号为 btcusdt，小写）
	if err := rtdsClient.SubscribeToCryptoPrices("binance", "btcusdt"); err != nil {
		return func() tea.Msg {
			return fmt.Errorf("订阅 BTC 价格失败: %w", err)
		}
	}

	// 使用 CreateCryptoPriceHandler 来正确解析价格
	handler := rtds.CreateCryptoPriceHandler(func(price *rtds.CryptoPrice) error {
		// 匹配 BTC 相关的符号（可能是 BTCUSDT, BTC/USD, BTC 等）
		symbol := strings.ToUpper(price.Symbol)
		if strings.Contains(symbol, "BTC") {
			value := price.Value.Float64()
			if value > 0 {
				globalBTCPriceMu.Lock()
				globalBTCPrice = value
				globalBTCPriceMu.Unlock()
			}
		}
		return nil
	})
	rtdsClient.RegisterHandler("crypto_prices", handler)

	return nil
}

func switchCycleCmd(slug string, start time.Time) tea.Cmd {
	return func() tea.Msg {
		return cycleChangeMsg{Cycle: slug, Start: start}
	}
}

func reconnectCmd(ctx context.Context, enableTrading bool) tea.Cmd {
	return connectCmd(ctx, enableTrading)
}

// priceHandler 价格变化处理器（MarketStream 会自动更新 BestBook，这里只需要占位）
type priceHandler struct {
	market *domain.Market
}

func (h *priceHandler) OnPriceChanged(ctx context.Context, event *events.PriceChangedEvent) error {
	// MarketStream 会自动处理 price_change 和 book 消息并更新 BestBook
	// 这里不需要做任何操作，只是确保 handler 存在
	return nil
}

// bookHandler 订单薄消息处理器（用于获取完整的买五、卖五数据）
type bookHandler struct {
	market *domain.Market
}

// startBookListener 启动独立的 WebSocket 连接来监听 book 消息
func startBookListener(ctx context.Context, market *domain.Market, proxyURL string) {
	wsURL := "wss://ws-subscriptions-clob.polymarket.com/ws/market"

	// 设置代理
	var dialer gorillaWS.Dialer
	if proxyURL != "" {
		proxyParsed, err := url.Parse(proxyURL)
		if err == nil {
			dialer.Proxy = http.ProxyURL(proxyParsed)
		}
	}

	// 初始化文件日志
	initFileLogger()

	// 连接 WebSocket
	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		fileLogger.Printf("连接订单薄 WebSocket 失败: %v", err)
		return
	}
	defer conn.Close()

	// 订阅市场
	subscribeMsg := map[string]interface{}{
		"assets_ids": []string{market.YesAssetID, market.NoAssetID},
		"type":       "market",
	}
	if err := conn.WriteJSON(subscribeMsg); err != nil {
		fileLogger.Printf("发送订阅消息失败: %v", err)
		return
	}

	// 监听消息
	// 首次订阅时会收到 book 消息作为快照，之后当有交易影响订单薄时会实时更新
	for {
		select {
		case <-ctx.Done():
			return
		default:
			// 设置读取超时，避免阻塞
			conn.SetReadDeadline(time.Now().Add(30 * time.Second))
			_, message, err := conn.ReadMessage()
			if err != nil {
				// 检查是否是超时错误
				if netErr, ok := err.(interface{ Timeout() bool }); ok && netErr.Timeout() {
					// 超时，继续循环
					continue
				}
				fileLogger.Printf("读取订单薄消息失败: %v", err)
				return
			}

			// 处理 PING/PONG 消息
			if len(message) > 0 {
				msgStr := string(message)
				if msgStr == "PING" {
					if err := conn.WriteMessage(gorillaWS.TextMessage, []byte("PONG")); err != nil {
						fileLogger.Printf("回复 PONG 失败: %v", err)
					}
					continue
				}
				if msgStr == "PONG" {
					continue
				}
			}

			// 检查是否是 book 消息
			var msgType struct {
				EventType string `json:"event_type"`
			}
			if err := json.Unmarshal(message, &msgType); err != nil {
				// 可能是数组格式的消息
				var msgArray []json.RawMessage
				if err2 := json.Unmarshal(message, &msgArray); err2 == nil {
					for _, rawMsg := range msgArray {
						if err := json.Unmarshal(rawMsg, &msgType); err == nil {
							if msgType.EventType == "book" {
								handleBookMessage(rawMsg, market)
							}
						}
					}
				}
				continue
			}

			// 处理 book 消息（首次订阅快照 + 实时更新）
			if msgType.EventType == "book" {
				handleBookMessage(message, market)
			} else if msgType.EventType == "subscribed" {
				fileLogger.Printf("✅ 订单薄订阅成功")
			}
		}
	}
}

// handleBookMessage 处理 book 消息，提取前5档买卖价格
// 根据官方文档：首次订阅时会收到 book 消息作为快照，之后当有交易影响订单薄时会实时更新
func handleBookMessage(message []byte, market *domain.Market) {
	type wsOrderLevel struct {
		Price string `json:"price"`
		Size  string `json:"size"`
	}

	type bookMessage struct {
		EventType string         `json:"event_type"`
		AssetID   string         `json:"asset_id"`
		Market    string         `json:"market"`
		Timestamp string         `json:"timestamp"`
		Hash      string         `json:"hash"`
		BestBid   string         `json:"best_bid"` // 可选字段
		BestAsk   string         `json:"best_ask"` // 可选字段
		Price     string         `json:"price"`    // 可选字段
		Bids      []wsOrderLevel `json:"bids"`     // 文档示例中使用 bids
		Asks      []wsOrderLevel `json:"asks"`     // 文档示例中使用 asks
		Buys      []wsOrderLevel `json:"buys"`     // 文档描述中使用 buys（兼容）
		Sells     []wsOrderLevel `json:"sells"`    // 文档描述中使用 sells（兼容）
	}

	var bm bookMessage
	if err := json.Unmarshal(message, &bm); err != nil {
		fileLogger.Printf("解析 book 消息失败: %v", err)
		return
	}

	// 过滤：只处理当前市场的消息
	if market != nil && bm.Market != "" && bm.Market != market.ConditionID {
		return
	}

	if bm.AssetID == "" {
		return
	}

	// 确定 token 类型
	var tokenType domain.TokenType
	if market != nil {
		if bm.AssetID == market.YesAssetID {
			tokenType = domain.TokenTypeUp
		} else if bm.AssetID == market.NoAssetID {
			tokenType = domain.TokenTypeDown
		} else {
			return
		}
	}

	// 兼容 bids/asks 和 buys/sells（根据文档，实际响应使用 bids/asks）
	bids := bm.Bids
	if len(bids) == 0 {
		bids = bm.Buys
	}
	asks := bm.Asks
	if len(asks) == 0 {
		asks = bm.Sells
	}

	// 解析所有买单并排序（从高到低，最接近盘口的5档）
	bidsList := make([]orderLevel, 0, len(bids))
	for _, bid := range bids {
		if bid.Price == "" || bid.Size == "" {
			continue
		}
		price, err := strconv.ParseFloat(bid.Price, 64)
		if err != nil || price <= 0 || price > 1.0 {
			continue
		}
		size, err := strconv.ParseFloat(bid.Size, 64)
		if err != nil || size <= 0 {
			continue
		}
		bidsList = append(bidsList, orderLevel{
			Price: price,
			Size:  size,
		})
	}
	// 对买单按价格从高到低排序（价格高的最接近盘口）
	for i := 0; i < len(bidsList)-1; i++ {
		for j := i + 1; j < len(bidsList); j++ {
			if bidsList[i].Price < bidsList[j].Price {
				bidsList[i], bidsList[j] = bidsList[j], bidsList[i]
			}
		}
	}
	// 只保留前5档（最接近盘口的5档）
	if len(bidsList) > orderbookDepth {
		bidsList = bidsList[:orderbookDepth]
	}

	// 解析所有卖单并排序（从低到高，最接近盘口的5档）
	asksList := make([]orderLevel, 0, len(asks))
	for _, ask := range asks {
		if ask.Price == "" || ask.Size == "" {
			continue
		}
		price, err := strconv.ParseFloat(ask.Price, 64)
		if err != nil || price <= 0 || price > 1.0 {
			continue
		}
		size, err := strconv.ParseFloat(ask.Size, 64)
		if err != nil || size <= 0 {
			continue
		}
		asksList = append(asksList, orderLevel{
			Price: price,
			Size:  size,
		})
	}
	// 对卖单按价格从低到高排序（价格低的最接近盘口）
	for i := 0; i < len(asksList)-1; i++ {
		for j := i + 1; j < len(asksList); j++ {
			if asksList[i].Price > asksList[j].Price {
				asksList[i], asksList[j] = asksList[j], asksList[i]
			}
		}
	}
	// 只保留前5档（最接近盘口的5档）
	if len(asksList) > orderbookDepth {
		asksList = asksList[:orderbookDepth]
	}

	// 更新全局订单薄（首次订阅时是快照，之后是增量更新）
	globalOrderbookMu.Lock()
	if len(bidsList) > 0 {
		globalOrderbook[tokenType] = bidsList
	}
	if len(asksList) > 0 {
		globalOrderbookAsks[tokenType] = asksList
	}
	globalOrderbookMu.Unlock()

	// 只写入日志文件，不输出到终端
	fileLogger.Printf("✅ 更新订单薄: token=%s bids=%d asks=%d", tokenType, len(bidsList), len(asksList))
}

// parseTokenIDs 解析 token IDs
func parseTokenIDs(clobTokenIDs string) (yesAssetID, noAssetID string) {
	var tokenArray []string
	if err := json.Unmarshal([]byte(clobTokenIDs), &tokenArray); err == nil {
		if len(tokenArray) >= 2 {
			return tokenArray[0], tokenArray[1]
		}
		return "", ""
	}

	re := regexp.MustCompile(`["'\[\]]`)
	cleaned := re.ReplaceAllString(clobTokenIDs, "")
	parts := regexp.MustCompile(`[,\-]\s*`).Split(cleaned, -1)
	if len(parts) >= 2 {
		yesAssetID = strings.TrimSpace(parts[0])
		noAssetID = strings.TrimSpace(parts[1])
		if yesAssetID != "" && noAssetID != "" {
			return yesAssetID, noAssetID
		}
	}

	return "", ""
}

func main() {
	// page 模式默认启用交易（网格开单）；如只想监控可加 -no-trade
	noTrade := flag.Bool("no-trade", false, "仅监控（不启动交易策略/不开单）")
	flag.Parse()
	enableTradingInPageMode = !*noTrade

	// 初始化文件日志记录器
	initFileLogger()

	// 重定向 logrus 输出到文件，避免干扰 TUI
	// MarketStream 和其他组件使用 logrus，需要重定向到文件
	logDir := "logs"
	if err := os.MkdirAll(logDir, 0755); err != nil {
		logDir = os.TempDir()
	}
	logFile := filepath.Join(logDir, "price-watcher-tui.log")
	file, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err == nil {
		// 设置 logrus 输出到文件（不输出到终端）
		logrus.SetOutput(file)
		logrus.SetLevel(logrus.InfoLevel)
		logrus.SetFormatter(&logrus.TextFormatter{
			FullTimestamp:   true,
			TimestampFormat: "2006-01-02 15:04:05",
			DisableColors:   true, // 禁用颜色，因为写入文件
		})
	}

	// 启用日志文件（用于调试）
	if len(os.Getenv("DEBUG")) > 0 {
		f, err := tea.LogToFile("debug.log", "debug")
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
	}

	p := tea.NewProgram(initialModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		log.Fatalf("运行程序失败: %v", err)
	}
}

// startTradingBotForPageMode 启动交易机器人（用于 page/TUI 模式）。
// 修复点：以前 price-watcher-tui 只连接 MarketStream 做展示，不会启动 TradingService/策略，因此 grid 在 page 模式下永远不会开单。
func startTradingBotForPageMode(
	ctx context.Context,
	cfg *config.Config,
	proxyURL string,
) (scheduler *bbgo.MarketScheduler, market *domain.Market, bestBook *marketstate.AtomicBestBook, cleanup func(), err error) {
	if cfg == nil {
		return nil, nil, nil, nil, fmt.Errorf("配置为空")
	}

	privateKey, err := signing.PrivateKeyFromHex(cfg.Wallet.PrivateKey)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("解析私钥失败: %w", err)
	}

	// 推导 API key（User WS 需要）
	tempClient := client.NewClient(
		"https://clob.polymarket.com",
		types.ChainPolygon,
		privateKey,
		nil,
	)
	creds, err := tempClient.CreateOrDeriveAPIKey(ctx, nil)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("推导 API 凭证失败: %w", err)
	}

	clobClient := client.NewClient(
		"https://clob.polymarket.com",
		types.ChainPolygon,
		privateKey,
		creds,
	)

	marketDataService := services.NewMarketDataService(clobClient)
	tradingService := services.NewTradingService(clobClient, cfg.DryRun)
	if cfg.Wallet.FunderAddress != "" {
		tradingService.SetFunderAddress(cfg.Wallet.FunderAddress, types.SignatureTypeGnosisSafe)
	}
	tradingService.SetOrderStatusSyncConfig(cfg.OrderStatusSyncIntervalWithOrders, cfg.OrderStatusSyncIntervalWithoutOrders)
	tradingService.SetMinOrderSize(cfg.MinOrderSize)
	tradingService.SetMinShareSize(cfg.MinShareSize)

	environ := bbgo.NewEnvironment()
	environ.SetMarketDataService(marketDataService)
	environ.SetTradingService(tradingService)
	environ.SetExecutor(bbgo.NewSerialCommandExecutor(2048))
	environ.SetConcurrentExecutor(bbgo.NewWorkerPoolCommandExecutor(2048, cfg.ConcurrentExecutorWorkers))
	if cfg.DirectModeDebounce > 0 {
		environ.SetDirectModeDebounce(cfg.DirectModeDebounce)
	}

	trader := bbgo.NewTrader(environ)
	loader := bbgo.NewStrategyLoader(tradingService)
	for _, mount := range cfg.ExchangeStrategies {
		shouldMount := false
		for _, on := range mount.On {
			if on == "polymarket" {
				shouldMount = true
				break
			}
		}
		if !shouldMount {
			continue
		}
		strategy, e := loader.LoadStrategy(ctx, mount.StrategyID, mount.Config)
		if e != nil {
			return nil, nil, nil, nil, fmt.Errorf("加载策略 %s 失败: %w", mount.StrategyID, e)
		}
		trader.AddStrategy(strategy)
	}

	if err := trader.InjectServices(ctx); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("注入服务失败: %w", err)
	}
	if err := trader.Initialize(ctx); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("初始化策略失败: %w", err)
	}

	userCreds := &websocket.UserCredentials{
		APIKey:     creds.Key,
		Secret:     creds.Secret,
		Passphrase: creds.Passphrase,
	}
	scheduler = bbgo.NewMarketScheduler(
		environ,
		marketDataService,
		"polymarket",
		proxyURL,
		userCreds,
	)

	// 架构层路由器：page 模式同样需要把 TradingService 的订单更新转发给 session，策略才能收到 OnOrderUpdate
	eventRouter := bbgo.NewSessionEventRouter()
	tradingService.OnOrderUpdate(eventRouter)

	// 启动市场调度器（创建初始 session）
	if err := scheduler.Start(ctx); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("启动市场调度器失败: %w", err)
	}
	session := scheduler.CurrentSession()
	market = scheduler.CurrentMarket()
	if session == nil || market == nil {
		return nil, nil, nil, nil, fmt.Errorf("无法获取当前会话或市场")
	}

	// 注入当前市场信息与 bestBook（执行层/策略 quote 依赖）
	tradingService.SetCurrentMarketInfo(market)
	tradingService.SetBestBook(session.BestBook())
	bestBook = session.BestBook()

	// 注册 session gate
	eventRouter.SetSession(session)
	if session.UserDataStream != nil {
		session.UserDataStream.OnOrderUpdate(eventRouter)
		session.UserDataStream.OnTradeUpdate(eventRouter)
	}
	session.OnTradeUpdate(tradingService)

	// 周期切换时更新路由与交易服务指向，并让 Trader 切换 session
	scheduler.OnSessionSwitch(func(_ *bbgo.ExchangeSession, newSession *bbgo.ExchangeSession, newMarket *domain.Market) {
		if newMarket != nil {
			tradingService.SetCurrentMarketInfo(newMarket)
		}
		if newSession != nil {
			tradingService.SetBestBook(newSession.BestBook())
			eventRouter.SetSession(newSession)
			if newSession.UserDataStream != nil {
				newSession.UserDataStream.OnOrderUpdate(eventRouter)
				newSession.UserDataStream.OnTradeUpdate(eventRouter)
			}
			newSession.OnTradeUpdate(tradingService)
			_ = trader.SwitchSession(ctx, newSession)
		} else {
			tradingService.SetBestBook(nil)
		}
	})

	// 启动环境 + 策略
	if err := environ.Start(ctx); err != nil {
		_ = scheduler.Stop(context.Background())
		return nil, nil, nil, nil, fmt.Errorf("启动环境失败: %w", err)
	}
	if err := trader.StartWithSession(ctx, session); err != nil {
		_ = scheduler.Stop(context.Background())
		return nil, nil, nil, nil, fmt.Errorf("启动策略失败: %w", err)
	}

	cleanup = func() {
		// 避免阻塞 UI：尽量快速 stop
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if scheduler != nil {
			_ = scheduler.Stop(stopCtx)
		}
		tradingService.Stop()
		_ = environ.Close()
	}

	return scheduler, market, bestBook, cleanup, nil
}
