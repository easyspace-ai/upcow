# 后台运行指南

本文档介绍如何在 Linux 上让交易机器人后台运行。

## 方法一：使用提供的脚本（推荐）

### 启动程序

```bash
# 使用默认配置
./scripts/run-background.sh

# 指定策略配置文件
./scripts/run-background.sh yml/updownthreshold.yaml
```

### 查看状态

```bash
./scripts/status-bot.sh
```

### 停止程序

```bash
./scripts/stop-bot.sh
```

### 查看日志

```bash
# 实时查看最新日志
tail -f logs/bot_*.log

# 查看最新的日志文件
ls -t logs/bot_*.log | head -1 | xargs tail -f
```

## 方法二：使用 nohup（简单方式）

```bash
# 编译程序
go build -o bin/bot ./cmd/bot

# 后台运行（输出到 nohup.out）
nohup ./bin/bot -config=yml/updownthreshold.yaml > logs/bot.log 2>&1 &

# 查看进程
ps aux | grep bot

# 停止程序（找到 PID 后）
kill <PID>
```

## 方法三：使用 screen（适合需要交互的场景）

```bash
# 安装 screen（如果未安装）
# Ubuntu/Debian:
sudo apt-get install screen

# CentOS/RHEL:
sudo yum install screen

# 启动新的 screen 会话
screen -S bot

# 在 screen 中运行程序
go run ./cmd/bot/main.go -config=yml/updownthreshold.yaml

# 按 Ctrl+A 然后按 D 来分离会话（程序继续运行）

# 重新连接到会话
screen -r bot

# 列出所有会话
screen -ls

# 终止会话（在会话内按 Ctrl+D 或运行）
screen -X -S bot quit
```

## 方法四：使用 tmux（更现代的终端复用器）

```bash
# 安装 tmux（如果未安装）
# Ubuntu/Debian:
sudo apt-get install tmux

# CentOS/RHEL:
sudo yum install tmux

# 启动新的 tmux 会话
tmux new -s bot

# 在 tmux 中运行程序
go run ./cmd/bot/main.go -config=yml/updownthreshold.yaml

# 按 Ctrl+B 然后按 D 来分离会话

# 重新连接到会话
tmux attach -t bot

# 列出所有会话
tmux ls

# 终止会话
tmux kill-session -t bot
```

## 方法五：使用 systemd（Linux 系统服务，开机自启）

### 创建 systemd 服务文件

创建 `/etc/systemd/system/betbot.service`（需要 root 权限）:

```ini
[Unit]
Description=BetBot Trading Bot
After=network.target

[Service]
Type=simple
User=your_username
WorkingDirectory=/path/to/upcow
ExecStart=/path/to/upcow/bin/bot -config=yml/updownthreshold.yaml
Restart=always
RestartSec=10
StandardOutput=append:/path/to/upcow/logs/bot.log
StandardError=append:/path/to/upcow/logs/bot.error.log

[Install]
WantedBy=multi-user.target
```

**注意**: 需要将以下内容替换为实际值：
- `your_username`: 运行服务的用户名
- `/path/to/upcow`: 项目实际路径

### 管理服务

```bash
# 重新加载 systemd 配置
sudo systemctl daemon-reload

# 启动服务
sudo systemctl start betbot

# 停止服务
sudo systemctl stop betbot

# 重启服务
sudo systemctl restart betbot

# 查看服务状态
sudo systemctl status betbot

# 启用开机自启
sudo systemctl enable betbot

# 禁用开机自启
sudo systemctl disable betbot

# 查看日志
sudo journalctl -u betbot -f
```

### 用户级服务（无需 root）

如果不想使用 root 权限，可以创建用户级服务：

```bash
# 创建用户 systemd 目录（如果不存在）
mkdir -p ~/.config/systemd/user

# 创建服务文件
cat > ~/.config/systemd/user/betbot.service <<EOF
[Unit]
Description=BetBot Trading Bot
After=network.target

[Service]
Type=simple
WorkingDirectory=/path/to/upcow
ExecStart=/path/to/upcow/bin/bot -config=yml/updownthreshold.yaml
Restart=always
RestartSec=10
StandardOutput=append:/path/to/upcow/logs/bot.log
StandardError=append:/path/to/upcow/logs/bot.error.log

[Install]
WantedBy=default.target
EOF

# 重新加载配置
systemctl --user daemon-reload

# 启动服务
systemctl --user start betbot

# 启用开机自启
systemctl --user enable betbot

# 查看状态
systemctl --user status betbot
```

## 日志管理

### 日志位置

- 脚本方式: `logs/bot_YYYYMMDD_HHMMSS.log`
- nohup 方式: `nohup.out` 或指定的日志文件
- systemd 方式: `logs/bot.log` 和 `logs/bot.error.log`，或使用 `journalctl -u betbot`

### 日志轮转

程序使用 `lumberjack` 库自动管理日志文件，配置在代码中。如果需要手动清理旧日志：

```bash
# 删除 7 天前的日志
find logs -name "bot_*.log" -mtime +7 -delete

# 压缩旧日志
find logs -name "bot_*.log" -mtime +1 -exec gzip {} \;
```

## 常见问题

### 1. 程序意外退出

- 检查日志文件查看错误信息
- 确保配置文件路径正确
- 检查网络连接和代理设置

### 2. 无法停止程序

```bash
# 查找进程
ps aux | grep bot

# 强制终止
kill -9 <PID>
```

### 3. 端口被占用

```bash
# 查找占用端口的进程
# Linux:
netstat -tulpn | grep :<PORT>
# 或
ss -tulpn | grep :<PORT>

# 终止进程
kill <PID>
```

## 推荐方案

- **开发/测试**: 使用方法三（screen）或方法四（tmux），方便查看输出和调试
- **生产环境**: 使用方法一（脚本）或方法五（systemd），更稳定可靠，支持自动重启和开机自启
- **简单场景**: 使用方法二（nohup），快速简单
