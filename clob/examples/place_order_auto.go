//go:build ignore
// +build ignore

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/betbot/gobet/clob/client"
	"github.com/betbot/gobet/clob/signing"
	"github.com/betbot/gobet/clob/types"
	"github.com/gorilla/websocket"
)

// UserJSON 用户配置文件结构
type UserJSON struct {
	PrivateKey       string `json:"private_key"`
	Proxy            string `json:"proxy"`
	Address          string `json:"address"`
	RecipientAddress string `json:"recipient_address"`
	ProxyAddress     string `json:"proxy_address"`
	APIKey           string `json:"api_key"`
	Secret           string `json:"secret"`
	Passphrase       string `json:"passphrase"`
}

// 示例：自动下单（获取市场信息 -> 订阅价格 -> 价格达到条件时下单 -> 监听订单状态）
// 使用方法：
//   export PRIVATE_KEY="your_private_key_hex"
//   export SIZE="1.0"  # 订单数量，默认 1.0
//   export ORDER_TYPE="GTC"  # 可选，GTC/FOK/FAK，默认 GTC
//   export TICK_SIZE="0.001"  # 可选，价格精度，默认 0.001
//   export API_KEY="your_api_key"  # 可选
//   export API_SECRET="your_api_secret"
//   export API_PASSPHRASE="your_api_passphrase"
//   export CHAIN_ID=137
//   export CLOB_API_URL="https://clob.polymarket.com"
//   go run place_order_auto.go

const (
	PriceThreshold = 0.62 // 价格阈值：62 cents
	MarketWSURL    = "wss://ws-subscriptions-clob.polymarket.com/ws/market"
	UserWSURL      = "wss://ws-subscriptions-clob.polymarket.com/ws/user"
)

// getCurrent15MinTimestamp 获取当前 15 分钟周期的时间戳
func getCurrent15MinTimestamp() int64 {
	now := time.Now()
	minutes := now.Minute()
	roundedMinutes := (minutes / 15) * 15

	periodStart := time.Date(now.Year(), now.Month(), now.Day(),
		now.Hour(), roundedMinutes, 0, 0, now.Location())

	return periodStart.Unix()
}

// generate15MinSlug 生成 15 分钟周期的 slug
func generate15MinSlug(timestamp int64) string {
	return fmt.Sprintf("btc-updown-15m-%d", timestamp)
}

// PriceChangeMessage 价格变化消息
type PriceChangeMessage struct {
	EventType    string        `json:"event_type"`
	Market       string        `json:"market"`
	PriceChanges []PriceChange `json:"price_changes"`
	Timestamp    string        `json:"timestamp"`
}

// PriceChange 价格变化
type PriceChange struct {
	AssetID string `json:"asset_id"`
	Price   string `json:"price"`
	Size    string `json:"size"`
	Side    string `json:"side"`
	BestBid string `json:"best_bid"`
	BestAsk string `json:"best_ask"`
}

// OrderMessage 订单消息
type OrderMessage struct {
	EventType    string `json:"event_type"`
	ID           string `json:"id"`
	AssetID      string `json:"asset_id"`
	Side         string `json:"side"`
	Price        string `json:"price"`
	OriginalSize string `json:"original_size"`
	SizeMatched  string `json:"size_matched"`
	Type         string `json:"type"` // PLACEMENT, UPDATE, CANCELLATION
	Status       string `json:"status"`
}

// loadUserJSON 加载 user.json 文件
func loadUserJSON() (*UserJSON, error) {
	// 尝试多个可能的路径（相对于当前工作目录）
	possiblePaths := []string{
		"data/user.json",
	}

	for _, path := range possiblePaths {
		if _, err := os.Stat(path); err == nil {
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}

			var userJSON UserJSON
			if err := json.Unmarshal(data, &userJSON); err != nil {
				continue
			}

			fmt.Printf("✅ 从 %s 加载用户配置\n", path)
			return &userJSON, nil
		}
	}

	return nil, fmt.Errorf("未找到 user.json 文件")
}

