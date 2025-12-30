#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
分析 velocityhedgehold 策略的交易日志
"""

import re
import json
from collections import defaultdict
from datetime import datetime
from pathlib import Path

def parse_log_line(line):
    """解析日志行，提取关键信息"""
    # 移除 ANSI 颜色代码
    line = re.sub(r'\x1b\[[0-9;]*m', '', line)
    
    # 提取时间戳
    time_match = re.search(r'\[(\d{2}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})\]', line)
    timestamp = time_match.group(1) if time_match else None
    
    # 提取策略相关日志
    if 'velocityhedgehold' not in line.lower():
        return None
    
    result = {
        'timestamp': timestamp,
        'line': line.strip(),
        'type': None,
        'data': {}
    }
    
    # 触发条件满足
    if '触发条件满足' in line:
        match = re.search(r'winner=(\w+)\s+vel=([\d.]+)\(c/s\)\s+delta=([\d.]+)c/([\d.]+)s', line)
        if match:
            result['type'] = 'trigger'
            result['data'] = {
                'winner': match.group(1),
                'velocity': float(match.group(2)),
                'delta_cents': float(match.group(3)),
                'delta_seconds': float(match.group(4))
            }
    
    # Entry 订单已提交
    elif 'Entry 订单已提交' in line:
        match = re.search(r'orderID=([^\s]+)\s+side=(\w+)\s+price=(\d+)c\s+size=([\d.]+)', line)
        if match:
            result['type'] = 'entry_submitted'
            result['data'] = {
                'order_id': match.group(1),
                'side': match.group(2),
                'price_cents': int(match.group(3)),
                'size': float(match.group(4))
            }
    
    # Entry 已成交并已挂 Hedge
    elif 'Entry 已成交并已挂 Hedge' in line:
        match = re.search(r'entryID=([^\s]+)\s+filled=([\d.]+)@(\d+)c\s+hedgeID=([^\s]+)\s+limit=(\d+)c\s+unhedgedMax=(\d+)s\s+sl=(\d+)c\s+tradesCount=(\d+)/(\d+)', line)
        if match:
            result['type'] = 'entry_filled_hedge_placed'
            result['data'] = {
                'entry_id': match.group(1),
                'entry_filled_size': float(match.group(2)),
                'entry_price_cents': int(match.group(3)),
                'hedge_id': match.group(4),
                'hedge_limit_cents': int(match.group(5)),
                'unhedged_max_seconds': int(match.group(6)),
                'stop_loss_cents': int(match.group(7)),
                'trades_count': int(match.group(8)),
                'max_trades': int(match.group(9))
            }
    
    # Hedge 订单已提交
    elif 'Hedge 订单已提交' in line:
        match = re.search(r'orderID=([^\s]+)\s+side=(\w+)\s+price=(\d+)c\s+size=([\d.]+)\s+entryOrderID=([^\s]+)', line)
        if match:
            result['type'] = 'hedge_submitted'
            result['data'] = {
                'hedge_id': match.group(1),
                'side': match.group(2),
                'price_cents': int(match.group(3)),
                'size': float(match.group(4)),
                'entry_id': match.group(5)
            }
    
    # 监控结束：仓位已对冲
    elif '监控结束：仓位已对冲' in line:
        match = re.search(r'up=([\d.]+)\s+down=([\d.]+)', line)
        if match:
            result['type'] = 'hedge_completed'
            result['data'] = {
                'up_size': float(match.group(1)),
                'down_size': float(match.group(2))
            }
    
    # 止损触发
    elif '止损触发' in line or '未对冲止损触发' in line:
        match = re.search(r'(价格|超时).*?entryOrderID=([^\s]+)\s+hedgeOrderID=([^\s]+)', line)
        if match:
            result['type'] = 'stop_loss'
            result['data'] = {
                'reason': match.group(1),
                'entry_id': match.group(2),
                'hedge_id': match.group(3)
            }
    
    # 周期切换
    elif '周期切换：交易计数器已重置' in line:
        match = re.search(r'tradesCount=(\d+)\s+maxTradesPerCycle=(\d+)', line)
        if match:
            result['type'] = 'cycle_reset'
            result['data'] = {
                'trades_count': int(match.group(1)),
                'max_trades': int(match.group(2))
            }
    
    return result if result['type'] else None

def analyze_log_file(log_file_path):
    """分析单个日志文件"""
    print(f"\n{'='*80}")
    print(f"分析日志文件: {log_file_path}")
    print(f"{'='*80}\n")
    
    trades = []
    current_trade = None
    
    with open(log_file_path, 'r', encoding='utf-8') as f:
        for line in f:
            parsed = parse_log_line(line)
            if not parsed:
                continue
            
            if parsed['type'] == 'trigger':
                if current_trade:
                    trades.append(current_trade)
                current_trade = {
                    'timestamp': parsed['timestamp'],
                    'trigger': parsed['data'],
                    'entry': None,
                    'hedge': None,
                    'hedge_completed': None,
                    'stop_loss': None
                }
            
            elif parsed['type'] == 'entry_submitted' and current_trade:
                current_trade['entry'] = parsed['data']
            
            elif parsed['type'] == 'entry_filled_hedge_placed' and current_trade:
                current_trade['entry_filled'] = parsed['data']
            
            elif parsed['type'] == 'hedge_submitted' and current_trade:
                current_trade['hedge'] = parsed['data']
            
            elif parsed['type'] == 'hedge_completed' and current_trade:
                current_trade['hedge_completed'] = parsed['data']
            
            elif parsed['type'] == 'stop_loss' and current_trade:
                current_trade['stop_loss'] = parsed['data']
    
    if current_trade:
        trades.append(current_trade)
    
    return trades

def print_summary(trades):
    """打印分析摘要"""
    if not trades:
        print("未找到任何交易记录")
        return
    
    print(f"\n{'='*80}")
    print("策略运行摘要")
    print(f"{'='*80}\n")
    
    print(f"总交易次数: {len(trades)}")
    
    # 统计方向
    directions = defaultdict(int)
    for trade in trades:
        if trade.get('trigger'):
            directions[trade['trigger']['winner']] += 1
    
    print(f"\n交易方向分布:")
    for direction, count in directions.items():
        print(f"  {direction.upper()}: {count} 次")
    
    # 统计对冲完成情况
    hedge_completed = sum(1 for t in trades if t.get('hedge_completed'))
    stop_loss_count = sum(1 for t in trades if t.get('stop_loss'))
    
    print(f"\n对冲完成: {hedge_completed}/{len(trades)}")
    print(f"止损触发: {stop_loss_count}/{len(trades)}")
    
    # 计算平均速度
    velocities = [t['trigger']['velocity'] for t in trades if t.get('trigger')]
    if velocities:
        avg_velocity = sum(velocities) / len(velocities)
        print(f"\n平均触发速度: {avg_velocity:.3f} c/s")
        print(f"最小速度: {min(velocities):.3f} c/s")
        print(f"最大速度: {max(velocities):.3f} c/s")
    
    # 详细交易信息
    print(f"\n{'='*80}")
    print("详细交易记录")
    print(f"{'='*80}\n")
    
    for i, trade in enumerate(trades, 1):
        print(f"交易 #{i}")
        print(f"  时间: {trade.get('timestamp', 'N/A')}")
        
        if trade.get('trigger'):
            trigger = trade['trigger']
            print(f"  触发方向: {trigger['winner'].upper()}")
            print(f"  速度: {trigger['velocity']:.3f} c/s")
            print(f"  价格变化: {trigger['delta_cents']:.1f} cents / {trigger['delta_seconds']:.1f} seconds")
        
        if trade.get('entry_filled'):
            entry = trade['entry_filled']
            print(f"  Entry: {entry['entry_filled_size']:.4f} @ {entry['entry_price_cents']}c")
            print(f"  Hedge: limit={entry['hedge_limit_cents']}c, max_wait={entry['unhedged_max_seconds']}s, sl={entry['stop_loss_cents']}c")
            print(f"  交易计数: {entry['trades_count']}/{entry['max_trades']}")
        
        if trade.get('hedge_completed'):
            hedge = trade['hedge_completed']
            print(f"  ✅ 对冲完成: UP={hedge['up_size']:.4f}, DOWN={hedge['down_size']:.4f}")
        
        if trade.get('stop_loss'):
            sl = trade['stop_loss']
            print(f"  ⚠️  止损触发: {sl['reason']}")
        
        print()

def main():
    """主函数"""
    log_dir = Path('logs')
    
    # 查找所有包含 velocityhedgehold 的日志文件
    log_files = []
    for log_file in log_dir.glob('*.log'):
        # 检查文件是否包含 velocityhedgehold
        try:
            with open(log_file, 'r', encoding='utf-8') as f:
                if 'velocityhedgehold' in f.read().lower():
                    log_files.append(log_file)
        except:
            pass
    
    if not log_files:
        print("未找到包含 velocityhedgehold 策略的日志文件")
        return
    
    # 按修改时间排序，最新的在前
    log_files.sort(key=lambda x: x.stat().st_mtime, reverse=True)
    
    all_trades = []
    for log_file in log_files:
        trades = analyze_log_file(log_file)
        all_trades.extend(trades)
    
    # 打印总体摘要
    print_summary(all_trades)
    
    # 保存详细结果到 JSON
    output_file = Path('velocityhedgehold_analysis.json')
    with open(output_file, 'w', encoding='utf-8') as f:
        json.dump(all_trades, f, indent=2, ensure_ascii=False)
    print(f"\n详细分析结果已保存到: {output_file}")

if __name__ == '__main__':
    main()
