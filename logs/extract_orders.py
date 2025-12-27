#!/usr/bin/env python3
import re
import glob
from collections import defaultdict

logs = sorted(glob.glob("btc-updown-15m-*.log"))

all_pairs = []

for log_file in logs:
    cycle_id = re.search(r'btc-updown-15m-(\d+)\.log', log_file).group(1)
    
    with open(log_file, 'r', encoding='utf-8', errors='ignore') as f:
        lines = f.readlines()
    
    i = 0
    while i < len(lines):
        line = lines[i]
        
        # 查找 Entry 订单
        if '📤 [velocityfollow] 步骤1: 下主单 Entry' in line:
            entry_match = re.search(r'side=(\w+).*price=(\d+)c.*size=([\d.]+)', line)
            time_match = re.search(r'\[25-12-27 (\d{2}:\d{2}:\d{2})\]', line)
            timestamp = time_match.group(1) if time_match else ""
            
            if entry_match:
                # 查找订单ID（在后续几行中）
                entry_order_id = None
                for j in range(i, min(i+10, len(lines))):
                    if '✅ [velocityfollow] 主单已提交' in lines[j]:
                        id_match = re.search(r'orderID=([\w]+)', lines[j])
                        if id_match:
                            entry_order_id = id_match.group(1)
                            break
                
                if entry_order_id:
                    # 查找对应的 Hedge 订单
                    hedge_order_id = None
                    hedge_match = None
                    for j in range(i, min(i+20, len(lines))):
                        if '📤 [velocityfollow] 步骤2: 下对冲单 Hedge' in lines[j]:
                            hedge_match = re.search(r'side=(\w+).*price=(\d+)c.*size=([\d.]+)', lines[j])
                            # 查找 hedge 订单ID
                            for k in range(j, min(j+10, len(lines))):
                                if '✅ [velocityfollow] 对冲单已提交' in lines[k]:
                                    hedge_id_match = re.search(r'orderID=([\w]+)', lines[k])
                                    if hedge_id_match:
                                        hedge_order_id = hedge_id_match.group(1)
                                        break
                            break
                    
                    if hedge_match and hedge_order_id:
                        all_pairs.append({
                            'cycle_id': cycle_id,
                            'timestamp': timestamp,
                            'entry_order_id': entry_order_id,
                            'entry_side': entry_match.group(1),
                            'entry_price_cents': int(entry_match.group(2)),
                            'entry_size': float(entry_match.group(3)),
                            'hedge_order_id': hedge_order_id,
                            'hedge_side': hedge_match.group(1),
                            'hedge_price_cents': int(hedge_match.group(2)),
                            'hedge_size': float(hedge_match.group(3))
                        })
        i += 1

# 输出表格
print("周期ID,时间,方向,Entry价格(c),Entry数量,Hedge价格(c),Hedge数量,总成本(USDC),UP盈亏(USDC),DOWN盈亏(USDC),Entry订单ID")
print("-" * 130)

total_cost = 0
total_up_profit = 0
total_down_profit = 0

for pair in sorted(all_pairs, key=lambda x: (x['cycle_id'], x['timestamp'])):
    entry_price = pair['entry_price_cents'] / 100.0
    entry_size = pair['entry_size']
    hedge_price = pair['hedge_price_cents'] / 100.0
    hedge_size = pair['hedge_size']
    
    cost = entry_price * entry_size + hedge_price * hedge_size
    total_cost += cost
    
    # 计算盈亏（假设结算时价格为 1.0）
    if pair['entry_side'] == 'up':
        up_profit = entry_size * (1.0 - entry_price)
        down_profit = hedge_size * (1.0 - hedge_price)
    else:
        up_profit = hedge_size * (1.0 - hedge_price)
        down_profit = entry_size * (1.0 - entry_price)
    
    total_up_profit += up_profit
    total_down_profit += down_profit
    
    print(f"{pair['cycle_id']},{pair['timestamp']},{pair['entry_side'].upper()},{pair['entry_price_cents']},{entry_size:.4f},{pair['hedge_price_cents']},{hedge_size:.4f},{cost:.2f},{up_profit:.2f},{down_profit:.2f},{pair['entry_order_id'][:25]}")

print("-" * 130)
print(f"总计,订单数: {len(all_pairs)}, 总成本: {total_cost:.2f} USDC, UP总盈亏: {total_up_profit:.2f} USDC, DOWN总盈亏: {total_down_profit:.2f} USDC, 总盈亏: {total_up_profit + total_down_profit:.2f} USDC")

