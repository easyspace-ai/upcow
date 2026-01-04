#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
生成订单表格，包含价格和数量信息
"""

import re
import json
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

def extract_trade_info(message):
    """从交易消息中提取信息"""
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
    
    # 尝试从 rawKeys 中提取更多信息（如果日志中有完整消息）
    # 注意：实际的价格和数量可能在 WebSocket 消息的完整内容中
    
    return info

def load_cycle_reports(report_dir="data/reports/cyclehedge"):
    """加载周期报表数据"""
    orders = []
    
    # 查找 JSONL 文件
    jsonl_files = list(Path(report_dir).glob("*.jsonl"))
    for jsonl_file in jsonl_files:
        try:
            with open(jsonl_file, 'r', encoding='utf-8') as f:
                for line in f:
                    line = line.strip()
                    if not line:
                        continue
                    try:
                        data = json.loads(line)
                        if 'orders' in data:
                            orders.extend(data['orders'])
                    except:
                        continue
        except Exception as e:
            print(f"读取 {jsonl_file} 失败: {e}")
    
    # 查找单个周期的 JSON 文件
    json_files = list(Path(report_dir).glob("*.json"))
    for json_file in json_files:
        try:
            with open(json_file, 'r', encoding='utf-8') as f:
                data = json.load(f)
                if isinstance(data, list):
                    for item in data:
                        if 'orders' in item:
                            orders.extend(item['orders'])
                elif isinstance(data, dict):
                    if 'orders' in data:
                        orders.extend(data['orders'])
        except Exception as e:
            print(f"读取 {json_file} 失败: {e}")
    
    return orders

def analyze_orders_from_logs(log_dir="logs"):
    """从日志中分析订单信息"""
    log_files = sorted(Path(log_dir).glob("btc-updown-15m-*.log"), 
                       key=lambda p: p.stat().st_mtime, reverse=True)
    
    trades = []
    order_groups = defaultdict(list)
    
    for log_file in log_files[:3]:  # 分析最新的3个文件
        try:
            with open(log_file, 'r', encoding='utf-8', errors='ignore') as f:
                for line in f:
                    parsed = parse_log_line(line)
                    if not parsed:
                        continue
                    
                    msg = parsed['message']
                    
                    # 提取交易消息
                    if 'UserWebSocket' in msg and 'event_type=trade' in msg:
                        trade_info = extract_trade_info(msg)
                        trade_info['timestamp'] = parsed['timestamp']
                        trade_info['file'] = log_file.name
                        trades.append(trade_info)
                        
                        # 按订单ID分组
                        if 'order_id' in trade_info:
                            order_groups[trade_info['order_id']].append(trade_info)
        except Exception as e:
            print(f"读取 {log_file} 失败: {e}")
    
    return trades, order_groups

def extract_quote_info(message):
    """从 quote 消息中提取信息"""
    info = {}
    
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
    
    return info

def generate_order_table():
    """生成订单表格"""
    print("=" * 100)
    print("📊 订单成交表格")
    print("=" * 100)
    print()
    
    # 从日志中提取订单信息
    trades, order_groups = analyze_orders_from_logs()
    
    # 从周期报表中加载订单详情
    report_orders = load_cycle_reports()
    
    # 从日志中提取 quote 信息（用于获取价格和数量）
    log_files = sorted(Path("logs").glob("btc-updown-15m-*.log"), 
                       key=lambda p: p.stat().st_mtime, reverse=True)
    
    quotes = []
    for log_file in log_files[:3]:
        try:
            with open(log_file, 'r', encoding='utf-8', errors='ignore') as f:
                for line in f:
                    parsed = parse_log_line(line)
                    if not parsed:
                        continue
                    
                    msg = parsed['message']
                    if '[cyclehedge]' in msg and 'quote:' in msg:
                        quote_info = extract_quote_info(msg)
                        quote_info['timestamp'] = parsed['timestamp']
                        quotes.append(quote_info)
        except:
            continue
    
    # 合并数据
    orders_dict = {}
    
    # 先从报表中获取详细信息
    for order in report_orders:
        order_id = order.get('orderID') or order.get('order_id')
        if order_id:
            orders_dict[order_id] = {
                'order_id': order_id,
                'side': order.get('side', ''),
                'price': order.get('price', order.get('filledPrice', 0)),
                'size': order.get('size', order.get('filledSize', order.get('quantity', 0))),
                'filled_size': order.get('filledSize', order.get('size', 0)),
                'status': order.get('status', ''),
                'token_type': order.get('tokenType', order.get('token_type', '')),
                'market': order.get('marketSlug', order.get('market', '')),
                'created_at': order.get('createdAt', order.get('created_at', '')),
                'filled_at': order.get('filledAt', order.get('filled_at', ''))
            }
    
    # 从交易消息中补充信息，并尝试从 quote 中获取价格
    for order_id, trade_list in order_groups.items():
        if order_id not in orders_dict:
            orders_dict[order_id] = {
                'order_id': order_id,
                'side': trade_list[0].get('side', ''),
                'price': 0,
                'size': 0,
                'filled_size': 0,
                'status': 'FILLED',
                'token_type': '',
                'market': '',
                'created_at': '',
                'filled_at': ''
            }
        
        # 更新时间戳
        if trade_list:
            orders_dict[order_id]['filled_at'] = trade_list[-1]['timestamp']
            orders_dict[order_id]['side'] = trade_list[0].get('side', orders_dict[order_id]['side'])
            
            # 尝试从最近的 quote 中获取价格和数量
            trade_time = trade_list[-1]['timestamp']
            for quote in reversed(quotes):
                # 找到交易时间之前的最近 quote
                if quote['timestamp'] <= trade_time:
                    if orders_dict[order_id]['side'] == 'BUY':
                        # 判断是 YES 还是 NO
                        asset_id = trade_list[0].get('asset_id', '')
                        # 根据 assetID 判断（需要知道哪个是 YES，哪个是 NO）
                        # 暂时使用 quote 中的价格
                        if 'need_up' in quote and quote['need_up'] > 0:
                            orders_dict[order_id]['price'] = quote.get('yes_bid', 0)
                            orders_dict[order_id]['size'] = quote.get('need_up', 0)
                        elif 'need_down' in quote and quote['need_down'] > 0:
                            orders_dict[order_id]['price'] = quote.get('no_bid', 0)
                            orders_dict[order_id]['size'] = quote.get('need_down', 0)
                    elif orders_dict[order_id]['side'] == 'SELL':
                        # 卖出订单，价格应该是 ask，但 quote 中只有 bid
                        # 暂时使用 bid 价格
                        if 'need_up' in quote:
                            orders_dict[order_id]['price'] = quote.get('yes_bid', 0)
                        elif 'need_down' in quote:
                            orders_dict[order_id]['price'] = quote.get('no_bid', 0)
                    break
    
    # 生成表格
    if not orders_dict:
        print("⚠️  未找到订单信息")
        print("   可能原因：")
        print("   1. 周期报表文件不存在或为空")
        print("   2. 日志中未包含完整的订单信息")
        print()
        print("尝试从日志中提取的交易记录：")
        if trades:
            print(f"\n   找到 {len(trades)} 条交易记录")
            print("   但缺少价格和数量信息")
        return
    
    # 按时间排序
    sorted_orders = sorted(orders_dict.values(), 
                          key=lambda x: x.get('filled_at', x.get('created_at', '')), 
                          reverse=True)
    
    # 打印表格
    print(f"{'序号':<6} {'订单ID':<36} {'方向':<6} {'价格(c)':<12} {'数量(shares)':<15} {'已成交':<15} {'价值(USDC)':<15} {'时间':<20}")
    print("-" * 120)
    
    total_buy_size = 0
    total_sell_size = 0
    total_buy_value = 0
    total_sell_value = 0
    
    for i, order in enumerate(sorted_orders, 1):
        order_id = order.get('order_id', 'N/A')
        side = order.get('side', 'N/A')
        price = order.get('price', 0)
        size = order.get('size', 0)
        filled_size = order.get('filled_size', size)
        status = order.get('status', 'N/A')
        
        # 时间格式化
        filled_at = order.get('filled_at', '')
        if isinstance(filled_at, datetime):
            time_str = filled_at.strftime('%Y-%m-%d %H:%M:%S')
        elif isinstance(filled_at, str):
            time_str = filled_at[:19] if len(filled_at) > 19 else filled_at
        else:
            time_str = str(filled_at)[:19]
        
        # 价格格式化
        if price > 0:
            price_str = f"{price:.4f}"
        else:
            price_str = "N/A"
        
        # 数量格式化
        if size > 0:
            size_str = f"{size:.4f}"
        else:
            size_str = "N/A"
        
        if filled_size > 0:
            filled_str = f"{filled_size:.4f}"
        else:
            filled_str = size_str
        
        # 计算价值（价格单位是 cents，数量单位是 shares）
        # 价值 = (价格 / 100) * 数量，因为价格是 cents，需要转换为 USDC
        if price > 0:
            if filled_size > 0:
                value = (price / 100) * filled_size
                value_str = f"{value:.2f}"
            elif size > 0:
                value = (price / 100) * size
                value_str = f"{value:.2f}"
            else:
                value_str = "N/A"
        else:
            value_str = "N/A"
        
        print(f"{i:<6} {order_id[:34]:<36} {side:<6} {price_str:<12} {size_str:<15} {filled_str:<15} {value_str:<15} {time_str:<20}")
        
        # 统计
        if side == 'BUY' and filled_size > 0:
            total_buy_size += filled_size
            if price > 0:
                total_buy_value += (price / 100) * filled_size  # 转换为 USDC
        elif side == 'SELL' and filled_size > 0:
            total_sell_size += filled_size
            if price > 0:
                total_sell_value += (price / 100) * filled_size  # 转换为 USDC
    
    print("-" * 120)
    print()
    
    # 统计信息
    print("📊 统计信息:")
    print(f"   总订单数: {len(sorted_orders)}")
    print(f"   买入订单: {sum(1 for o in sorted_orders if o.get('side') == 'BUY')}")
    print(f"   卖出订单: {sum(1 for o in sorted_orders if o.get('side') == 'SELL')}")
    print()
    
    if total_buy_size > 0:
        print(f"   买入总数量: {total_buy_size:.4f} shares")
        print(f"   买入总价值: {total_buy_value:.2f} USDC")
        if total_buy_size > 0:
            avg_buy_price = (total_buy_value * 100) / total_buy_size
            print(f"   平均买入价格: {avg_buy_price:.4f} c")
    print()
    
    if total_sell_size > 0:
        print(f"   卖出总数量: {total_sell_size:.4f} shares")
        print(f"   卖出总价值: {total_sell_value:.2f} USDC")
        if total_sell_size > 0:
            avg_sell_price = (total_sell_value * 100) / total_sell_size
            print(f"   平均卖出价格: {avg_sell_price:.4f} c")
    print()
    
    if total_buy_value > 0 and total_sell_value > 0:
        net_value = total_sell_value - total_buy_value
        print(f"   净盈亏: {net_value:+.2f} USDC")
        if total_buy_value > 0:
            roi = (net_value / total_buy_value) * 100
            print(f"   收益率: {roi:+.2f}%")
    print()

if __name__ == "__main__":
    generate_order_table()

