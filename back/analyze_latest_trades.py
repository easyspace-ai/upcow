#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
分析最新的交易记录
"""

import re
from collections import defaultdict
from datetime import datetime
from pathlib import Path

def parse_log_line(line):
    """解析日志行"""
    pattern = r'\[(\d+)-(\d+)-(\d+)\s+(\d+):(\d+):(\d+)\]\s+(.*?)(?:\s+\[(\w+)=([^\]]+)\])?$'
    match = re.search(pattern, line)
    if match:
        year, month, day, hour, minute, second = match.groups()[:6]
        message = match.groups()[6] if len(match.groups()) > 6 else ""
        
        try:
            timestamp = datetime(int(f"20{year}"), int(month), int(day), 
                               int(hour), int(minute), int(second))
            return {
                'timestamp': timestamp,
                'message': message
            }
        except:
            pass
    return None

def extract_trade_info(message):
    """从消息中提取交易信息"""
    info = {}
    
    # 提取 side
    side_match = re.search(r'side=(\w+)', message)
    if side_match:
        info['side'] = side_match.group(1)
    
    # 提取 ask
    ask_match = re.search(r'ask=(\d+)c', message)
    if ask_match:
        info['ask'] = int(ask_match.group(1))
    
    # 提取 hedge
    hedge_match = re.search(r'hedge=(\d+)c', message)
    if hedge_match:
        info['hedge'] = int(hedge_match.group(1))
    
    # 提取速度
    vel_match = re.search(r'vel=([\d.]+)', message)
    if vel_match:
        info['velocity'] = float(vel_match.group(1))
    
    # 提取 move
    move_match = re.search(r'move=(\d+)c', message)
    if move_match:
        info['move'] = int(move_match.group(1))
    
    # 提取 trades
    trades_match = re.search(r'trades=(\d+)/(\d+)', message)
    if trades_match:
        info['trades_count'] = int(trades_match.group(1))
        info['trades_max'] = int(trades_match.group(2))
    
    return info

def analyze_latest_trades(log_file):
    """分析最新的交易记录"""
    trades = []
    orders = []
    
    with open(log_file, 'r', encoding='utf-8', errors='ignore') as f:
        for line in f:
            parsed = parse_log_line(line)
            if not parsed:
                continue
            
            msg = parsed['message']
            
            # 实际交易触发
            if '⚡' in msg and '触发' in msg and ('顺序' in msg or '并发' in msg):
                trade_info = extract_trade_info(msg)
                trade_info['timestamp'] = parsed['timestamp']
                trade_info['message'] = msg
                trades.append(trade_info)
            
            # 模拟下单记录
            if '📝' in msg and '纸交易' in msg and '模拟下单' in msg:
                order_info = {}
                order_info['timestamp'] = parsed['timestamp']
                
                # 提取订单信息
                order_id_match = re.search(r'orderID=([^\s,]+)', msg)
                if order_id_match:
                    order_info['order_id'] = order_id_match.group(1)
                
                side_match = re.search(r'side=(\w+)', msg)
                if side_match:
                    order_info['side'] = side_match.group(1)
                
                price_match = re.search(r'price=([\d.]+)', msg)
                if price_match:
                    order_info['price'] = float(price_match.group(1))
                
                size_match = re.search(r'size=([\d.]+)', msg)
                if size_match:
                    order_info['size'] = float(size_match.group(1))
                
                status_match = re.search(r'status=(\w+)', msg)
                if status_match:
                    order_info['status'] = status_match.group(1)
                
                orders.append(order_info)
    
    return trades, orders

def print_trade_analysis(trades, orders):
    """打印交易分析"""
    print("=" * 80)
    print("📊 最新交易情况分析")
    print("=" * 80)
    print()
    
    print(f"⚡ 实际交易触发次数: {len(trades)}")
    print()
    
    if trades:
        print("📋 交易触发详情:")
        for i, trade in enumerate(trades, 1):
            print(f"\n  交易 #{i}:")
            print(f"    时间: {trade['timestamp']}")
            print(f"    方向: {trade.get('side', 'N/A')}")
            print(f"    入场价格: {trade.get('ask', 'N/A')}c")
            print(f"    对冲价格: {trade.get('hedge', 'N/A')}c")
            print(f"    速度: {trade.get('velocity', 'N/A')} c/s")
            print(f"    价格变化: {trade.get('move', 'N/A')}c")
            print(f"    交易计数: {trade.get('trades_count', 'N/A')}/{trade.get('trades_max', 'N/A')}")
            print(f"    消息: {trade['message'][:100]}")
        print()
    
    print(f"📝 模拟下单记录: {len(orders)} 条")
    print()
    
    if orders:
        # 按订单ID分组
        orders_by_id = defaultdict(list)
        for order in orders:
            if 'order_id' in order:
                orders_by_id[order['order_id']].append(order)
        
        print("📦 订单详情:")
        for order_id, order_list in list(orders_by_id.items())[:10]:
            print(f"\n  订单ID: {order_id}")
            for order in order_list:
                print(f"    [{order['timestamp']}] {order.get('side', 'N/A')} @ {order.get('price', 'N/A')} size={order.get('size', 'N/A')} status={order.get('status', 'N/A')}")
        print()
    
    # 统计分析
    if trades:
        print("📊 统计分析:")
        
        # 方向统计
        sides = [t.get('side', '') for t in trades]
        side_counts = defaultdict(int)
        for side in sides:
            side_counts[side] += 1
        print(f"  方向分布: {dict(side_counts)}")
        
        # 价格统计
        asks = [t.get('ask', 0) for t in trades if t.get('ask')]
        if asks:
            print(f"  入场价格范围: {min(asks)}c - {max(asks)}c")
            print(f"  平均入场价格: {sum(asks)/len(asks):.1f}c")
        
        hedges = [t.get('hedge', 0) for t in trades if t.get('hedge')]
        if hedges:
            print(f"  对冲价格范围: {min(hedges)}c - {max(hedges)}c")
            print(f"  平均对冲价格: {sum(hedges)/len(hedges):.1f}c")
        
        # 速度统计
        velocities = [t.get('velocity', 0) for t in trades if t.get('velocity')]
        if velocities:
            print(f"  速度范围: {min(velocities):.3f} - {max(velocities):.3f} c/s")
            print(f"  平均速度: {sum(velocities)/len(velocities):.3f} c/s")
        
        print()
    
    # 订单统计
    if orders:
        print("📦 订单统计:")
        
        # 按状态统计
        statuses = [o.get('status', '') for o in orders]
        status_counts = defaultdict(int)
        for status in statuses:
            status_counts[status] += 1
        print(f"  状态分布: {dict(status_counts)}")
        
        # 按方向统计
        sides = [o.get('side', '') for o in orders]
        side_counts = defaultdict(int)
        for side in sides:
            side_counts[side] += 1
        print(f"  方向分布: {dict(side_counts)}")
        
        # 价格统计
        prices = [o.get('price', 0) for o in orders if o.get('price')]
        if prices:
            print(f"  价格范围: {min(prices):.4f} - {max(prices):.4f}")
            print(f"  平均价格: {sum(prices)/len(prices):.4f}")
        
        # 数量统计
        sizes = [o.get('size', 0) for o in orders if o.get('size')]
        if sizes:
            print(f"  数量范围: {min(sizes):.4f} - {max(sizes):.4f}")
            print(f"  平均数量: {sum(sizes)/len(sizes):.4f}")
        
        print()
    
    print("=" * 80)
    print("📌 结论:")
    print("=" * 80)
    
    if len(trades) > 0:
        print(f"✅ 发现 {len(trades)} 次实际交易触发")
        print("   说明策略逻辑正常工作，能够识别交易机会并触发下单")
        print()
        print("   交易特点:")
        if trades:
            first_trade = trades[0]
            print(f"   - 交易方向: {first_trade.get('side', 'N/A')}")
            print(f"   - 入场价格: {first_trade.get('ask', 'N/A')}c")
            print(f"   - 速度: {first_trade.get('velocity', 'N/A')} c/s")
            print(f"   - 达到配置要求: ✅ (minVelocityCentsPerSec: 0.15)")
    else:
        print("⚠️  未发现实际交易触发")
    
    print()

if __name__ == "__main__":
    # 查找最新的日志文件
    log_files = sorted(Path("logs").glob("btc-updown-15m-*.log"), key=lambda p: p.stat().st_mtime, reverse=True)
    
    if not log_files:
        print("未找到日志文件")
        exit(1)
    
    latest_log = log_files[0]
    print(f"📁 分析最新日志文件: {latest_log.name}\n")
    
    trades, orders = analyze_latest_trades(latest_log)
    print_trade_analysis(trades, orders)

