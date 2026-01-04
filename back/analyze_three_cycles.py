#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
分析三个交易周期的开单情况
"""

import re
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
                'field': field,
                'raw': line.strip()
            }
        except:
            pass
    return None

def analyze_cycle_log(log_file):
    """分析单个周期的日志"""
    cycle_name = log_file.stem.replace('btc-updown-15m-', '')
    
    stats = {
        'cycle': cycle_name,
        'file': log_file.name,
        'orders': [],
        'order_attempts': [],
        'order_success': [],
        'order_failed': [],
        'strategy_triggers': [],
        'skip_reasons': defaultdict(int),
        'cycle_end_protection': 0,
        'market_quality_skip': 0,
        'liquidity_skip': 0,
        'inventory_skip': 0,
        'cooldown_skip': 0,
        'price_events': 0,
        'time_range': {'start': None, 'end': None},
        'main_orders': [],  # 主单
        'hedge_orders': [],  # 对冲单
    }
    
    print(f"\n{'='*80}")
    print(f"📊 分析周期: {cycle_name}")
    print(f"📁 文件: {log_file.name}")
    print(f"{'='*80}")
    
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
                    msg_lower = msg.lower()
                    
                    # 统计价格事件
                    if '价格更新' in msg or '价格触发' in msg:
                        stats['price_events'] += 1
                    
                    # 查找策略触发
                    if 'velocityfollow' in msg_lower and ('触发' in msg or 'trigger' in msg_lower):
                        if '⚡' in msg or '触发' in msg:
                            stats['strategy_triggers'].append({
                                'line': line_num,
                                'timestamp': parsed['timestamp'],
                                'message': msg,
                                'component': parsed['component']
                            })
                    
                    # 查找下单相关
                    if any(keyword in msg for keyword in ['下主单', 'Entry', '下对冲单', 'Hedge', '下单', 'place order', 'PlaceOrder']):
                        if '下主单' in msg or 'Entry' in msg:
                            stats['main_orders'].append({
                                'line': line_num,
                                'timestamp': parsed['timestamp'],
                                'message': msg,
                                'component': parsed['component']
                            })
                        elif '下对冲单' in msg or 'Hedge' in msg:
                            stats['hedge_orders'].append({
                                'line': line_num,
                                'timestamp': parsed['timestamp'],
                                'message': msg,
                                'component': parsed['component']
                            })
                        
                        stats['order_attempts'].append({
                            'line': line_num,
                            'timestamp': parsed['timestamp'],
                            'message': msg,
                            'component': parsed['component']
                        })
                    
                    # 查找订单成功
                    if any(keyword in msg for keyword in ['下单成功', 'order.*success', '✅.*下单', '主单已提交', '订单已提交', '订单创建成功']):
                        stats['order_success'].append({
                            'line': line_num,
                            'timestamp': parsed['timestamp'],
                            'message': msg,
                            'component': parsed['component']
                        })
                    
                    # 查找订单失败
                    if any(keyword in msg for keyword in ['下单失败', 'order.*fail', '❌.*下单', '主单下单失败']):
                        stats['order_failed'].append({
                            'line': line_num,
                            'timestamp': parsed['timestamp'],
                            'message': msg,
                            'component': parsed['component']
                        })
                    
                    # 查找模拟下单（纸交易）
                    if any(keyword in msg for keyword in ['模拟下单', '纸交易.*下单', 'dry.*run.*order', '📝.*纸交易']):
                        stats['orders'].append({
                            'line': line_num,
                            'timestamp': parsed['timestamp'],
                            'message': msg,
                            'component': parsed['component']
                        })
                    
                    # 统计跳过原因
                    if '跳过' in msg or 'skip' in msg_lower:
                        if '周期结束前保护' in msg or 'cycle.*end' in msg_lower:
                            stats['cycle_end_protection'] += 1
                            stats['skip_reasons']['周期结束前保护'] += 1
                        elif 'MarketQuality' in msg or 'marketQuality' in msg_lower or '市场质量' in msg:
                            stats['market_quality_skip'] += 1
                            stats['skip_reasons']['市场质量门控'] += 1
                        elif '库存偏斜' in msg or 'inventory' in msg_lower:
                            stats['inventory_skip'] += 1
                            stats['skip_reasons']['库存偏斜保护'] += 1
                        elif '流动性' in msg or 'liquidity' in msg_lower:
                            stats['liquidity_skip'] += 1
                            stats['skip_reasons']['订单簿流动性不足'] += 1
                        elif '冷却期' in msg or 'cooldown' in msg_lower:
                            stats['cooldown_skip'] += 1
                            stats['skip_reasons']['冷却期保护'] += 1
                        else:
                            stats['skip_reasons']['其他原因'] += 1
    
    except Exception as e:
        print(f"  ⚠️  读取文件出错: {e}")
        return stats
    
    return stats

def print_cycle_summary(stats):
    """打印周期分析摘要"""
    print(f"\n📈 周期统计:")
    print(f"  - 时间范围: {stats['time_range']['start']} 至 {stats['time_range']['end']}")
    print(f"  - 价格事件数: {stats['price_events']:,}")
    print(f"  - 策略触发次数: {len(stats['strategy_triggers'])}")
    print(f"  - 订单尝试次数: {len(stats['order_attempts'])}")
    print(f"  - 主单尝试次数: {len(stats['main_orders'])}")
    print(f"  - 对冲单尝试次数: {len(stats['hedge_orders'])}")
    print(f"  - 模拟下单次数: {len(stats['orders'])}")
    print(f"  - 下单成功次数: {len(stats['order_success'])}")
    print(f"  - 下单失败次数: {len(stats['order_failed'])}")
    
    if stats['skip_reasons']:
        print(f"\n⏸️  跳过原因统计:")
        for reason, count in sorted(stats['skip_reasons'].items(), key=lambda x: x[1], reverse=True):
            print(f"  - {reason}: {count:,} 次")
    
    if stats['strategy_triggers']:
        print(f"\n⚡ 策略触发详情 (前5条):")
        for i, trigger in enumerate(stats['strategy_triggers'][:5], 1):
            print(f"  {i}. [{trigger['timestamp']}] {trigger['message'][:100]}")
    
    if stats['main_orders']:
        print(f"\n📤 主单尝试详情:")
        for i, order in enumerate(stats['main_orders'], 1):
            print(f"  {i}. [{order['timestamp']}] {order['message'][:120]}")
    
    if stats['hedge_orders']:
        print(f"\n🔄 对冲单尝试详情:")
        for i, order in enumerate(stats['hedge_orders'], 1):
            print(f"  {i}. [{order['timestamp']}] {order['message'][:120]}")
    
    if stats['order_success']:
        print(f"\n✅ 下单成功详情:")
        for i, order in enumerate(stats['order_success'], 1):
            print(f"  {i}. [{order['timestamp']}] {order['message'][:120]}")
    
    if stats['order_failed']:
        print(f"\n❌ 下单失败详情:")
        for i, order in enumerate(stats['order_failed'], 1):
            print(f"  {i}. [{order['timestamp']}] {order['message'][:120]}")
    
    if stats['orders']:
        print(f"\n📝 模拟下单详情:")
        for i, order in enumerate(stats['orders'], 1):
            print(f"  {i}. [{order['timestamp']}] {order['message'][:120]}")
    
    # 结论
    print(f"\n📌 开单情况总结:")
    if len(stats['orders']) > 0 or len(stats['order_success']) > 0:
        print(f"  ✅ 本周期有开单记录")
        print(f"     - 模拟下单: {len(stats['orders'])} 次")
        print(f"     - 实际下单成功: {len(stats['order_success'])} 次")
        print(f"     - 下单失败: {len(stats['order_failed'])} 次")
    else:
        print(f"  ⚠️  本周期未发现开单记录")
        if len(stats['strategy_triggers']) > 0:
            print(f"     - 策略触发了 {len(stats['strategy_triggers'])} 次，但未开单")
        if stats['skip_reasons']:
            print(f"     - 主要跳过原因:")
            for reason, count in sorted(stats['skip_reasons'].items(), key=lambda x: x[1], reverse=True)[:3]:
                print(f"       • {reason}: {count:,} 次")
        print(f"\n     💡 可能原因分析:")
        print(f"       1. 速度/价格变化未达到策略阈值 (minMoveCents=3, minVelocityCentsPerSec=0.3)")
        print(f"       2. 市场质量门控过滤 (enableMarketQualityGate=true, minScore=70)")
        print(f"       3. 订单簿流动性不足 (价差过大)")
        print(f"       4. 冷却期保护 (cooldownMs=1500ms)")
        print(f"       5. 周期结束前保护 (CycleEndProtectionMinutes)")
        print(f"       6. 每周期最多交易1次限制 (maxTradesPerCycle=1)")

def main():
    """主函数"""
    log_dir = Path("logs")
    
    # 三个周期的日志文件
    cycles = [
        "btc-updown-15m-1766728800.log",
        "btc-updown-15m-1766729700.log",
        "btc-updown-15m-1766730600.log"
    ]
    
    all_stats = []
    
    for cycle_file in cycles:
        log_file = log_dir / cycle_file
        if log_file.exists():
            stats = analyze_cycle_log(log_file)
            print_cycle_summary(stats)
            all_stats.append(stats)
        else:
            print(f"⚠️  文件不存在: {log_file}")
    
    # 汇总统计
    print(f"\n\n{'='*80}")
    print("📊 三个周期汇总统计")
    print(f"{'='*80}")
    
    total_triggers = sum(len(s['strategy_triggers']) for s in all_stats)
    total_orders = sum(len(s['orders']) + len(s['order_success']) for s in all_stats)
    total_attempts = sum(len(s['order_attempts']) for s in all_stats)
    total_main = sum(len(s['main_orders']) for s in all_stats)
    total_hedge = sum(len(s['hedge_orders']) for s in all_stats)
    total_success = sum(len(s['order_success']) for s in all_stats)
    total_failed = sum(len(s['order_failed']) for s in all_stats)
    
    print(f"\n总体统计:")
    print(f"  - 策略触发总次数: {total_triggers:,}")
    print(f"  - 订单尝试总次数: {total_attempts}")
    print(f"  - 主单尝试总次数: {total_main}")
    print(f"  - 对冲单尝试总次数: {total_hedge}")
    print(f"  - 开单总次数: {total_orders}")
    print(f"    • 模拟下单: {sum(len(s['orders']) for s in all_stats)}")
    print(f"    • 实际下单成功: {total_success}")
    print(f"    • 下单失败: {total_failed}")
    
    print(f"\n各周期开单情况:")
    for stats in all_stats:
        cycle_orders = len(stats['orders']) + len(stats['order_success'])
        status = "✅ 有开单" if cycle_orders > 0 else "⚠️  未开单"
        print(f"  - {stats['cycle']}: {status} (触发{len(stats['strategy_triggers'])}次, 开单{cycle_orders}次)")

if __name__ == "__main__":
    main()

