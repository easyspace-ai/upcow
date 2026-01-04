#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
分析交易日志，统计开单情况
"""

import re
import os
from collections import defaultdict
from datetime import datetime
from pathlib import Path

def parse_log_line(line):
    """解析日志行，提取时间戳、级别、组件和消息"""
    # 匹配格式: [级别][时间] 消息内容 [component=xxx]
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

def analyze_logs(log_dir="logs"):
    """分析日志文件"""
    log_files = list(Path(log_dir).glob("*.log"))
    
    stats = {
        'total_files': len(log_files),
        'orders': [],
        'order_attempts': [],
        'order_success': [],
        'order_failed': [],
        'strategy_triggers': [],
        'price_updates': 0,
        'orderbook_updates': 0,
        'markets': set(),
        'time_range': {'start': None, 'end': None},
        'skip_reasons': defaultdict(int),
        'cycle_end_protection': 0,
        'market_quality_skip': 0,
        'liquidity_skip': 0,
        'inventory_skip': 0
    }
    
    # 关键词模式
    order_patterns = {
        '下单': r'下单|place.*order|PlaceOrder',
        '模拟下单': r'模拟下单|纸交易.*下单|dry.*run.*order|📝.*纸交易',
        '下单成功': r'下单成功|order.*success|✅.*下单|主单已提交|订单已提交',
        '下单失败': r'下单失败|order.*fail|❌.*下单|主单下单失败',
        '订单创建': r'订单创建|创建订单|create.*order',
        '订单提交': r'订单提交|提交订单|submit.*order',
        '触发交易': r'触发|trigger|⚡.*触发',
        '策略下单': r'策略.*下单|strategy.*order|velocityfollow.*下单|步骤1.*下主单|下主单.*Entry|下对冲单|Hedge',
        '订单簿检查': r'订单簿流动性|订单簿无流动性|订单簿流动性不足|订单簿流动性充足',
        '策略触发实际': r'⚡.*触发\(|触发\(并发\)|触发\(顺序\)'
    }
    
    print(f"📊 开始分析 {len(log_files)} 个日志文件...\n")
    
    for log_file in log_files:
        print(f"📁 分析文件: {log_file.name}")
        file_stats = {
            'orders': 0,
            'order_attempts': 0,
            'order_success': 0,
            'order_failed': 0,
            'triggers': 0,
            'lines': 0
        }
        
        try:
            with open(log_file, 'r', encoding='utf-8', errors='ignore') as f:
                for line_num, line in enumerate(f, 1):
                    file_stats['lines'] += 1
                    parsed = parse_log_line(line)
                    
                    if parsed:
                        # 更新时间范围
                        if stats['time_range']['start'] is None or parsed['timestamp'] < stats['time_range']['start']:
                            stats['time_range']['start'] = parsed['timestamp']
                        if stats['time_range']['end'] is None or parsed['timestamp'] > stats['time_range']['end']:
                            stats['time_range']['end'] = parsed['timestamp']
                        
                        msg = parsed['message'].lower()
                        
                        # 统计价格更新
                        if '价格更新' in parsed['message'] or 'price' in msg:
                            stats['price_updates'] += 1
                        
                        # 统计订单簿更新
                        if '订单簿' in parsed['message'] or 'orderbook' in msg:
                            stats['orderbook_updates'] += 1
                        
                        # 提取市场信息
                        market_match = re.search(r'btc-updown-15m-(\d+)', parsed['message'])
                        if market_match:
                            stats['markets'].add(market_match.group(0))
                        
                        # 检查订单相关关键词
                        for key, pattern in order_patterns.items():
                            if re.search(pattern, parsed['message'], re.IGNORECASE):
                                if key == '下单' or key == '订单创建' or key == '订单提交':
                                    file_stats['order_attempts'] += 1
                                    stats['order_attempts'].append({
                                        'file': log_file.name,
                                        'line': line_num,
                                        'timestamp': parsed['timestamp'],
                                        'message': parsed['message'],
                                        'component': parsed['component']
                                    })
                                elif key == '模拟下单':
                                    file_stats['orders'] += 1
                                    stats['orders'].append({
                                        'file': log_file.name,
                                        'line': line_num,
                                        'timestamp': parsed['timestamp'],
                                        'message': parsed['message'],
                                        'component': parsed['component']
                                    })
                                elif key == '下单成功':
                                    file_stats['order_success'] += 1
                                    stats['order_success'].append({
                                        'file': log_file.name,
                                        'line': line_num,
                                        'timestamp': parsed['timestamp'],
                                        'message': parsed['message'],
                                        'component': parsed['component']
                                    })
                                elif key == '下单失败':
                                    file_stats['order_failed'] += 1
                                    stats['order_failed'].append({
                                        'file': log_file.name,
                                        'line': line_num,
                                        'timestamp': parsed['timestamp'],
                                        'message': parsed['message'],
                                        'component': parsed['component']
                                    })
                                elif key == '触发交易' or key == '策略下单':
                                    file_stats['triggers'] += 1
                                    stats['strategy_triggers'].append({
                                        'file': log_file.name,
                                        'line': line_num,
                                        'timestamp': parsed['timestamp'],
                                        'message': parsed['message'],
                                        'component': parsed['component']
                                    })
                        
                        # 特别查找 velocityfollow 策略的触发信息
                        if 'velocityfollow' in parsed['message'].lower():
                            if '触发' in parsed['message'] or 'trigger' in msg:
                                file_stats['triggers'] += 1
                                stats['strategy_triggers'].append({
                                    'file': log_file.name,
                                    'line': line_num,
                                    'timestamp': parsed['timestamp'],
                                    'message': parsed['message'],
                                    'component': parsed['component']
                                })
                            
                            # 统计跳过原因
                            if '跳过' in parsed['message']:
                                if '周期结束前保护' in parsed['message']:
                                    stats['cycle_end_protection'] += 1
                                    stats['skip_reasons']['周期结束前保护'] += 1
                                elif 'MarketQuality' in parsed['message'] or 'marketQuality' in msg:
                                    stats['market_quality_skip'] += 1
                                    stats['skip_reasons']['市场质量门控'] += 1
                                elif '库存偏斜' in parsed['message']:
                                    stats['inventory_skip'] += 1
                                    stats['skip_reasons']['库存偏斜保护'] += 1
                                elif '流动性' in parsed['message']:
                                    stats['liquidity_skip'] += 1
                                    stats['skip_reasons']['订单簿流动性不足'] += 1
                                else:
                                    stats['skip_reasons']['其他原因'] += 1
        
        except Exception as e:
            print(f"  ⚠️  读取文件出错: {e}")
            continue
        
        print(f"  - 总行数: {file_stats['lines']:,}")
        print(f"  - 订单尝试: {file_stats['order_attempts']}")
        print(f"  - 模拟下单: {file_stats['orders']}")
        print(f"  - 下单成功: {file_stats['order_success']}")
        print(f"  - 下单失败: {file_stats['order_failed']}")
        print(f"  - 策略触发: {file_stats['triggers']}")
        print()
    
    return stats

def print_summary(stats):
    """打印分析摘要"""
    print("=" * 80)
    print("📊 交易日志分析报告")
    print("=" * 80)
    print()
    
    print(f"📁 日志文件统计:")
    print(f"  - 总文件数: {stats['total_files']}")
    print(f"  - 时间范围: {stats['time_range']['start']} 至 {stats['time_range']['end']}")
    print(f"  - 涉及市场数: {len(stats['markets'])}")
    if stats['markets']:
        print(f"  - 市场列表: {', '.join(sorted(stats['markets']))}")
    print()
    
    print(f"📈 数据统计:")
    print(f"  - 价格更新次数: {stats['price_updates']:,}")
    print(f"  - 订单簿更新次数: {stats['orderbook_updates']:,}")
    print()
    
    print(f"📤 开单情况:")
    print(f"  - 订单尝试次数: {len(stats['order_attempts'])}")
    print(f"  - 模拟下单次数: {len(stats['orders'])}")
    print(f"  - 下单成功次数: {len(stats['order_success'])}")
    print(f"  - 下单失败次数: {len(stats['order_failed'])}")
    print(f"  - 策略触发次数: {len(stats['strategy_triggers'])}")
    print()
    
    if stats['order_attempts']:
        print("📋 订单尝试详情 (前10条):")
        for i, order in enumerate(stats['order_attempts'][:10], 1):
            print(f"  {i}. [{order['timestamp']}] {order['message'][:100]}")
        print()
    
    if stats['orders']:
        print("📝 模拟下单详情 (前10条):")
        for i, order in enumerate(stats['orders'][:10], 1):
            print(f"  {i}. [{order['timestamp']}] {order['message'][:100]}")
        print()
    
    if stats['order_success']:
        print("✅ 下单成功详情 (前10条):")
        for i, order in enumerate(stats['order_success'][:10], 1):
            print(f"  {i}. [{order['timestamp']}] {order['message'][:100]}")
        print()
    
    if stats['order_failed']:
        print("❌ 下单失败详情 (前10条):")
        for i, order in enumerate(stats['order_failed'][:10], 1):
            print(f"  {i}. [{order['timestamp']}] {order['message'][:100]}")
        print()
    
    if stats['strategy_triggers']:
        print("⚡ 策略触发详情 (前20条):")
        for i, trigger in enumerate(stats['strategy_triggers'][:20], 1):
            print(f"  {i}. [{trigger['timestamp']}] {trigger['message'][:120]}")
        print()
    
    # 按市场统计
    if stats['markets']:
        print("📊 按市场统计:")
        market_stats = defaultdict(int)
        for order in stats['orders'] + stats['order_success'] + stats['order_failed']:
            market_match = re.search(r'btc-updown-15m-(\d+)', order['message'])
            if market_match:
                market_stats[market_match.group(0)] += 1
        
        for market in sorted(stats['markets']):
            count = market_stats.get(market, 0)
            print(f"  - {market}: {count} 次订单")
        print()
    
    # 跳过原因统计
    if stats['skip_reasons']:
        print("⏸️  策略跳过原因统计:")
        for reason, count in sorted(stats['skip_reasons'].items(), key=lambda x: x[1], reverse=True):
            print(f"  - {reason}: {count:,} 次")
        print()
    
    # 结论
    print("=" * 80)
    print("📌 分析结论:")
    print("=" * 80)
    
    if len(stats['orders']) == 0 and len(stats['order_success']) == 0:
        print("⚠️  未发现任何实际开单记录")
        print()
        print("   主要原因分析:")
        if stats['cycle_end_protection'] > 0:
            print(f"   1. 周期结束前保护: {stats['cycle_end_protection']:,} 次跳过")
            print("      (策略在周期结束前2-3分钟停止交易，避免周期切换风险)")
        if stats['market_quality_skip'] > 0:
            print(f"   2. 市场质量门控: {stats['market_quality_skip']:,} 次跳过")
            print("      (市场质量分数低于阈值，订单簿质量不满足交易条件)")
        if stats['liquidity_skip'] > 0:
            print(f"   3. 订单簿流动性不足: {stats['liquidity_skip']:,} 次跳过")
        if stats['inventory_skip'] > 0:
            print(f"   4. 库存偏斜保护: {stats['inventory_skip']:,} 次跳过")
        
        print()
        print("   其他可能原因:")
        print("   - 速度/价格变化未达到策略阈值")
        print("   - 纸交易模式下日志格式可能不同")
        print("   - 策略配置了 oncePerCycle=true，每个周期只交易一次")
        print("   - 市场质量门控 (enableMarketQualityGate=true) 过滤了大部分交易机会")
    else:
        print(f"✅ 发现 {len(stats['orders']) + len(stats['order_success'])} 次开单记录")
        if len(stats['order_failed']) > 0:
            print(f"⚠️  发现 {len(stats['order_failed'])} 次下单失败")
    
    if len(stats['strategy_triggers']) > 0:
        print(f"📊 策略共触发 {len(stats['strategy_triggers']):,} 次价格事件")
        if len(stats['orders']) == 0:
            print("   但未发现对应的下单记录")
            print("   说明: 价格事件触发不等于交易触发，策略有多个过滤条件")
    
    print()

if __name__ == "__main__":
    stats = analyze_logs()
    print_summary(stats)

