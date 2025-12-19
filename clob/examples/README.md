# CLOB Client Go 示例

这个目录包含了使用 `gobet/clob` SDK 的示例代码，每个示例都可以独立运行和测试。

## 环境变量配置

所有示例都通过环境变量读取配置。常用的环境变量包括：

- `PRIVATE_KEY`: 你的私钥（十六进制字符串，不带 0x 前缀）
- `CHAIN_ID`: 链 ID（137 = Polygon 主网，80002 = Amoy 测试网）
- `CLOB_API_URL`: CLOB API 地址（默认: `https://clob.polymarket.com`）
- `API_KEY`: API 密钥（如果已创建过）
- `API_SECRET`: API 密钥的 Secret
- `API_PASSPHRASE`: API 密钥的 Passphrase

## 快速开始

### 测试示例

使用测试脚本快速验证示例是否能正常编译：

```bash
./test_example.sh create_api_key
```

或者直接编译和运行：

```bash
go run create_api_key.go
```

## 示例列表

### 1. create_api_key.go
创建或推导 API 密钥。

```bash
export PRIVATE_KEY="your_private_key_hex"
export CHAIN_ID=137
go run create_api_key.go
```

### 2. get_markets.go
获取市场列表。

```bash
export PRIVATE_KEY="your_private_key_hex"  # 可选
export MARKET_SLUG="btc-updown-15m-1765960200"  # 可选，指定市场
go run get_markets.go
```

### 3. fetch_gamma_market.go
从 Gamma API 获取市场数据（不需要认证）。

```bash
export MARKET_SLUG="btc-updown-15m-1765960200"
go run fetch_gamma_market.go
```

### 4. get_orderbook.go
获取订单簿。

```bash
export TOKEN_ID="your_token_id"
export SIDE="BUY"  # 可选
go run get_orderbook.go
```

### 5. get_price.go
获取市场价格。

```bash
export TOKEN_ID="your_token_id"
go run get_price.go
```

### 6. get_open_orders.go
获取开放订单（需要 API 凭证）。

```bash
export PRIVATE_KEY="your_private_key_hex"
export API_KEY="your_api_key"  # 可选
export API_SECRET="your_api_secret"  # 可选
export API_PASSPHRASE="your_api_passphrase"  # 可选
export MARKET_SLUG="btc-updown-15m-1765960200"  # 可选，过滤市场
export TOKEN_ID="token_id"  # 可选，过滤 token
go run get_open_orders.go
```

### 7. place_order.go
下单（需要 API 凭证）。

```bash
export PRIVATE_KEY="your_private_key_hex"
export TOKEN_ID="token_id"  # 条件代币资产 ID
export PRICE="0.65"  # 订单价格（小数）
export SIZE="1.0"  # 订单数量（条件代币数量）
export SIDE="BUY"  # BUY 或 SELL
export ORDER_TYPE="GTC"  # 可选，GTC/FOK/FAK，默认 GTC
export TICK_SIZE="0.001"  # 可选，价格精度，默认 0.001
export NEG_RISK="false"  # 可选，是否为负风险市场，默认 false
export API_KEY="your_api_key"  # 可选
export API_SECRET="your_api_secret"  # 可选
export API_PASSPHRASE="your_api_passphrase"  # 可选
go run place_order.go
```

### 8. place_order_auto.go
自动下单示例：获取当前市场信息 -> 订阅价格 -> 价格高于 62 cents 时自动下单 -> 监听订单成交状态。

**自动加载配置**：程序会自动尝试从以下位置加载 `user.json` 文件：
- `data/user.json`
- `../data/user.json`
- `../../data/user.json`
- `user.json`
- `../user.json`

如果找到 `user.json`，会自动加载其中的 `private_key`、`api_key`、`secret`、`passphrase` 等配置。

