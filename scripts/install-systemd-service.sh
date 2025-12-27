#!/bin/bash

# 安装 systemd 服务脚本
# 使用方法: sudo ./scripts/install-systemd-service.sh [用户名] [项目路径] [配置文件]

set -e

# 获取脚本所在目录
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# 检查是否为 root
if [ "$EUID" -ne 0 ]; then
    echo "❌ 请使用 sudo 运行此脚本"
    echo "   使用方法: sudo $0 [用户名] [项目路径] [配置文件]"
    exit 1
fi

# 参数
SERVICE_USER="${1:-$SUDO_USER}"
PROJECT_PATH="${2:-$PROJECT_ROOT}"
CONFIG_FILE="${3:-yml/updownthreshold.yaml}"

# 验证用户是否存在
if ! id "$SERVICE_USER" &>/dev/null; then
    echo "❌ 用户不存在: $SERVICE_USER"
    exit 1
fi

# 验证项目路径
if [ ! -d "$PROJECT_PATH" ]; then
    echo "❌ 项目路径不存在: $PROJECT_PATH"
    exit 1
fi

# 验证配置文件
if [ ! -f "$PROJECT_PATH/$CONFIG_FILE" ]; then
    echo "⚠️  配置文件不存在: $PROJECT_PATH/$CONFIG_FILE"
    echo "   将使用默认配置"
fi

# 验证可执行文件
BOT_BINARY="$PROJECT_PATH/bin/bot"
if [ ! -f "$BOT_BINARY" ]; then
    echo "⚠️  可执行文件不存在: $BOT_BINARY"
    echo "   正在编译..."
    cd "$PROJECT_PATH"
    mkdir -p bin
    sudo -u "$SERVICE_USER" go build -o "$BOT_BINARY" ./cmd/bot
    if [ ! -f "$BOT_BINARY" ]; then
        echo "❌ 编译失败"
        exit 1
    fi
    echo "✅ 编译成功"
fi

# 创建日志目录
LOG_DIR="$PROJECT_PATH/logs"
mkdir -p "$LOG_DIR"
chown "$SERVICE_USER:$SERVICE_USER" "$LOG_DIR"

# 创建服务文件
SERVICE_FILE="/etc/systemd/system/betbot.service"
cat > "$SERVICE_FILE" <<EOF
[Unit]
Description=BetBot Trading Bot
After=network.target

[Service]
Type=simple
User=$SERVICE_USER
WorkingDirectory=$PROJECT_PATH
ExecStart=$BOT_BINARY -config=$CONFIG_FILE
Restart=always
RestartSec=10
StandardOutput=append:$LOG_DIR/bot.log
StandardError=append:$LOG_DIR/bot.error.log

[Install]
WantedBy=multi-user.target
EOF

echo "✅ 服务文件已创建: $SERVICE_FILE"
echo ""
echo "配置信息:"
echo "  用户: $SERVICE_USER"
echo "  项目路径: $PROJECT_PATH"
echo "  配置文件: $CONFIG_FILE"
echo "  可执行文件: $BOT_BINARY"
echo "  日志目录: $LOG_DIR"
echo ""

# 重新加载 systemd
systemctl daemon-reload
echo "✅ systemd 配置已重新加载"
echo ""

echo "📋 管理命令:"
echo "  启动服务: sudo systemctl start betbot"
echo "  停止服务: sudo systemctl stop betbot"
echo "  重启服务: sudo systemctl restart betbot"
echo "  查看状态: sudo systemctl status betbot"
echo "  查看日志: sudo journalctl -u betbot -f"
echo "  启用开机自启: sudo systemctl enable betbot"
echo "  禁用开机自启: sudo systemctl disable betbot"
echo ""
echo "是否现在启动服务并启用开机自启? (y/n)"
read -r answer
if [ "$answer" = "y" ] || [ "$answer" = "Y" ]; then
    systemctl enable betbot
    systemctl start betbot
    sleep 2
    systemctl status betbot --no-pager
    echo ""
    echo "✅ 服务已启动并启用开机自启"
else
    echo "ℹ️  服务已安装但未启动，请手动运行: sudo systemctl start betbot"
fi
