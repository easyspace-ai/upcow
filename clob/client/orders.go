package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/betbot/gobet/clob/signing"
	"github.com/betbot/gobet/clob/types"
)

// PostOrder 提交订单
func (c *Client) PostOrder(ctx context.Context, order *types.SignedOrder, orderType types.OrderType, deferExec bool) (*types.OrderResponse, error) {
	if err := c.CanL2Auth(); err != nil {
		return nil, err
	}

	// 速率限制：等待直到允许请求
	if err := c.rateLimiter.Wait(ctx, "clob:order:post"); err != nil {
		return nil, fmt.Errorf("速率限制等待失败: %w", err)
	}

	// 构建订单载荷
	// 根据官方 API，格式应该是 NewOrder，包含 order、owner、orderType、deferExec
	// order 字段需要是完整的 SignedOrder 对象
	orderPayload := types.NewOrder{
		Order:     *order,
		Owner:     c.authConfig.Creds.Key,
		OrderType: orderType,
		DeferExec: deferExec,
	}

	// 构建 L2 认证头
	bodyBytes, err := json.Marshal(orderPayload)
	if err != nil {
		return nil, fmt.Errorf("序列化订单载荷失败: %w", err)
	}
	bodyStr := string(bodyBytes)

	// 调试：打印订单载荷
	fmt.Printf("[DEBUG] Order Payload JSON: %s\n", bodyStr)
	l2HeaderArgs := &types.L2HeaderArgs{
		Method:      "POST",
		RequestPath: EndpointPostOrder,
		Body:        &bodyStr,
	}

	headers, err := signing.CreateL2Headers(
		c.authConfig.PrivateKey,
		c.authConfig.Creds,
		l2HeaderArgs,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("创建 L2 认证头失败: %w", err)
	}

	// 转换为 map
	headerMap := map[string]string{
		"POLY_ADDRESS":    headers.PolyAddress,
		"POLY_SIGNATURE":  headers.PolySignature,
		"POLY_TIMESTAMP":  headers.PolyTimestamp,
		"POLY_API_KEY":    headers.PolyAPIKey,
		"POLY_PASSPHRASE": headers.PolyPassphrase,
	}

	// 发送请求（使用 NewOrder 结构体）
	resp, err := c.httpClient.post(EndpointPostOrder, headerMap, orderPayload)
	if err != nil {
		return nil, fmt.Errorf("提交订单失败: %w", err)
	}

	// 记录响应状态
	fmt.Printf("[HTTP DEBUG] Response Status: %s\n", resp.Status)
	fmt.Printf("[HTTP DEBUG] Response StatusCode: %d\n", resp.StatusCode)

	var orderResp types.OrderResponse
	if err := parseResponse(resp, &orderResp); err != nil {
		fmt.Printf("[HTTP DEBUG] 解析响应失败: %v\n", err)
		return nil, fmt.Errorf("解析订单响应失败: %w", err)
	}

	// 记录响应内容
	respBytes, _ := json.Marshal(orderResp)
	fmt.Printf("[HTTP DEBUG] Order Response: %s\n", string(respBytes))

	return &orderResp, nil
}

