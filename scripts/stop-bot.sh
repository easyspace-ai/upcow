#!/bin/bash

# 停止后台运行的程序

set -e

# 获取脚本所在目录
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

PID_FILE="$PROJECT_ROOT/bot.pid"

if [ ! -f "$PID_FILE" ]; then
    echo "⚠️  未找到 PID 文件，程序可能未运行"
    exit 1
fi

PID=$(cat "$PID_FILE")

if ! ps -p "$PID" > /dev/null 2>&1; then
    echo "⚠️  进程不存在 (PID: $PID)，清理 PID 文件"
    rm -f "$PID_FILE"
    exit 0
fi

echo "🛑 正在停止程序 (PID: $PID)..."

# 发送 SIGTERM 信号（优雅关闭）
kill "$PID"

# 等待进程结束（最多等待 10 秒）
for i in {1..10}; do
    if ! ps -p "$PID" > /dev/null 2>&1; then
        echo "✅ 程序已停止"
        rm -f "$PID_FILE"
        exit 0
    fi
    sleep 1
done

# 如果还在运行，强制杀死
if ps -p "$PID" > /dev/null 2>&1; then
    echo "⚠️  程序未响应，强制终止..."
    kill -9 "$PID"
    sleep 1
fi

if ps -p "$PID" > /dev/null 2>&1; then
    echo "❌ 无法停止程序"
    exit 1
else
    echo "✅ 程序已强制停止"
    rm -f "$PID_FILE"
fi
