#!/usr/bin/env python3
"""
分析纸交易模式下的日志
"""
import re
import json
from collections import defaultdict
from datetime import datetime
from pathlib import Path

def parse_log_line(line):
    """解析日志行"""
    # 格式: [36mINFO[0m[25-12-31 02:31:09] message
    match = re.match(r'\[.*?\]\[(.*?)\] (.*)', line)
    if match:
        timestamp = match.group(1)
        message = match.group(2)
        return timestamp, message
    return None, line

def analyze_logs(log_file):
    """分析日志文件"""
    print(f"\n{'='*80}")
    print(f"分析日志文件: {log_file}")
    print(f"{'='*80}\n")
    
    stats = {
        'total_lines': 0,
        'entry_orders': [],
        'hedge_orders': [],
        'total_capital_checks': [],
        'hedge_checks': [],
        'errors': [],
        'warnings': [],
        'circuit_breaker': [],
        'position_updates': [],
    }
    
    current_entry = None
    
    try:
        with open(log_file, 'r', encoding='utf-8') as f:
            for line_num, line in enumerate(f, 1):
                stats['total_lines'] += 1
                timestamp, message = parse_log_line(line.strip())
                
                # 总资金检查
                if '总资金' in message or 'MaxTotalCapitalUSDC' in message:
                    stats['total_capital_checks'].append((line_num, timestamp, message))
                
                # 完全对冲检查
                if 'RequireFullyHedged' in message or '禁止开新单' in message or '已对冲' in message:
                    stats['hedge_checks'].append((line_num, timestamp, message))
                
                # Entry 订单
                if 'Entry' in message and ('订单' in message or '下单' in message):
                    stats['entry_orders'].append((line_num, timestamp, message))
                
                # Hedge 订单
                if 'hedge' in message.lower() and ('订单' in message or '下单' in message):
                    stats['hedge_orders'].append((line_num, timestamp, message))
                
                # 错误
                if 'ERROR' in line or '错误' in message or '失败' in message:
                    stats['errors'].append((line_num, timestamp, message))
                
                # 警告
                if 'WARN' in line or '⚠️' in message:
                    stats['warnings'].append((line_num, timestamp, message))
                
                # Circuit Breaker
                if 'Circuit Breaker' in message or 'circuit breaker' in message.lower():
                    stats['circuit_breaker'].append((line_num, timestamp, message))
                
                # 持仓更新
                if '持仓' in message or 'position' in message.lower():
                    stats['position_updates'].append((line_num, timestamp, message))
    
    except Exception as e:
        print(f"读取日志文件失败: {e}")
        return None
    
    # 打印统计信息
    print(f"📊 日志统计:")
    print(f"  总行数: {stats['total_lines']}")
    print(f"  Entry 订单: {len(stats['entry_orders'])}")
    print(f"  Hedge 订单: {len(stats['hedge_orders'])}")
    print(f"  总资金检查: {len(stats['total_capital_checks'])}")
    print(f"  对冲检查: {len(stats['hedge_checks'])}")
    print(f"  错误: {len(stats['errors'])}")
    print(f"  警告: {len(stats['warnings'])}")
    print(f"  Circuit Breaker: {len(stats['circuit_breaker'])}")
    print(f"  持仓更新: {len(stats['position_updates'])}")
    
    # 打印关键信息
    if stats['total_capital_checks']:
        print(f"\n💰 总资金检查记录 (最近10条):")
        for line_num, ts, msg in stats['total_capital_checks'][-10:]:
            print(f"  [{ts}] {msg}")
    
    if stats['hedge_checks']:
        print(f"\n🔒 对冲检查记录 (最近20条):")
        for line_num, ts, msg in stats['hedge_checks'][-20:]:
            print(f"  [{ts}] {msg}")
    
    if stats['entry_orders']:
        print(f"\n📈 Entry 订单记录 (最近10条):")
        for line_num, ts, msg in stats['entry_orders'][-10:]:
            print(f"  [{ts}] {msg}")
    
    if stats['hedge_orders']:
        print(f"\n🛡️ Hedge 订单记录 (最近10条):")
        for line_num, ts, msg in stats['hedge_orders'][-10:]:
            print(f"  [{ts}] {msg}")
    
    if stats['errors']:
        print(f"\n❌ 错误记录 (最近10条):")
        for line_num, ts, msg in stats['errors'][-10:]:
            print(f"  [{ts}] {msg}")
    
    if stats['warnings']:
        print(f"\n⚠️ 警告记录 (最近20条):")
        for line_num, ts, msg in stats['warnings'][-20:]:
            print(f"  [{ts}] {msg}")
    
    if stats['circuit_breaker']:
        print(f"\n🔒 Circuit Breaker 记录:")
        for line_num, ts, msg in stats['circuit_breaker']:
            print(f"  [{ts}] {msg}")
    
    return stats

def main():
    log_dir = Path('logs')
    
    # 找到最新的日志文件
    log_files = sorted(log_dir.glob('btc-updown-15m-*.log'), key=lambda p: p.stat().st_mtime, reverse=True)
    
    if not log_files:
        print("未找到日志文件")
        return
    
    print(f"找到 {len(log_files)} 个日志文件")
    
    # 分析最新的3个日志文件
    for log_file in log_files[:3]:
        stats = analyze_logs(log_file)
        if stats:
            print("\n")

if __name__ == '__main__':
    main()
