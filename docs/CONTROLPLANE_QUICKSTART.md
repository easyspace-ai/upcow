# Controlplane（Server + Dashboard）快速启动与密钥/账号管理（Badger 方案）

本文档描述当前推荐的**可测试/可部署**流程：

- **助记词不走 HTTP、不进 SQLite、不进明文 `.env`**
- 敏感配置（如 builder creds、relayer URL 等）可导入 **Badger（加密 at-rest）**
- `funder_address` 采用官方文档推荐：**relayer expected safe**，必要时支持 **deploy**
- bot 启动时 **运行时配置不落盘**（Linux：memfd + `/proc/self/fd/*`）
- bot 异常退出可 **自动重启**（指数退避 + 最大次数）
- 本地可用 `cmd/bot-launcher` **独立启动 bot**（不依赖 server，仍不落盘私钥）

---

## 1. 关键概念

- **account_id（三位数字）**：例如 `456`  
  用于生成派生路径：`m/44'/60'/4'/5/6`
- **EOA**：由（全局助记词 + account_id 派生路径）推导出的地址
- **funder_address（Proxy Wallet / Safe）**：用于持有 USDC / 持仓  
  推荐获取方式：`relayer.GetExpectedSafe(EOA)`  
  对于“新钱包”，可能需要 **deploy**（需要 builder creds）
- **Badger secrets db**：加密 KV（必须提供 32 bytes `GOBET_SECRET_KEY` 才能加密/解密）
  - `mnemonic`：明文助记词（Badger 落盘加密）
  - `env/<KEY>`：导入的配置项（等价 `.env`，但不在服务器落盘）

---

## 2. 本地：准备 Badger（助记词 + 配置导入）

### 2.1 生成 Badger 加密密钥（本地保存，**不要提交到仓库**）

```bash
# Bash/Zsh 语法：
export GOBET_SECRET_KEY="$(openssl rand -hex 32)"
export GOBET_SECRET_DB="data/secrets.badger"

# Fish shell 语法：
set -x GOBET_SECRET_KEY (openssl rand -hex 32)
set -x GOBET_SECRET_DB "data/secrets.badger"
```

> **重要**：`GOBET_SECRET_KEY` 必须是 **32 bytes** 的 hex 格式（64 个十六进制字符）。
> 
> 验证密钥格式：
> ```bash
> # Bash/Zsh：
> echo "$GOBET_SECRET_KEY" | wc -c  # 应该是 65（64 字符 + 换行符）
> 
> # Fish：
> echo $GOBET_SECRET_KEY | wc -c  # 应该是 65（64 字符 + 换行符）
> ```
> 
> 如果遇到 "decoded key length must be 32, got 48" 错误，说明密钥被误解析为 base64。请重新生成密钥，确保使用 `openssl rand -hex 32` 生成的是纯 hex 格式（64 个字符）。

### 2.2 写入助记词到 Badger（本地执行一次）

```bash
go run ./cmd/mnemonic-init -badger "$GOBET_SECRET_DB"
```

会提示输入助记词，并写入 Badger 的 `mnemonic` 键（落盘加密）。

### 2.3 把本地 `.env` 导入 Badger（可选，但推荐）

把你现有 `.env` 的 `KEY=VALUE` 全部导入到 Badger 的 `env/<KEY>`：

```bash
go run ./cmd/env2badger -in .env -badger "$GOBET_SECRET_DB"
```

导入后，**服务器可以完全不需要 `.env` 文件**。

> 建议：把仅本地用的变量拆到另一个文件，避免导入无关项。

---

## 3. 服务器部署（不落盘 `.env`）

### 3.1 拷贝文件到服务器

- 拷贝整个目录：`data/secrets.badger/`
- 拷贝 controlplane 的二进制/镜像，以及 SQLite（如果需要迁移现有数据）

### 3.2 服务器启动时只注入最小环境变量

服务器侧不需要 `.env` 文件，但必须注入：

