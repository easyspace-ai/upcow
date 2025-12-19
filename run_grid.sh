#!/bin/bash
cd /Users/leven/space/pm/gobet

echo "=========================================="
echo "启动网格交易机器人（BBGO 架构）"
echo "=========================================="

# 停止旧进程
pkill -9 -f bot_bbgo 2>/dev/null
sleep 1

# 检查必要文件
if [ ! -f "data/user.json" ]; then
    echo "❌ 错误: data/user.json 不存在"
    echo "请先创建 data/user.json 文件，包含钱包私钥和代理地址"
    exit 1
fi

if [ ! -f "config.yaml" ]; then
    echo "❌ 错误: config.yaml 不存在"
    exit 1
fi

# 编译程序
echo "📦 编译程序..."
go build -o bin/bot_bbgo ./cmd/bot/main_bbgo.go
if [ $? -ne 0 ]; then
    echo "❌ 编译失败"
    exit 1
fi

echo "✅ 编译成功"
echo ""
echo "🚀 启动程序..."
echo "按 Ctrl+C 停止"
echo ""

# 运行程序
./bin/bot_bbgo 2>&1 | tee -a logs/grid_trading_$(date +%Y%m%d_%H%M%S).log

