#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
分析所有日志文件的交易利润情况
"""

import re
from collections import defaultdict
from datetime import datetime
from pathlib import Path
import glob

def parse_log_line(line):
    """解析日志行"""
    # 格式: [25-12-26 16:16:58] INFO message [component=xxx]
    # 或者: [36mINFO[0m[25-12-26 16:16:58] message [component=xxx]
    # 匹配包含emoji的行
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
                'message': line  # 使用整行作为消息
            }
        except Exception as e:
            pass
    
    return None

def extract_order_info(message):
    """从消息中提取订单信息"""
    info = {}
    
    # 提取订单ID
    order_id_match = re.search(r'orderID=([^\s,]+)', message)
    if order_id_match:
        info['order_id'] = order_id_match.group(1)
    
    # 提取assetID
    asset_id_match = re.search(r'assetID=([^\s,]+)', message)
    if asset_id_match:
        info['asset_id'] = asset_id_match.group(1)
    
    # 提取方向
    side_match = re.search(r'side=(\w+)', message)
    if side_match:
        info['side'] = side_match.group(1)
    
    # 提取价格
    price_match = re.search(r'price=([\d.]+)', message)
    if price_match:
        info['price'] = float(price_match.group(1))
    
    # 提取数量
    size_match = re.search(r'size=([\d.]+)', message)
    if size_match:
        info['size'] = float(size_match.group(1))
    
    # 提取状态
    status_match = re.search(r'status=(\w+)', message)
    if status_match:
        info['status'] = status_match.group(1)
    
    return info

def extract_trade_info(message):
    """从交易触发消息中提取信息"""
    info = {}
    
    # 提取方向
    side_match = re.search(r'side=(\w+)', message)
    if side_match:
        info['side'] = side_match.group(1)
    
    # 提取入场价格
    ask_match = re.search(r'ask=(\d+)c', message)
    if ask_match:
        info['entry_price'] = int(ask_match.group(1))
    
    # 提取对冲价格
    hedge_match = re.search(r'hedge=(\d+)c', message)
    if hedge_match:
        info['hedge_price'] = int(hedge_match.group(1))
    
    return info

def analyze_profit_from_file(log_file):
    """从单个日志文件分析利润情况"""
    orders = []
    trades = []
    
    with open(log_file, 'r', encoding='utf-8', errors='ignore') as f:
        for line in f:
            parsed = parse_log_line(line)
            if not parsed:
                continue
            
            msg = parsed['message']
            
            # 模拟下单记录（包括BUY和SELL）
            if '📝' in msg and '纸交易' in msg and '模拟下单' in msg:
                order_info = extract_order_info(msg)
                if order_info:  # 确保提取到信息
                    order_info['timestamp'] = parsed['timestamp']
                    order_info['type'] = 'order'
                    orders.append(order_info)
            
            # 交易触发
            if '⚡' in msg and '触发' in msg:
                trade_info = extract_trade_info(msg)
                if trade_info:  # 确保提取到信息
                    trade_info['timestamp'] = parsed['timestamp']
                    trade_info['type'] = 'trade'
                    trades.append(trade_info)
    
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
        
        # 找到Entry订单（在交易触发时间附近）
        for order in orders:
            if order.get('type') != 'order':
                continue
            
            if order.get('side') != 'BUY':
                continue
            
            time_diff = abs((order['timestamp'] - trade['timestamp']).total_seconds())
            if time_diff < 5:  # 5秒内
                if order.get('status') == 'filled':
                    # 检查价格是否匹配
                    if 'entry_price' in trade:
                        expected_price = trade['entry_price'] / 100.0
                        if abs(order.get('price', 0) - expected_price) < 0.01:
                            trade_orders['entry_order'] = order
                            break
        
        # 找到Hedge订单
        for order in orders:
            if order.get('type') != 'order':
                continue
            
            if order.get('side') != 'BUY':
                continue
            
            time_diff = abs((order['timestamp'] - trade['timestamp']).total_seconds())
            if time_diff < 5:  # 5秒内
                if order.get('status') == 'open':
                    # 检查是否是对冲订单（价格互补）
                    if 'hedge_price' in trade:
                        expected_price = trade['hedge_price'] / 100.0
                        if abs(order.get('price', 0) - expected_price) < 0.01:
                            trade_orders['hedge_order'] = order
                            break
        
        matched_trades.append(trade_orders)
    
    return matched_trades

def match_exits_to_trades(matched_trades, orders):
    """将出场订单匹配到交易"""
    # 为每个交易找到对应的SELL订单
    for trade_data in matched_trades:
        trade = trade_data['trade']
        entry_order = trade_data['entry_order']
        hedge_order = trade_data['hedge_order']
        
        if not entry_order:
            continue
        
        # 找到在Entry订单之后的SELL订单
        for order in orders:
            if order.get('type') != 'order':
                continue
            
            if order.get('side') != 'SELL':
                continue
            
            # SELL订单应该在Entry订单之后或同时
            if order['timestamp'] < entry_order['timestamp']:
                continue
            
            # 检查时间差（应该在合理范围内，比如5分钟内）
            time_diff = (order['timestamp'] - entry_order['timestamp']).total_seconds()
            if time_diff > 300:  # 5分钟内
                continue
            
            # 通过assetID匹配：SELL订单的assetID应该和Entry或Hedge订单的assetID匹配
            order_asset_id = order.get('asset_id', '')
            entry_asset_id = entry_order.get('asset_id', '')
            hedge_asset_id = hedge_order.get('asset_id', '') if hedge_order else ''
            
            matched = False
            exit_token = ''
            
            # 匹配Entry订单的assetID（平仓Entry单）
            if order_asset_id == entry_asset_id and entry_asset_id:
                matched = True
                exit_token = trade.get('side', '').lower()
            
            # 匹配Hedge订单的assetID（平仓Hedge单）
            if not matched and order_asset_id == hedge_asset_id and hedge_asset_id:
                matched = True
                # Hedge单是对侧的
                exit_token = 'down' if trade.get('side', '').lower() == 'up' else 'up'
            
            if matched:
                # 检查是否已经匹配过（避免重复）
                already_matched = False
                for existing_exit in trade_data['exit_orders']:
                    if existing_exit.get('order_id') == order.get('order_id'):
                        already_matched = True
                        break
                
                if not already_matched:
                    # 价格转换：确保正确转换为cents
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
    trade = trade_data['trade']
    entry_order = trade_data['entry_order']
    hedge_order = trade_data['hedge_order']
    exit_orders = trade_data['exit_orders']
    
    profit_info = {
        'entry_cost': 0,
        'hedge_cost': 0,
        'total_cost': 0,
        'exit_revenue': 0,
        'profit': 0,
        'profit_cents': 0,
        'profit_pct': 0,
        'status': 'unknown'
    }
    
    # 计算Entry成本
    if entry_order and entry_order.get('price') and entry_order.get('size'):
        profit_info['entry_cost'] = entry_order['price'] * entry_order['size']
    
    # 计算Hedge成本
    if hedge_order and hedge_order.get('price') and hedge_order.get('size'):
        if hedge_order.get('status') == 'filled':
            profit_info['hedge_cost'] = hedge_order['price'] * hedge_order['size']
        else:
            # Hedge单未成交，成本为0
            profit_info['hedge_cost'] = 0
    
    # 总成本
    profit_info['total_cost'] = profit_info['entry_cost'] + profit_info['hedge_cost']
    
    # 计算出场收入（去重：优先使用order_id，否则使用token+price）
    unique_exit_orders = {}
    for exit_order in exit_orders:
        order_id = exit_order.get('order_id', '')
        if order_id:
            # 优先使用order_id作为唯一标识
            if order_id not in unique_exit_orders:
                unique_exit_orders[order_id] = exit_order
        else:
            # 如果没有order_id，使用token+price作为key
            token = exit_order.get('token', 'unknown')
            price = exit_order.get('exit_price', 0)
            key = f"{token}_{price}"
            if key not in unique_exit_orders:
                unique_exit_orders[key] = exit_order
    
    # 更新exit_orders为去重后的列表（用于后续显示）
    exit_orders[:] = list(unique_exit_orders.values())
    
    # 计算出场收入
    for exit_order in unique_exit_orders.values():
        if exit_order.get('exit_price') and exit_order.get('size'):
            exit_revenue = (exit_order['exit_price'] / 100.0) * exit_order['size']
            profit_info['exit_revenue'] += exit_revenue
    
    # 计算利润
    # 注意：如果Hedge单未成交，只计算Entry成本
    actual_cost = profit_info['entry_cost']
    if hedge_order and hedge_order.get('status') == 'filled':
        actual_cost = profit_info['total_cost']
    
    if actual_cost > 0:
        profit_info['profit'] = profit_info['exit_revenue'] - actual_cost
        profit_info['profit_cents'] = profit_info['profit'] * 100
        profit_info['profit_pct'] = (profit_info['profit'] / actual_cost) * 100 if actual_cost > 0 else 0
    elif profit_info['exit_revenue'] > 0:
        # 如果只有出场收入但没有成本（不应该发生），利润就是收入
        profit_info['profit'] = profit_info['exit_revenue']
        profit_info['profit_cents'] = profit_info['profit'] * 100
        profit_info['profit_pct'] = 0
    
    # 判断状态
    if len(exit_orders) > 0:
        if profit_info['profit'] > 0:
            profit_info['status'] = 'profit'
        elif profit_info['profit'] < 0:
            profit_info['status'] = 'loss'
        else:
            profit_info['status'] = 'breakeven'
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
    
    # 表格标题
    print(f"{'交易#':<6} {'时间':<20} {'方向':<6} {'Entry价格':<12} {'Hedge价格':<12} {'Entry状态':<12} {'Hedge状态':<12} {'出场价格':<20} {'Entry成本':<12} {'Hedge成本':<12} {'总成本':<12} {'出场收入':<12} {'利润(USDC)':<14} {'利润(c)':<12} {'状态':<12}")
    print("-" * 120)
    
    total_entry_cost = 0
    total_hedge_cost = 0
    total_exit_revenue = 0
    total_profit = 0
    
    for i, trade_data in enumerate(all_matched_trades, 1):
        trade = trade_data['trade']
        entry_order = trade_data['entry_order']
        hedge_order = trade_data['hedge_order']
        exit_orders = trade_data['exit_orders']
        
        profit_info = calculate_profit(trade_data)
        
        # 准备显示数据
        time_str = trade['timestamp'].strftime("%m-%d %H:%M:%S")
        side = trade.get('side', 'N/A').upper()
        entry_price = f"{trade.get('entry_price', 0)}c" if 'entry_price' in trade else "N/A"
        hedge_price = f"{trade.get('hedge_price', 0)}c" if 'hedge_price' in trade else "N/A"
        entry_status = entry_order.get('status', 'N/A') if entry_order else "N/A"
        hedge_status = hedge_order.get('status', 'N/A') if hedge_order else "N/A"
        
        # 出场价格（如果有多个，显示所有价格，去重）
        exit_price_str = "N/A"
        if exit_orders:
            # 去重：按token和价格去重
            unique_exits = {}
            for e in exit_orders:
                if e.get('exit_price'):
                    token = e.get('token', 'unknown')
                    price = e.get('exit_price', 0)
                    # 使用order_id作为唯一标识，如果没有则使用token+price
                    order_id = e.get('order_id', '')
                    if order_id:
                        key = order_id
                    else:
                        key = f"{token}_{price}"
                    if key not in unique_exits:
                        unique_exits[key] = e
            
            # 按token分组显示
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
        
        entry_cost = f"{profit_info['entry_cost']:.4f}" if profit_info['entry_cost'] > 0 else "0.0000"
        hedge_cost = f"{profit_info['hedge_cost']:.4f}" if profit_info['hedge_cost'] > 0 else "0.0000"
        total_cost = f"{profit_info['total_cost']:.4f}" if profit_info['total_cost'] > 0 else "0.0000"
        exit_revenue = f"{profit_info['exit_revenue']:.4f}" if profit_info['exit_revenue'] > 0 else "0.0000"
        profit_usdc = f"{profit_info['profit']:.4f}" if profit_info['profit'] != 0 else "0.0000"
        profit_cents = f"{profit_info['profit_cents']:.2f}" if profit_info['profit_cents'] != 0 else "0.00"
        
        status_emoji = {
            'profit': '✅',
            'loss': '❌',
            'breakeven': '➖',
            'hedged': '🔒',
            'open': '⏳',
            'unknown': '❓'
        }
        status = f"{status_emoji.get(profit_info['status'], '❓')} {profit_info['status']}"
        
        print(f"{i:<6} {time_str:<20} {side:<6} {entry_price:<12} {hedge_price:<12} {entry_status:<12} {hedge_status:<12} {exit_price_str:<20} {entry_cost:<12} {hedge_cost:<12} {total_cost:<12} {exit_revenue:<12} {profit_usdc:<14} {profit_cents:<12} {status:<12}")
        
        # 累计统计
        total_entry_cost += profit_info['entry_cost']
        total_hedge_cost += profit_info['hedge_cost']
        total_exit_revenue += profit_info['exit_revenue']
        total_profit += profit_info['profit']
    
    print("-" * 120)
    
    # 总计行
    total_profit_cents = total_profit * 100
    total_profit_pct = (total_profit / total_entry_cost * 100) if total_entry_cost > 0 else 0
    
    print(f"{'总计':<6} {'':<20} {'':<6} {'':<12} {'':<12} {'':<12} {'':<12} {'':<20} {total_entry_cost:<12.4f} {total_hedge_cost:<12.4f} {total_entry_cost + total_hedge_cost:<12.4f} {total_exit_revenue:<12.4f} {total_profit:<14.4f} {total_profit_cents:<12.2f} {'':<12}")
    print()
    
    # 详细统计
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
    
    # 按状态统计
    status_counts = defaultdict(int)
    for trade_data in all_matched_trades:
        profit_info = calculate_profit(trade_data)
        status_counts[profit_info['status']] += 1
    
    print("交易状态统计:")
    for status, count in sorted(status_counts.items()):
        emoji = {
            'profit': '✅',
            'loss': '❌',
            'breakeven': '➖',
            'hedged': '🔒',
            'open': '⏳',
            'unknown': '❓'
        }
        print(f"  {emoji.get(status, '❓')} {status}: {count} 笔")
    print()

if __name__ == "__main__":
    # 查找所有日志文件
    log_files = sorted(glob.glob("logs/btc-updown-15m-*.log"), key=lambda p: Path(p).stat().st_mtime)
    
    if not log_files:
        print("未找到日志文件")
        exit(1)
    
    print(f"📁 分析 {len(log_files)} 个日志文件\n")
    
    all_orders = []
    all_trades = []
    
    # 分析所有日志文件
    for log_file in log_files:
        orders, trades = analyze_profit_from_file(log_file)
        all_orders.extend(orders)
        all_trades.extend(trades)
    
    print(f"📊 找到 {len(all_trades)} 笔交易触发，{len(all_orders)} 个订单\n")
    
    # 匹配订单到交易
    matched_trades = match_orders_to_trades(all_orders, all_trades)
    matched_trades = match_exits_to_trades(matched_trades, all_orders)
    
    # 按时间排序
    matched_trades.sort(key=lambda x: x['trade']['timestamp'])
    
    print_profit_analysis(matched_trades)

