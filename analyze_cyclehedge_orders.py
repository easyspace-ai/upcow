#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
分析 cyclehedge 策略的开单情况
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

def extract_quote_info(message):
    """从 quote 消息中提取信息"""
    info = {}
    
    # 提取 profit
    profit_match = re.search(r'profit=(\d+)c', message)
    if profit_match:
        info['profit'] = int(profit_match.group(1))
    
    # 提取 cost
    cost_match = re.search(r'cost=(\d+)c', message)
    if cost_match:
        info['cost'] = int(cost_match.group(1))
    
    # 提取 targetNotional (tn)
    tn_match = re.search(r'tn=([\d.]+)', message)
    if tn_match:
        info['target_notional'] = float(tn_match.group(1))
    
    # 提取 shares
    shares_match = re.search(r'shares=([\d.]+)', message)
    if shares_match:
        info['shares'] = float(shares_match.group(1))
    
    # 提取 need up/down
    need_match = re.search(r'need\(up=([\d.]+)\s+down=([\d.]+)\)', message)
    if need_match:
        info['need_up'] = float(need_match.group(1))
        info['need_down'] = float(need_match.group(2))
    
    # 提取 bids
    bids_match = re.search(r'bids\(yes=(\d+)c\s+no=(\d+)c\)', message)
    if bids_match:
        info['yes_bid'] = int(bids_match.group(1))
        info['no_bid'] = int(bids_match.group(2))
    
    # 提取 book
    book_match = re.search(r'book\(yes\s+(\d+)/(\d+)\s+no\s+(\d+)/(\d+)\)', message)
    if book_match:
        info['yes_bid_book'] = int(book_match.group(1))
        info['yes_ask_book'] = int(book_match.group(2))
        info['no_bid_book'] = int(book_match.group(3))
        info['no_ask_book'] = int(book_match.group(4))
    
    # 提取 source
    src_match = re.search(r'src=([^\s|]+)', message)
    if src_match:
        info['source'] = src_match.group(1)
    
    # 提取 market
    market_match = re.search(r'market=([^\s]+)', message)
    if market_match:
        info['market'] = market_match.group(1)
    
    return info

def extract_order_info(message):
    """从订单相关消息中提取信息"""
    info = {}
    
    # 提取 orderID
    order_id_match = re.search(r'orderID=([^\s,]+)', message)
    if order_id_match:
        info['order_id'] = order_id_match.group(1)
    
    # 提取 assetID
    asset_id_match = re.search(r'assetID=([^\s,]+)', message)
    if asset_id_match:
        info['asset_id'] = asset_id_match.group(1)
    
    # 提取 side
    side_match = re.search(r'side=(\w+)', message)
    if side_match:
        info['side'] = side_match.group(1)
    
    return info