// CancelOrder 取消订单
func (c *Client) CancelOrder(ctx context.Context, orderID string) (*types.OrderResponse, error) {
	if err := c.CanL2Auth(); err != nil {
		return nil, err
	}

	// 速率限制：等待直到允许请求
	if err := c.rateLimiter.Wait(ctx, "clob:order:delete"); err != nil {
		return nil, fmt.Errorf("速率限制等待失败: %w", err)
	}

	// 构建请求参数
	params := map[string]string{
		"orderID": orderID,
	}

	// 构建 L2 认证头
	l2HeaderArgs := &types.L2HeaderArgs{
		Method:      "DELETE",
		RequestPath: EndpointCancelOrder,
		Body:        nil,
	}

	headers, err := signing.CreateL2Headers(
		c.authConfig.PrivateKey,
		c.authConfig.Creds,
		l2HeaderArgs,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("创建 L2 认证头失败: %w", err)
	}

	// 转换为 map
	headerMap := map[string]string{
		"POLY_ADDRESS":    headers.PolyAddress,
		"POLY_SIGNATURE":  headers.PolySignature,
		"POLY_TIMESTAMP":  headers.PolyTimestamp,
		"POLY_API_KEY":    headers.PolyAPIKey,
		"POLY_PASSPHRASE": headers.PolyPassphrase,
	}

	// 【修复】添加详细的日志记录
	if httpDebug {
		fmt.Printf("[HTTP DEBUG] CancelOrder: orderID=%s endpoint=%s\n", orderID, EndpointCancelOrder)
	}

	// 发送请求
	resp, err := c.httpClient.delete(EndpointCancelOrder, headerMap, params)
	if err != nil {
		return nil, fmt.Errorf("取消订单失败: %w", err)
	}

	// 【修复】检查响应状态码
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		// 读取响应体以获取详细错误信息
		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		bodyStr := string(bodyBytes)
		
		// 如果响应是 JSON，尝试解析错误信息
		var errResp struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(bodyBytes, &errResp) == nil && errResp.Error != "" {
			return nil, fmt.Errorf("HTTP 错误 %d: %s (orderID=%s)", resp.StatusCode, errResp.Error, orderID)
		}
		
		return nil, fmt.Errorf("HTTP 错误 %d: %s (orderID=%s)", resp.StatusCode, bodyStr, orderID)
	}

	var orderResp types.OrderResponse
	if err := parseResponse(resp, &orderResp); err != nil {
		return nil, fmt.Errorf("解析订单响应失败: %w (orderID=%s)", err, orderID)
	}

	return &orderResp, nil
}

// GetOpenOrders 获取开放订单
func (c *Client) GetOpenOrders(ctx context.Context, params *types.OpenOrderParams) (types.OpenOrdersResponse, error) {
	// 速率限制：等待直到允许请求
	if err := c.rateLimiter.Wait(ctx, "clob:orders:get"); err != nil {
		return nil, fmt.Errorf("速率限制等待失败: %w", err)
	}
	if err := c.CanL2Auth(); err != nil {
		return nil, err
	}

	// 构建查询参数
	queryParams := make(map[string]string)
	if params != nil {
		if params.ID != nil {
			queryParams["id"] = *params.ID
		}
		if params.Market != nil {
			queryParams["market"] = *params.Market
		}
		if params.AssetID != nil {
			queryParams["asset_id"] = *params.AssetID
		}
	}

	// 构建 L2 认证头
	l2HeaderArgs := &types.L2HeaderArgs{
		Method:      "GET",
		RequestPath: EndpointGetOpenOrders,
		Body:        nil,
	}

	headers, err := signing.CreateL2Headers(
		c.authConfig.PrivateKey,
		c.authConfig.Creds,
		l2HeaderArgs,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("创建 L2 认证头失败: %w", err)
	}

	// 转换为 map
	headerMap := map[string]string{
		"POLY_ADDRESS":    headers.PolyAddress,
		"POLY_SIGNATURE":  headers.PolySignature,
		"POLY_TIMESTAMP":  headers.PolyTimestamp,
		"POLY_API_KEY":    headers.PolyAPIKey,
		"POLY_PASSPHRASE": headers.PolyPassphrase,
	}

	// 发送请求
	resp, err := c.httpClient.get(EndpointGetOpenOrders, headerMap, queryParams)
	if err != nil {
		return nil, fmt.Errorf("获取开放订单失败: %w", err)
	}

	var apiResp types.OpenOrdersAPIResponse
	if err := parseResponse(resp, &apiResp); err != nil {
		return nil, err
	}

	// 转换为 OpenOrdersResponse（数组类型）
	orders := types.OpenOrdersResponse(apiResp.Data)
	return orders, nil
}

