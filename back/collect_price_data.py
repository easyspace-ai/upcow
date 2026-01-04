#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
价格数据收集工具

从运行中的策略实时收集价格数据，用于后续的速度统计分析。
可以作为一个独立的策略运行，或者从日志中提取数据。
"""

import json
import csv
import time
from datetime import datetime
from pathlib import Path
from typing import List, Dict

class PriceDataCollector:
    """价格数据收集器"""
    
    def __init__(self, output_file: str = "price_data.csv"):
        self.output_file = output_file
        self.data_points = []
        self.fieldnames = ['timestamp', 'up_price', 'down_price', 'up_bid', 'up_ask', 'down_bid', 'down_ask', 'market_slug']
    
    def add_price_point(self, timestamp: datetime, up_price: float = None, down_price: float = None,
                       up_bid: int = None, up_ask: int = None, down_bid: int = None, down_ask: int = None,
                       market_slug: str = ""):
        """添加价格数据点"""
        point = {
            'timestamp': timestamp.isoformat(),
            'up_price': up_price if up_price is not None else '',
            'down_price': down_price if down_price is not None else '',
            'up_bid': up_bid if up_bid is not None else '',
            'up_ask': up_ask if up_ask is not None else '',
            'down_bid': down_bid if down_bid is not None else '',
            'down_ask': down_ask if down_ask is not None else '',
            'market_slug': market_slug
        }
        self.data_points.append(point)
    
    def save(self):
        """保存数据到 CSV"""
        with open(self.output_file, 'w', newline='', encoding='utf-8') as f:
            writer = csv.DictWriter(f, fieldnames=self.fieldnames)
            writer.writeheader()
            writer.writerows(self.data_points)
        print(f"✅ 已保存 {len(self.data_points)} 个数据点到 {self.output_file}")

def extract_prices_from_logs(log_dir: str = "logs", output_file: str = "price_data_from_logs.csv"):
    """从日志文件中提取价格数据"""
    import re
    from collections import defaultdict
    
    collector = PriceDataCollector(output_file)
    parser = LogParser()
    
    log_path = Path(log_dir)
    if not log_path.exists():
        print(f"❌ 日志目录不存在: {log_dir}")
        return
    
    # 按市场分组
    market_data = defaultdict(list)
    
    for log_file in log_path.glob("*.log"):
        print(f"处理日志文件: {log_file}")
        with open(log_file, 'r', encoding='utf-8') as f:
            for line in f:
                timestamp = parser.parse_timestamp(line)
                if not timestamp:
                    continue
                
                # 提取市场名称
                market_match = re.search(r'market=([^\s]+)', line)
                market_slug = market_match.group(1) if market_match else ""
                
                # 提取价格信息
                price_info = parser.extract_price_from_log(line)
                if price_info:
                    token_type, price_cents = price_info
                    market_data[market_slug].append({
                        'timestamp': timestamp,
                        'token_type': token_type,
                        'price_cents': price_cents
                    })
                
                # 提取盘口数据
                book_match = re.search(r'UP:\s*bid=(\d+)c\s+ask=(\d+)c.*DOWN:\s*bid=(\d+)c\s+ask=(\d+)c', line)
                if book_match:
                    up_bid, up_ask, down_bid, down_ask = map(int, book_match.groups())
                    up_mid = (up_bid + up_ask) / 2.0
                    down_mid = (down_bid + down_ask) / 2.0
                    
                    collector.add_price_point(
                        timestamp=timestamp,
                        up_price=up_mid / 100.0,
                        down_price=down_mid / 100.0,
                        up_bid=up_bid,
                        up_ask=up_ask,
                        down_bid=down_bid,
                        down_ask=down_ask,
                        market_slug=market_slug
                    )
    
    # 合并同一时间戳的 UP/DOWN 价格
    time_points = defaultdict(dict)
    for market, points in market_data.items():
        for point in points:
            ts_key = point['timestamp'].isoformat()
            if ts_key not in time_points:
                time_points[ts_key] = {'market': market, 'timestamp': point['timestamp']}
            time_points[ts_key][point['token_type']] = point['price_cents'] / 100.0
    
    # 添加到收集器
    for ts_key, data in time_points.items():
        collector.add_price_point(
            timestamp=data['timestamp'],
            up_price=data.get('up'),
            down_price=data.get('down'),
            market_slug=data.get('market', '')
        )
    
    collector.save()

if __name__ == '__main__':
    import argparse
    
    parser = argparse.ArgumentParser(description='价格数据收集工具')
    parser.add_argument('--log-dir', type=str, default='logs', help='日志目录')
    parser.add_argument('--output', type=str, default='price_data.csv', help='输出 CSV 文件')
    
    args = parser.parse_args()
    
    print("🔍 开始从日志提取价格数据...")
    extract_prices_from_logs(args.log_dir, args.output)
    print("✅ 完成！")