def analyze_cyclehedge_orders(log_dir="logs"):
    """分析 cyclehedge 策略开单情况"""
    log_files = sorted(Path(log_dir).glob("btc-updown-15m-*.log"), 
                       key=lambda p: p.stat().st_mtime, reverse=True)
    
    stats = {
        'total_files': len(log_files),
        'quotes': [],              # quote 记录（报价）
        'closeouts': [],           # closeout 记录（接近结算）
        'order_executions': [],    # 订单执行记录
        'order_fills': [],         # 订单成交记录
        'cycle_resets': [],        # 周期重置记录
        'time_range': {'start': None, 'end': None}
    }
    
    print(f"📊 开始分析 cyclehedge 策略的开单情况...\n")
    
    for log_file in log_files[:5]:  # 只分析最新的5个文件
        print(f"📁 分析文件: {log_file.name}")
        file_stats = {
            'quotes': 0,
            'closeouts': 0,
            'orders': 0,
            'fills': 0,
            'resets': 0
        }
        
        try:
            with open(log_file, 'r', encoding='utf-8', errors='ignore') as f:
                for line_num, line in enumerate(f, 1):
                    parsed = parse_log_line(line)
                    if not parsed:
                        continue
                    
                    msg = parsed['message']
                    timestamp = parsed['timestamp']
                    
                    # 更新时间范围
                    if stats['time_range']['start'] is None or timestamp < stats['time_range']['start']:
                        stats['time_range']['start'] = timestamp
                    if stats['time_range']['end'] is None or timestamp > stats['time_range']['end']:
                        stats['time_range']['end'] = timestamp
                    
                    # cyclehedge quote 记录
                    if '[cyclehedge]' in msg and 'quote:' in msg:
                        file_stats['quotes'] += 1
                        quote_info = extract_quote_info(msg)
                        quote_info['timestamp'] = timestamp
                        quote_info['file'] = log_file.name
                        quote_info['line'] = line_num
                        quote_info['message'] = msg
                        stats['quotes'].append(quote_info)
                    
                    # closeout 记录
                    if '[cyclehedge]' in msg and 'closeout:' in msg:
                        file_stats['closeouts'] += 1
                        closeout_info = {
                            'timestamp': timestamp,
                            'file': log_file.name,
                            'line': line_num,
                            'message': msg
                        }
                        stats['closeouts'].append(closeout_info)
                    
                    # 周期重置记录
                    if '[cyclehedge]' in msg and '周期重置' in msg:
                        file_stats['resets'] += 1
                        reset_info = {
                            'timestamp': timestamp,
                            'file': log_file.name,
                            'message': msg[:200]
                        }
                        stats['cycle_resets'].append(reset_info)
                    
                    # 订单成交记录（WebSocket 消息）
                    if 'UserWebSocket' in msg and 'event_type=trade' in msg:
                        file_stats['fills'] += 1
                        order_info = extract_order_info(msg)
                        order_info['timestamp'] = timestamp
                        order_info['file'] = log_file.name
                        order_info['message'] = msg[:200]
                        stats['order_fills'].append(order_info)
        
        except Exception as e:
            print(f"  ⚠️  读取文件出错: {e}")
            continue
        
        print(f"  - Quote 记录: {file_stats['quotes']}")
        print(f"  - Closeout 记录: {file_stats['closeouts']}")
        print(f"  - 订单成交: {file_stats['fills']}")
        print(f"  - 周期重置: {file_stats['resets']}")
        print()
    
    return stats

