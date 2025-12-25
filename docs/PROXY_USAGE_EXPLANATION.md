# REST API 调用是否使用代理？

## ✅ 答案：**是的，默认使用代理**

## 🔍 代理配置流程

### 1. 代理配置来源（优先级从高到低）

```
配置文件 (config.yaml) 
  ↓
环境变量 (HTTP_PROXY, HTTPS_PROXY, http_proxy, https_proxy)
  ↓
默认值 (http://127.0.0.1:15236)
```

### 2. 代码实现

#### 步骤1: 配置文件加载时设置环境变量

```go
// pkg/config/config.go:504-510
if proxyConfig != nil {
    proxyURL := fmt.Sprintf("http://%s:%d", proxyConfig.Host, proxyConfig.Port)
    os.Setenv("HTTP_PROXY", proxyURL)
    os.Setenv("HTTPS_PROXY", proxyURL)
    os.Setenv("http_proxy", proxyURL)
    os.Setenv("https_proxy", proxyURL)
}
```

#### 步骤2: CLOB 客户端创建时读取环境变量

```go
// clob/client/client.go:69-80
func getProxyURL() string {
    // 检查常见的代理环境变量
    proxyVars := []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"}
    for _, v := range proxyVars {
        if val := os.Getenv(v); val != "" {
            return val  // ✅ 找到环境变量，使用它
        }
    }
    // 默认使用代理
    return "http://127.0.0.1:15236"  // ✅ 默认代理
}
```

#### 步骤3: HTTP 客户端配置代理

```go
// clob/client/http.go:29-45
func newHTTPClient(host string, authConfig *AuthConfig, useProxy bool, proxyURL *url.URL) *httpClient {
    transport := &http.Transport{
        MaxIdleConns:        100,
        IdleConnTimeout:     90 * time.Second,
        TLSHandshakeTimeout: 10 * time.Second,
    }

    // 默认使用代理
    if useProxy && proxyURL != nil {
        transport.Proxy = http.ProxyURL(proxyURL)  // ✅ 设置代理
    } else if useProxy {
        // 如果 useProxy 为 true 但 proxyURL 为 nil，尝试使用默认代理
        if defaultProxy, err := url.Parse("http://127.0.0.1:15236"); err == nil {
            transport.Proxy = http.ProxyURL(defaultProxy)  // ✅ 使用默认代理
        }
    }

    client := &http.Client{
        Transport: transport,
        Timeout:   30 * time.Second,
    }
    // ...
}
```

## 📊 代理使用情况

### GetTopOfBook 调用 REST API 时

```go
// GetTopOfBook 调用 REST API
yesBook, restErr = s.clobClient.GetOrderBook(ctx, market.YesAssetID, nil)
noBook, restErr = s.clobClient.GetOrderBook(ctx, market.NoAssetID, nil)

// ↓ 这些调用都会经过 HTTP 客户端
// ↓ HTTP 客户端已经配置了代理
// ↓ 所以 REST API 调用会使用代理
```

### 代理配置位置

1. **配置文件** (`config.yaml`):
   ```yaml
   proxy:
     host: "127.0.0.1"
     port: 15236
   ```

2. **环境变量**:
   - `HTTP_PROXY`
   - `HTTPS_PROXY`
   - `http_proxy`
   - `https_proxy`

3. **默认值**:
   - `http://127.0.0.1:15236`

## 🎯 为什么可能超时？

### 代理可能导致超时的原因

1. **代理服务器响应慢**
   - 代理服务器本身响应慢
   - 代理服务器到目标服务器的网络延迟

2. **代理连接问题**
   - 代理服务器不可用
   - 代理服务器连接超时
   - 代理服务器限流

3. **代理配置问题**
   - 代理地址错误
   - 代理端口错误
   - 代理认证失败

## 🔍 如何检查代理是否正常工作？

### 1. 检查环境变量

```bash
echo $HTTP_PROXY
echo $HTTPS_PROXY
```

### 2. 检查配置文件

```yaml
# config.yaml
proxy:
  host: "127.0.0.1"
  port: 15236
```

### 3. 检查日志

查看是否有代理相关的日志：
- "使用代理连接 WebSocket"
- "使用代理获取市场数据"

### 4. 测试代理连接

```bash
# 测试代理是否可用
curl -x http://127.0.0.1:15236 https://clob.polymarket.com/health
```

## 💡 如果代理有问题怎么办？

### 方案1: 禁用代理（不推荐）

修改代码，禁用代理：
```go
// clob/client/client.go
useProxy := false  // 禁用代理
```

### 方案2: 检查代理服务器状态

```bash
# 检查代理服务器是否运行
netstat -an | grep 15236

# 或使用 telnet 测试
telnet 127.0.0.1 15236
```

### 方案3: 更换代理服务器

修改配置文件：
```yaml
proxy:
  host: "your-proxy-host"
  port: your-proxy-port
```

## 📝 总结

### REST API 调用使用代理

- ✅ **默认使用代理**：`http://127.0.0.1:15236`
- ✅ **代理配置来源**：配置文件 > 环境变量 > 默认值
- ✅ **所有 REST API 调用**：都会经过代理

### 代理可能导致超时

- ⚠️ **代理服务器响应慢**：可能导致 REST API 调用超时
- ⚠️ **代理连接问题**：可能导致 REST API 调用失败
- ⚠️ **代理限流**：可能导致 REST API 调用被限流

### 建议

1. **检查代理服务器状态**：确保代理服务器正常运行
2. **监控代理性能**：检查代理服务器的响应时间
3. **优化代理配置**：如果代理有问题，考虑更换或禁用

---

**结论**：
- ✅ **REST API 调用默认使用代理** (`http://127.0.0.1:15236`)
- ⚠️ **代理问题可能导致超时**：如果代理服务器响应慢或不可用，会导致 REST API 调用超时
- 💡 **建议**：检查代理服务器状态，确保代理正常工作