// GetOrder 获取订单详情
func (c *Client) GetOrder(ctx context.Context, orderID string) (*types.OpenOrder, error) {
	if err := c.CanL2Auth(); err != nil {
		return nil, err
	}

	endpoint := EndpointGetOrder + orderID

	// 构建 L2 认证头
	l2HeaderArgs := &types.L2HeaderArgs{
		Method:      "GET",
		RequestPath: endpoint,
		Body:        nil,
	}

	headers, err := signing.CreateL2Headers(
		c.authConfig.PrivateKey,
		c.authConfig.Creds,
		l2HeaderArgs,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("创建 L2 认证头失败: %w", err)
	}

	// 转换为 map
	headerMap := map[string]string{
		"POLY_ADDRESS":    headers.PolyAddress,
		"POLY_SIGNATURE":  headers.PolySignature,
		"POLY_TIMESTAMP":  headers.PolyTimestamp,
		"POLY_API_KEY":    headers.PolyAPIKey,
		"POLY_PASSPHRASE": headers.PolyPassphrase,
	}

	// 发送请求
	resp, err := c.httpClient.get(endpoint, headerMap, nil)
	if err != nil {
		return nil, fmt.Errorf("获取订单详情失败: %w", err)
	}

	var order types.OpenOrder
	if err := parseResponse(resp, &order); err != nil {
		return nil, err
	}

	return &order, nil
}

// CreateOrder 创建签名订单
func (c *Client) CreateOrder(ctx context.Context, req *types.UserOrder, options *types.CreateOrderOptions) (*types.SignedOrder, error) {
	return c.CreateOrderWithFunder(ctx, req, options, "", types.SignatureTypeBrowser)
}

// CreateOrderWithFunder 创建签名订单（支持指定 funderAddress 和 signatureType）
func (c *Client) CreateOrderWithFunder(ctx context.Context, req *types.UserOrder, options *types.CreateOrderOptions, funderAddress string, signatureType types.SignatureType) (*types.SignedOrder, error) {
	if c.authConfig.PrivateKey == nil {
		return nil, fmt.Errorf("私钥未设置，无法创建订单")
	}

	// 创建订单构建器
	builder := NewOrderBuilder(c, signatureType, funderAddress)

	// 构建并签名订单
	return builder.BuildOrder(ctx, req, options)
}

// PlaceLimitOrder 下限价单（GTC - Good-Til-Cancelled）
// 订单会留在订单簿中直到成交或手动取消
//
// 参数:
//   - tokenID: Token ID
//   - side: 买入或卖出
//   - size: Token 数量
//   - price: 限价
//   - options: 订单选项（包含 tickSize 和 negRisk）
func (c *Client) PlaceLimitOrder(ctx context.Context, tokenID string, side types.Side, size float64, price float64, options *types.CreateOrderOptions) (*types.OrderResponse, error) {
	if err := c.CanL2Auth(); err != nil {
		return nil, err
	}

	// 创建用户订单
	userOrder := &types.UserOrder{
		TokenID: tokenID,
		Side:    side,
		Size:    size,
		Price:   price,
	}

	// 构建并签名订单
	signedOrder, err := c.CreateOrder(ctx, userOrder, options)
	if err != nil {
		return nil, fmt.Errorf("创建订单失败: %w", err)
	}

	// 提交 GTC 订单
	return c.PostOrder(ctx, signedOrder, types.OrderTypeGTC, false)
}

// PlaceOrderFOK 下 FOK 订单（Fill-Or-Kill）
// 必须全部成交，否则完全取消
//
// 注意: FOK 订单有严格的精度要求：
//   - Price: 2位小数（tick size 0.01）
//   - Size: 4位小数
//   - Maker amount (USDC for buy): 2位小数
func (c *Client) PlaceOrderFOK(ctx context.Context, tokenID string, side types.Side, size float64, price float64, options *types.CreateOrderOptions) (*types.OrderResponse, error) {
	if err := c.CanL2Auth(); err != nil {
		return nil, err
	}

	// 验证 FOK 精度要求
	if err := ValidateFOKPrecision(size, price, side); err != nil {
		return nil, fmt.Errorf("FOK 订单精度验证失败: %w", err)
	}

	// 创建用户订单
	userOrder := &types.UserOrder{
		TokenID: tokenID,
		Side:    side,
		Size:    size,
		Price:   price,
	}

	// 构建并签名订单（使用 FOK 精度）
	signedOrder, err := c.CreateOrderFOK(ctx, userOrder, options)
	if err != nil {
		return nil, fmt.Errorf("创建 FOK 订单失败: %w", err)
	}

	// 提交 FOK 订单
	return c.PostOrder(ctx, signedOrder, types.OrderTypeFOK, false)
}