// getEnvOrUserJSON 优先从环境变量获取，否则从 user.json 获取
func getEnvOrUserJSON(envKey string, userJSON *UserJSON, userKey string, defaultValue string) string {
	if val := os.Getenv(envKey); val != "" {
		return val
	}
	if userJSON != nil {
		switch userKey {
		case "private_key":
			return userJSON.PrivateKey
		case "api_key":
			return userJSON.APIKey
		case "secret":
			return userJSON.Secret
		case "passphrase":
			return userJSON.Passphrase
		}
	}
	return defaultValue
}

func main() {
	// 尝试加载 user.json
	userJSON, err := loadUserJSON()
	if err != nil {
		fmt.Printf("提示: %v，将使用环境变量\n", err)
	}

	// 从环境变量或 user.json 读取配置
	privateKeyHex := getEnvOrUserJSON("PRIVATE_KEY", userJSON, "private_key", "")
	if privateKeyHex == "" {
		fmt.Fprintf(os.Stderr, "错误: 请设置 PRIVATE_KEY 环境变量或在 user.json 中配置 private_key\n")
		os.Exit(1)
	}

	chainIDStr := os.Getenv("CHAIN_ID")
	if chainIDStr == "" {
		chainIDStr = "137"
	}
	chainIDInt, err := strconv.Atoi(chainIDStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: CHAIN_ID 必须是数字: %v\n", err)
		os.Exit(1)
	}
	chainID := types.Chain(chainIDInt)

	host := os.Getenv("CLOB_API_URL")
	if host == "" {
		host = "https://clob.polymarket.com"
	}

	sizeStr := os.Getenv("SIZE")
	if sizeStr == "" {
		sizeStr = "3.0"
	}
	size, err := strconv.ParseFloat(sizeStr, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: SIZE 必须是数字: %v\n", err)
		os.Exit(1)
	}

	// 解析私钥
	privateKey, err := signing.PrivateKeyFromHex(privateKeyHex)
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: 解析私钥失败: %v\n", err)
		os.Exit(1)
	}

	// 获取地址
	address := signing.GetAddressFromPrivateKey(privateKey)
	fmt.Println("=" + strings.Repeat("=", 60))
	fmt.Println("📋 账户信息")
	fmt.Println("=" + strings.Repeat("=", 60))
	fmt.Printf("签名者地址 (Signer Address): %s\n", address.Hex())
	fmt.Printf("链 ID: %d (Polygon Mainnet)\n", chainID)
	fmt.Printf("API 地址: %s\n", host)
	fmt.Printf("价格阈值: %.2f (62 cents)\n", PriceThreshold)
	fmt.Printf("订单数量: %.2f\n", size)

	// 如果有 user.json，尝试读取 proxy_address
	if userJSON != nil && userJSON.ProxyAddress != "" {
		fmt.Printf("代理地址 (Proxy/Funder Address): %s\n", userJSON.ProxyAddress)
		fmt.Println("\n💡 提示：")
		fmt.Println("  - 如果是 BUY 订单，需要在代理地址中存入 USDC")
		fmt.Println("  - 如果是 SELL 订单，需要在代理地址中存入对应的 Token")
		fmt.Println("  - 首次交易需要在 Polymarket UI 中设置授权 (approval)")
		fmt.Println("  - 查看余额: https://polygonscan.com/address/" + userJSON.ProxyAddress)
	} else {
		fmt.Println("\n💡 提示：")
		fmt.Println("  - 如果是 BUY 订单，需要在签名者地址中存入 USDC")
		fmt.Println("  - 如果是 SELL 订单，需要在签名者地址中存入对应的 Token")
		fmt.Println("  - 首次交易需要在 Polymarket UI 中设置授权 (approval)")
		fmt.Println("  - 查看余额: https://polygonscan.com/address/" + address.Hex())
	}
	fmt.Println("=" + strings.Repeat("=", 60))
	fmt.Println()

	// 获取或创建 API 凭证（优先从 user.json，然后环境变量，最后创建）
	var creds *types.ApiKeyCreds
	apiKey := getEnvOrUserJSON("API_KEY", userJSON, "api_key", "")
	apiSecret := getEnvOrUserJSON("API_SECRET", userJSON, "secret", "")
	apiPassphrase := getEnvOrUserJSON("API_PASSPHRASE", userJSON, "passphrase", "")

	if apiKey != "" && apiSecret != "" && apiPassphrase != "" {
		creds = &types.ApiKeyCreds{
			Key:        apiKey,
			Secret:     apiSecret,
			Passphrase: apiPassphrase,
		}
		fmt.Println("✅ 使用现有的 API 凭证（从 user.json 或环境变量）")
	} else {
		tempClient := client.NewClient(host, chainID, privateKey, nil)
		ctx := context.Background()
		fmt.Println("正在创建或推导 API 密钥...")
		creds, err = tempClient.CreateOrDeriveAPIKey(ctx, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "错误: 创建 API 密钥失败: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("✅ API 密钥已创建")
		fmt.Println("\n提示: 可以将以下凭证保存到 data/user.json 中，下次运行时自动加载：")
		fmt.Printf("  \"api_key\": \"%s\",\n", creds.Key)
		fmt.Printf("  \"secret\": \"%s\",\n", creds.Secret)
		fmt.Printf("  \"passphrase\": \"%s\"\n", creds.Passphrase)
		fmt.Println()
	}

	// 创建客户端
	clobClient := client.NewClient(host, chainID, privateKey, creds)

	// 确定签名类型和资金地址
	// 如果使用代理钱包（proxy_address），需要使用 POLY_GNOSIS_SAFE 签名类型
	var signatureType types.SignatureType = types.SignatureTypeBrowser // 默认 Browser (0)
	var funderAddress string = ""

	if userJSON != nil && userJSON.ProxyAddress != "" {
		// 使用代理钱包时，使用 POLY_GNOSIS_SAFE 签名类型
		// 注意：根据官方文档，POLY_GNOSIS_SAFE = 2，但我们当前只有 Browser(0) 和 Magic(1)
		// 暂时使用 Browser，但设置 funderAddress
		signatureType = types.SignatureTypeGnosisSafe // GNOSIS_SAFE = 2，代理钱包必须使用此类型
		funderAddress = userJSON.ProxyAddress
		fmt.Printf("✅ 检测到代理地址，将使用代理钱包下单\n")
		fmt.Printf("   Maker 地址（资金地址）: %s\n", funderAddress)
		fmt.Printf("   Signer 地址（签名地址）: %s\n", address.Hex())
		fmt.Printf("   SignatureType: %d (GNOSIS_SAFE - 代理钱包)\n", signatureType)
	}

	// 获取当前市场信息
	ctx := context.Background()
	currentTs := getCurrent15MinTimestamp()
	slug := generate15MinSlug(currentTs)
	fmt.Printf("\n获取市场信息: %s\n", slug)

	market, err := clobClient.FetchMarketFromGamma(ctx, slug)
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: 获取市场信息失败: %v\n", err)
		os.Exit(1)
	}

	// 解析 token IDs（处理 JSON 数组格式）
	clobTokenIDs := market.ClobTokenIDs
	// 移除可能的 JSON 数组标记
	clobTokenIDs = strings.Trim(clobTokenIDs, "[]\"'")
	clobTokenIDs = strings.ReplaceAll(clobTokenIDs, "\"", "")
	clobTokenIDs = strings.ReplaceAll(clobTokenIDs, "'", "")

	tokenIDs := strings.Split(clobTokenIDs, ",")
	if len(tokenIDs) < 2 {
		fmt.Fprintf(os.Stderr, "错误: 无法解析 token IDs: %s\n", market.ClobTokenIDs)
		os.Exit(1)
	}
	yesTokenID := strings.TrimSpace(tokenIDs[0])
	noTokenID := strings.TrimSpace(tokenIDs[1])

	// 移除可能的引号
	yesTokenID = strings.Trim(yesTokenID, "\"'")
	noTokenID = strings.Trim(noTokenID, "\"'")

	fmt.Printf("✅ 市场信息获取成功\n")
	fmt.Printf("  Market: %s\n", market.Slug)
	fmt.Printf("  YES Token ID: %s\n", yesTokenID)
	fmt.Printf("  NO Token ID: %s\n", noTokenID)
	fmt.Println()

	// 状态管理
	var (
		orderPlaced bool
		orderID     string
		mu          sync.RWMutex
	)

	// 创建上下文和取消函数
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 设置信号处理
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\n收到中断信号，正在退出...")
		cancel()
	}()

	// 连接市场价格 WebSocket
	fmt.Println("正在连接市场价格 WebSocket...")
	marketConn, _, err := websocket.DefaultDialer.Dial(MarketWSURL, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: 连接市场价格 WebSocket 失败: %v\n", err)
		os.Exit(1)
	}
	defer marketConn.Close()
	fmt.Println("✅ 市场价格 WebSocket 已连接")

	// 订阅市场
	subscribeMsg := map[string]interface{}{
		"assets_ids": []string{yesTokenID, noTokenID},
		"type":       "market",
	}
	if err := marketConn.WriteJSON(subscribeMsg); err != nil {
		fmt.Fprintf(os.Stderr, "错误: 订阅市场失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✅ 已订阅市场价格")

	// 连接用户订单 WebSocket
	fmt.Println("正在连接用户订单 WebSocket...")
	userConn, _, err := websocket.DefaultDialer.Dial(UserWSURL, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: 连接用户订单 WebSocket 失败: %v\n", err)
		os.Exit(1)
	}
	defer userConn.Close()
	fmt.Println("✅ 用户订单 WebSocket 已连接")

	// 认证用户 WebSocket
	authMsg := map[string]interface{}{
		"auth": map[string]string{
			"apikey":     creds.Key,
			"secret":     creds.Secret,
			"passphrase": creds.Passphrase,
		},
		"type": "user",
	}
	if err := userConn.WriteJSON(authMsg); err != nil {
		fmt.Fprintf(os.Stderr, "错误: 认证用户 WebSocket 失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✅ 用户 WebSocket 已认证")
	fmt.Println()

	// 启动 PING 循环
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				marketConn.WriteMessage(websocket.TextMessage, []byte("PING"))
				userConn.WriteMessage(websocket.TextMessage, []byte("PING"))
			}
		}
	}()

	// 处理市场价格消息
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				_, message, err := marketConn.ReadMessage()
				if err != nil {
					if !strings.Contains(err.Error(), "use of closed network connection") {
						fmt.Printf("错误: 读取市场价格消息失败: %v\n", err)
					}
					return
				}

				// 处理 PONG
				if string(message) == "PONG" {
					fmt.Println("收到 PONG")
					continue
				}

				// 检查是否是数组格式（订单簿快照数组）
				var messages []map[string]interface{}
				if err := json.Unmarshal(message, &messages); err == nil && len(messages) > 0 {
					// 是数组格式，遍历处理每个消息
					for _, msg := range messages {
						eventTypeStr, _ := msg["event_type"].(string)
						if eventTypeStr == "book" {
							// 订单簿快照，可以忽略或处理
							fmt.Println("收到订单簿快照数组（初始快照）")
							continue
						}
						// 重新序列化为 JSON 进行后续处理
						msgBytes, _ := json.Marshal(msg)
						message = msgBytes
						break // 只处理第一个消息
					}
				}

				// 先解析消息类型
				var eventType struct {
					EventType string `json:"event_type"`
				}
				if err := json.Unmarshal(message, &eventType); err != nil {
					// 如果还是失败，可能是其他格式，记录但不中断
					fmt.Printf("警告: 解析消息类型失败: %v\n", err)
					msgPreview := string(message)
					if len(msgPreview) > 100 {
						msgPreview = msgPreview[:100] + "..."
					}
					fmt.Printf("原始消息前100字符: %s\n", msgPreview)
					continue
				}

				// 根据事件类型处理
				switch eventType.EventType {
				case "price_change":
					var msg PriceChangeMessage
					if err := json.Unmarshal(message, &msg); err != nil {
						fmt.Printf("警告: 解析价格变化消息失败: %v\n", err)
						continue
					}

					for _, change := range msg.PriceChanges {
						// 检查是否是我们要监控的 token
						if change.AssetID != yesTokenID && change.AssetID != noTokenID {
							continue
						}

						// 解析价格（使用 best_ask）
						askPrice, err := strconv.ParseFloat(change.BestAsk, 64)
						if err != nil {
							fmt.Printf("警告: 解析 ask 价格失败: %v\n", err)
							continue
						}

						// 解析 best_bid
						bidPrice, err := strconv.ParseFloat(change.BestBid, 64)
						if err != nil {
							bidPrice = 0
						}

						// 确定 token 类型
						tokenType := "YES"
						if change.AssetID == noTokenID {
							tokenType = "NO"
						}

						fmt.Printf("价格更新: %s Token, BestBid=%.4f, BestAsk=%.4f, Side=%s\n",
							tokenType, bidPrice, askPrice, change.Side)

						// 检查是否达到价格阈值且未下单
						mu.RLock()
						shouldPlaceOrder := !orderPlaced && askPrice > PriceThreshold
						mu.RUnlock()

						if shouldPlaceOrder {
							mu.Lock()
							if !orderPlaced {
								orderPlaced = true
								mu.Unlock()

								fmt.Printf("\n🎯 价格达到阈值 (%.4f > %.2f)，准备下单...\n", askPrice, PriceThreshold)
								fmt.Printf("  Token: %s\n", tokenType)

								// 确定订单方向（价格高于 62，买入该 token）
								side := types.SideBuy

								// 构建订单
								userOrder := &types.UserOrder{
									TokenID: change.AssetID,
									Price:   askPrice,
									Size:    size,
									Side:    side,
								}

								// 订单选项
								tickSize := types.TickSize0001
								negRisk := false
								options := &types.CreateOrderOptions{
									TickSize: tickSize,
									NegRisk:  &negRisk,
								}

								// 打印订单构建前的信息
								fmt.Println("\n" + strings.Repeat("=", 70))
								fmt.Println("📝 订单构建信息")
								fmt.Println(strings.Repeat("=", 70))
								fmt.Printf("用户订单 (UserOrder):\n")
								fmt.Printf("  TokenID: %s\n", userOrder.TokenID)
								fmt.Printf("  Price: %.6f\n", userOrder.Price)
								fmt.Printf("  Size: %.6f\n", userOrder.Size)
								fmt.Printf("  Side: %s\n", userOrder.Side)
								fmt.Printf("订单选项 (Options):\n")
								fmt.Printf("  TickSize: %s\n", options.TickSize)
								fmt.Printf("  NegRisk: %v\n", *options.NegRisk)
								fmt.Printf("地址配置:\n")
								fmt.Printf("  Maker (Funder): %s\n", funderAddress)
								if funderAddress == "" {
									fmt.Printf("    (使用 Signer 地址作为 Maker)\n")
								}
								fmt.Printf("  Signer: %s\n", address.Hex())
								fmt.Printf("  SignatureType: %d\n", signatureType)
								fmt.Println(strings.Repeat("=", 70))

								// 创建并提交订单（使用 funderAddress 和 signatureType）
								signedOrder, err := clobClient.CreateOrderWithFunder(ctx, userOrder, options, funderAddress, signatureType)
								if err != nil {
									fmt.Fprintf(os.Stderr, "\n❌ 错误: 创建订单失败: %v\n", err)
									mu.Lock()
									orderPlaced = false
									mu.Unlock()
									continue
								}

								// 打印签名后的订单信息
								fmt.Println("\n" + strings.Repeat("=", 70))
								fmt.Println("✅ 订单签名成功")
								fmt.Println(strings.Repeat("=", 70))
								fmt.Printf("签名订单 (SignedOrder):\n")
								fmt.Printf("  Salt: %d\n", signedOrder.Salt)
								fmt.Printf("  Maker: %s\n", signedOrder.Maker)
								fmt.Printf("  Signer: %s\n", signedOrder.Signer)
								fmt.Printf("  Taker: %s\n", signedOrder.Taker)
								fmt.Printf("  TokenID: %s\n", signedOrder.TokenID)
								fmt.Printf("  MakerAmount: %s (wei, USDC精度6位)\n", signedOrder.MakerAmount)
								fmt.Printf("  TakerAmount: %s (wei, Token精度)\n", signedOrder.TakerAmount)
								fmt.Printf("  Expiration: %s\n", signedOrder.Expiration)
								fmt.Printf("  Nonce: %s\n", signedOrder.Nonce)
								fmt.Printf("  FeeRateBps: %s\n", signedOrder.FeeRateBps)
								fmt.Printf("  Side: %s (%d)\n", signedOrder.Side, signedOrder.Side)
								fmt.Printf("  SignatureType: %d\n", signedOrder.SignatureType)
								fmt.Printf("  Signature: %s...%s\n", signedOrder.Signature[:20], signedOrder.Signature[len(signedOrder.Signature)-10:])

								// 计算并显示实际金额
								makerAmountBig := new(big.Int)
								makerAmountBig.SetString(signedOrder.MakerAmount, 10)
								takerAmountBig := new(big.Int)
								takerAmountBig.SetString(signedOrder.TakerAmount, 10)

								// USDC 精度为 6
								makerAmountDecimal := new(big.Float).Quo(new(big.Float).SetInt(makerAmountBig), big.NewFloat(1e6))
								// Token 精度通常也是 6（条件代币）
								takerAmountDecimal := new(big.Float).Quo(new(big.Float).SetInt(takerAmountBig), big.NewFloat(1e6))

								fmt.Printf("\n实际金额:\n")
								fmt.Printf("  MakerAmount (USDC): %s USDC\n", makerAmountDecimal.Text('f', 6))
								fmt.Printf("  TakerAmount (Token): %s Token\n", takerAmountDecimal.Text('f', 6))
								fmt.Printf("  订单价格: %.6f\n", askPrice)
								fmt.Printf("  订单数量: %.6f\n", size)
								fmt.Println(strings.Repeat("=", 70))

								orderType := types.OrderTypeGTC
								fmt.Printf("\n正在提交订单 (OrderType: %s)...\n", orderType)
								orderResp, err := clobClient.PostOrder(ctx, signedOrder, orderType, false)
								if err != nil {
									fmt.Fprintf(os.Stderr, "错误: 提交订单失败: %v\n", err)
									mu.Lock()
									orderPlaced = false
									mu.Unlock()
									continue
								}

								// 打印订单响应
								fmt.Println("\n" + strings.Repeat("=", 70))
								fmt.Println("📤 订单提交响应")
								fmt.Println(strings.Repeat("=", 70))
								fmt.Printf("  Success: %v\n", orderResp.Success)
								fmt.Printf("  OrderID: %s\n", orderResp.OrderID)
								if orderResp.ErrorMsg != "" {
									fmt.Printf("  ErrorMsg: %s\n", orderResp.ErrorMsg)
								}
								fmt.Println(strings.Repeat("=", 70))

								if !orderResp.Success {
									fmt.Fprintf(os.Stderr, "\n❌ 订单提交失败: %s\n", orderResp.ErrorMsg)
									if strings.Contains(orderResp.ErrorMsg, "balance") || strings.Contains(orderResp.ErrorMsg, "allowance") {
										fmt.Println("\n💡 解决方案：")
										if userJSON != nil && userJSON.ProxyAddress != "" {
											fmt.Printf("  1. 检查代理地址余额: https://polygonscan.com/address/%s\n", userJSON.ProxyAddress)
											fmt.Printf("  2. BUY 订单需要 USDC，SELL 订单需要对应的 Token\n")
											fmt.Printf("  3. 首次交易需要在 Polymarket UI 设置授权\n")
											fmt.Printf("  4. 代理地址: %s\n", userJSON.ProxyAddress)
										} else {
											fmt.Printf("  1. 检查账户余额: https://polygonscan.com/address/%s\n", address.Hex())
											fmt.Printf("  2. BUY 订单需要 USDC，SELL 订单需要对应的 Token\n")
											fmt.Printf("  3. 首次交易需要在 Polymarket UI 设置授权\n")
											fmt.Printf("  4. 签名者地址: %s\n", address.Hex())
										}
										fmt.Printf("  5. 订单详情: Token=%s, Price=%.4f, Size=%.2f, Side=%s\n\n",
											tokenType, askPrice, size, side)
									}
									mu.Lock()
									orderPlaced = false
									mu.Unlock()
									continue
								}

								mu.Lock()
								orderID = orderResp.OrderID
								mu.Unlock()

								fmt.Printf("✅ 订单提交成功！\n")
								fmt.Printf("  订单 ID: %s\n", orderResp.OrderID)
								fmt.Printf("  价格: %.4f\n", askPrice)
								fmt.Printf("  数量: %.2f\n", size)
								fmt.Printf("  方向: %s\n", side)
								fmt.Println("\n等待订单成交...")
							} else {
								mu.Unlock()
							}
						}
					}

				case "book":
					// 订单簿快照（可选处理）
					fmt.Println("收到订单簿快照消息（单个）")

				case "tick_size_change":
					// Tick size 变化（可选处理）
					fmt.Println("收到 tick size 变化消息")

				case "last_trade_price":
					// 最后交易价格（可选处理）
					fmt.Println("收到最后交易价格消息")

				default:
					fmt.Printf("收到未知消息类型: %s\n", eventType.EventType)
					fmt.Printf("原始消息: %s\n", string(message))
				}
			}
		}
	}()

	// 处理用户订单消息
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				_, message, err := userConn.ReadMessage()
				if err != nil {
					if !strings.Contains(err.Error(), "use of closed network connection") {
						fmt.Printf("错误: 读取用户订单消息失败: %v\n", err)
					}
					return
				}

				// 处理 PONG
				if string(message) == "PONG" {
					fmt.Println("[User WS] 收到 PONG")
					continue
				}

				// 调试：打印收到的原始消息
				fmt.Printf("[User WS] 收到消息: %s\n", string(message))

				// 解析订单消息
				var orderMsg OrderMessage
				if err := json.Unmarshal(message, &orderMsg); err != nil {
					fmt.Printf("[User WS] 警告: 解析订单消息失败: %v\n", err)
					fmt.Printf("[User WS] 原始消息: %s\n", string(message))
					continue
				}

				fmt.Printf("[User WS] 解析成功: EventType=%s, OrderID=%s, Type=%s\n",
					orderMsg.EventType, orderMsg.ID, orderMsg.Type)

				if orderMsg.EventType == "order" {
					mu.RLock()
					currentOrderID := orderID
					mu.RUnlock()

					fmt.Printf("[User WS] 订单消息: ID=%s, Type=%s, Status=%s\n",
						orderMsg.ID, orderMsg.Type, orderMsg.Status)

					if currentOrderID != "" && orderMsg.ID == currentOrderID {
						fmt.Printf("[User WS] ✅ 匹配到我们的订单: %s\n", currentOrderID)
						fmt.Printf("\n📦 收到订单消息:\n")
						fmt.Printf("  订单 ID: %s\n", orderMsg.ID)
						fmt.Printf("  类型: %s\n", orderMsg.Type)
						fmt.Printf("  状态: %s\n", orderMsg.Status)
						fmt.Printf("  价格: %s\n", orderMsg.Price)
						fmt.Printf("  原始数量: %s\n", orderMsg.OriginalSize)
						fmt.Printf("  已成交数量: %s\n", orderMsg.SizeMatched)

						// 检查订单是否已成交
						if orderMsg.Type == "UPDATE" {
							originalSize, _ := strconv.ParseFloat(orderMsg.OriginalSize, 64)
							sizeMatched, _ := strconv.ParseFloat(orderMsg.SizeMatched, 64)

							fmt.Printf("[User WS] 订单更新: SizeMatched=%.2f, OriginalSize=%.2f\n",
								sizeMatched, originalSize)

							if sizeMatched >= originalSize {
								fmt.Println("\n✅ 订单已完全成交！")
								fmt.Println("程序将在 3 秒后退出...")
								time.Sleep(3 * time.Second)
								cancel()
								return
							} else {
								fmt.Printf("[User WS] 订单部分成交: %.2f/%.2f\n", sizeMatched, originalSize)
							}
						} else if orderMsg.Type == "PLACEMENT" {
							fmt.Printf("[User WS] 订单已下单: %s\n", orderMsg.ID)
						} else if orderMsg.Type == "CANCELLATION" {
							fmt.Println("\n⚠️  订单已取消")
							cancel()
							return
						}
					} else if currentOrderID == "" {
						fmt.Printf("[User WS] 收到订单消息，但当前没有待监控的订单\n")
					} else {
						fmt.Printf("[User WS] 收到其他订单的消息: %s (我们的订单: %s)\n",
							orderMsg.ID, currentOrderID)
					}
				} else if orderMsg.EventType == "trade" {
					fmt.Printf("[User WS] 收到交易消息: %s\n", string(message))
				}
			}
		}
	}()

	// 等待完成或中断
	fmt.Println("开始监听价格变化...")
	fmt.Println("按 Ctrl+C 退出\n")

	<-ctx.Done()
	fmt.Println("\n程序已退出")
}