def print_analysis_report(stats):
    """打印分析报告"""
    print("=" * 80)
    print("📊 cyclehedge 策略开单情况分析报告")
    print("=" * 80)
    print()
    
    print(f"📁 日志文件统计:")
    print(f"  - 分析文件数: {stats['total_files']}")
    if stats['time_range']['start'] and stats['time_range']['end']:
        print(f"  - 时间范围: {stats['time_range']['start']} 至 {stats['time_range']['end']}")
        duration = stats['time_range']['end'] - stats['time_range']['start']
        print(f"  - 持续时间: {duration}")
    print()
    
    print(f"🔄 周期重置:")
    print(f"  - 重置次数: {len(stats['cycle_resets'])}")
    if stats['cycle_resets']:
        print("  最近重置记录:")
        for i, reset in enumerate(stats['cycle_resets'][:5], 1):
            print(f"    {i}. [{reset['timestamp']}] {reset['message']}")
    print()
    
    print(f"📊 Quote 记录（报价）:")
    print(f"  - 总记录数: {len(stats['quotes'])}")
    if stats['quotes']:
        print("\n  最近10条 Quote 记录:")
        for i, quote in enumerate(stats['quotes'][:10], 1):
            print(f"\n  Quote #{i}:")
            print(f"    时间: {quote['timestamp']}")
            print(f"    利润目标: {quote.get('profit', 'N/A')}c")
            print(f"    成本: {quote.get('cost', 'N/A')}c")
            print(f"    目标名义价值: {quote.get('target_notional', 'N/A')} USDC")
            print(f"    需要数量: UP={quote.get('need_up', 'N/A')}, DOWN={quote.get('need_down', 'N/A')}")
            print(f"    买价: YES={quote.get('yes_bid', 'N/A')}c, NO={quote.get('no_bid', 'N/A')}c")
            print(f"    盘口: YES {quote.get('yes_bid_book', 'N/A')}/{quote.get('yes_ask_book', 'N/A')}, NO {quote.get('no_bid_book', 'N/A')}/{quote.get('no_ask_book', 'N/A')}")
            print(f"    数据源: {quote.get('source', 'N/A')}")
        
        # 统计分析
        print("\n  📊 Quote 统计分析:")
        
        # 利润目标统计
        profits = [q.get('profit', 0) for q in stats['quotes'] if q.get('profit')]
        if profits:
            profit_counts = defaultdict(int)
            for profit in profits:
                profit_counts[profit] += 1
            print(f"    利润目标分布: {dict(sorted(profit_counts.items()))}")
        
        # 需要数量统计
        need_ups = [q.get('need_up', 0) for q in stats['quotes'] if q.get('need_up', 0) > 0]
        need_downs = [q.get('need_down', 0) for q in stats['quotes'] if q.get('need_down', 0) > 0]
        if need_ups or need_downs:
            print(f"    需要 UP 数量: 平均={sum(need_ups)/len(need_ups):.2f} (范围: {min(need_ups) if need_ups else 0:.2f}-{max(need_ups) if need_ups else 0:.2f})")
            print(f"    需要 DOWN 数量: 平均={sum(need_downs)/len(need_downs):.2f} (范围: {min(need_downs) if need_downs else 0:.2f}-{max(need_downs) if need_downs else 0:.2f})")
        
        # 价格统计
        yes_bids = [q.get('yes_bid', 0) for q in stats['quotes'] if q.get('yes_bid')]
        no_bids = [q.get('no_bid', 0) for q in stats['quotes'] if q.get('no_bid')]
        if yes_bids:
            print(f"    YES 买价: 平均={sum(yes_bids)/len(yes_bids):.1f}c (范围: {min(yes_bids)}c-{max(yes_bids)}c)")
        if no_bids:
            print(f"    NO 买价: 平均={sum(no_bids)/len(no_bids):.1f}c (范围: {min(no_bids)}c-{max(no_bids)}c)")
        
        # 数据源统计
        sources = [q.get('source', '') for q in stats['quotes'] if q.get('source')]
        if sources:
            source_counts = defaultdict(int)
            for source in sources:
                source_counts[source] += 1
            print(f"    数据源分布: {dict(source_counts)}")
    else:
        print("  ⚠️  未发现任何 Quote 记录")
    print()
    
    print(f"⏸️  Closeout 记录（接近结算）:")
    print(f"  - 总记录数: {len(stats['closeouts'])}")
    if stats['closeouts']:
        print("  最近10条:")
        for i, closeout in enumerate(stats['closeouts'][:10], 1):
            print(f"    {i}. [{closeout['timestamp']}] {closeout['message'][:100]}")
    print()
    
    print(f"✅ 订单成交记录:")
    print(f"  - 总成交数: {len(stats['order_fills'])}")
    if stats['order_fills']:
        print("  最近10条成交记录:")
        for i, fill in enumerate(stats['order_fills'][:10], 1):
            print(f"    {i}. [{fill['timestamp']}] orderID={fill.get('order_id', 'N/A')[:20]}... side={fill.get('side', 'N/A')}")
        
        # 方向统计
        sides = [f.get('side', '') for f in stats['order_fills'] if f.get('side')]
        if sides:
            side_counts = defaultdict(int)
            for side in sides:
                side_counts[side] += 1
            print(f"\n  方向分布: {dict(side_counts)}")
    else:
        print("  ⚠️  未发现任何订单成交记录")
    print()
    
    # 结论
    print("=" * 80)
    print("📌 分析结论:")
    print("=" * 80)
    
    if len(stats['quotes']) == 0:
        print("⚠️  未发现任何 Quote 记录")
        print("   说明策略可能未正常运行或未满足下单条件")
    else:
        print(f"✅ 发现 {len(stats['quotes'])} 条 Quote 记录")
        print("   说明策略正常运行，持续计算报价")
        
        # 检查是否有实际下单
        if len(stats['order_fills']) == 0:
            print("⚠️  但未发现订单成交记录")
            print("   可能原因:")
            print("   1. 订单未满足最小下单数量要求")
            print("   2. 订单价格与市场价差过大，未成交")
            print("   3. 订单被取消（closeout 窗口）")
        else:
            print(f"✅ 发现 {len(stats['order_fills'])} 笔订单成交")
            print("   说明策略已成功下单并成交")
    
    if len(stats['closeouts']) > 0:
        print(f"\n📌 发现 {len(stats['closeouts'])} 条 closeout 记录")
        print("   说明策略在接近结算时正确取消了订单")
    
    print()

if __name__ == "__main__":
    stats = analyze_cyclehedge_orders()
    print_analysis_report(stats)