```bash
# 方式 1: 使用 user.json（推荐）
# 确保 data/user.json 存在并包含 private_key 和 API 凭证
export SIZE="1.0"  # 可选，订单数量，默认 1.0
go run place_order_auto.go

# 方式 2: 使用环境变量
export PRIVATE_KEY="your_private_key_hex"
export SIZE="1.0"  # 订单数量，默认 1.0
export ORDER_TYPE="GTC"  # 可选，GTC/FOK/FAK，默认 GTC
export API_KEY="your_api_key"  # 可选
export API_SECRET="your_api_secret"
export API_PASSPHRASE="your_api_passphrase"
go run place_order_auto.go
```

**功能说明**：
- 自动获取当前 15 分钟周期的市场信息
- 订阅市场价格变化（WebSocket）
- 当任意 token（YES 或 NO）的价格高于 0.62 时，自动买入该 token
- 监听用户订单 WebSocket，等待订单成交
- 订单成交后自动退出

### 9. cancel_order.go
取消订单（需要 API 凭证）。

```bash
export PRIVATE_KEY="your_private_key_hex"
export ORDER_ID="order_id_to_cancel"
export API_KEY="your_api_key"  # 可选
export API_SECRET="your_api_secret"  # 可选
export API_PASSPHRASE="your_api_passphrase"  # 可选
go run cancel_order.go
```

## 使用流程示例

### 步骤 1: 创建 API 密钥

```bash
export PRIVATE_KEY="your_private_key_hex"
export CHAIN_ID=137
go run create_api_key.go
```

保存输出的 API 凭证：
- `API_KEY`
- `API_SECRET`
- `API_PASSPHRASE`

### 步骤 2: 获取市场信息

```bash
export MARKET_SLUG="btc-updown-15m-1765960200"
go run fetch_gamma_market.go
```

从输出中获取 `clobTokenIds`（通常是逗号分隔的 YES 和 NO token IDs）。

### 步骤 3: 查看订单簿

```bash
export TOKEN_ID="yes_token_id"  # 从步骤 2 获取
go run get_orderbook.go
```

### 步骤 4: 查看开放订单

```bash
export PRIVATE_KEY="your_private_key_hex"
export API_KEY="your_api_key"
export API_SECRET="your_api_secret"
export API_PASSPHRASE="your_api_passphrase"
go run get_open_orders.go
```

### 步骤 5: 下单

```bash
export PRIVATE_KEY="your_private_key_hex"
export TOKEN_ID="yes_token_id"  # 从步骤 2 获取
export PRICE="0.65"  # 订单价格
export SIZE="1.0"  # 订单数量
export SIDE="BUY"  # BUY 或 SELL
export API_KEY="your_api_key"  # 从步骤 1 获取
export API_SECRET="your_api_secret"
export API_PASSPHRASE="your_api_passphrase"
go run place_order.go
```

### 步骤 6: 取消订单（如果需要）

```bash
export PRIVATE_KEY="your_private_key_hex"
export API_KEY="your_api_key"
export API_SECRET="your_api_secret"
export API_PASSPHRASE="your_api_passphrase"
export ORDER_ID="order_id_to_cancel"
go run cancel_order.go
```

## 注意事项

1. **私钥安全**: 永远不要将私钥提交到版本控制系统或公开分享。
2. **API 凭证**: API 凭证用于 L2 认证，应该妥善保管。
3. **测试网**: 在测试时可以使用 Amoy 测试网（`CHAIN_ID=80002`）。
4. **速率限制**: 注意 API 的速率限制，避免过于频繁的请求。
5. **错误处理**: 所有示例都包含基本的错误处理，但生产环境可能需要更完善的错误处理逻辑。

## 故障排除

### 错误: 解析私钥失败
- 确保私钥是有效的十六进制字符串（64 个字符）
- 确保没有包含 `0x` 前缀

### 错误: 创建 API 密钥失败
- 检查网络连接
- 确认 `CLOB_API_URL` 正确
- 确认 `CHAIN_ID` 正确

### 错误: 获取市场数据失败
- 确认市场 slug 存在且正确
- 检查网络连接

### 错误: 认证失败
- 确认 API 凭证正确
- 确认私钥与 API 密钥匹配
- 检查 `CHAIN_ID` 是否正确

