# CTF 拆分测试 Demo

这个 demo 演示了如何使用 CTF (Conditional Token Framework) 进行 USDC 的拆分操作。

## 功能说明

1. **自动获取市场**: 自动获取下一个 BTC 15 分钟市场的信息
2. **Split 操作**: 将 USDC 拆分为 YES + NO 代币（1 USDC -> 1 YES + 1 NO）

## 前置要求

1. 在当前目录（`cmd/test-ctf-split/`）创建 `.env` 配置文件，包含以下内容：
```bash
# 必需配置
PRIVATE_KEY=0x你的私钥

# 可选配置（有默认值）
RPC_URL=https://polygon-rpc.com
AMOUNT=1.0
CHAIN_ID=137
PROXY_ADDRESS=0x代理地址  # 用于查询余额的代理地址（可选，不配置则使用私钥生成的地址）

# Relayer 配置（可选，如果配置了这些，将通过 Relayer 执行交易，gasless）
BUILDER_API_KEY=your-builder-api-key
BUILDER_SECRET=your-builder-secret
BUILDER_PASS_PHRASE=your-builder-passphrase
```

2. 确保账户有足够的：
   - **USDC 余额**（用于 split 操作）
     - Relayer 模式：代理地址需要有 USDC 余额
     - 直接调用模式：交易账户地址需要有 USDC 余额
   - **USDC 授权**：Split 操作需要先授权 USDC 给 CTF 合约
   - **MATIC 余额**（仅直接调用模式需要）：用于支付 gas 费用
     - ⚠️ **Relayer 模式不需要 MATIC**：gas 费用由 Polymarket 支付（gasless）

## 使用方法

### 基本用法

```bash
# 进入目录
cd cmd/test-ctf-split

# 创建 .env 文件（参考上面的格式）
# 然后运行（使用默认参数，拆分 1.0 USDC）
go run main.go
```

### 配置说明

在 `.env` 文件中可以配置以下参数：

- `PRIVATE_KEY`: **必需**，你的钱包私钥（带或不带 0x 前缀都可以）
- `RPC_URL`: 可选，Polygon RPC 节点 URL（默认根据链ID自动选择）
- `AMOUNT`: 可选，要拆分的 USDC 数量（默认 1.0）
- `CHAIN_ID`: 可选，链 ID（137 = Polygon 主网，80002 = Amoy 测试网，默认 137）
- `PROXY_ADDRESS`: 可选，用于查询余额的代理地址（不配置则使用私钥生成的地址）
- `BUILDER_API_KEY`: 可选，Builder API Key（如果配置了，将通过 Relayer 执行交易，gasless）
- `BUILDER_SECRET`: 可选，Builder API Secret（需要与 BUILDER_API_KEY 一起配置）
- `BUILDER_PASS_PHRASE`: 可选，Builder API Passphrase（需要与 BUILDER_API_KEY 一起配置）

### 示例配置

```bash
# 拆分 5.0 USDC
PRIVATE_KEY=0x你的私钥
AMOUNT=5.0

# 使用测试网
PRIVATE_KEY=0x你的私钥
CHAIN_ID=80002
RPC_URL=https://rpc-amoy.polygon.technology
```

## 操作流程

1. **获取市场信息**: 自动计算下一个 BTC 15 分钟市场的 slug，并通过 Gamma API 获取市场详情和 conditionId
2. **检查余额**: 显示 USDC 余额、授权情况，以及 YES/NO 代币余额
3. **Split 操作**:
   - 创建拆分交易
   - 显示交易详情
   - 发送交易并等待确认
4. **显示最终状态**: 显示操作后的余额情况

## Relayer 模式 vs 直接调用模式

| 特性 | 直接调用模式 | Relayer 模式 |
|------|------------|------------|
| Gas 费用 | ✅ 需要支付 MATIC | ❌ **不需要 MATIC**（Polymarket 支付，gasless） |
| MATIC 余额要求 | ✅ 需要 | ❌ **不需要** |
| USDC 余额要求 | 交易账户地址需要有余额 | 代理地址需要有余额 |
| 配置要求 | 只需私钥 | 需要私钥 + 代理地址 + Builder API 凭证 |
| 适用场景 | 直接使用 EOA 地址 | 使用代理钱包（Magic/Polymarket.com） |

### 使用 Relayer 模式（推荐）

如果配置了 `PROXY_ADDRESS` 和 Builder API 凭证，程序会自动使用 Relayer 模式：
- ✅ **Gasless**：交易费用由 Polymarket 支付，**完全不需要 MATIC**
- ✅ **通过代理钱包**：使用代理地址的余额执行交易
- ✅ **更安全**：不需要在交易账户地址保留余额和 MATIC

### 使用直接调用模式

如果没有配置 Builder API 凭证，程序会使用直接调用 CTF 合约的方式：
- ⚠️ **需要支付 Gas**：每次操作都需要支付 MATIC
- ⚠️ **余额要求**：交易账户地址需要有足够的 USDC 和授权

## 注意事项

1. **Gas 费用和 MATIC 要求**: 
   - **直接调用模式**：每次操作都需要支付 MATIC 作为 gas 费用，交易账户地址需要有 MATIC 余额
   - **Relayer 模式**：**完全 gasless，不需要任何 MATIC**，费用由 Polymarket 支付
2. **授权**: Split 操作需要先授权 USDC 给 CTF 合约。如果授权不足，操作会失败
3. **市场可用性**: 如果下一个周期的市场尚未创建，程序会报错。请稍后再试
4. **Builder API 凭证**: 如果使用 Relayer 模式，需要从 Polymarket Builder Program 获取 API 凭证
5. **市场选择**: 本工具会自动获取**下一个** BTC 15 分钟市场，用于提前拆分准备

## 相关文档

- [CTF Overview](data/docs/developers_CTF_overview.md)
- [Split USDC](data/docs/developers_CTF_split.md)