```bash
export GOBET_SECRET_KEY="..."          # 与本地生成的一致
export GOBET_SECRET_DB="data/secrets.badger"  # 可选，默认就是这个
```

其它配置（端口、builder creds、relayer url、自动重启开关等）会从 Badger 的 `env/<KEY>` 读取。

启动示例：

```bash
go run ./cmd/server -listen :9999
```

---

## 4. Dashboard/HTTP：账号与 bot 的操作流程

### 4.1 创建账号（只需要 account_id）

UI 上输入三位数 `account_id`（例如 `456`）即可。

服务端会：
- 解密 Badger 中的 `mnemonic`
- 推导 EOA
- 通过 relayer `GetExpectedSafe(EOA)` 得到 `funder_address`
- 若 safe 未部署且配置了 builder creds，则会尝试 **deploy + 轮询确认**

### 4.2 创建 bot（配置无需 wallet）

创建 bot 时 YAML **不需要**填写 `wallet.private_key / wallet.funder_address`。

### 4.3 绑定账号到 bot（1 账号 1 bot）

绑定只写 `bots.account_id`，不写入任何私钥到 config/db/version。

### 4.4 启动 bot（运行时配置不落盘）

启动时 server 会：
- 用 `account_id` 推导私钥与 funder
- 将钱包注入到运行时配置（内存）
- Linux 下使用 memfd 传给子进程：`-config /proc/self/fd/3`
- 并支持自动重启（见下节）

---

## 5. bot 自动重启（server 托管启动时）

通过配置开启：

- `GOBET_BOT_AUTO_RESTART=1`
- `GOBET_BOT_RESTART_MAX=5`（默认 5；0=不限制）
- `GOBET_BOT_RESTART_BASE_DELAY=2s`
- `GOBET_BOT_RESTART_MAX_DELAY=60s`

这些配置建议写入 Badger：

```bash
# 假设你在本地编辑 .env 后再 env2badger 导入
GOBET_BOT_AUTO_RESTART=1
GOBET_BOT_RESTART_MAX=5
GOBET_BOT_RESTART_BASE_DELAY=2s
GOBET_BOT_RESTART_MAX_DELAY=60s
```

**注意**：手动 stop 会把 `desired_running` 置为 0，因此不会触发自动重启。

---

## 6. 本地独立启动 bot（调试用，不依赖 server）

当你需要脱离 controlplane 单独调试 bot，可用：

```bash
export GOBET_SECRET_KEY="..."
export GOBET_SECRET_DB="data/secrets.badger"

go run ./cmd/bot-launcher \
  -config /path/to/base-config.yaml \
  -id 456 \
  -bot-bin bot
```

它会从 Badger 读取助记词，按 `-id` 派生私钥并注入 wallet，然后用 memfd 启动 bot（不落盘私钥）。

只查看派生信息不启动：

```bash
go run ./cmd/bot-launcher -config /path/to/base-config.yaml -id 456 -dry-run
```

---

## 7. 常见问题排查

### 7.1 “safe not deployed yet; missing builder creds for deploy”

表示你在使用新钱包，但没有配置 builder creds，服务端无法自动 deploy safe。

解决：
- 在 Badger 的 `env/` 中导入/设置：
  - `BUILDER_API_KEY`
  - `BUILDER_SECRET`
  - `BUILDER_PASS_PHRASE`
  - `POLYMARKET_RELAYER_URL`（可选）

然后重新创建账号或重启/启动 bot。

### 7.2 “funder_address mismatch”

表示 SQLite 中 `accounts.funder_address` 与 relayer 推导的 expected-safe 不一致（可能手工写错、或历史数据脏）。

解决：
- 重新创建该账号，或修正数据库中的 `funder_address`。

### 7.3 Badger 无法打开/解密

确认：
- `GOBET_SECRET_KEY` 是否与生成 Badger 时一致
- `GOBET_SECRET_DB` 路径是否指向正确目录

