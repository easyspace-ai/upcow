# 回退到 REST API 日志记录说明

## 📝 修改内容

为了便于排查为什么回退到 REST API，我们在代码中添加了详细的日志记录。

### 1. GetTopOfBook（策略主要接口）

**文件：** `internal/services/trading_orders.go:350-392`

**添加的日志：**
```go
log.Debugf("⚠️ [GetTopOfBook] 回退到REST API: market=%s reason=%s", market.Slug, wsFallbackReason)
```

**记录的回退原因：**
- `WebSocket book为nil` - WebSocket 连接不存在
- `当前市场信息为nil` - 当前市场信息未初始化
- `市场不匹配: cur=%s expected=%s` - 市场 slug 不匹配
- `数据过期: age=%.1fs (要求<60s)` - 数据超过60秒未更新
- `YES数据不完整: bid=%d ask=%d` - YES 方向缺少 bid 或 ask
- `NO数据不完整: bid=%d ask=%d` - NO 方向缺少 bid 或 ask
- `Session为nil` - Session 未初始化

### 2. GetBestPrice（单个资产价格）

**文件：** `internal/services/trading_orders.go:231-310`

**添加的日志：**
```go
log.Debugf("⚠️ [GetBestPrice] 回退到REST API: assetID=%s reason=%s", assetID, wsFallbackReason)
```

**记录的回退原因：**
- `WebSocket book为nil` - WebSocket 连接不存在
- `市场信息为nil` - 市场信息未初始化
- `数据过期: age=%.1fs (要求<30s)` - 数据超过30秒未更新
- `YES数据不完整: bid=%.4f ask=%.4f` - YES 方向缺少 bid 或 ask
- `NO数据不完整: bid=%.4f ask=%.4f` - NO 方向缺少 bid 或 ask
- `YES价差过大: %dc (要求<=30c)` - YES 方向价差超过30分
- `NO价差过大: %dc (要求<=30c)` - NO 方向价差超过30分
- `assetID不匹配: assetID=%s YES=%s NO=%s` - assetID 不匹配
- `Session为nil` - Session 未初始化

## 📊 日志示例

### GetTopOfBook 回退日志

```
[DEBUG] ⚠️ [GetTopOfBook] 回退到REST API: market=btc-updown-15m-1766730600 reason=数据过期: age=65.3s (要求<60s)
```

```
[DEBUG] ⚠️ [GetTopOfBook] 回退到REST API: market=btc-updown-15m-1766730600 reason=YES数据不完整: bid=0 ask=9900
```

```
[DEBUG] ⚠️ [GetTopOfBook] 回退到REST API: market=btc-updown-15m-1766730600 reason=市场不匹配: cur=btc-updown-15m-1766729700 expected=btc-updown-15m-1766730600
```

### GetBestPrice 回退日志

```
[DEBUG] ⚠️ [GetBestPrice] 回退到REST API: assetID=0x123... reason=YES价差过大: 35c (要求<=30c)
```

```
[DEBUG] ⚠️ [GetBestPrice] 回退到REST API: assetID=0x123... reason=数据过期: age=35.2s (要求<30s)
```

## 🔍 排查方法

### 1. 查看回退频率

统计日志中回退到 REST API 的次数：
```bash
grep "回退到REST API" logs/*.log | wc -l
```

### 2. 分析回退原因

统计各种回退原因的频率：
```bash
grep "回退到REST API" logs/*.log | grep -o "reason=[^ ]*" | sort | uniq -c | sort -rn
```

### 3. 检查 WebSocket 连接状态

如果频繁出现 `WebSocket book为nil` 或 `数据过期`，说明：
- WebSocket 连接可能断开
- 数据更新不及时
- 需要检查 WebSocket 连接健康状态

### 4. 检查市场匹配

如果出现 `市场不匹配`，说明：
- 周期切换时，WebSocket 数据可能还停留在上一个周期
- 需要确保周期切换时及时更新 WebSocket 订阅

## 📌 常见问题排查

### 问题1：频繁回退到 REST API

**可能原因：**
- WebSocket 连接不稳定
- 数据更新不及时
- 市场切换频繁

**解决方法：**
- 检查 WebSocket 连接状态
- 检查数据更新频率
- 考虑进一步放宽新鲜度要求

### 问题2：数据不完整

**可能原因：**
- WebSocket 数据只更新了部分（只有 bid 或只有 ask）
- 市场流动性差，订单簿不完整

**解决方法：**
- 检查 WebSocket 数据更新逻辑
- 检查市场流动性

### 问题3：价差过大

**可能原因：**
- WebSocket 数据异常（脏数据）
- 市场流动性极差

**解决方法：**
- 检查 WebSocket 数据质量
- 如果价差真的很大，回退到 REST API 是正确的

## ✅ 总结

通过添加详细的日志记录，我们可以：
- ✅ 快速定位回退到 REST API 的原因
- ✅ 分析 WebSocket 数据可用性
- ✅ 优化 WebSocket 连接和数据更新逻辑
- ✅ 提高系统可观测性

这些日志将帮助我们更好地理解系统行为，优化 WebSocket 数据使用率。

