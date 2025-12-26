#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
详细分析交易日志，查找策略决策过程
"""

import re
import os
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
        component = match.groups()[7] if len(match.groups()) > 7 else ""
        field = match.groups()[8] if len(match.groups()) > 8 else ""
        
        try:
            timestamp = datetime(int(f"20{year}"), int(month), int(day), 
                               int(hour), int(minute), int(second))
            return {
                'timestamp': timestamp,
                'message': message,
                'component': component,
                'field': field
            }
        except:
            pass
    return None

def analyze_logs_detailed(log_dir="logs"):
    """详细分析日志文件"""
    log_files = list(Path(log_dir).glob("*.log"))
    
    stats = {
        'total_files': len(log_files),
        'actual_triggers': [],      # 实际交易触发
        'skip_reasons': defaultdict(int),
        'skip_details': [],
        'market_quality': [],
        'liquidity_checks': [],
        'speed_calculations': [],
        'time_range': {'start': None, 'end': None}
    }
    
    print(f"📊 开始详细分析 {len(log_files)} 个日志文件...\n")
    
    for log_file in log_files:
        if log_file.name == 'combined_2025-12-26_14-00.log':
            continue  # 跳过空文件
            
        print(f"📁 分析文件: {log_file.name}")
        file_stats = {
            'triggers': 0,
            'skips': 0,
            'market_quality': 0,
            'liquidity': 0
        }
        
        try:
            with open(log_file, 'r', encoding='utf-8', errors='ignore') as f:
                for line_num, line in enumerate(f, 1):
                    parsed = parse_log_line(line)
                    
                    if parsed:
                        # 更新时间范围
                        if stats['time_range']['start'] is None or parsed['timestamp'] < stats['time_range']['start']:
                            stats['time_range']['start'] = parsed['timestamp']
                        if stats['time_range']['end'] is None or parsed['timestamp'] > stats['time_range']['end']:
                            stats['time_range']['end'] = parsed['timestamp']
                        
                        msg = parsed['message']
                        
                        # 实际交易触发（关键日志）
                        if '⚡' in msg and '触发' in msg and ('顺序' in msg or '并发' in msg):
                            file_stats['triggers'] += 1
                            stats['actual_triggers'].append({
                                'file': log_file.name,
                                'line': line_num,
                                'timestamp': parsed['timestamp'],
                                'message': msg,
                                'component': parsed['component']
                            })
                        
                        # 跳过原因统计
                        if '跳过' in msg or '⏸️' in msg or '⏭️' in msg:
                            file_stats['skips'] += 1
                            if '周期结束前保护' in msg:
                                stats['skip_reasons']['周期结束前保护'] += 1
                            elif '价差过大' in msg:
                                stats['skip_reasons']['价差过大'] += 1
                            elif '已达上限' in msg:
                                stats['skip_reasons']['交易次数已达上限'] += 1
                            elif 'MarketQuality' in msg or '市场质量' in msg:
                                stats['skip_reasons']['市场质量门控'] += 1
                                stats['market_quality'].append({
                                    'file': log_file.name,
                                    'line': line_num,
                                    'timestamp': parsed['timestamp'],
                                    'message': msg
                                })
                            elif '流动性' in msg:
                                stats['skip_reasons']['订单簿流动性不足'] += 1
                                stats['liquidity_checks'].append({
                                    'file': log_file.name,
                                    'line': line_num,
                                    'timestamp': parsed['timestamp'],
                                    'message': msg
                                })
                            elif '冷却期' in msg or '冷却' in msg:
                                stats['skip_reasons']['冷却期'] += 1
                            else:
                                stats['skip_reasons']['其他原因'] += 1
                                stats['skip_details'].append({
                                    'file': log_file.name,
                                    'line': line_num,
                                    'timestamp': parsed['timestamp'],
                                    'message': msg[:200]
                                })
                        
                        # 市场质量信息
                        if 'MarketQuality' in msg or '质量分数' in msg or '质量分' in msg:
                            stats['market_quality'].append({
                                'file': log_file.name,
                                'line': line_num,
                                'timestamp': parsed['timestamp'],
                                'message': msg
                            })
                        
                        # 订单簿流动性检查
                        if '订单簿流动性' in msg or '流动性充足' in msg or '流动性不足' in msg:
                            stats['liquidity_checks'].append({
                                'file': log_file.name,
                                'line': line_num,
                                'timestamp': parsed['timestamp'],
                                'message': msg
                            })
        
        except Exception as e:
            print(f"  ⚠️  读取文件出错: {e}")
            continue
        
        print(f"  - 实际触发: {file_stats['triggers']}")
        print(f"  - 跳过次数: {file_stats['skips']}")
        print(f"  - 市场质量检查: {file_stats['market_quality']}")
        print(f"  - 流动性检查: {file_stats['liquidity']}")
        print()
    
    return stats

def print_detailed_summary(stats):
    """打印详细摘要"""
    print("=" * 80)
    print("📊 详细交易日志分析报告")
    print("=" * 80)
    print()
    
    print(f"📁 日志文件统计:")
    print(f"  - 总文件数: {stats['total_files']}")
    print(f"  - 时间范围: {stats['time_range']['start']} 至 {stats['time_range']['end']}")
    print()
    
    print(f"⚡ 实际交易触发:")
    print(f"  - 触发次数: {len(stats['actual_triggers'])}")
    if stats['actual_triggers']:
        print("  详情:")
        for i, trigger in enumerate(stats['actual_triggers'][:10], 1):
            print(f"    {i}. [{trigger['timestamp']}] {trigger['message'][:120]}")
    else:
        print("  ⚠️  未发现任何实际交易触发记录")
    print()
    
    print(f"⏸️  跳过原因统计:")
    total_skips = sum(stats['skip_reasons'].values())
    for reason, count in sorted(stats['skip_reasons'].items(), key=lambda x: x[1], reverse=True):
        percentage = (count / total_skips * 100) if total_skips > 0 else 0
        print(f"  - {reason}: {count:,} 次 ({percentage:.1f}%)")
    print()
    
    if stats['market_quality']:
        print(f"📊 市场质量检查记录 (前10条):")
        for i, mq in enumerate(stats['market_quality'][:10], 1):
            print(f"    {i}. [{mq['timestamp']}] {mq['message'][:120]}")
        print()
    
    if stats['liquidity_checks']:
        print(f"💧 订单簿流动性检查记录 (前10条):")
        for i, liq in enumerate(stats['liquidity_checks'][:10], 1):
            print(f"    {i}. [{liq['timestamp']}] {liq['message'][:120]}")
        print()
    
    if stats['skip_details']:
        print(f"🔍 其他跳过原因详情 (前20条):")
        for i, skip in enumerate(stats['skip_details'][:20], 1):
            print(f"    {i}. [{skip['timestamp']}] {skip['message']}")
        print()
    
    # 结论
    print("=" * 80)
    print("📌 分析结论:")
    print("=" * 80)
    
    if len(stats['actual_triggers']) == 0:
        print("⚠️  未发现任何实际交易触发记录")
        print()
        print("可能原因:")
        if stats['skip_reasons']['周期结束前保护'] > 0:
            print(f"  1. 周期结束前保护: {stats['skip_reasons']['周期结束前保护']:,} 次跳过")
            print("     - 这是主要限制因素，每个周期只有前12分钟可以交易")
        if stats['skip_reasons']['市场质量门控'] > 0:
            print(f"  2. 市场质量门控: {stats['skip_reasons']['市场质量门控']:,} 次跳过")
            print("     - 市场质量分数可能不满足要求")
        if stats['skip_reasons']['订单簿流动性不足'] > 0:
            print(f"  3. 订单簿流动性不足: {stats['skip_reasons']['订单簿流动性不足']:,} 次跳过")
        if stats['skip_reasons']['冷却期'] > 0:
            print(f"  4. 冷却期限制: {stats['skip_reasons']['冷却期']:,} 次跳过")
        if stats['skip_reasons']['交易次数已达上限'] > 0:
            print(f"  5. 交易次数已达上限: {stats['skip_reasons']['交易次数已达上限']:,} 次跳过")
        
        print()
        print("建议:")
        print("  1. 检查速度计算结果，确认是否达到 minVelocityCentsPerSec: 0.15")
        print("  2. 检查市场质量分数，确认是否满足 marketQualityMinScore: 30")
        print("  3. 考虑临时缩短周期结束前保护时间进行测试")
    else:
        print(f"✅ 发现 {len(stats['actual_triggers'])} 次实际交易触发")
        print("  说明策略逻辑正常工作，有交易机会时会触发")
    
    print()

if __name__ == "__main__":
    stats = analyze_logs_detailed()
    print_detailed_summary(stats)

