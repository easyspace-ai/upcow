#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
分析订单明细和调价行为
"""
import re
from datetime import datetime
from collections import defaultdict

log_file = "logs/velocityfollow-dashboard.log"

# 存储订单信息
orders = []
reorder_monitors = []
reorder_triggers = []
reorder_executes = []
reorder_success = []

print("=" * 80)
print("订单明细和调价行为分析")
print("=" * 80)

with open(log_file, 'r', encoding='utf-8') as f:
    for line in f:
        # 提取订单提交信息
        if "[OrderExecutor]" in line and "订单已提交" in line:
            time_match = re.search(r'time="([^"]+)"', line)
            order_match = re.search(r'orderID=([^ ]+)', line)
            direction_match = re.search(r'direction=([^ ]+)', line)
            price_match = re.search(r'price=([0-9.]+)', line)
            size_match = re.search(r'size=([0-9.]+)', line)
            
            if time_match and order_match and price_match and size_match:
                order_type = "Entry" if "Entry" in line else "Hedge"
                orders.append({
                    'time': time_match.group(1),
                    'order_id': order_match.group(1),
                    'type': order_type,
                    'direction': direction_match.group(1) if direction_match else "unknown",
                    'price': float(price_match.group(1)),
                    'size': float(size_match.group(1))
                })
        
        # 提取调价监控启动
        if "[调价监控]" in line:
            time_match = re.search(r'time="([^"]+)"', line)
            entry_match = re.search(r'entryOrderID=([^ ]+)', line)
            hedge_match = re.search(r'hedgeOrderID=([^ ]+)', line)
            entry_time_match = re.search(r'entryFilledTime=([^ ]+)', line)
            
            if time_match and entry_match and hedge_match and entry_time_match:
                reorder_monitors.append({
                    'time': time_match.group(1),
                    'entry_id': entry_match.group(1),
                    'hedge_id': hedge_match.group(1),
                    'entry_filled_time': entry_time_match.group(1)
                })
        
        # 提取调价触发
        if "[调价触发]" in line:
            time_match = re.search(r'time="([^"]+)"', line)
            entry_match = re.search(r'entryOrderID=([^ ]+)', line)
            reorder_triggers.append({
                'time': time_match.group(1) if time_match else "",
                'entry_id': entry_match.group(1) if entry_match else ""
            })
        
        # 提取调价执行
        if "[调价执行]" in line:
            time_match = re.search(r'time="([^"]+)"', line)
            entry_match = re.search(r'entryOrderID=([^ ]+)', line)
            reorder_executes.append({
                'time': time_match.group(1) if time_match else "",
                'entry_id': entry_match.group(1) if entry_match else ""
            })
        
        # 提取调价成功
        if "[调价成功]" in line:
            time_match = re.search(r'time="([^"]+)"', line)
            reorder_success.append({
                'time': time_match.group(1) if time_match else ""
            })

print(f"\n【订单统计】")
print(f"Entry订单数量: {len([o for o in orders if o['type'] == 'Entry'])}")
print(f"Hedge订单数量: {len([o for o in orders if o['type'] == 'Hedge'])}")
print(f"调价监控启动: {len(reorder_monitors)}")
print(f"调价触发: {len(reorder_triggers)}")
print(f"调价执行: {len(reorder_executes)}")
print(f"调价成功: {len(reorder_success)}")

print(f"\n【最近20个订单明细】")
recent_orders = sorted(orders, key=lambda x: x['time'])[-20:]
for o in recent_orders:
    print(f"{o['time']} | {o['type']:6s} | {o['direction']:8s} | 价格={o['price']:.4f} | 数量={o['size']:.4f} | ID={o['order_id'][:20]}...")

print(f"\n【调价监控详情（最近10个）】")
for m in reorder_monitors[-10:]:
    print(f"监控启动: {m['time']} | Entry={m['entry_id'][:20]}... | Hedge={m['hedge_id'][:20]}... | Entry成交时间={m['entry_filled_time']}")

if reorder_triggers:
    print(f"\n【调价触发详情】")
    for t in reorder_triggers:
        print(f"触发时间: {t['time']} | Entry={t['entry_id'][:20]}...")
else:
    print(f"\n【⚠️ 警告：没有发现任何调价触发！】")
    print("可能原因：")
    print("1. Hedge订单在15秒内就成交了（利润空间不够大）")
    print("2. 调价逻辑有问题")
    print("3. 订单状态检查有问题")

if reorder_executes:
    print(f"\n【调价执行详情】")
    for e in reorder_executes:
        print(f"执行时间: {e['time']} | Entry={e['entry_id'][:20]}...")
else:
    print(f"\n【⚠️ 警告：没有发现任何调价执行！】")

if reorder_success:
    print(f"\n【调价成功详情】")
    for s in reorder_success:
        print(f"成功时间: {s['time']}")
else:
    print(f"\n【⚠️ 警告：没有发现任何调价成功！】")

# 分析为什么没有调价
print(f"\n【调价未触发原因分析】")
print("检查前5个监控订单的Hedge订单成交情况：")
for m in reorder_monitors[:5]:
    hedge_id = m['hedge_id']
    hedge_filled = False
    fill_time = None
    with open(log_file, 'r', encoding='utf-8') as f:
        for line in f:
            if hedge_id in line and ('filled' in line.lower() or '成交' in line):
                time_match = re.search(r'time="([^"]+)"', line)
                if time_match:
                    fill_time = time_match.group(1)
                    hedge_filled = True
                    break
    
    if hedge_filled:
        print(f"  Entry={m['entry_id'][:20]}... | Hedge在 {fill_time} 成交（未触发调价）")
    else:
        print(f"  Entry={m['entry_id'][:20]}... | Hedge未成交（应该触发调价！）")
