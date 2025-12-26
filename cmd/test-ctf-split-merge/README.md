# CTF 拆分与合并测试 Demo

这个 demo 演示了如何使用 CTF (Conditional Token Framework) 进行 USDC 的拆分和合并操作。

## 功能说明

1. **自动获取市场**: 自动获取当前 BTC 15 分钟市场的信息
2. **Split 操作**: 将 USDC 拆分为 YES + NO 代币（1 USDC -> 1 YES + 1 NO）
3. **Merge 操作**: 将 YES + NO 代币合并回 USDC（1 YES + 1 NO -> 1 USDC）

## 前置要求

1. 创建 `data/user.json` 配置文件，包含以下内容：
```json
{
  "private_key": "你的私钥（不带0x前缀）",
  "rpc_url": "Polygon RPC 节点 URL（可选）"
}
```

2. 确保账户有足够的：
   - USDC 余额（用于 split 操作）
   - MATIC 余额（用于支付 gas 费用）
   - USDC 已授权给 CTF 合约（如果需要 split）

## 使用方法

### 基本用法

```bash
# 使用默认参数（拆分/合并 1.0 USDC）
go run cmd/test-ctf-split-merge/main.go
```

### 自定义参数

```bash
# 设置拆分/合并数量
export AMOUNT="5.0"

# 设置链 ID（137 = Polygon 主网，80002 = Amoy 测试网）
export CHAIN_ID=137

# 设置 RPC URL（可选，会从 user.json 读取）
export RPC_URL="https://polygon-rpc.com"

# 只执行 split，跳过 merge
export SKIP_MERGE=true

# 只执行 merge，跳过 split
export SKIP_SPLIT=true

# 运行
go run cmd/test-ctf-split-merge/main.go
```

## 操作流程

1. **获取市场信息**: 自动计算当前 BTC 15 分钟市场的 slug，并通过 Gamma API 获取市场详情和 conditionId
2. **检查余额**: 显示 USDC 余额、授权情况，以及 YES/NO 代币余额
3. **Split 操作**（如果未跳过）:
   - 创建拆分交易
   - 显示交易详情
   - 等待用户确认
   - 发送交易并等待确认
4. **Merge 操作**（如果未跳过）:
   - 检查 YES/NO 代币余额
   - 创建合并交易（数量取两者最小值）
   - 显示交易详情
   - 等待用户确认
   - 发送交易并等待确认
5. **显示最终状态**: 显示操作后的余额情况

## 注意事项

1. **Gas 费用**: 每次操作都需要支付 MATIC 作为 gas 费用
2. **授权**: Split 操作需要先授权 USDC 给 CTF 合约。如果授权不足，操作会失败
3. **余额检查**: Merge 操作需要同时拥有足够的 YES 和 NO 代币
4. **市场可用性**: 如果当前周期的市场尚未创建，程序会报错。请稍后再试

## 示例输出

```
╔══════════════════════════════════════════════════════════════════════════╗
║   CTF 拆分与合并测试 Demo                                                 ║
╚══════════════════════════════════════════════════════════════════════════╝

账户地址: 0x1234...
RPC节点: https://polygon-rpc.com
链ID: 137

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
步骤 1: 获取当前 BTC 15 分钟市场
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
当前时间: 2025-01-26T10:30:00+08:00
周期开始时间戳: 1766683800
市场 Slug: btc-updown-15m-1766683800

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
步骤 2: 通过 Gamma API 获取市场信息
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
市场 ID: ...
问题: Will BTC be up or down in the 15-minute period starting at ...?
Condition ID: 0x61cb89066ab57e926fad059bf8f947d7d6eedcde4c904e28fc2ba4a5cd4ef2ca
...
```

## 相关文档

- [CTF Overview](data/docs/developers_CTF_overview.md)
- [Split USDC](data/docs/developers_CTF_split.md)
- [Merge Tokens](data/docs/developers_CTF_merge.md)

