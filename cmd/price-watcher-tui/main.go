package main

import (
	"context"
	"encoding/json"
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
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/infrastructure/websocket"
	"github.com/betbot/gobet/internal/marketstate"
	"github.com/betbot/gobet/internal/services"
	"github.com/betbot/gobet/pkg/config"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	gorillaWS "github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

const (
	orderbookDepth = 5 // 显示订单薄的深度（买五、卖五）
)

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
}

func initialModel() model {
	ctx, cancel := context.WithCancel(context.Background())
	// 初始化全局 context
	currentModelCtx = ctx
	return model{
		upBids:      make([]orderLevel, 0),
		upAsks:      make([]orderLevel, 0),
		downBids:    make([]orderLevel, 0),
		downAsks:    make([]orderLevel, 0),
		connected:   false,
		ctx:         ctx,
		cancel:      cancel,
		bestBook:    marketstate.NewAtomicBestBook(),
		btcPrice:    0,
		btcPriceStr: "N/A",
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(
		tickCmd(),
		connectCmd(),
	)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			if m.cancel != nil {
				m.cancel()
			}
			return m, tea.Quit
		}

	case tickMsg:
		// 检查周期切换
		currentTs := services.GetCurrent15MinTimestamp()
		currentSlug := services.Generate15MinSlug(currentTs)
		if m.currentCycle != currentSlug {
			return m, tea.Batch(
				tickCmd(),
				switchCycleCmd(currentSlug, time.Unix(currentTs, 0)),
			)
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
		// 周期切换时，先关闭旧的连接
		initFileLogger() // 确保日志已初始化
		if m.cancel != nil {
			fileLogger.Printf("周期切换：关闭旧连接 (旧周期: %s -> 新周期: %s)", m.currentCycle, msg.Cycle)
			m.cancel() // 取消旧的 context，这会停止旧的 goroutine
		}
		// 关闭旧的连接
		// 注意：MarketStream 和 RTDS Client 会通过 context 取消来关闭
		// context 取消后，相关的 goroutine 应该会自动退出
		// 创建新的 context
		ctx, cancel := context.WithCancel(context.Background())
		m.ctx = ctx
		m.cancel = cancel
		// 设置全局变量，供 connectCmd 使用
		currentModelCtx = ctx
		targetCycleSlug = msg.Cycle  // 设置目标周期
		targetCycleStart = msg.Start // 设置目标周期开始时间
		m.connected = false          // 标记为未连接，等待新连接
		m.currentCycle = msg.Cycle
		m.cycleStart = msg.Start
		return m, reconnectCmd()

	case connectedMsg:
		m.marketStream = msg.marketStream
		m.rtdsClient = msg.rtdsClient
		m.market = msg.market
		m.bestBook = msg.bestBook
		m.currentCycle = msg.cycle
		m.cycleStart = msg.start
		m.connected = true
		// 确保使用正确的 context
		if m.ctx == nil {
			ctx, cancel := context.WithCancel(context.Background())
			m.ctx = ctx
			m.cancel = cancel
			currentModelCtx = ctx // 同步到全局变量
		}
		// 清除目标周期信息（连接成功后不再需要）
		targetCycleSlug = ""
		targetCycleStart = time.Time{}
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

	// 显示卖单（asks）- 从高到低（倒序，价格高的在上）
	s.WriteString(askStyle.Render("卖单 (Asks)"))
	s.WriteString("\n")
	if len(asks) > 0 {
		// asks 已经按价格从高到低排序（倒序），直接显示前5档
		for i := 0; i < len(asks) && i < orderbookDepth; i++ {
			ask := asks[i]
			s.WriteString(fmt.Sprintf("  %6.2f  %8.2f\n", ask.Price*100, ask.Size))
		}
	} else {
		s.WriteString("  --\n")
	}

	s.WriteString("\n")

	// 显示中间价
	// 注意：asks 是从高到低排序（倒序），bids 是从高到低排序
	var midPrice float64
	if len(bids) > 0 && len(asks) > 0 {
		// 中间价 = (最高买价 + 最低卖价) / 2
		// asks 是从高到低排序（倒序），最低卖价在最后
		// bids 是从高到低排序，bids[0] 是最高买价（最接近盘口）
		lowestAsk := asks[len(asks)-1].Price // 最低卖价在最后
		highestBid := bids[0].Price           // 最高买价在最前
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

// connectCmd 需要访问 model 的 ctx 和周期信息，所以我们使用全局变量来传递
// 注意：这是一个临时解决方案，更好的方式是重构代码结构
var (
	currentModelCtx  context.Context
	targetCycleSlug  string
	targetCycleStart time.Time
)

func connectCmd() tea.Cmd {
	return func() tea.Msg {
		// 加载配置
		config.SetConfigPath("config.yaml")
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("加载配置失败: %w", err)
		}

		// 使用全局的 context（在 cycleChangeMsg 或 Init 中设置）
		ctx := currentModelCtx
		if ctx == nil {
			// 如果全局 context 为空，创建一个新的（首次连接时）
			ctx = context.Background()
		}

		// 获取目标周期的市场（使用全局变量中指定的周期，如果没有则使用当前周期）
		var currentTs int64
		var currentSlug string
		if targetCycleSlug != "" {
			// 使用周期切换时指定的周期
			currentSlug = targetCycleSlug
			currentTs = targetCycleStart.Unix()
			initFileLogger()
			fileLogger.Printf("连接指定周期: %s (时间戳: %d)", currentSlug, currentTs)
		} else {
			// 首次连接，使用当前周期
			currentTs = services.GetCurrent15MinTimestamp()
			currentSlug = services.Generate15MinSlug(currentTs)
			initFileLogger()
			fileLogger.Printf("首次连接当前周期: %s (时间戳: %d)", currentSlug, currentTs)
		}

		// 获取市场信息
		gammaMarket, err := client.FetchMarketFromGamma(ctx, currentSlug)
		if err != nil {
			return fmt.Errorf("获取市场信息失败: %w", err)
		}

		// 解析 token IDs
		yesAssetID, noAssetID := parseTokenIDs(gammaMarket.ClobTokenIDs)
		if yesAssetID == "" || noAssetID == "" {
			return fmt.Errorf("解析 token IDs 失败: %s", gammaMarket.ClobTokenIDs)
		}

		market := &domain.Market{
			Slug:        gammaMarket.Slug,
			ConditionID: gammaMarket.ConditionID,
			YesAssetID:  yesAssetID,
			NoAssetID:   noAssetID,
			Timestamp:   currentTs,
		}

		// 设置代理
		proxyURL := ""
		if cfg.Proxy != nil {
			proxyURL = fmt.Sprintf("http://%s:%d", cfg.Proxy.Host, cfg.Proxy.Port)
		}

		// 创建 MarketStream
		marketStream := websocket.NewMarketStream()
		marketStream.SetProxyURL(proxyURL)

		// 创建价格处理器（用于触发更新）
		handler := &priceHandler{}
		marketStream.OnPriceChanged(handler)

		// 连接市场数据流
		if err := marketStream.Connect(ctx, market); err != nil {
			return fmt.Errorf("连接市场数据流失败: %w", err)
		}

		// 使用 MarketStream 自带的 BestBook
		bestBook := marketStream.BestBook()
		if bestBook == nil {
			return fmt.Errorf("MarketStream BestBook 为空")
		}

		// 启动 goroutine 监听 book 消息以获取完整的订单薄数据（买五、卖五）
		go startBookListener(ctx, market, proxyURL)

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

func reconnectCmd() tea.Cmd {
	return connectCmd()
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
	// 使用 recover 捕获 panic，避免程序崩溃
	defer func() {
		if r := recover(); r != nil {
			fileLogger.Printf("订单薄监听器 panic 恢复: %v", r)
		}
	}()

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

	// 连接 WebSocket（带重试机制）
	var conn *gorillaWS.Conn
	maxRetries := 3
	for i := 0; i < maxRetries; i++ {
		var err error
		conn, _, err = dialer.Dial(wsURL, nil)
		if err == nil {
			break
		}
		fileLogger.Printf("连接订单薄 WebSocket 失败 (尝试 %d/%d): %v", i+1, maxRetries, err)
		if i < maxRetries-1 {
			time.Sleep(time.Duration(i+1) * time.Second)
		}
	}
	if conn == nil {
		fileLogger.Printf("连接订单薄 WebSocket 失败，已重试 %d 次", maxRetries)
		return
	}
	defer func() {
		if conn != nil {
			conn.Close()
		}
	}()

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
			// 检查连接状态
			if conn == nil {
				fileLogger.Printf("WebSocket 连接已关闭")
				return
			}

			// 使用 goroutine 安全地读取消息，避免 panic
			messageChan := make(chan []byte, 1)
			errChan := make(chan error, 1)
			go func() {
				defer func() {
					if r := recover(); r != nil {
						errChan <- fmt.Errorf("读取消息 panic: %v", r)
					}
				}()
				// 设置读取超时
				if err := conn.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
					errChan <- err
					return
				}
				_, msg, err := conn.ReadMessage()
				if err != nil {
					errChan <- err
					return
				}
				messageChan <- msg
			}()

			// 等待读取结果或超时
			var message []byte
			select {
			case <-ctx.Done():
				return
			case err := <-errChan:
				// 检查是否是超时错误
				if netErr, ok := err.(interface{ Timeout() bool }); ok && netErr.Timeout() {
					// 超时，继续循环
					continue
				}
				// 检查是否是连接关闭错误
				if gorillaWS.IsUnexpectedCloseError(err, gorillaWS.CloseGoingAway, gorillaWS.CloseAbnormalClosure) {
					fileLogger.Printf("WebSocket 连接意外关闭: %v", err)
				} else {
					fileLogger.Printf("读取订单薄消息失败: %v", err)
				}
				// 连接失败，退出 goroutine（不 panic）
				return
			case msg := <-messageChan:
				// 成功读取消息
				message = msg
			case <-time.After(35 * time.Second):
				// 读取超时
				continue
			}

			// 处理消息
			if len(message) == 0 {
				continue
			}

			// 处理 PING/PONG 消息
			if len(message) > 0 {
				msgStr := string(message)
				if msgStr == "PING" {
					if conn != nil {
						if err := conn.WriteMessage(gorillaWS.TextMessage, []byte("PONG")); err != nil {
							fileLogger.Printf("回复 PONG 失败: %v", err)
							return
						}
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

	// 解析所有卖单并排序（从高到低，倒序）
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
	// 对卖单按价格从高到低排序（倒序）
	for i := 0; i < len(asksList)-1; i++ {
		for j := i + 1; j < len(asksList); j++ {
			if asksList[i].Price < asksList[j].Price {
				asksList[i], asksList[j] = asksList[j], asksList[i]
			}
		}
	}
	// 只保留前5档
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
