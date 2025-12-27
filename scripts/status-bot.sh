#!/bin/bash

# 查看程序运行状态

set -e

# 获取脚本所在目录
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

PID_FILE="$PROJECT_ROOT/bot.pid"

if [ ! -f "$PID_FILE" ]; then
    echo "❌ 程序未运行（未找到 PID 文件）"
    exit 0
fi

PID=$(cat "$PID_FILE")

if ps -p "$PID" > /dev/null 2>&1; then
    echo "✅ 程序正在运行"
    echo "   PID: $PID"
    echo ""
    echo "进程信息:"
    ps -p "$PID" -o pid,etime,command
    echo ""
    echo "最近日志 (最后 10 行):"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    LATEST_LOG=$(ls -t "$PROJECT_ROOT/logs/bot_"*.log 2>/dev/null | head -1)
    if [ -n "$LATEST_LOG" ]; then
        tail -n 10 "$LATEST_LOG"
    else
        echo "未找到日志文件"
    fi
else
    echo "❌ 程序未运行（进程不存在）"
    echo "   清理 PID 文件..."
    rm -f "$PID_FILE"
fi
