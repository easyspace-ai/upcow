#!/bin/bash

# 测试示例脚本
# 使用方法: ./test_example.sh <example_name>
# 示例: ./test_example.sh create_api_key

set -e

EXAMPLE_NAME=$1

if [ -z "$EXAMPLE_NAME" ]; then
    echo "用法: $0 <example_name>"
    echo ""
    echo "可用的示例:"
    echo "  - create_api_key"
    echo "  - get_markets"
    echo "  - fetch_gamma_market"
    echo "  - get_orderbook"
    echo "  - get_price"
    echo "  - get_open_orders"
    echo "  - cancel_order"
    exit 1
fi

EXAMPLE_FILE="${EXAMPLE_NAME}.go"

if [ ! -f "$EXAMPLE_FILE" ]; then
    echo "错误: 找不到示例文件: $EXAMPLE_FILE"
    exit 1
fi

echo "正在编译示例: $EXAMPLE_FILE"
go build "$EXAMPLE_FILE"

if [ $? -eq 0 ]; then
    echo "✅ 编译成功！"
    echo ""
    echo "运行示例:"
    echo "  go run $EXAMPLE_FILE"
    echo ""
    echo "或者使用编译后的二进制文件:"
    echo "  ./${EXAMPLE_NAME}"
else
    echo "❌ 编译失败"
    exit 1
fi

