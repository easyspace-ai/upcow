#!/bin/bash

# 后台运行脚本
# 使用方法: ./scripts/run-background.sh [策略配置文件]

set -e

# 获取脚本所在目录
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# 策略配置文件（可选）
STRATEGY_CONFIG="${1:-yml/updownthreshold.yaml}"

# 日志目录
LOG_DIR="$PROJECT_ROOT/logs"
mkdir -p "$LOG_DIR"

# 日志文件（带时间戳）
TIMESTAMP=$(date +"%Y%m%d_%H%M%S")
LOG_FILE="$LOG_DIR/bot_${TIMESTAMP}.log"

# PID 文件
PID_FILE="$PROJECT_ROOT/bot.pid"

# 检查是否已经在运行
if [ -f "$PID_FILE" ]; then
    OLD_PID=$(cat "$PID_FILE")
    if ps -p "$OLD_PID" > /dev/null 2>&1; then
        echo "⚠️  程序已经在运行中 (PID: $OLD_PID)"
        echo "   如需重启，请先运行: kill $OLD_PID"
        exit 1
    else
        echo "清理旧的 PID 文件..."
        rm -f "$PID_FILE"
    fi
fi

# 切换到项目根目录
cd "$PROJECT_ROOT"

# 编译程序（如果还没有编译）
if [ ! -f "$PROJECT_ROOT/bin/bot" ]; then
    echo "📦 正在编译程序..."
    mkdir -p "$PROJECT_ROOT/bin"
    go build -o "$PROJECT_ROOT/bin/bot" ./cmd/bot
fi

# 启动程序（后台运行）
echo "🚀 正在启动程序..."
echo "   配置文件: $STRATEGY_CONFIG"
echo "   日志文件: $LOG_FILE"
echo "   PID 文件: $PID_FILE"

# 使用 nohup 后台运行
nohup "$PROJECT_ROOT/bin/bot" -config="$STRATEGY_CONFIG" > "$LOG_FILE" 2>&1 &
PID=$!

# 保存 PID
echo $PID > "$PID_FILE"

# 等待一下，检查进程是否成功启动
sleep 2
if ps -p $PID > /dev/null 2>&1; then
    echo "✅ 程序已启动 (PID: $PID)"
    echo ""
    echo "📋 管理命令:"
    echo "   查看日志: tail -f $LOG_FILE"
    echo "   查看进程: ps -p $PID"
    echo "   停止程序: kill $PID"
    echo "   或运行: ./scripts/stop-bot.sh"
else
    echo "❌ 程序启动失败，请查看日志: $LOG_FILE"
    rm -f "$PID_FILE"
    exit 1
fi