// PlaceOrderFAK 下 FAK 订单（Fill-And-Kill）
// 尽可能成交，剩余部分自动取消（最适合复制交易）
//
// 注意: FAK 订单精度要求与 FOK 相同
func (c *Client) PlaceOrderFAK(ctx context.Context, tokenID string, side types.Side, size float64, price float64, options *types.CreateOrderOptions) (*types.OrderResponse, error) {
	return c.PlaceOrderFAKWithFunder(ctx, tokenID, side, size, price, options, "", types.SignatureTypeBrowser)
}

// PlaceOrderFAKWithFunder 下 FAK 订单（支持 funderAddress）
func (c *Client) PlaceOrderFAKWithFunder(ctx context.Context, tokenID string, side types.Side, size float64, price float64, options *types.CreateOrderOptions, funderAddress string, signatureType types.SignatureType) (*types.OrderResponse, error) {
	if err := c.CanL2Auth(); err != nil {
		return nil, err
	}

	// 验证 FAK 精度要求
	if err := ValidateFOKPrecision(size, price, side); err != nil {
		return nil, fmt.Errorf("FAK 订单精度验证失败: %w", err)
	}

	// 创建用户订单
	userOrder := &types.UserOrder{
		TokenID: tokenID,
		Side:    side,
		Size:    size,
		Price:   price,
	}

	// 构建并签名订单（使用 FOK 精度，支持 funderAddress）
	var signedOrder *types.SignedOrder
	var err error
	if funderAddress != "" {
		signedOrder, err = c.CreateOrderFOKWithFunder(ctx, userOrder, options, funderAddress, signatureType)
	} else {
		signedOrder, err = c.CreateOrderFOK(ctx, userOrder, options)
	}
	if err != nil {
		return nil, fmt.Errorf("创建 FAK 订单失败: %w", err)
	}

	// 提交 FAK 订单
	return c.PostOrder(ctx, signedOrder, types.OrderTypeFAK, false)
}

// PlaceMarketOrder 下市价单
// 通过获取订单簿计算最优价格，然后使用 FAK 下单
//
// 参数:
//   - tokenID: Token ID
//   - side: 买入或卖出
//   - amountUSDC: USDC 金额（买入时）或 token 数量（卖出时）
//   - options: 订单选项
func (c *Client) PlaceMarketOrder(ctx context.Context, tokenID string, side types.Side, amountUSDC float64, options *types.CreateOrderOptions) (*types.OrderResponse, error) {
	if err := c.CanL2Auth(); err != nil {
		return nil, err
	}

	// 获取订单簿
	book, err := c.GetOrderBook(ctx, tokenID, nil)
	if err != nil {
		return nil, fmt.Errorf("获取订单簿失败: %w", err)
	}

	// 计算最优成交价格和数量
	totalSize, avgPrice, _ := CalculateOptimalFill(book, side, amountUSDC)
	if totalSize == 0 {
		return nil, fmt.Errorf("订单簿流动性不足，无法成交")
	}

	// 使用 FAK 下单（允许部分成交）
	return c.PlaceOrderFAK(ctx, tokenID, side, totalSize, avgPrice, options)
}

