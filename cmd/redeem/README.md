# Redeem - 自动赎回工具

自动检测并赎回已解决的 Polymarket 持仓（通过 Relayer API，gasless）。

## 功能特性

- ✅ 自动检测已解决的持仓（curPrice = 0 或 1）
- ✅ 通过 Relayer API 执行 gasless 赎回
- ✅ 定时任务：启动时立即运行，之后每 3 分钟运行一次
- ✅ 支持代理配置（解决连接重置错误）

## 使用方法

### 配置

1. **创建 `user.json` 文件**（在 `example/redeem/` 目录下）：
```json
{
  "private_key": "your-private-key",
  "proxy_address": "your-proxy-address"
}
```

2. **配置 Builder API 凭证**（环境变量或 `.env` 文件）：
```bash
BUILDER_API_KEY=your-api-key
BUILDER_SECRET=your-secret
BUILDER_PASS_PHRASE=your-passphrase
```

3. **（可选）配置代理**（如果遇到连接重置错误）：
```bash
export HTTPS_PROXY=http://proxy.example.com:8080
```

### 运行

```bash
cd example/redeem
go run main.go
```

## 代理配置

如果遇到 `connection reset by peer` 错误，**强烈建议配置 HTTP 代理**：

```bash
# 设置代理环境变量
export HTTPS_PROXY=http://proxy.example.com:8080
export HTTP_PROXY=http://proxy.example.com:8080

# 运行程序
go run main.go
```

程序会自动检测代理配置并显示：
```
[Redeem] Proxy configuration detected: http://proxy.example.com:8080
```

如果没有配置代理：
```
[Redeem] No proxy configured - using direct connection
[Redeem] If you encounter connection reset errors, consider setting HTTP_PROXY or HTTPS_PROXY environment variable
```

## 常见问题

### 连接重置错误

**错误信息**：
```
read tcp ...: read: connection reset by peer
```

**解决方案**：
1. **配置代理**（最有效）：
   ```bash
   export HTTPS_PROXY=http://your-proxy:port
   ```

2. **检查网络连接**：
   - 确保网络稳定
   - 检查防火墙设置

3. **使用 VPN 或代理服务**：
   - 某些地区可能需要代理才能稳定访问

### 其他错误

- **API 凭证错误**：确保 Builder API 凭证正确
- **Safe 未部署**：程序会显示警告，但不影响运行
- **配额限制**：Relayer API 有每日配额限制

## 工作原理

1. **检测持仓**：每 3 分钟检查一次持仓
2. **筛选可赎回**：找出 curPrice = 0 或 1 的持仓
3. **执行赎回**：通过 Relayer API 提交赎回交易（gasless）
4. **跟踪状态**：避免重复提交已处理的赎回

## 日志输出

```
[Redeem] Starting auto-redeem worker...
[Redeem] Loaded user config from ./user.json
[Redeem] Auto-redeemer started - runs immediately on startup and then every 3 minutes
[AutoRedeemer] 🚀 Initial redemption run starting...
[AutoRedeemer] Found 3 redeemable positions to submit
[AutoRedeemer] ✅ Redemption submitted via Relayer: txID=... hash=...
```

## 停止程序

按 `Ctrl+C` 优雅停止程序。

