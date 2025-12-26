#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
分析所有日志文件的交易利润情况（包括压缩文件）
"""

import re
import gzip
from collections import defaultdict
from datetime import datetime
from pathlib import Path
import glob

def parse_log_line(line):
    """解析日志行"""
    # 格式: [25-12-26 16:16:58] INFO message [component=xxx]
    if '📝' not in line and '⚡' not in line and '📤' not in line:
        return None
    
    pattern = r'\[(\d+)-(\d+)-(\d+)\s+(\d+):(\d+):(\d+)\]'
    match = re.search(pattern, line)
    if match:
        year, month, day, hour, minute, second = match.groups()
        try:
            timestamp = datetime(int(f"20{year}"), int(month), int(day), 
                               int(hour), int(minute), int(second))
            return {
                'timestamp': timestamp,
                'message': line
            }
        except:
            pass
    return None

def extract_order_info(message):
    """从消息中提取订单信息"""
    info = {}
    
    order_id_match = re.search(r'orderID=([^\s,]+)', message)
    if order_id_match:
        info['order_id'] = order_id_match.group(1)
    
    asset_id_match = re.search(r'assetID=([^\s,]+)', message)
    if asset_id_match:
        info['asset_id'] = asset_id_match.group(1)
    
    side_match = re.search(r'side=(\w+)', message)
    if side_match:
        info['side'] = side_match.group(1)
    
    price_match = re.search(r'price=([\d.]+)', message)
    if price_match:
        info['price'] = float(price_match.group(1))
    
    size_match = re.search(r'size=([\d.]+)', message)
    if size_match:
        info['size'] = float(size_match.group(1))
    
    status_match = re.search(r'status=(\w+)', message)
    if status_match:
        info['status'] = status_match.group(1)
    
    return info if info else None

def extract_trade_info(message):
    """从交易触发消息中提取信息"""
    info = {}
    
    side_match = re.search(r'side=(\w+)', message)
    if side_match:
        info['side'] = side_match.group(1)
    
    ask_match = re.search(r'ask=(\d+)c', message)
    if ask_match:
        info['entry_price'] = int(ask_match.group(1))
    
    hedge_match = re.search(r'hedge=(\d+)c', message)
    if hedge_match:
        info['hedge_price'] = int(hedge_match.group(1))
    
    return info if info else None

def analyze_profit_from_file(log_file):
    """从单个日志文件分析利润情况"""
    orders = []
    trades = []
    
    # 判断是否是压缩文件
    if log_file.endswith('.gz'):
        open_func = gzip.open
        mode = 'rt'
    else:
        open_func = open
        mode = 'r'
    
    try:
        with open_func(log_file, mode, encoding='utf-8', errors='ignore') as f:
            for line in f:
                parsed = parse_log_line(line)
                if not parsed:
                    continue
                
                msg = parsed['message']
                
                if '📝' in msg and '纸交易' in msg and '模拟下单' in msg:
                    order_info = extract_order_info(msg)
                    if order_info:
                        order_info['timestamp'] = parsed['timestamp']
                        order_info['type'] = 'order'
                        orders.append(order_info)
                
                if '⚡' in msg and '触发' in msg:
                    trade_info = extract_trade_info(msg)
                    if trade_info:
                        trade_info['timestamp'] = parsed['timestamp']
                        trade_info['type'] = 'trade'
                        trades.append(trade_info)
    except Exception as e:
        print(f"⚠️ 读取文件 {log_file} 时出错: {e}")
    
    return orders, trades

def match_orders_to_trades(orders, trades):
    """将订单匹配到交易"""
    matched_trades = []
    
    for trade in trades:
        trade_orders = {
            'trade': trade,
            'entry_order': None,
            'hedge_order': None,
            'exit_orders': []
        }
        
        for order in orders:
            if order.get('type') != 'order' or order.get('side') != 'BUY':
                continue
            
            time_diff = abs((order['timestamp'] - trade['timestamp']).total_seconds())
            if time_diff < 5:
                if order.get('status') == 'filled':
                    if 'entry_price' in trade:
                        expected_price = trade['entry_price'] / 100.0
                        if abs(order.get('price', 0) - expected_price) < 0.01:
                            trade_orders['entry_order'] = order
                            break
        
        for order in orders:
            if order.get('type') != 'order' or order.get('side') != 'BUY':
                continue
            
            time_diff = abs((order['timestamp'] - trade['timestamp']).total_seconds())
            if time_diff < 5:
                if order.get('status') == 'open':
                    if 'hedge_price' in trade:
                        expected_price = trade['hedge_price'] / 100.0
                        if abs(order.get('price', 0) - expected_price) < 0.01:
                            trade_orders['hedge_order'] = order
                            break
        
        matched_trades.append(trade_orders)
    
    return matched_trades

def match_exits_to_trades(matched_trades, orders):
    """将出场订单匹配到交易"""
    for trade_data in matched_trades:
        entry_order = trade_data['entry_order']
        hedge_order = trade_data['hedge_order']
        
        if not entry_order:
            continue
        
        for order in orders:
            if order.get('type') != 'order' or order.get('side') != 'SELL':
                continue
            
            if order['timestamp'] < entry_order['timestamp']:
                continue
            
            time_diff = (order['timestamp'] - entry_order['timestamp']).total_seconds()
            if time_diff > 300:
                continue
            
            order_asset_id = order.get('asset_id', '')
            entry_asset_id = entry_order.get('asset_id', '')
            hedge_asset_id = hedge_order.get('asset_id', '') if hedge_order else ''
            
            matched = False
            exit_token = ''
            
            if order_asset_id == entry_asset_id and entry_asset_id:
                matched = True
                exit_token = trade_data['trade'].get('side', '').lower()
            
            if not matched and order_asset_id == hedge_asset_id and hedge_asset_id:
                matched = True
                exit_token = 'down' if trade_data['trade'].get('side', '').lower() == 'up' else 'up'
            
            if matched:
                already_matched = any(e.get('order_id') == order.get('order_id') 
                                    for e in trade_data['exit_orders'])
                
                if not already_matched:
                    price_decimal = order.get('price', 0)
                    exit_price_cents = int(round(price_decimal * 100)) if price_decimal else 0
                    
                    exit_info = {
                        'timestamp': order['timestamp'],
                        'exit_price': exit_price_cents,
                        'size': order.get('size', 0),
                        'reason': 'sell_order',
                        'order_id': order.get('order_id', ''),
                        'token': exit_token,
                        'type': 'exit'
                    }
                    trade_data['exit_orders'].append(exit_info)
    
    return matched_trades

def calculate_profit(trade_data):
    """计算利润"""
    entry_order = trade_data['entry_order']
    hedge_order = trade_data['hedge_order']
    exit_orders = trade_data['exit_orders']
    
    profit_info = {
        'entry_cost': entry_order['price'] * entry_order['size'] if entry_order and entry_order.get('price') and entry_order.get('size') else 0,
        'hedge_cost': hedge_order['price'] * hedge_order['size'] if hedge_order and hedge_order.get('status') == 'filled' and hedge_order.get('price') and hedge_order.get('size') else 0,
        'exit_revenue': 0,
        'profit': 0,
        'profit_cents': 0,
        'profit_pct': 0,
        'status': 'unknown'
    }
    
    profit_info['total_cost'] = profit_info['entry_cost'] + profit_info['hedge_cost']
    
    unique_exit_orders = {}
    for exit_order in exit_orders:
        order_id = exit_order.get('order_id', '')
        if order_id:
            if order_id not in unique_exit_orders:
                unique_exit_orders[order_id] = exit_order
        else:
            key = f"{exit_order.get('token', 'unknown')}_{exit_order.get('exit_price', 0)}"
            if key not in unique_exit_orders:
                unique_exit_orders[key] = exit_order
    
    exit_orders[:] = list(unique_exit_orders.values())
    
    for exit_order in unique_exit_orders.values():
        if exit_order.get('exit_price') and exit_order.get('size'):
            profit_info['exit_revenue'] += (exit_order['exit_price'] / 100.0) * exit_order['size']
    
    actual_cost = profit_info['entry_cost']
    if hedge_order and hedge_order.get('status') == 'filled':
        actual_cost = profit_info['total_cost']
    
    if actual_cost > 0:
        profit_info['profit'] = profit_info['exit_revenue'] - actual_cost
        profit_info['profit_cents'] = profit_info['profit'] * 100
        profit_info['profit_pct'] = (profit_info['profit'] / actual_cost) * 100 if actual_cost > 0 else 0
    
    if len(exit_orders) > 0:
        profit_info['status'] = 'profit' if profit_info['profit'] > 0 else 'loss' if profit_info['profit'] < 0 else 'breakeven'
    elif hedge_order and hedge_order.get('status') == 'filled':
        profit_info['status'] = 'hedged'
    else:
        profit_info['status'] = 'open'
    
    return profit_info

def print_profit_analysis(all_matched_trades):
    """打印利润分析"""
    print("=" * 120)
    print("📊 全部日志交易订单和利润分析")
    print("=" * 120)
    print()
    
    print(f"{'交易#':<6} {'时间':<20} {'方向':<6} {'Entry价格':<12} {'Hedge价格':<12} {'Entry状态':<12} {'Hedge状态':<12} {'出场价格':<20} {'Entry成本':<12} {'Hedge成本':<12} {'总成本':<12} {'出场收入':<12} {'利润(USDC)':<14} {'利润(c)':<12} {'状态':<12}")
    print("-" * 120)
    
    total_entry_cost = 0
    total_hedge_cost = 0
    total_exit_revenue = 0
    total_profit = 0
    
    for i, trade_data in enumerate(all_matched_trades, 1):
        trade = trade_data['trade']
        profit_info = calculate_profit(trade_data)
        
        time_str = trade['timestamp'].strftime("%m-%d %H:%M:%S")
        side = trade.get('side', 'N/A').upper()
        entry_price = f"{trade.get('entry_price', 0)}c" if 'entry_price' in trade else "N/A"
        hedge_price = f"{trade.get('hedge_price', 0)}c" if 'hedge_price' in trade else "N/A"
        entry_status = trade_data['entry_order'].get('status', 'N/A') if trade_data['entry_order'] else "N/A"
        hedge_status = trade_data['hedge_order'].get('status', 'N/A') if trade_data['hedge_order'] else "N/A"
        
        exit_price_str = "N/A"
        if trade_data['exit_orders']:
            unique_exits = {}
            for e in trade_data['exit_orders']:
                key = e.get('order_id') or f"{e.get('token', 'unknown')}_{e.get('exit_price', 0)}"
                if key not in unique_exits:
                    unique_exits[key] = e
            
            exit_by_token = {}
            for e in unique_exits.values():
                token = e.get('token', 'unknown').upper()
                if token not in exit_by_token:
                    exit_by_token[token] = []
                exit_by_token[token].append(e.get('exit_price', 0))
            
            exit_prices = []
            for token, prices in sorted(exit_by_token.items()):
                if len(prices) == 1:
                    exit_prices.append(f"{token}:{prices[0]}c")
                else:
                    exit_prices.append(f"{token}:{','.join(map(str, prices))}c")
            
            if exit_prices:
                exit_price_str = ", ".join(exit_prices)
        
        status_emoji = {'profit': '✅', 'loss': '❌', 'breakeven': '➖', 'hedged': '🔒', 'open': '⏳', 'unknown': '❓'}
        status = f"{status_emoji.get(profit_info['status'], '❓')} {profit_info['status']}"
        
        print(f"{i:<6} {time_str:<20} {side:<6} {entry_price:<12} {hedge_price:<12} {entry_status:<12} {hedge_status:<12} {exit_price_str:<20} {profit_info['entry_cost']:<12.4f} {profit_info['hedge_cost']:<12.4f} {profit_info['total_cost']:<12.4f} {profit_info['exit_revenue']:<12.4f} {profit_info['profit']:<14.4f} {profit_info['profit_cents']:<12.2f} {status:<12}")
        
        total_entry_cost += profit_info['entry_cost']
        total_hedge_cost += profit_info['hedge_cost']
        total_exit_revenue += profit_info['exit_revenue']
        total_profit += profit_info['profit']
    
    print("-" * 120)
    total_profit_cents = total_profit * 100
    total_profit_pct = (total_profit / total_entry_cost * 100) if total_entry_cost > 0 else 0
    
    print(f"{'总计':<6} {'':<20} {'':<6} {'':<12} {'':<12} {'':<12} {'':<12} {'':<20} {total_entry_cost:<12.4f} {total_hedge_cost:<12.4f} {total_entry_cost + total_hedge_cost:<12.4f} {total_exit_revenue:<12.4f} {total_profit:<14.4f} {total_profit_cents:<12.2f} {'':<12}")
    print()
    
    print("=" * 120)
    print("📊 利润统计")
    print("=" * 120)
    print()
    
    print(f"总交易数: {len(all_matched_trades)} 笔")
    print(f"总Entry成本: {total_entry_cost:.4f} USDC")
    print(f"总Hedge成本: {total_hedge_cost:.4f} USDC")
    print(f"总成本: {total_entry_cost + total_hedge_cost:.4f} USDC")
    print(f"总出场收入: {total_exit_revenue:.4f} USDC")
    print(f"总利润: {total_profit:.4f} USDC ({total_profit_cents:.2f} cents)")
    print(f"利润率: {total_profit_pct:.2f}%")
    print()
    
    status_counts = defaultdict(int)
    for trade_data in all_matched_trades:
        profit_info = calculate_profit(trade_data)
        status_counts[profit_info['status']] += 1
    
    print("交易状态统计:")
    emoji = {'profit': '✅', 'loss': '❌', 'breakeven': '➖', 'hedged': '🔒', 'open': '⏳', 'unknown': '❓'}
    for status, count in sorted(status_counts.items()):
        print(f"  {emoji.get(status, '❓')} {status}: {count} 笔")
    print()

if __name__ == "__main__":
    # 查找所有日志文件（包括压缩文件）
    log_files = []
    for pattern in ['logs/*.log', 'logs/*.log.gz']:
        log_files.extend(glob.glob(pattern))
    
    log_files = sorted(log_files, key=lambda p: Path(p).stat().st_mtime)
    
    if not log_files:
        print("未找到日志文件")
        exit(1)
    
    print(f"📁 分析 {len(log_files)} 个日志文件\n")
    
    all_orders = []
    all_trades = []
    
    for log_file in log_files:
        orders, trades = analyze_profit_from_file(log_file)
        all_orders.extend(orders)
        all_trades.extend(trades)
        if orders or trades:
            print(f"  ✅ {log_file}: {len(trades)} 笔交易, {len(orders)} 个订单")
    
    print(f"\n📊 总计: {len(all_trades)} 笔交易触发，{len(all_orders)} 个订单\n")
    
    matched_trades = match_orders_to_trades(all_orders, all_trades)
    matched_trades = match_exits_to_trades(matched_trades, all_orders)
    matched_trades.sort(key=lambda x: x['trade']['timestamp'])
    
    print_profit_analysis(matched_trades)