// CreateOrderFOK 创建符合 FOK/FAK 精度要求的订单
// FOK/FAK 要求：
//   - Price: 2位小数（tick size 0.01）
//   - Size: 4位小数
//   - Maker amount (USDC for buy): 2位小数
//   - Taker amount (tokens): 4位小数
func (c *Client) CreateOrderFOK(ctx context.Context, req *types.UserOrder, options *types.CreateOrderOptions) (*types.SignedOrder, error) {
	if c.authConfig.PrivateKey == nil {
		return nil, fmt.Errorf("私钥未设置，无法创建订单")
	}

	// 确保价格是 2 位小数（tick size 0.01）
	price := float64(int(req.Price*100+0.5)) / 100

	// 确保数量是 4 位小数
	size := float64(int(req.Size*10000+0.5)) / 10000

	// 计算 USDC 金额并确保是 2 位小数
	usdcValue := size * price
	usdcValue = float64(int(usdcValue*100+0.5)) / 100

	// 如果买入订单，确保最小 $1 USDC
	const minOrderUSDC = 1.0
	if req.Side == types.SideBuy && usdcValue < minOrderUSDC && price > 0 {
		usdcValue = minOrderUSDC
		// 重新计算 size
		size = usdcValue / price
		size = float64(int(size*10000+0.5)) / 10000
	}

	// 确保最小 token 数量
	const minTokenSize = 0.1
	if size < minTokenSize {
		size = minTokenSize
		usdcValue = size * price
		usdcValue = float64(int(usdcValue*100+0.5)) / 100
	}

	// 创建修改后的用户订单
	userOrder := &types.UserOrder{
		TokenID:    req.TokenID,
		Side:       req.Side,
		Size:       size,
		Price:      price,
		FeeRateBps: req.FeeRateBps,
		Nonce:      req.Nonce,
		Expiration: req.Expiration,
		Taker:      req.Taker,
	}

	// 使用标准订单构建器（但精度已经满足 FOK 要求）
	// 使用默认的 funderAddress 和 signatureType（可以通过 CreateOrderWithFunder 覆盖）
	return c.CreateOrder(ctx, userOrder, options)
}

// CreateOrderFOKWithFunder 创建符合 FOK/FAK 精度要求的订单（支持 funderAddress）
// FOK/FAK 要求：
//   - Price: 2位小数（tick size 0.01）
//   - Size: 4位小数
//   - Maker amount (USDC for buy): 2位小数
//   - Taker amount (tokens): 4位小数
func (c *Client) CreateOrderFOKWithFunder(ctx context.Context, req *types.UserOrder, options *types.CreateOrderOptions, funderAddress string, signatureType types.SignatureType) (*types.SignedOrder, error) {
	if c.authConfig.PrivateKey == nil {
		return nil, fmt.Errorf("私钥未设置，无法创建订单")
	}

	// 确保价格是 2 位小数（tick size 0.01）
	price := float64(int(req.Price*100+0.5)) / 100

	// 确保数量是 4 位小数
	size := float64(int(req.Size*10000+0.5)) / 10000

	// 计算 USDC 金额并确保是 2 位小数
	usdcValue := size * price
	usdcValue = float64(int(usdcValue*100+0.5)) / 100

	// 如果买入订单，确保最小 $1 USDC
	const minOrderUSDC = 1.0
	if req.Side == types.SideBuy && usdcValue < minOrderUSDC && price > 0 {
		usdcValue = minOrderUSDC
		// 重新计算 size
		size = usdcValue / price
		size = float64(int(size*10000+0.5)) / 10000
	}

	// 确保最小 token 数量
	const minTokenSize = 0.1
	if size < minTokenSize {
		size = minTokenSize
		usdcValue = size * price
		usdcValue = float64(int(usdcValue*100+0.5)) / 100
	}

	// 创建修改后的用户订单
	userOrder := &types.UserOrder{
		TokenID:    req.TokenID,
		Side:       req.Side,
		Size:       size,
		Price:      price,
		FeeRateBps: req.FeeRateBps,
		Nonce:      req.Nonce,
		Expiration: req.Expiration,
		Taker:      req.Taker,
	}

	// 使用支持 funderAddress 的订单构建器
	return c.CreateOrderWithFunder(ctx, userOrder, options, funderAddress, signatureType)
}
