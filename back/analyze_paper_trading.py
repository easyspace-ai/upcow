#!/usr/bin/env python3
"""
分析纸交易模式下的日志 - 重点关注新功能
"""
import re
import sys
from collections import defaultdict
from pathlib import Path

def analyze_log_file(log_path):
    """分析单个日志文件"""
    print(f"\n{'='*80}")
    print(f"📋 分析日志: {log_path}")
    print(f"{'='*80}\n")
    
    stats = {
        'total_capital_checks': [],
        'hedge_checks': [],
        'entry_orders': [],
        'hedge_orders': [],
        'errors': [],
        'warnings': [],
        'circuit_breaker': [],
        'position_info': [],
    }
    
    try:
        with open(log_path, 'r', encoding='utf-8', errors='ignore') as f:
            for line_num, line in enumerate(f, 1):
                line = line.strip()
                if not line:
                    continue
                
                # 提取时间戳和消息
                timestamp_match = re.search(r'\[(\d{2}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})\]', line)
                timestamp = timestamp_match.group(1) if timestamp_match else "N/A"
                
                # 总资金检查
                if '总资金' in line or 'MaxTotalCapitalUSDC' in line or 'totalCapital' in line.lower():
                    stats['total_capital_checks'].append((line_num, timestamp, line))
                
                # 完全对冲检查
                if any(keyword in line for keyword in [
                    'RequireFullyHedged', '禁止开新单', '已对冲', '未对冲', 
                    'manageExistingExposure', 'remaining', 'hedgeOrderID'
                ]):
                    stats['hedge_checks'].append((line_num, timestamp, line))
                
                # Entry 订单
                if 'Entry' in line and ('订单' in line or '下单' in line or 'order' in line.lower()):
                    stats['entry_orders'].append((line_num, timestamp, line))
                
                # Hedge 订单
                if 'hedge' in line.lower() and ('订单' in line or '下单' in line or 'order' in line.lower()):
                    stats['hedge_orders'].append((line_num, timestamp, line))
                
                # 错误
                if 'ERROR' in line or '错误' in line or '失败' in line:
                    stats['errors'].append((line_num, timestamp, line))
                
                # 警告
                if 'WARN' in line or '⚠️' in line or '🚫' in line:
                    stats['warnings'].append((line_num, timestamp, line))
                
                # Circuit Breaker
                if 'Circuit Breaker' in line or 'circuit breaker' in line.lower():
                    stats['circuit_breaker'].append((line_num, timestamp, line))
                
                # 持仓信息
                if '持仓' in line or 'position' in line.lower() or 'upSize' in line or 'downSize' in line:
                    stats['position_info'].append((line_num, timestamp, line))
    
    except FileNotFoundError:
        print(f"❌ 文件不存在: {log_path}")
        return None
    except Exception as e:
        print(f"❌ 读取文件失败: {e}")
        return None
    
    # 打印统计
    print(f"📊 统计信息:")
    print(f"  总资金检查: {len(stats['total_capital_checks'])} 次")
    print(f"  对冲检查: {len(stats['hedge_checks'])} 次")
    print(f"  Entry 订单: {len(stats['entry_orders'])} 次")
    print(f"  Hedge 订单: {len(stats['hedge_orders'])} 次")
    print(f"  错误: {len(stats['errors'])} 次")
    print(f"  警告: {len(stats['warnings'])} 次")
    print(f"  Circuit Breaker: {len(stats['circuit_breaker'])} 次")
    print(f"  持仓信息: {len(stats['position_info'])} 次")
    
    # 打印关键信息
    if stats['total_capital_checks']:
        print(f"\n💰 总资金检查记录 (最近10条):")
        for line_num, ts, msg in stats['total_capital_checks'][-10:]:
            # 提取关键信息
            if '限制' in msg or '禁止' in msg:
                print(f"  [{ts}] ⚠️ {msg[:150]}")
            else:
                print(f"  [{ts}] {msg[:150]}")
    
    if stats['hedge_checks']:
        print(f"\n🔒 对冲检查记录 (最近20条):")
        for line_num, ts, msg in stats['hedge_checks'][-20:]:
            # 高亮显示禁止开新单的记录
            if '禁止开新单' in msg or '🚫' in msg:
                print(f"  [{ts}] 🚫 {msg[:150]}")
            elif '已对冲' in msg:
                print(f"  [{ts}] ✅ {msg[:150]}")
            else:
                print(f"  [{ts}] {msg[:150]}")
    
    if stats['entry_orders']:
        print(f"\n📈 Entry 订单记录 (最近10条):")
        for line_num, ts, msg in stats['entry_orders'][-10:]:
            print(f"  [{ts}] {msg[:150]}")
    
    if stats['hedge_orders']:
        print(f"\n🛡️ Hedge 订单记录 (最近10条):")
        for line_num, ts, msg in stats['hedge_orders'][-10:]:
            if '失败' in msg or '失败' in msg:
                print(f"  [{ts}] ❌ {msg[:150]}")
            elif '成功' in msg or '✅' in msg:
                print(f"  [{ts}] ✅ {msg[:150]}")
            else:
                print(f"  [{ts}] {msg[:150]}")
    
    if stats['errors']:
        print(f"\n❌ 错误记录 (最近10条):")
        for line_num, ts, msg in stats['errors'][-10:]:
            print(f"  [{ts}] {msg[:150]}")
    
    if stats['warnings']:
        print(f"\n⚠️ 警告记录 (最近20条):")
        for line_num, ts, msg in stats['warnings'][-20:]:
            print(f"  [{ts}] {msg[:150]}")
    
    if stats['circuit_breaker']:
        print(f"\n🔒 Circuit Breaker 记录:")
        for line_num, ts, msg in stats['circuit_breaker']:
            print(f"  [{ts}] {msg[:150]}")
    
    return stats

def main():
    # 支持命令行参数指定日志文件
    if len(sys.argv) > 1:
        log_files = sys.argv[1:]
    else:
        # 默认查找 logs 目录下的最新日志
        log_dirs = [
            Path('logs'),
            Path('.'),
            Path('..'),
        ]
        
        log_files = []
        for log_dir in log_dirs:
            if log_dir.exists():
                # 查找所有 .log 文件
                found = list(log_dir.glob('*.log'))
                if found:
                    log_files.extend(found)
                    break
        
        if not log_files:
            print("❌ 未找到日志文件")
            print("用法: python3 analyze_paper_trading.py [日志文件路径...]")
            print("或者将日志文件放在当前目录或 logs/ 目录下")
            return
    
    if not log_files:
        print("❌ 未找到日志文件")
        return
    
    # 按修改时间排序，最新的在前
    log_files = sorted(log_files, key=lambda p: p.stat().st_mtime if p.exists() else 0, reverse=True)
    
    print(f"找到 {len(log_files)} 个日志文件")
    
    # 分析所有日志文件
    for log_file in log_files[:5]:  # 最多分析5个
        stats = analyze_log_file(log_file)
        if stats:
            print("\n")

if __name__ == '__main__':
    main()
