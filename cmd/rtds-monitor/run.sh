#!/bin/bash

# RTDS 监控工具运行脚本
# 用法: ./run.sh [参数...]

# 默认参数
PROXY="${PROXY:-http://127.0.0.1:15236}"
CRYPTO_SOURCE="${CRYPTO_SOURCE:-chainlink}"
CRYPTO_SYMBOLS="${CRYPTO_SYMBOLS:-btc/usd}"
VERBOSE="${VERBOSE:-true}"
LOG_FILE="${LOG_FILE:-logs/rtds-monitor.log}"

# 创建日志目录
mkdir -p "$(dirname "$LOG_FILE")"

# 构建命令
CMD="go run cmd/rtds-monitor/main.go"
CMD="$CMD -proxy=$PROXY"
CMD="$CMD -crypto-source=$CRYPTO_SOURCE"
CMD="$CMD -crypto-symbols=$CRYPTO_SYMBOLS"

if [ "$VERBOSE" = "true" ]; then
    CMD="$CMD -verbose"
fi

# 如果提供了其他参数，追加
if [ $# -gt 0 ]; then
    CMD="$CMD $@"
fi

echo "启动 RTDS 监控工具..."
echo "日志文件: $LOG_FILE"
echo "命令: $CMD"
echo ""

# 运行并同时输出到控制台和文件
$CMD 2>&1 | tee "$LOG_FILE"

