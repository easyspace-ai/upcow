#!/bin/bash

# Go 版本（可修改）
GO_VERSION="1.23.0"
GO_ARCH="amd64"

# 检测系统架构
ARCH=$(uname -m)
if [ "$ARCH" = "aarch64" ] || [ "$ARCH" = "arm64" ]; then
    GO_ARCH="arm64"
fi

echo "正在下载 Go ${GO_VERSION} (${GO_ARCH})..."

# 下载 Go
cd /tmp
wget -q https://go.dev/dl/go${GO_VERSION}.linux-${GO_ARCH}.tar.gz

if [ $? -ne 0 ]; then
    echo "下载失败，请检查网络连接或版本号"
    exit 1
fi

# 删除旧版本
echo "删除旧版本..."
sudo rm -rf /usr/local/go

# 解压安装
echo "安装中..."
sudo tar -C /usr/local -xzf go${GO_VERSION}.linux-${GO_ARCH}.tar.gz

# 设置环境变量
echo "配置环境变量..."
if ! grep -q "/usr/local/go/bin" ~/.bashrc; then
    echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
    echo 'export GOPATH=$HOME/go' >> ~/.bashrc
    echo 'export GOPROXY=https://goproxy.cn,direct' >> ~/.bashrc
fi

# 使环境变量立即生效
export PATH=$PATH:/usr/local/go/bin
export GOPATH=$HOME/go
export GOPROXY=https://goproxy.cn,direct

# 验证安装
echo ""
echo "安装完成！"
go version

# 清理
rm -f /tmp/go${GO_VERSION}.linux-${GO_ARCH}.tar.gz

echo ""
echo "提示：如果 go version 命令不生效，请执行：source ~/.bashrc"
